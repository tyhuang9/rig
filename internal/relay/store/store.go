package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/relay/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrInvalid  = errors.New("relay store invalid input")
	ErrNotFound = errors.New("relay store record not found")
	ErrConflict = errors.New("relay store conflict")
	ErrExpired  = errors.New("relay store record expired")
	ErrReplay   = errors.New("relay store replay detected")
	ErrCapacity = errors.New("relay store capacity reached")
	codePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
)

const enrollmentCapacityLock int64 = 0x524947454e524f4c

type Options struct {
	Now         func() time.Time
	NewUUID     func() uuid.UUID
	RandomBytes func([]byte) error
}
type database interface {
	Begin(context.Context) (pgx.Tx, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}
type Store struct {
	pool        database
	close       func()
	now         func() time.Time
	newUUID     func() uuid.UUID
	randomBytes func([]byte) error
}

func Open(ctx context.Context, dsn string, options Options) (*Store, error) {
	if strings.TrimSpace(dsn) != dsn || dsn == "" {
		return nil, fmt.Errorf("%w: database DSN", ErrInvalid)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("relay store open: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("relay store ping: %w", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return New(pool, options)
}

func New(pool *pgxpool.Pool, options Options) (*Store, error) {
	if pool == nil {
		return nil, fmt.Errorf("%w: pool", ErrInvalid)
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.NewUUID == nil {
		options.NewUUID = uuid.New
	}
	if options.RandomBytes == nil {
		options.RandomBytes = func(dst []byte) error { _, err := rand.Read(dst); return err }
	}
	return &Store{pool: pool, close: pool.Close, now: options.Now, newUUID: options.NewUUID, randomBytes: options.RandomBytes}, nil
}
func (s *Store) Close() {
	if s != nil && s.close != nil {
		s.close()
	}
}
func newWithDatabase(db database, options Options) (*Store, error) {
	if db == nil {
		return nil, ErrInvalid
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.NewUUID == nil {
		options.NewUUID = uuid.New
	}
	if options.RandomBytes == nil {
		options.RandomBytes = func(dst []byte) error { _, err := rand.Read(dst); return err }
	}
	return &Store{pool: db, now: options.Now, newUUID: options.NewUUID, randomBytes: options.RandomBytes}, nil
}
func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
func validUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}
func validCode(value string) bool    { return codePattern.MatchString(value) }
func validTime(value time.Time) bool { return !value.IsZero() }
func validateHash(value []byte) bool { return len(value) == 32 }
func deliveryLockKey(deliveryID string) int64 {
	digest := sha256.Sum256([]byte("rig.relay.delivery.v1\x00" + deliveryID))
	return int64(binary.BigEndian.Uint64(digest[:8]))
}

const topologyShardCount = 64

type routeTopologyKey struct {
	installationID int64
	repositoryID   int64
	ref            string
}

type topologyLockSet struct {
	bindings      map[int64]struct{}
	routes        map[routeTopologyKey]struct{}
	subscriptions map[string]struct{}
}

func newTopologyLockSet() topologyLockSet {
	return topologyLockSet{bindings: make(map[int64]struct{}), routes: make(map[routeTopologyKey]struct{}), subscriptions: make(map[string]struct{})}
}

func (locks *topologyLockSet) addBinding(installationID int64) {
	locks.bindings[installationID] = struct{}{}
}

func (locks *topologyLockSet) addRoute(installationID, repositoryID int64, ref string) {
	locks.routes[routeTopologyKey{installationID: installationID, repositoryID: repositoryID, ref: ref}] = struct{}{}
}

func (locks *topologyLockSet) addRoutes(routes map[routeTopologyKey]struct{}) {
	for route := range routes {
		locks.routes[route] = struct{}{}
	}
}

func (locks *topologyLockSet) addSubscription(subscriptionID string) {
	locks.subscriptions[subscriptionID] = struct{}{}
}

func bindingTopologyShard(installationID int64) int16 {
	digest := sha256.Sum256([]byte(fmt.Sprintf("rig.relay.topology.binding.v1\x00%d", installationID)))
	return int16(binary.BigEndian.Uint64(digest[:8]) % topologyShardCount)
}

func routeTopologyShard(installationID, repositoryID int64, ref string) int16 {
	digest := sha256.Sum256([]byte(fmt.Sprintf("rig.relay.topology.route.v1\x00%d\x00%d\x00%s", installationID, repositoryID, ref)))
	return int16(binary.BigEndian.Uint64(digest[:8]) % topologyShardCount)
}

func subscriptionTopologyShard(subscriptionID string) int16 {
	digest := sha256.Sum256([]byte("rig.relay.topology.subscription.v1\x00" + subscriptionID))
	return int16(binary.BigEndian.Uint64(digest[:8]) % topologyShardCount)
}

func (locks topologyLockSet) shardIDs() []int16 {
	shards := make(map[int16]struct{}, len(locks.bindings)+len(locks.routes)+len(locks.subscriptions))
	for installationID := range locks.bindings {
		shards[bindingTopologyShard(installationID)] = struct{}{}
	}
	for route := range locks.routes {
		shards[routeTopologyShard(route.installationID, route.repositoryID, route.ref)] = struct{}{}
	}
	for subscriptionID := range locks.subscriptions {
		shards[subscriptionTopologyShard(subscriptionID)] = struct{}{}
	}
	ordered := make([]int16, 0, len(shards))
	for shard := range shards {
		ordered = append(ordered, shard)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	return ordered
}

func acquireTopologyLocks(ctx context.Context, tx pgx.Tx, locks topologyLockSet) error {
	shards := locks.shardIDs()
	if len(shards) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT shard_id FROM relay_topology_lock_shards WHERE shard_id=ANY($1::smallint[]) ORDER BY shard_id FOR UPDATE`, shards)
	if err != nil {
		return err
	}
	defer rows.Close()
	matched := 0
	for rows.Next() {
		var shard int16
		if err = rows.Scan(&shard); err != nil {
			return err
		}
		if matched >= len(shards) || shard != shards[matched] {
			return ErrConflict
		}
		matched++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if matched != len(shards) {
		return ErrConflict
	}
	return nil
}

func queryRouteTopologyKeys(ctx context.Context, tx pgx.Tx, query string, args ...any) (map[routeTopologyKey]struct{}, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := make(map[routeTopologyKey]struct{})
	for rows.Next() {
		var installationID, repositoryID int64
		var ref string
		if err = rows.Scan(&installationID, &repositoryID, &ref); err != nil {
			return nil, err
		}
		keys[routeTopologyKey{installationID: installationID, repositoryID: repositoryID, ref: ref}] = struct{}{}
	}
	return keys, rows.Err()
}

func topologyRoutesCovered(locked topologyLockSet, current map[routeTopologyKey]struct{}) bool {
	for route := range current {
		if _, ok := locked.routes[route]; !ok {
			return false
		}
	}
	return true
}
func validateRefSHA(ref, sha string) error {
	if err := protocol.ValidRef(ref); err != nil {
		return fmt.Errorf("%w: ref", ErrInvalid)
	}
	if err := protocol.ValidSHA(sha); err != nil {
		return fmt.Errorf("%w: SHA", ErrInvalid)
	}
	return nil
}
func rollback(ctx context.Context, tx pgx.Tx) {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(rollbackCtx)
}
func conflictError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "23505" || pgErr.Code == "23514" || pgErr.Code == "23503") {
		return ErrConflict
	}
	return err
}

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
	codePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
)

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
func subscriptionLockKey(id string) int64 {
	parsed := uuid.MustParse(id)
	b := parsed[:]
	return int64(uint64(b[0])<<56 | uint64(b[1])<<48 | uint64(b[2])<<40 | uint64(b[3])<<32 | uint64(b[4])<<24 | uint64(b[5])<<16 | uint64(b[6])<<8 | uint64(b[7]))
}
func routeLockKey(installationID, repositoryID int64, ref string) int64 {
	digest := sha256.Sum256([]byte(fmt.Sprintf("rig.relay.route.v1\x00%d\x00%d\x00%s", installationID, repositoryID, ref)))
	return int64(binary.BigEndian.Uint64(digest[:8]))
}
func bindingLockKey(installationID int64) int64 {
	digest := sha256.Sum256([]byte(fmt.Sprintf("rig.relay.binding.v1\x00%d", installationID)))
	return int64(binary.BigEndian.Uint64(digest[:8]))
}
func deliveryLockKey(deliveryID string) int64 {
	digest := sha256.Sum256([]byte("rig.relay.delivery.v1\x00" + deliveryID))
	return int64(binary.BigEndian.Uint64(digest[:8]))
}
func acquireAdvisoryLocks(ctx context.Context, tx pgx.Tx, keys map[int64]struct{}) error {
	ordered := make([]int64, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	for _, key := range ordered {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
			return err
		}
	}
	return nil
}
func queryRouteLockKeys(ctx context.Context, tx pgx.Tx, query string, args ...any) (map[int64]struct{}, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := make(map[int64]struct{})
	for rows.Next() {
		var installationID, repositoryID int64
		var ref string
		if err = rows.Scan(&installationID, &repositoryID, &ref); err != nil {
			return nil, err
		}
		keys[routeLockKey(installationID, repositoryID, ref)] = struct{}{}
	}
	return keys, rows.Err()
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

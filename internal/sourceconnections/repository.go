package sourceconnections

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrNotFound        = errors.New("source connection not found")
	ErrIdentityExists  = errors.New("source identity is already connected")
	ErrStaleGeneration = errors.New("source credential generation is stale")
)

type Repository struct{ db *sql.DB }

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

func (repository *Repository) CreatePending(ctx context.Context, owner string, expiresAt time.Time, interval time.Duration, nextPollAt, now time.Time) (Connection, error) {
	id, err := newConnectionID()
	if err != nil {
		return Connection{}, err
	}
	_, err = repository.db.ExecContext(ctx, `INSERT INTO source_connections(id, owner_user_id, provider, status, pending_expires_at, poll_interval_seconds, next_poll_at, created_at, updated_at) VALUES (?, ?, 'github', 'pending', ?, ?, ?, ?, ?)`, id, owner, timestamp(expiresAt), int(interval/time.Second), timestamp(nextPollAt), timestamp(now), timestamp(now))
	if err != nil {
		return Connection{}, err
	}
	return repository.Get(ctx, owner, id)
}

func (repository *Repository) Get(ctx context.Context, owner, id string) (Connection, error) {
	row := repository.db.QueryRowContext(ctx, connectionSelect+` WHERE owner_user_id = ? AND id = ?`, owner, id)
	connection, err := scanConnection(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Connection{}, ErrNotFound
	}
	return connection, err
}

func (repository *Repository) List(ctx context.Context, owner string) ([]Connection, error) {
	rows, err := repository.db.QueryContext(ctx, connectionSelect+` WHERE owner_user_id = ? ORDER BY created_at DESC, id`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Connection
	for rows.Next() {
		connection, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, connection)
	}
	return result, rows.Err()
}

func (repository *Repository) AdvancePoll(ctx context.Context, owner, id string, interval time.Duration, nextPollAt, now time.Time) error {
	result, err := repository.db.ExecContext(ctx, `UPDATE source_connections SET poll_interval_seconds = ?, next_poll_at = ?, updated_at = ? WHERE owner_user_id = ? AND id = ? AND status = 'pending'`, int(interval/time.Second), timestamp(nextPollAt), timestamp(now), owner, id)
	return mutationResult(result, err)
}

func (repository *Repository) MarkTerminal(ctx context.Context, owner, id, status, code string, now time.Time) error {
	if status != StatusDenied && status != StatusExpired && status != StatusAccessLost {
		return errors.New("invalid terminal connection status")
	}
	result, err := repository.db.ExecContext(ctx, `UPDATE source_connections SET status = ?, pending_expires_at = NULL, poll_interval_seconds = NULL, next_poll_at = NULL, last_error_code = ?, updated_at = ? WHERE owner_user_id = ? AND id = ?`, status, nullable(code), timestamp(now), owner, id)
	return mutationResult(result, err)
}

func (repository *Repository) Connect(ctx context.Context, owner, id string, bundle TokenBundle, now time.Time) error {
	result, err := repository.db.ExecContext(ctx, `UPDATE source_connections SET status = 'connected', provider_user_id = ?, provider_login = ?, credential_generation = ?, pending_expires_at = NULL, poll_interval_seconds = NULL, next_poll_at = NULL, access_expires_at = ?, refresh_expires_at = ?, last_error_code = NULL, connected_at = COALESCE(connected_at, ?), disconnected_at = NULL, updated_at = ? WHERE owner_user_id = ? AND id = ? AND status IN ('pending','connected','access_lost') AND credential_generation < ?`, bundle.ProviderUserID, bundle.ProviderLogin, bundle.Generation, timestamp(bundle.AccessExpiresAt), timestamp(bundle.RefreshExpiresAt), timestamp(now), timestamp(now), owner, id, bundle.Generation)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return ErrIdentityExists
		}
		return err
	}
	count, countErr := result.RowsAffected()
	if countErr != nil {
		return countErr
	}
	if count == 0 {
		if _, getErr := repository.Get(ctx, owner, id); getErr != nil {
			return getErr
		}
		return ErrStaleGeneration
	}
	return nil
}

func (repository *Repository) Disconnect(ctx context.Context, owner, id string, now time.Time) error {
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM github_installations WHERE connection_id IN (SELECT id FROM source_connections WHERE owner_user_id = ? AND id = ?)`, owner, id); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE source_connections SET status = 'disconnected', pending_expires_at = NULL, poll_interval_seconds = NULL, next_poll_at = NULL, provider_user_id = NULL, provider_login = NULL, credential_generation = 0, access_expires_at = NULL, refresh_expires_at = NULL, last_error_code = NULL, disconnected_at = COALESCE(disconnected_at, ?), updated_at = ? WHERE owner_user_id = ? AND id = ?`, timestamp(now), timestamp(now), owner, id)
	if err != nil {
		return err
	}
	if err := mutationResult(result, nil); err != nil {
		return err
	}
	return tx.Commit()
}

// UpsertInstallationPage updates display metadata for only the returned provider
// page. It deliberately does not prune rows from other or partial pages.
func (repository *Repository) UpsertInstallationPage(ctx context.Context, owner, id string, installations []Installation) error {
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owned int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM source_connections WHERE owner_user_id = ? AND id = ? AND status = 'connected'`, owner, id).Scan(&owned); err != nil {
		return err
	}
	if owned != 1 {
		return ErrNotFound
	}
	for _, installation := range installations {
		_, err := tx.ExecContext(ctx, `INSERT INTO github_installations(connection_id, installation_id, account_login, account_type, target_type, repository_selection, suspended_at, cached_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(connection_id, installation_id) DO UPDATE SET account_login = excluded.account_login, account_type = excluded.account_type, target_type = excluded.target_type, repository_selection = excluded.repository_selection, suspended_at = excluded.suspended_at, cached_at = excluded.cached_at`, id, installation.ID, installation.AccountLogin, installation.AccountType, installation.TargetType, installation.RepositorySelection, nullableTime(installation.SuspendedAt), timestamp(installation.CachedAt))
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

const connectionSelect = `SELECT id, owner_user_id, status, provider_user_id, provider_login, credential_generation, pending_expires_at, poll_interval_seconds, next_poll_at, access_expires_at, refresh_expires_at, last_error_code, connected_at, disconnected_at, created_at, updated_at FROM source_connections`

type rowScanner interface{ Scan(...any) error }

func scanConnection(row rowScanner) (Connection, error) {
	var connection Connection
	var providerUserID, providerLogin, pendingExpiresAt, nextPollAt, accessExpiresAt, refreshExpiresAt, lastErrorCode, connectedAt, disconnectedAt sql.NullString
	var pollInterval sql.NullInt64
	var createdAt, updatedAt string
	err := row.Scan(&connection.ID, &connection.OwnerUserID, &connection.Status, &providerUserID, &providerLogin, &connection.CredentialGeneration, &pendingExpiresAt, &pollInterval, &nextPollAt, &accessExpiresAt, &refreshExpiresAt, &lastErrorCode, &connectedAt, &disconnectedAt, &createdAt, &updatedAt)
	if err != nil {
		return Connection{}, err
	}
	connection.ProviderUserID = providerUserID.String
	connection.ProviderLogin = providerLogin.String
	connection.LastErrorCode = lastErrorCode.String
	if pollInterval.Valid {
		connection.PollInterval = time.Duration(pollInterval.Int64) * time.Second
	}
	for _, item := range []struct {
		raw    sql.NullString
		target **time.Time
	}{{pendingExpiresAt, &connection.PendingExpiresAt}, {nextPollAt, &connection.NextPollAt}, {accessExpiresAt, &connection.AccessExpiresAt}, {refreshExpiresAt, &connection.RefreshExpiresAt}, {connectedAt, &connection.ConnectedAt}, {disconnectedAt, &connection.DisconnectedAt}} {
		if !item.raw.Valid {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, item.raw.String)
		if err != nil {
			return Connection{}, fmt.Errorf("parse connection timestamp: %w", err)
		}
		*item.target = &parsed
	}
	connection.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Connection{}, err
	}
	connection.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	return connection, err
}

func newConnectionID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func mutationResult(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func timestamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return timestamp(*value)
}

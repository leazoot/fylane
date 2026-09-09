// Package pgstore is the PostgreSQL implementation of the relay's metadata
// store (authsrv.Store plus device status/usage metrics). It holds metadata
// only: no file bodies, diffs, listings, paths, or sensitive
// names — such data must never gain a column here.
package pgstore

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/leazoot/fylane/shared/authsrv"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store implements authsrv.Store over PostgreSQL. Errors from the underlying
// database are swallowed into the interface's boolean results only where the
// interface demands it; mutating calls surface them.
type Store struct {
	db *sql.DB
}

// Open connects to the PostgreSQL database at dsn and applies pending
// migrations (versioned, forward-only).
func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening relay database: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connecting to relay database: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database pool.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL
		)`); err != nil {
		return fmt.Errorf("creating schema_migrations: %w", err)
	}
	entries, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(entries)
	var current int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("reading migration version: %w", err)
	}
	for _, name := range entries {
		base := strings.TrimPrefix(name, "migrations/")
		prefix, _, ok := strings.Cut(base, "_")
		if !ok {
			return fmt.Errorf("migration %s: name must be <version>_<description>.sql", name)
		}
		version, err := strconv.Atoi(prefix)
		if err != nil || version <= 0 {
			return fmt.Errorf("migration %s: invalid version prefix", name)
		}
		if version <= current {
			continue
		}
		body, err := migrationsFS.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("applying migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, applied_at) VALUES ($1, now())`, version); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// --- authsrv.Store ---

func (s *Store) CreateDevice(d *authsrv.Device) error {
	_, err := s.db.Exec(`INSERT INTO devices (id, secret_hash, name, created_at) VALUES ($1, $2, $3, $4)`,
		d.ID, d.SecretHash, d.Name, d.CreatedAt.UTC())
	return err
}

// lookupErr translates database errors into the store contract: a missing
// row is authsrv.ErrNotFound; anything else is an infrastructure failure
// the caller must surface as retryable, never as an invalid credential.
func lookupErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return authsrv.ErrNotFound
	}
	return fmt.Errorf("relay store: %w", err)
}

func (s *Store) GetDevice(id string) (*authsrv.Device, error) {
	var d authsrv.Device
	err := s.db.QueryRow(`SELECT id, secret_hash, name, created_at FROM devices WHERE id = $1`, id).
		Scan(&d.ID, &d.SecretHash, &d.Name, &d.CreatedAt)
	if err != nil {
		return nil, lookupErr(err)
	}
	return &d, nil
}

func (s *Store) PutPairingCode(c *authsrv.PairingCode) error {
	_, err := s.db.Exec(`INSERT INTO pairing_codes (code, device_id, expires_at) VALUES ($1, $2, $3)`,
		c.Code, c.DeviceID, c.ExpiresAt.UTC())
	return err
}

func (s *Store) TakePairingCode(code string, now time.Time) (*authsrv.PairingCode, error) {
	var c authsrv.PairingCode
	err := s.db.QueryRow(
		`DELETE FROM pairing_codes WHERE code = $1 RETURNING code, device_id, expires_at`, code).
		Scan(&c.Code, &c.DeviceID, &c.ExpiresAt)
	if err != nil {
		return nil, lookupErr(err)
	}
	if now.After(c.ExpiresAt) {
		return nil, authsrv.ErrNotFound
	}
	return &c, nil
}

func (s *Store) CreateClient(c *authsrv.Client) error {
	uris, err := json.Marshal(c.RedirectURIs)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO clients (id, name, redirect_uris, created_at) VALUES ($1, $2, $3, $4)`,
		c.ID, c.Name, uris, c.CreatedAt.UTC())
	return err
}

func (s *Store) GetClient(id string) (*authsrv.Client, error) {
	var c authsrv.Client
	var uris []byte
	err := s.db.QueryRow(`SELECT id, name, redirect_uris, created_at FROM clients WHERE id = $1`, id).
		Scan(&c.ID, &c.Name, &uris, &c.CreatedAt)
	if err != nil {
		return nil, lookupErr(err)
	}
	if err := json.Unmarshal(uris, &c.RedirectURIs); err != nil {
		return nil, fmt.Errorf("relay store: decoding redirect_uris: %w", err)
	}
	return &c, nil
}

func (s *Store) PutAuthRequest(r *authsrv.AuthRequest) error {
	_, err := s.db.Exec(`INSERT INTO auth_requests (id, client_id, redirect_uri, code_challenge, state, verify_code, device_id, continue_nonce, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (id) DO UPDATE SET expires_at = excluded.expires_at`,
		r.ID, r.ClientID, r.RedirectURI, r.CodeChallenge, r.State, r.VerifyCode, r.DeviceID, r.NonceHash, r.ExpiresAt.UTC())
	return err
}

func (s *Store) GetAuthRequest(id string, now time.Time) (*authsrv.AuthRequest, error) {
	var r authsrv.AuthRequest
	err := s.db.QueryRow(
		`SELECT id, client_id, redirect_uri, code_challenge, state, verify_code, device_id, continue_nonce, expires_at
		 FROM auth_requests WHERE id = $1`, id).
		Scan(&r.ID, &r.ClientID, &r.RedirectURI, &r.CodeChallenge, &r.State, &r.VerifyCode, &r.DeviceID, &r.NonceHash, &r.ExpiresAt)
	if err != nil {
		return nil, lookupErr(err)
	}
	if now.After(r.ExpiresAt) {
		return nil, authsrv.ErrNotFound
	}
	return &r, nil
}

func (s *Store) BindAuthRequest(id, deviceID, nonceHash string, now time.Time) error {
	res, err := s.db.Exec(
		`UPDATE auth_requests SET device_id = $2, continue_nonce = $3
		 WHERE id = $1 AND device_id = '' AND expires_at > $4`,
		id, deviceID, nonceHash, now.UTC())
	if err != nil {
		return fmt.Errorf("relay store: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return authsrv.ErrNotFound
	}
	return nil
}

func (s *Store) TakeAuthRequest(id string, now time.Time) (*authsrv.AuthRequest, error) {
	var r authsrv.AuthRequest
	err := s.db.QueryRow(
		`DELETE FROM auth_requests WHERE id = $1
		 RETURNING id, client_id, redirect_uri, code_challenge, state, verify_code, device_id, continue_nonce, expires_at`, id).
		Scan(&r.ID, &r.ClientID, &r.RedirectURI, &r.CodeChallenge, &r.State, &r.VerifyCode, &r.DeviceID, &r.NonceHash, &r.ExpiresAt)
	if err != nil {
		return nil, lookupErr(err)
	}
	if now.After(r.ExpiresAt) {
		return nil, authsrv.ErrNotFound
	}
	return &r, nil
}

func (s *Store) PutAuthCode(c *authsrv.AuthCode) error {
	_, err := s.db.Exec(`INSERT INTO auth_codes (code, client_id, device_id, redirect_uri, code_challenge, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		c.Code, c.ClientID, c.DeviceID, c.RedirectURI, c.CodeChallenge, c.ExpiresAt.UTC())
	return err
}

func (s *Store) TakeAuthCode(code string, now time.Time) (*authsrv.AuthCode, error) {
	var c authsrv.AuthCode
	err := s.db.QueryRow(
		`DELETE FROM auth_codes WHERE code = $1
		 RETURNING code, client_id, device_id, redirect_uri, code_challenge, expires_at`, code).
		Scan(&c.Code, &c.ClientID, &c.DeviceID, &c.RedirectURI, &c.CodeChallenge, &c.ExpiresAt)
	if err != nil {
		return nil, lookupErr(err)
	}
	if now.After(c.ExpiresAt) {
		return nil, authsrv.ErrNotFound
	}
	return &c, nil
}

func (s *Store) PutAccessToken(t *authsrv.AccessToken) error {
	_, err := s.db.Exec(`INSERT INTO access_tokens (token, device_id, client_id, expires_at)
		VALUES ($1, $2, $3, $4)`, t.Token, t.DeviceID, t.ClientID, t.ExpiresAt.UTC())
	return err
}

func (s *Store) GetAccessToken(token string, now time.Time) (*authsrv.AccessToken, error) {
	var t authsrv.AccessToken
	err := s.db.QueryRow(
		`SELECT token, device_id, client_id, expires_at FROM access_tokens WHERE token = $1`, token).
		Scan(&t.Token, &t.DeviceID, &t.ClientID, &t.ExpiresAt)
	if err != nil {
		return nil, lookupErr(err)
	}
	if now.After(t.ExpiresAt) {
		return nil, authsrv.ErrNotFound
	}
	return &t, nil
}

func (s *Store) PutRefreshToken(t *authsrv.RefreshToken) error {
	_, err := s.db.Exec(`INSERT INTO refresh_tokens (token, family, device_id, client_id, expires_at, used, revoked)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		t.Token, t.Family, t.DeviceID, t.ClientID, t.ExpiresAt.UTC(), t.Used, t.Revoked)
	return err
}

func (s *Store) GetRefreshToken(token string) (*authsrv.RefreshToken, error) {
	var t authsrv.RefreshToken
	err := s.db.QueryRow(
		`SELECT token, family, device_id, client_id, expires_at, used, revoked
		 FROM refresh_tokens WHERE token = $1`, token).
		Scan(&t.Token, &t.Family, &t.DeviceID, &t.ClientID, &t.ExpiresAt, &t.Used, &t.Revoked)
	if err != nil {
		return nil, lookupErr(err)
	}
	return &t, nil
}

func (s *Store) MarkRefreshTokenUsed(token string) error {
	_, err := s.db.Exec(`UPDATE refresh_tokens SET used = TRUE WHERE token = $1`, token)
	return err
}

func (s *Store) RevokeRefreshFamily(family string) (int, error) {
	res, err := s.db.Exec(`UPDATE refresh_tokens SET revoked = TRUE WHERE family = $1 AND NOT revoked`, family)
	if err != nil {
		return 0, fmt.Errorf("relay store: revoking refresh family: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// --- Device status and usage metrics (online status, version, call
// counts, error codes, latency) ---

// SetDeviceOnline records tunnel connect/disconnect.
func (s *Store) SetDeviceOnline(ctx context.Context, deviceID string, online bool, version string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE devices SET online = $2, version = COALESCE(NULLIF($3, ''), version), last_online_at = now()
		 WHERE id = $1`, deviceID, online, version)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("unknown device")
	}
	return nil
}

// RecordCall accumulates per-device usage metrics. errorCode is empty for
// successful calls; non-empty codes are also counted per code so Beta failure
// distributions are queryable, not just the latest code.
func (s *Store) RecordCall(ctx context.Context, deviceID string, latency time.Duration, errorCode string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO device_stats (device_id, call_count, error_count, last_error_code, total_latency_ms, last_call_at)
		 VALUES ($1, 1, CASE WHEN $2 = '' THEN 0 ELSE 1 END, $2, $3, now())
		 ON CONFLICT (device_id) DO UPDATE SET
			call_count = device_stats.call_count + 1,
			error_count = device_stats.error_count + CASE WHEN $2 = '' THEN 0 ELSE 1 END,
			last_error_code = CASE WHEN $2 = '' THEN device_stats.last_error_code ELSE $2 END,
			total_latency_ms = device_stats.total_latency_ms + $3,
			last_call_at = now()`,
		deviceID, errorCode, latency.Milliseconds()); err != nil {
		return err
	}
	if errorCode != "" {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO device_error_codes (device_id, error_code, hits, last_at)
			 VALUES ($1, $2, 1, now())
			 ON CONFLICT (device_id, error_code) DO UPDATE SET
				hits = device_error_codes.hits + 1,
				last_at = now()`,
			deviceID, errorCode); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RecordConnect counts one successful tunnel connection. Together with
// call/error counts this makes the Beta connection-success and reconnect-churn
// rates queryable from metadata alone.
func (s *Store) RecordConnect(ctx context.Context, deviceID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO device_stats (device_id, connect_count)
		 VALUES ($1, 1)
		 ON CONFLICT (device_id) DO UPDATE SET
			connect_count = device_stats.connect_count + 1`,
		deviceID)
	return err
}

// DeviceMetrics is the queryable Beta metric set for one device:
// connection counts, call success/failure, latency, and the per-code failure
// breakdown. Success rate = (CallCount-ErrorCount)/CallCount; mean call
// latency = TotalLatencyMS/CallCount (for write calls this latency includes
// the local approval wait, making it the remote proxy for approval latency —
// the precise measure lives in the Companion's local audit log).
type DeviceMetrics struct {
	DeviceID       string
	Online         bool
	Version        string
	ConnectCount   int64
	CallCount      int64
	ErrorCount     int64
	LastErrorCode  string
	TotalLatencyMS int64
	ErrorCodes     map[string]int64
}

// Metrics returns per-device Beta metrics for every device that has stats.
func (s *Store) Metrics(ctx context.Context) ([]*DeviceMetrics, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT d.id, d.online, d.version, st.connect_count, st.call_count,
			st.error_count, st.last_error_code, st.total_latency_ms
		 FROM devices d JOIN device_stats st ON st.device_id = d.id
		 ORDER BY d.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[string]*DeviceMetrics{}
	var out []*DeviceMetrics
	for rows.Next() {
		m := &DeviceMetrics{ErrorCodes: map[string]int64{}}
		if err := rows.Scan(&m.DeviceID, &m.Online, &m.Version, &m.ConnectCount,
			&m.CallCount, &m.ErrorCount, &m.LastErrorCode, &m.TotalLatencyMS); err != nil {
			return nil, err
		}
		byID[m.DeviceID] = m
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	codes, err := s.db.QueryContext(ctx,
		`SELECT device_id, error_code, hits FROM device_error_codes`)
	if err != nil {
		return nil, err
	}
	defer codes.Close()
	for codes.Next() {
		var id, code string
		var hits int64
		if err := codes.Scan(&id, &code, &hits); err != nil {
			return nil, err
		}
		if m := byID[id]; m != nil {
			m.ErrorCodes[code] = hits
		}
	}
	return out, codes.Err()
}

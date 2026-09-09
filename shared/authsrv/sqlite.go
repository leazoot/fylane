package authsrv

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

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// SQLiteStore is the single-file implementation of Store. It exists so the
// authorization server can run without PostgreSQL: the Companion serving its
// own OAuth endpoints, and a self-hosted relay that should stay one binary
// plus one file. The PostgreSQL store remains the hosted deployment.
//
// Only OAuth/pairing metadata is stored — the same column set as the relay's
// PostgreSQL schema.
type SQLiteStore struct {
	db *sql.DB
}

// OpenSQLite opens (creating if needed) the database at path and applies
// pending migrations. Migrations are versioned and forward-only.
func OpenSQLite(ctx context.Context, path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening auth database: %w", err)
	}
	// One connection serializes writers; WAL keeps readers non-blocking.
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("applying %s: %w", pragma, err)
		}
	}
	s := &SQLiteStore{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying database.
func (s *SQLiteStore) Close() error { return s.db.Close() }

func (s *SQLiteStore) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`); err != nil {
		return fmt.Errorf("creating schema_migrations: %w", err)
	}

	entries, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("listing migrations: %w", err)
	}
	sort.Strings(entries)

	var current int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("reading migration version: %w", err)
	}

	var latest int
	for _, name := range entries {
		version, err := migrationVersion(name)
		if err != nil {
			return err
		}
		if version <= latest {
			return fmt.Errorf("migration %s: version %d out of order", name, version)
		}
		latest = version
		if version <= current {
			continue
		}
		body, err := migrationsFS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("reading migration %s: %w", name, err)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("beginning migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("applying migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			version, sqlTime(time.Now())); err != nil {
			tx.Rollback()
			return fmt.Errorf("recording migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("committing migration %s: %w", name, err)
		}
	}
	if current > latest {
		return fmt.Errorf("auth schema version %d is newer than this build (max %d)", current, latest)
	}
	return nil
}

func migrationVersion(name string) (int, error) {
	base := strings.TrimPrefix(name, "migrations/")
	prefix, _, ok := strings.Cut(base, "_")
	if !ok {
		return 0, fmt.Errorf("migration %s: name must be <version>_<description>.sql", name)
	}
	version, err := strconv.Atoi(prefix)
	if err != nil || version <= 0 {
		return 0, fmt.Errorf("migration %s: invalid version prefix %q", name, prefix)
	}
	return version, nil
}

// Timestamps are stored as RFC 3339 UTC strings, as in the Companion store.

func sqlTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseSQLTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("auth store: parsing timestamp: %w", err)
	}
	return t, nil
}

// sqliteLookupErr translates database errors into the Store contract: a
// missing row is ErrNotFound; anything else is an infrastructure failure the
// caller must surface as retryable, never as an invalid credential.
func sqliteLookupErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return fmt.Errorf("auth store: %w", err)
}

// --- Devices ---

func (s *SQLiteStore) CreateDevice(d *Device) error {
	_, err := s.db.Exec(`INSERT INTO devices (id, secret_hash, name, created_at) VALUES (?, ?, ?, ?)`,
		d.ID, d.SecretHash, d.Name, sqlTime(d.CreatedAt))
	return err
}

func (s *SQLiteStore) GetDevice(id string) (*Device, error) {
	var d Device
	var created string
	err := s.db.QueryRow(`SELECT id, secret_hash, name, created_at FROM devices WHERE id = ?`, id).
		Scan(&d.ID, &d.SecretHash, &d.Name, &created)
	if err != nil {
		return nil, sqliteLookupErr(err)
	}
	if d.CreatedAt, err = parseSQLTime(created); err != nil {
		return nil, err
	}
	return &d, nil
}

// --- Pairing codes ---

func (s *SQLiteStore) PutPairingCode(c *PairingCode) error {
	_, err := s.db.Exec(`INSERT INTO pairing_codes (code, device_id, expires_at) VALUES (?, ?, ?)`,
		c.Code, c.DeviceID, sqlTime(c.ExpiresAt))
	return err
}

func (s *SQLiteStore) TakePairingCode(code string, now time.Time) (*PairingCode, error) {
	var c PairingCode
	var expires string
	err := s.take(
		`SELECT code, device_id, expires_at FROM pairing_codes WHERE code = ?`,
		`DELETE FROM pairing_codes WHERE code = ?`, code,
		func(row *sql.Row) error { return row.Scan(&c.Code, &c.DeviceID, &expires) })
	if err != nil {
		return nil, err
	}
	if c.ExpiresAt, err = parseSQLTime(expires); err != nil {
		return nil, err
	}
	if now.After(c.ExpiresAt) {
		return nil, ErrNotFound
	}
	return &c, nil
}

// take reads a single-use record and deletes it in one transaction, so two
// concurrent redemptions of the same code cannot both succeed.
func (s *SQLiteStore) take(selectSQL, deleteSQL, key string, scan func(*sql.Row) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("auth store: %w", err)
	}
	defer tx.Rollback()
	if err := scan(tx.QueryRow(selectSQL, key)); err != nil {
		return sqliteLookupErr(err)
	}
	if _, err := tx.Exec(deleteSQL, key); err != nil {
		return fmt.Errorf("auth store: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("auth store: %w", err)
	}
	return nil
}

// --- Clients ---

func (s *SQLiteStore) CreateClient(c *Client) error {
	uris, err := json.Marshal(c.RedirectURIs)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO clients (id, name, redirect_uris, created_at) VALUES (?, ?, ?, ?)`,
		c.ID, c.Name, string(uris), sqlTime(c.CreatedAt))
	return err
}

func (s *SQLiteStore) GetClient(id string) (*Client, error) {
	var c Client
	var uris, created string
	err := s.db.QueryRow(`SELECT id, name, redirect_uris, created_at FROM clients WHERE id = ?`, id).
		Scan(&c.ID, &c.Name, &uris, &created)
	if err != nil {
		return nil, sqliteLookupErr(err)
	}
	if err := json.Unmarshal([]byte(uris), &c.RedirectURIs); err != nil {
		return nil, fmt.Errorf("auth store: decoding redirect_uris: %w", err)
	}
	if c.CreatedAt, err = parseSQLTime(created); err != nil {
		return nil, err
	}
	return &c, nil
}

// --- Authorization requests ---

const authRequestColumns = `id, client_id, redirect_uri, code_challenge, state,
	verify_code, device_id, continue_nonce, expires_at`

func scanAuthRequest(row *sql.Row) (*AuthRequest, error) {
	var r AuthRequest
	var expires string
	if err := row.Scan(&r.ID, &r.ClientID, &r.RedirectURI, &r.CodeChallenge, &r.State,
		&r.VerifyCode, &r.DeviceID, &r.NonceHash, &expires); err != nil {
		return nil, err
	}
	var err error
	if r.ExpiresAt, err = parseSQLTime(expires); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *SQLiteStore) PutAuthRequest(r *AuthRequest) error {
	// The authorize handler re-arms an existing request after a wrong pairing
	// code, so this is an upsert of the deadline only.
	_, err := s.db.Exec(`INSERT INTO auth_requests (`+authRequestColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET expires_at = excluded.expires_at`,
		r.ID, r.ClientID, r.RedirectURI, r.CodeChallenge, r.State,
		r.VerifyCode, r.DeviceID, r.NonceHash, sqlTime(r.ExpiresAt))
	return err
}

func (s *SQLiteStore) GetAuthRequest(id string, now time.Time) (*AuthRequest, error) {
	r, err := scanAuthRequest(s.db.QueryRow(
		`SELECT `+authRequestColumns+` FROM auth_requests WHERE id = ?`, id))
	if err != nil {
		return nil, sqliteLookupErr(err)
	}
	if now.After(r.ExpiresAt) {
		return nil, ErrNotFound
	}
	return r, nil
}

func (s *SQLiteStore) BindAuthRequest(id, deviceID, nonceHash string, now time.Time) error {
	res, err := s.db.Exec(
		`UPDATE auth_requests SET device_id = ?, continue_nonce = ?
		 WHERE id = ? AND device_id = '' AND expires_at > ?`,
		deviceID, nonceHash, id, sqlTime(now))
	if err != nil {
		return fmt.Errorf("auth store: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) TakeAuthRequest(id string, now time.Time) (*AuthRequest, error) {
	var out *AuthRequest
	err := s.take(
		`SELECT `+authRequestColumns+` FROM auth_requests WHERE id = ?`,
		`DELETE FROM auth_requests WHERE id = ?`, id,
		func(row *sql.Row) error {
			var err error
			out, err = scanAuthRequest(row)
			return err
		})
	if err != nil {
		return nil, err
	}
	if now.After(out.ExpiresAt) {
		return nil, ErrNotFound
	}
	return out, nil
}

// --- Authorization codes ---

func (s *SQLiteStore) PutAuthCode(c *AuthCode) error {
	_, err := s.db.Exec(`INSERT INTO auth_codes (code, client_id, device_id, redirect_uri, code_challenge, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		c.Code, c.ClientID, c.DeviceID, c.RedirectURI, c.CodeChallenge, sqlTime(c.ExpiresAt))
	return err
}

func (s *SQLiteStore) TakeAuthCode(code string, now time.Time) (*AuthCode, error) {
	var c AuthCode
	var expires string
	err := s.take(
		`SELECT code, client_id, device_id, redirect_uri, code_challenge, expires_at
		 FROM auth_codes WHERE code = ?`,
		`DELETE FROM auth_codes WHERE code = ?`, code,
		func(row *sql.Row) error {
			return row.Scan(&c.Code, &c.ClientID, &c.DeviceID, &c.RedirectURI, &c.CodeChallenge, &expires)
		})
	if err != nil {
		return nil, err
	}
	if c.ExpiresAt, err = parseSQLTime(expires); err != nil {
		return nil, err
	}
	if now.After(c.ExpiresAt) {
		return nil, ErrNotFound
	}
	return &c, nil
}

// --- Tokens ---

func (s *SQLiteStore) PutAccessToken(t *AccessToken) error {
	_, err := s.db.Exec(`INSERT INTO access_tokens (token, device_id, client_id, expires_at)
		VALUES (?, ?, ?, ?)`, t.Token, t.DeviceID, t.ClientID, sqlTime(t.ExpiresAt))
	return err
}

func (s *SQLiteStore) GetAccessToken(token string, now time.Time) (*AccessToken, error) {
	var t AccessToken
	var expires string
	err := s.db.QueryRow(
		`SELECT token, device_id, client_id, expires_at FROM access_tokens WHERE token = ?`, token).
		Scan(&t.Token, &t.DeviceID, &t.ClientID, &expires)
	if err != nil {
		return nil, sqliteLookupErr(err)
	}
	if t.ExpiresAt, err = parseSQLTime(expires); err != nil {
		return nil, err
	}
	if now.After(t.ExpiresAt) {
		return nil, ErrNotFound
	}
	return &t, nil
}

func (s *SQLiteStore) PutRefreshToken(t *RefreshToken) error {
	_, err := s.db.Exec(`INSERT INTO refresh_tokens (token, family, device_id, client_id, expires_at, used, revoked)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.Token, t.Family, t.DeviceID, t.ClientID, sqlTime(t.ExpiresAt), t.Used, t.Revoked)
	return err
}

func (s *SQLiteStore) GetRefreshToken(token string) (*RefreshToken, error) {
	var t RefreshToken
	var expires string
	err := s.db.QueryRow(
		`SELECT token, family, device_id, client_id, expires_at, used, revoked
		 FROM refresh_tokens WHERE token = ?`, token).
		Scan(&t.Token, &t.Family, &t.DeviceID, &t.ClientID, &expires, &t.Used, &t.Revoked)
	if err != nil {
		return nil, sqliteLookupErr(err)
	}
	if t.ExpiresAt, err = parseSQLTime(expires); err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *SQLiteStore) MarkRefreshTokenUsed(token string) error {
	_, err := s.db.Exec(`UPDATE refresh_tokens SET used = 1 WHERE token = ?`, token)
	return err
}

func (s *SQLiteStore) RevokeRefreshFamily(family string) (int, error) {
	res, err := s.db.Exec(`UPDATE refresh_tokens SET revoked = 1 WHERE family = ? AND revoked = 0`, family)
	if err != nil {
		return 0, fmt.Errorf("authsrv store: revoking refresh family: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// PurgeExpired deletes records whose lifetime has passed and reports how many
// rows went. Expired records are already treated as absent by every read, so
// this is housekeeping — but on a long-running install it is the difference
// between a file that grows forever and one that does not. Refresh tokens are
// kept until expiry even when used or revoked: reuse detection needs them.
func (s *SQLiteStore) PurgeExpired(ctx context.Context, now time.Time) (int64, error) {
	cutoff := sqlTime(now)
	var total int64
	for _, table := range []string{"pairing_codes", "auth_requests", "auth_codes", "access_tokens", "refresh_tokens"} {
		res, err := s.db.ExecContext(ctx, `DELETE FROM `+table+` WHERE expires_at < ?`, cutoff)
		if err != nil {
			return total, fmt.Errorf("auth store: purging %s: %w", table, err)
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

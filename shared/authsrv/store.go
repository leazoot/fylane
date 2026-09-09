package authsrv

import (
	"errors"
	"sync"
	"time"
)

// Store is the persistence boundary of the auth server. Three implementations
// exist: MemoryStore below (tests and throwaway runs), SQLiteStore (a
// Companion or a self-hosted relay that should stay one binary and one file),
// and the relay's PostgreSQL store. Only OAuth/pairing metadata lives here —
// never file bodies or paths (database rules).
type Store interface {
	CreateDevice(d *Device) error
	GetDevice(id string) (*Device, error)

	PutPairingCode(c *PairingCode) error
	// TakePairingCode consumes a live pairing code (single use).
	TakePairingCode(code string, now time.Time) (*PairingCode, error)

	CreateClient(c *Client) error
	GetClient(id string) (*Client, error)

	PutAuthRequest(r *AuthRequest) error
	TakeAuthRequest(id string, now time.Time) (*AuthRequest, error)
	// GetAuthRequest reads a pending request without consuming it.
	GetAuthRequest(id string, now time.Time) (*AuthRequest, error)
	// BindAuthRequest attaches an approving device and the continuation
	// nonce hash to a pending, still-unbound request (push pairing).
	BindAuthRequest(id, deviceID, nonceHash string, now time.Time) error

	PutAuthCode(c *AuthCode) error
	TakeAuthCode(code string, now time.Time) (*AuthCode, error)

	PutAccessToken(t *AccessToken) error
	GetAccessToken(token string, now time.Time) (*AccessToken, error)

	PutRefreshToken(t *RefreshToken) error
	GetRefreshToken(token string) (*RefreshToken, error)
	MarkRefreshTokenUsed(token string) error
	// RevokeRefreshFamily invalidates every refresh token of a family
	// (rotation-reuse response) and returns how many were revoked.
	//
	// The error is not decoration: a store that could not perform the
	// revocation leaves the family live, and a count of zero cannot say
	// whether that happened or whether there was nothing left to revoke.
	// Reporting the first as the second turns an outage into a line that
	// reads like a handled incident.
	RevokeRefreshFamily(family string) (int, error)
}

// ErrNotFound reports that a looked-up record does not exist (or expired).
// Any other lookup error means the store itself failed — callers MUST answer
// with a retryable 5xx, never an OAuth invalid_* code: telling a platform a
// credential is invalid because the database was down makes it discard a
// perfectly good token.
var ErrNotFound = errors.New("not found")

// Device is a paired Companion installation.
type Device struct {
	ID         string
	SecretHash string
	Name       string
	CreatedAt  time.Time
}

// PairingCode links a short-lived human-readable code to a device.
type PairingCode struct {
	Code      string
	DeviceID  string
	ExpiresAt time.Time
}

// Client is a dynamically registered OAuth client (public, PKCE-only).
type Client struct {
	ID           string
	Name         string
	RedirectURIs []string
	CreatedAt    time.Time
}

// AuthRequest is a pending /authorize request awaiting either the typed
// pairing code or a push-pairing approval from the device.
type AuthRequest struct {
	ID            string
	ClientID      string
	RedirectURI   string
	CodeChallenge string
	State         string
	// VerifyCode is the short comparison word shown on the pairing page and
	// in the Companion prompt so the user can spot a session-confusion
	// attack. It is display-only and grants nothing.
	VerifyCode string
	// DeviceID and NonceHash are set when a device approves via push
	// pairing; the continuation nonce is stored hashed (single use).
	DeviceID  string
	NonceHash string
	ExpiresAt time.Time
}

// AuthCode is a one-time authorization code bound to client, device, and
// PKCE challenge.
type AuthCode struct {
	Code          string
	ClientID      string
	DeviceID      string
	RedirectURI   string
	CodeChallenge string
	ExpiresAt     time.Time
}

// AccessToken is a short-lived bearer credential bound to a device.
type AccessToken struct {
	Token     string
	DeviceID  string
	ClientID  string
	ExpiresAt time.Time
}

// RefreshToken is a rotating long-lived credential. Used tokens stay stored
// so reuse (theft indicator) can be detected and the family revoked.
type RefreshToken struct {
	Token     string
	Family    string
	DeviceID  string
	ClientID  string
	ExpiresAt time.Time
	Used      bool
	Revoked   bool
}

// MemoryStore keeps everything in process memory.
type MemoryStore struct {
	mu       sync.Mutex
	devices  map[string]*Device
	pairing  map[string]*PairingCode
	clients  map[string]*Client
	authReqs map[string]*AuthRequest
	codes    map[string]*AuthCode
	access   map[string]*AccessToken
	refresh  map[string]*RefreshToken
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		devices:  map[string]*Device{},
		pairing:  map[string]*PairingCode{},
		clients:  map[string]*Client{},
		authReqs: map[string]*AuthRequest{},
		codes:    map[string]*AuthCode{},
		access:   map[string]*AccessToken{},
		refresh:  map[string]*RefreshToken{},
	}
}

func (m *MemoryStore) CreateDevice(d *Device) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.devices[d.ID] = d
	return nil
}

func (m *MemoryStore) GetDevice(id string) (*Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d, ok := m.devices[id]; ok {
		return d, nil
	}
	return nil, ErrNotFound
}

func (m *MemoryStore) PutPairingCode(c *PairingCode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pairing[c.Code] = c
	return nil
}

func (m *MemoryStore) TakePairingCode(code string, now time.Time) (*PairingCode, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.pairing[code]
	if !ok {
		return nil, ErrNotFound
	}
	delete(m.pairing, code)
	if now.After(c.ExpiresAt) {
		return nil, ErrNotFound
	}
	return c, nil
}

func (m *MemoryStore) CreateClient(c *Client) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clients[c.ID] = c
	return nil
}

func (m *MemoryStore) GetClient(id string) (*Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.clients[id]; ok {
		return c, nil
	}
	return nil, ErrNotFound
}

func (m *MemoryStore) PutAuthRequest(r *AuthRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.authReqs[r.ID] = r
	return nil
}

func (m *MemoryStore) GetAuthRequest(id string, now time.Time) (*AuthRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.authReqs[id]
	if !ok || now.After(r.ExpiresAt) {
		return nil, ErrNotFound
	}
	cp := *r
	return &cp, nil
}

func (m *MemoryStore) BindAuthRequest(id, deviceID, nonceHash string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.authReqs[id]
	if !ok || now.After(r.ExpiresAt) || r.DeviceID != "" {
		return ErrNotFound
	}
	r.DeviceID = deviceID
	r.NonceHash = nonceHash
	return nil
}

func (m *MemoryStore) TakeAuthRequest(id string, now time.Time) (*AuthRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.authReqs[id]
	if !ok {
		return nil, ErrNotFound
	}
	delete(m.authReqs, id)
	if now.After(r.ExpiresAt) {
		return nil, ErrNotFound
	}
	return r, nil
}

func (m *MemoryStore) PutAuthCode(c *AuthCode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.codes[c.Code] = c
	return nil
}

func (m *MemoryStore) TakeAuthCode(code string, now time.Time) (*AuthCode, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.codes[code]
	if !ok {
		return nil, ErrNotFound
	}
	delete(m.codes, code)
	if now.After(c.ExpiresAt) {
		return nil, ErrNotFound
	}
	return c, nil
}

func (m *MemoryStore) PutAccessToken(t *AccessToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.access[t.Token] = t
	return nil
}

func (m *MemoryStore) GetAccessToken(token string, now time.Time) (*AccessToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.access[token]
	if !ok || now.After(t.ExpiresAt) {
		return nil, ErrNotFound
	}
	return t, nil
}

func (m *MemoryStore) PutRefreshToken(t *RefreshToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refresh[t.Token] = t
	return nil
}

func (m *MemoryStore) GetRefreshToken(token string) (*RefreshToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.refresh[token]; ok {
		return t, nil
	}
	return nil, ErrNotFound
}

func (m *MemoryStore) MarkRefreshTokenUsed(token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.refresh[token]; ok {
		t.Used = true
	}
	return nil
}

func (m *MemoryStore) RevokeRefreshFamily(family string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, t := range m.refresh {
		if t.Family == family && !t.Revoked {
			t.Revoked = true
			n++
		}
	}
	return n, nil
}

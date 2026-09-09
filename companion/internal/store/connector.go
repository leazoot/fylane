package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Connector records one platform connection. TokenReference is a
// lookup key into the OS keychain / encrypted store — raw OAuth tokens or
// secrets must never be stored in this row.
type Connector struct {
	ID                int64     `json:"id"`
	Provider          string    `json:"provider"`
	RemoteConnectorID string    `json:"remote_connector_id"`
	Status            string    `json:"status"`
	Capabilities      []string  `json:"capabilities"`
	LastConnectedAt   time.Time `json:"last_connected_at,omitzero"`
	LastToolCallAt    time.Time `json:"last_tool_call_at,omitzero"`
	TokenReference    string    `json:"-"`
}

func (c *Connector) validate() error {
	switch {
	case c.Provider == "":
		return fmt.Errorf("connector: missing provider")
	case c.RemoteConnectorID == "":
		return fmt.Errorf("connector %s: missing remote connector id", c.Provider)
	case c.Status == "":
		return fmt.Errorf("connector %s/%s: missing status", c.Provider, c.RemoteConnectorID)
	}
	return nil
}

// UpsertConnector inserts the connector or, when (provider,
// remote_connector_id) already exists, replaces its mutable fields. The
// row ID is written back to c.ID.
//
// The two timestamps are the exception: a zero value leaves the stored one
// alone. They are written by two different events — pairing sets
// last_connected_at, a tool call sets last_tool_call_at — and neither knows
// the other's answer, so letting a caller's zero win would mean each event
// erased the other's record of when it happened.
func (s *Store) UpsertConnector(ctx context.Context, c *Connector) error {
	if err := c.validate(); err != nil {
		return err
	}
	capabilities, err := marshalStrings(c.Capabilities)
	if err != nil {
		return fmt.Errorf("connector %s/%s: encoding capabilities: %w", c.Provider, c.RemoteConnectorID, err)
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO connectors
			(provider, remote_connector_id, status, capabilities, last_connected_at, last_tool_call_at, token_reference)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (provider, remote_connector_id) DO UPDATE SET
			status = excluded.status,
			capabilities = excluded.capabilities,
			last_connected_at = COALESCE(excluded.last_connected_at, connectors.last_connected_at),
			last_tool_call_at = COALESCE(excluded.last_tool_call_at, connectors.last_tool_call_at),
			token_reference = excluded.token_reference
		RETURNING id`,
		c.Provider, c.RemoteConnectorID, c.Status, capabilities,
		formatNullableTime(c.LastConnectedAt), formatNullableTime(c.LastToolCallAt),
		c.TokenReference)
	if err := row.Scan(&c.ID); err != nil {
		return fmt.Errorf("upserting connector %s/%s: %w", c.Provider, c.RemoteConnectorID, err)
	}
	return nil
}

// GetConnector returns the connector for (provider, remoteConnectorID), or
// ErrNotFound.
func (s *Store) GetConnector(ctx context.Context, provider, remoteConnectorID string) (*Connector, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, provider, remote_connector_id, status, capabilities, last_connected_at, last_tool_call_at, token_reference
		FROM connectors WHERE provider = ? AND remote_connector_id = ?`,
		provider, remoteConnectorID)
	c, err := scanConnector(row)
	if err != nil {
		return nil, fmt.Errorf("getting connector %s/%s: %w", provider, remoteConnectorID, err)
	}
	return c, nil
}

// ListConnectors returns all connectors ordered by provider.
func (s *Store) ListConnectors(ctx context.Context) ([]*Connector, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, provider, remote_connector_id, status, capabilities, last_connected_at, last_tool_call_at, token_reference
		FROM connectors ORDER BY provider, remote_connector_id`)
	if err != nil {
		return nil, fmt.Errorf("listing connectors: %w", err)
	}
	defer rows.Close()
	var out []*Connector
	for rows.Next() {
		c, err := scanConnector(rows)
		if err != nil {
			return nil, fmt.Errorf("listing connectors: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing connectors: %w", err)
	}
	return out, nil
}

// DeleteConnector removes the connector row by ID.
func (s *Store) DeleteConnector(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM connectors WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting connector %d: %w", id, err)
	}
	return requireRow(res, "connector", fmt.Sprint(id))
}

func scanConnector(row interface{ Scan(...any) error }) (*Connector, error) {
	var (
		c                       Connector
		capabilities            string
		lastConnected, lastCall sql.NullString
	)
	err := row.Scan(&c.ID, &c.Provider, &c.RemoteConnectorID, &c.Status,
		&capabilities, &lastConnected, &lastCall, &c.TokenReference)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(capabilities), &c.Capabilities); err != nil {
		return nil, fmt.Errorf("decoding capabilities: %w", err)
	}
	if c.LastConnectedAt, err = parseNullableTime(lastConnected); err != nil {
		return nil, err
	}
	if c.LastToolCallAt, err = parseNullableTime(lastCall); err != nil {
		return nil, err
	}
	return &c, nil
}

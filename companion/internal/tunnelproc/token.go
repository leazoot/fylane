package tunnelproc

import (
	"fmt"
	"os"

	"github.com/zalando/go-keyring"
)

// A Cloudflare tunnel token is a credential, so it lives where credentials
// live: the OS keychain, or an environment variable for headless setups. It
// never goes into config.json and never appears in a log line.

const (
	keyringService = "fylane-companion"
	// TokenEnv overrides the stored token, for servers and containers where
	// there is no keychain to talk to.
	TokenEnv = "FYLANE_TUNNEL_PROVIDER_TOKEN"
)

func tokenKey(kind Kind) string { return "tunnel:" + string(kind) }

// SaveToken stores the provider token in the OS keychain.
func SaveToken(kind Kind, token string) error {
	if err := keyring.Set(keyringService, tokenKey(kind), token); err != nil {
		return fmt.Errorf("storing the tunnel token in the OS keychain: %w", err)
	}
	return nil
}

// LoadToken reads the provider token: the environment first, then the
// keychain. An absent token is not an error — most providers need none.
func LoadToken(kind Kind) (string, error) {
	if t := os.Getenv(TokenEnv); t != "" {
		return t, nil
	}
	t, err := keyring.Get(keyringService, tokenKey(kind))
	if err == keyring.ErrNotFound {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading the tunnel token from the OS keychain: %w", err)
	}
	return t, nil
}

// DeleteToken removes a stored provider token.
func DeleteToken(kind Kind) error {
	err := keyring.Delete(keyringService, tokenKey(kind))
	if err == nil || err == keyring.ErrNotFound {
		return nil
	}
	return fmt.Errorf("removing the tunnel token from the OS keychain: %w", err)
}

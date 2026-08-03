// Package authstore persists the OIDC tokens `tiny login` obtains.
// One JSON file, 0600, under the user config dir — the CLI equivalent
// of a browser session. Developer keys never touch this store.
package authstore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

type Credentials struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	// Workspace is the slug picked at login for accounts with several
	// workspaces; empty means "let the server use my only one".
	Workspace string `json:"workspace,omitempty"`
	// Issuer records which realm issued the tokens so login/refresh
	// stay consistent across --auth-url overrides.
	Issuer string `json:"issuer"`
}

var ErrNotLoggedIn = errors.New("not logged in — run `tiny login`")

func path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tiny", "auth.json"), nil
}

func Load() (*Credentials, error) {
	p, err := path()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotLoggedIn
		}
		return nil, err
	}
	var c Credentials
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if c.RefreshToken == "" && c.AccessToken == "" {
		return nil, ErrNotLoggedIn
	}
	return &c, nil
}

func Save(c *Credentials) error {
	p, err := path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

func Clear() error {
	p, err := path()
	if err != nil {
		return err
	}
	err = os.Remove(p)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

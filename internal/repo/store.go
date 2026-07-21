package repo

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// Store manages the configured repos and their cached indexes. It is the entry
// point the CLI and (later) the installer use.
type Store struct {
	cfg   *Config
	fetch fetcher // injectable for tests
}

// fetcher retrieves an index document from a URL.
type fetcher func(ctx context.Context, rawURL string) ([]byte, error)

// Open loads the repo config (seeding the default repo on first run).
func Open() (*Store, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, err
	}
	return &Store{cfg: cfg, fetch: httpFetch}, nil
}

// List returns the configured repos in order.
func (s *Store) List() []Repo { return s.cfg.Repos }

// Add registers a new repo. Names must be unique; URLs must be http(s).
func (s *Store) Add(name, rawURL string) error {
	if name == "" || rawURL == "" {
		return fmt.Errorf("repo name and url are required")
	}
	if u, err := url.Parse(rawURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("repo url must be http(s): %q", rawURL)
	}
	if s.cfg.find(name) >= 0 {
		return fmt.Errorf("repo %q already exists", name)
	}
	s.cfg.Repos = append(s.cfg.Repos, Repo{Name: name, URL: rawURL})
	return s.cfg.Save()
}

// Remove drops a repo and its cached index.
func (s *Store) Remove(name string) error {
	i := s.cfg.find(name)
	if i < 0 {
		return fmt.Errorf("repo %q not found", name)
	}
	s.cfg.Repos = append(s.cfg.Repos[:i], s.cfg.Repos[i+1:]...)
	if dir, err := cacheDir(); err == nil {
		_ = os.Remove(filepath.Join(dir, name+".yaml"))
	}
	return s.cfg.Save()
}

// Update fetches every configured repo's index and caches it. It returns the
// first fetch error but still caches whatever succeeded, so a single dead repo
// doesn't block the others.
func (s *Store) Update(ctx context.Context) error {
	dir, err := cacheDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var firstErr error
	for _, r := range s.cfg.Repos {
		data, err := s.fetch(ctx, r.URL)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("update %q: %w", r.Name, err)
			}
			continue
		}
		// Validate before caching so a corrupt fetch can't poison the cache.
		if _, err := ParseIndex(data); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("update %q: %w", r.Name, err)
			}
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, r.Name+".yaml"), data, 0o644); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("cache %q: %w", r.Name, err)
			}
		}
	}
	return firstErr
}

// Merged reads the cached indexes and returns a merged, resolvable view.
// Repos with no cached index yet (never updated) are skipped; the caller can
// suggest `tiny repo update`.
func (s *Store) Merged() (*Merged, error) {
	dir, err := cacheDir()
	if err != nil {
		return nil, err
	}
	order := make([]string, 0, len(s.cfg.Repos))
	byRepo := make(map[string]*Index, len(s.cfg.Repos))
	for _, r := range s.cfg.Repos {
		data, err := os.ReadFile(filepath.Join(dir, r.Name+".yaml"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		idx, err := ParseIndex(data)
		if err != nil {
			return nil, fmt.Errorf("cached index for %q: %w", r.Name, err)
		}
		order = append(order, r.Name)
		byRepo[r.Name] = idx
	}
	return NewMerged(order, byRepo), nil
}

// httpFetch is the default fetcher.
func httpFetch(ctx context.Context, rawURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", rawURL, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20)) // 32 MiB cap
}

package repo

import (
	"fmt"
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"
)

const (
	// DefaultRepoName is the baked-in repo added on first run. Removable.
	DefaultRepoName = "tinysystems"
	// DefaultRepoURL is the baked-in default index. Placeholder until hosting
	// is decided (design §9.2) — a static index on GitHub Pages / GHCR OCI.
	DefaultRepoURL = "https://tinysystems.github.io/modules/index.yaml"
)

// Repo is a configured repo: a unique name and its index URL.
type Repo struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Config is the client-side repo list, persisted at
// ${XDG_CONFIG_HOME:-~/.config}/tiny/repos.yaml.
type Config struct {
	Repos []Repo `json:"repos"`
}

// configPath returns the repos.yaml location.
func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tiny", "repos.yaml"), nil
}

// cacheDir returns the per-repo index cache directory.
func cacheDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tiny", "repos"), nil
}

// defaultConfig is the config seeded on first run: just the default repo.
func defaultConfig() *Config {
	return &Config{Repos: []Repo{{Name: DefaultRepoName, URL: DefaultRepoURL}}}
}

// LoadConfig reads repos.yaml, seeding the default repo when the file is
// absent. A present-but-empty file is respected (the user removed the default).
func LoadConfig() (*Config, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return defaultConfig(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}

// Save writes repos.yaml, creating the config dir if needed.
func (c *Config) Save() error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// find returns the index of a repo by name, or -1.
func (c *Config) find(name string) int {
	for i := range c.Repos {
		if c.Repos[i].Name == name {
			return i
		}
	}
	return -1
}

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
	// DefaultRepoURL is the baked-in default index — the tiny-systems/modules
	// repo, served raw off main. (A prettier GitHub Pages URL can replace this
	// once Pages is enabled; the raw URL is a plain-GET static file either way.)
	DefaultRepoURL = "https://raw.githubusercontent.com/tiny-systems/modules/main/index.yaml"
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

// tinyHome is tiny's single local-state folder: $TINY_HOME, else ~/.tiny — one
// discoverable place (repos, cache, prefs), like ~/.kube or ~/.docker, rather
// than scattering across the OS config/cache dirs. Cluster config
// (ingressClass, storage, broker) is NOT here — that lives in the cluster.
func tinyHome() (string, error) {
	if h := os.Getenv("TINY_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".tiny"), nil
}

// configPath returns the repos.yaml location (~/.tiny/repos.yaml).
func configPath() (string, error) {
	h, err := tinyHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "repos.yaml"), nil
}

// cacheDir returns the index cache directory (~/.tiny/cache).
func cacheDir() (string, error) {
	h, err := tinyHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "cache"), nil
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

// Package config loads rig's user configuration and provider credentials.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Agent struct {
	Socket string   `yaml:"socket"` // fixed-path agent socket rig starts on demand
	Keys   []string `yaml:"keys"`   // private keys loaded into it (passphrase-less)
}

type SSH struct {
	User         string `yaml:"user"`
	PublicKey    string `yaml:"public_key"`
	ForwardAgent *bool  `yaml:"forward_agent"`
	Agent        *Agent `yaml:"agent"`
}

type VolumeSpec struct {
	Name   string `yaml:"name"`
	SizeGB int    `yaml:"size_gb"`
	Type   string `yaml:"type"` // provider volume type, e.g. NVMe_Shared
}

type Profile struct {
	Types    []string    `yaml:"types"` // preference order, first available wins
	Spot     bool        `yaml:"spot"`
	Location string      `yaml:"location"` // optional pin, e.g. FIN-01
	Image    string      `yaml:"image"`    // empty = provider default for the type
	Volume   *VolumeSpec `yaml:"volume"`
	Ports    []int       `yaml:"ports"`
	TTL      string      `yaml:"ttl"`
	EnvFiles []string    `yaml:"env_files"` // pushed to remote ~/.rig/env
	OSGB     int         `yaml:"os_volume_gb"`
}

type Defaults struct {
	Image string `yaml:"image"`
	TTL   string `yaml:"ttl"`
	OSGB  int    `yaml:"os_volume_gb"`
}

type Config struct {
	Provider        string             `yaml:"provider"`
	CredentialsFile string             `yaml:"credentials_file"`
	SSH             SSH                `yaml:"ssh"`
	Defaults        Defaults           `yaml:"defaults"`
	Profiles        map[string]Profile `yaml:"profiles"`

	path string
}

func DefaultPath() string {
	if p := os.Getenv("RIG_CONFIG"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "rig", "config.yml")
}

func Load() (*Config, error) {
	cfg := &Config{
		Provider: "verda",
		SSH:      SSH{User: "root"},
		Defaults: Defaults{TTL: "12h"},
		path:     DefaultPath(),
	}
	data, err := os.ReadFile(cfg.path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil // config is optional; env creds may suffice
		}
		return nil, err
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", cfg.path, err)
	}
	if cfg.Provider == "" {
		cfg.Provider = "verda"
	}
	if cfg.SSH.User == "" {
		cfg.SSH.User = "root"
	}
	if cfg.Defaults.TTL == "" {
		cfg.Defaults.TTL = "12h"
	}
	return cfg, nil
}

func (c *Config) Path() string { return c.path }

// Profile returns the named profile with defaults applied.
func (c *Config) Profile(name string) (Profile, error) {
	p, ok := c.Profiles[name]
	if !ok {
		var names []string
		for n := range c.Profiles {
			names = append(names, n)
		}
		return Profile{}, fmt.Errorf("profile %q not found in %s (have: %s)", name, c.path, strings.Join(names, ", "))
	}
	if p.Image == "" {
		p.Image = c.Defaults.Image
	}
	if p.TTL == "" {
		p.TTL = c.Defaults.TTL
	}
	if p.OSGB == 0 {
		p.OSGB = c.Defaults.OSGB
	}
	return p, nil
}

func (p Profile) TTLDuration() (time.Duration, error) {
	if p.TTL == "" {
		return 12 * time.Hour, nil
	}
	return time.ParseDuration(p.TTL)
}

func (c *Config) ForwardAgent() bool {
	if c.SSH.ForwardAgent == nil {
		return true
	}
	return *c.SSH.ForwardAgent
}

// PublicKey returns the SSH public key to register with the provider,
// from config or by scanning standard locations.
func (c *Config) PublicKey() (path, key string, err error) {
	candidates := []string{}
	if c.SSH.PublicKey != "" {
		candidates = append(candidates, ExpandHome(c.SSH.PublicKey))
	} else {
		home, _ := os.UserHomeDir()
		for _, n := range []string{"id_ed25519.pub", "id_rsa.pub", "id_ecdsa.pub"} {
			candidates = append(candidates, filepath.Join(home, ".ssh", n))
		}
		// fall back to any .pub in ~/.ssh
		if m, _ := filepath.Glob(filepath.Join(home, ".ssh", "*.pub")); m != nil {
			candidates = append(candidates, m...)
		}
	}
	for _, p := range candidates {
		if b, err := os.ReadFile(p); err == nil && strings.HasPrefix(string(b), "ssh-") {
			return p, strings.TrimSpace(string(b)), nil
		}
	}
	return "", "", fmt.Errorf("no SSH public key found; set ssh.public_key in %s", c.path)
}

// IdentityFile returns the private key matching ssh.public_key, if present
// on disk, so connections work even without a live ssh-agent.
func (c *Config) IdentityFile() string {
	if c.SSH.PublicKey == "" {
		return ""
	}
	priv := strings.TrimSuffix(ExpandHome(c.SSH.PublicKey), ".pub")
	if _, err := os.Stat(priv); err == nil {
		return priv
	}
	return ""
}

// Credentials resolves provider credentials: environment first, then
// credentials_file (a KEY=VALUE env file, `export` prefixes allowed).
func (c *Config) Credentials() (id, secret string, err error) {
	id, secret = os.Getenv("VERDA_CLIENT_ID"), os.Getenv("VERDA_CLIENT_SECRET")
	if id != "" && secret != "" {
		return id, secret, nil
	}
	if c.CredentialsFile == "" {
		return "", "", fmt.Errorf("VERDA_CLIENT_ID/VERDA_CLIENT_SECRET not set and no credentials_file in %s", c.path)
	}
	env, err := ParseEnvFile(ExpandHome(c.CredentialsFile))
	if err != nil {
		return "", "", err
	}
	id, secret = env["VERDA_CLIENT_ID"], env["VERDA_CLIENT_SECRET"]
	if id == "" || secret == "" {
		return "", "", fmt.Errorf("%s missing VERDA_CLIENT_ID/VERDA_CLIENT_SECRET", c.CredentialsFile)
	}
	return id, secret, nil
}

func ParseEnvFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return out, nil
}

func ExpandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

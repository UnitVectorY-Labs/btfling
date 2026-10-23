package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"
)

type Peer struct {
	Name      string `yaml:"name" json:"name"`
	ID        string `yaml:"id" json:"id"`
	PublicKey []byte `yaml:"public_key" json:"public_key"`
}
type Config struct {
	Version    int    `yaml:"version"`
	Name       string `yaml:"name"`
	ID         string `yaml:"id"`
	PrivateKey []byte `yaml:"private_key"`
	Peers      []Peer `yaml:"peers,omitempty"`
}

func DefaultDir() (string, error) {
	h, e := os.UserHomeDir()
	return filepath.Join(h, ".config", "btfling"), e
}
func ValidName(s string) bool {
	if len(s) == 0 || len(s) > 64 || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}
func (c *Config) Validate() error {
	if c.Version != 1 || !ValidName(c.Name) {
		return errors.New("invalid config version or nickname")
	}
	if b, e := hex.DecodeString(c.ID); e != nil || len(b) != 16 {
		return errors.New("invalid local device ID")
	}
	if len(c.PrivateKey) != ed25519.SeedSize {
		return errors.New("invalid private key seed")
	}
	names, ids := map[string]bool{}, map[string]bool{}
	for _, p := range c.Peers {
		b, e := hex.DecodeString(p.ID)
		if !ValidName(p.Name) || e != nil || len(b) != 16 || p.ID == c.ID || names[p.Name] || ids[p.ID] || len(p.PublicKey) != ed25519.PublicKeySize {
			return errors.New("invalid or duplicate peer")
		}
		names[p.Name] = true
		ids[p.ID] = true
	}
	return nil
}
func Load(dir string) (*Config, error) {
	b, e := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if e != nil {
		return nil, e
	}
	c := new(Config)
	d := yaml.NewDecoder(strings.NewReader(string(b)))
	d.KnownFields(true)
	if e = d.Decode(c); e != nil {
		return nil, e
	}
	return c, c.Validate()
}

// LoadOrInit creates a device identity only when config.yaml is absent.
// Invalid or unreadable existing configurations are returned as errors.
func LoadOrInit(dir string) (*Config, bool, error) {
	c, err := Load(dir)
	if err == nil {
		return c, false, nil
	}
	if !os.IsNotExist(err) {
		return nil, false, err
	}
	// Preserve dangling symlinks and other existing directory entries too.
	if _, err := os.Lstat(filepath.Join(dir, "config.yaml")); err == nil {
		return nil, false, errors.New("configuration path exists but cannot be loaded")
	} else if !os.IsNotExist(err) {
		return nil, false, err
	}
	hostname, _ := os.Hostname()
	c, err = Init(dir, defaultName(hostname))
	return c, err == nil, err
}
func defaultName(hostname string) string {
	var name strings.Builder
	for _, r := range strings.TrimSpace(hostname) {
		if unicode.IsControl(r) || r == '/' || r == '\\' {
			r = '-'
		}
		if name.Len()+len(string(r)) > 64 {
			break
		}
		name.WriteRune(r)
	}
	value := strings.TrimSpace(name.String())
	if value == "" {
		return "btfling"
	}
	return value
}
func Init(dir, name string) (*Config, error) {
	if !ValidName(name) {
		return nil, errors.New("nickname must be 1–64 characters without control characters or slashes")
	}
	if _, e := os.Lstat(filepath.Join(dir, "config.yaml")); e == nil {
		return nil, errors.New("configuration already exists")
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	id := make([]byte, 16)
	if _, e := rand.Read(id); e != nil {
		return nil, e
	}
	c := &Config{Version: 1, Name: name, ID: hex.EncodeToString(id)}
	if e := c.Keygen(); e != nil {
		return nil, e
	}
	return c, c.Save(dir)
}
func (c *Config) Save(dir string) error {
	if e := c.Validate(); e != nil {
		return e
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	if e := os.Chmod(dir, 0700); e != nil {
		return e
	}
	b, e := yaml.Marshal(c)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, ".config-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(f.Name(), filepath.Join(dir, "config.yaml"))
}
func (c *Config) Keygen() error {
	if len(c.PrivateKey) != 0 {
		return nil
	}
	c.PrivateKey = make([]byte, ed25519.SeedSize)
	_, e := rand.Read(c.PrivateKey)
	return e
}
func (c *Config) PublicKey() []byte {
	if len(c.PrivateKey) == 0 {
		return nil
	}
	return ed25519.NewKeyFromSeed(c.PrivateKey).Public().(ed25519.PublicKey)
}
func Fingerprint(pub []byte) string { h := sha256.Sum256(pub); return hex.EncodeToString(h[:]) }
func (c *Config) Peer(name string) (Peer, error) {
	for _, p := range c.Peers {
		if p.Name == name {
			return p, nil
		}
	}
	return Peer{}, fmt.Errorf("unknown peer %q; run pair first", name)
}
func (c *Config) Add(p Peer) error {
	for _, old := range c.Peers {
		if old.Name == p.Name || old.ID == p.ID {
			return errors.New("peer name or device already exists; remove it explicitly before replacing trust")
		}
	}
	c.Peers = append(c.Peers, p)
	return c.Validate()
}

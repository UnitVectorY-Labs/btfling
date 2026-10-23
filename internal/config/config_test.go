package config

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestConfigRoundTripAndTrust(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "btfling")
	c, e := Init(dir, "alice")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Init(dir, "overwrite"); e == nil {
		t.Fatal("overwrote config")
	}
	if e = c.Keygen(); e != nil {
		t.Fatal(e)
	}
	pub := c.PublicKey()
	if e = c.Keygen(); e != nil || !bytes.Equal(pub, c.PublicKey()) {
		t.Fatal("rotated key")
	}
	p := Peer{Name: "bob", ID: "0123456789abcdef0123456789abcdef", PublicKey: bytes.Repeat([]byte{7}, 32)}
	if e = c.Add(p); e != nil {
		t.Fatal(e)
	}
	if e = c.Add(p); e == nil {
		t.Fatal("silently replaced trust")
	}
	c.Peers = c.Peers[:1]
	if e = c.Save(dir); e != nil {
		t.Fatal(e)
	}
	got, e := Load(dir)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(got.PublicKey(), pub) || got.Peers[0].Name != "bob" {
		t.Fatal("round-trip mismatch")
	}
	if runtime.GOOS != "windows" {
		s, e := os.Stat(filepath.Join(dir, "config.yaml"))
		if e != nil {
			t.Fatal(e)
		}
		if s.Mode().Perm() != 0600 {
			t.Fatal("private config permissions", s.Mode())
		}
	}
}
func TestInvalidConfig(t *testing.T) {
	for _, s := range []string{"", " leading", "trailing ", "../file", "a\nb", "a\x1bb", "a\\b"} {
		if ValidName(s) {
			t.Fatal("accepted nickname", s)
		}
	}
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("version: 1\nunknown: true\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := Load(dir); e == nil {
		t.Fatal("accepted unknown config fields")
	}
}

func TestLoadOrInitPreservesConfiguration(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new")
	c, created, e := LoadOrInit(dir)
	if e != nil || !created {
		t.Fatalf("create: %v %v", created, e)
	}
	if !ValidName(c.Name) || len(c.PrivateKey) != 32 {
		t.Fatal("invalid automatic defaults")
	}
	c.Name = "my-device"
	if e = c.Keygen(); e != nil {
		t.Fatal(e)
	}
	if e = c.Save(dir); e != nil {
		t.Fatal(e)
	}
	before, e := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if e != nil {
		t.Fatal(e)
	}
	loaded, created, e := LoadOrInit(dir)
	if e != nil || created || loaded.ID != c.ID {
		t.Fatalf("reload: %v %v", created, e)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if !bytes.Equal(before, after) {
		t.Fatal("existing identity or key changed")
	}
	if e = os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("invalid: ["), 0600); e != nil {
		t.Fatal(e)
	}
	if _, _, e = LoadOrInit(dir); e == nil {
		t.Fatal("silently replaced invalid config")
	}
	after, _ = os.ReadFile(filepath.Join(dir, "config.yaml"))
	if string(after) != "invalid: [" {
		t.Fatal("overwrote invalid file")
	}
}
func TestDefaultName(t *testing.T) {
	for _, hostname := range []string{"", "   ", "my-laptop.local", " x/y\\z\n ", string(bytes.Repeat([]byte("é"), 80))} {
		if name := defaultName(hostname); !ValidName(name) {
			t.Fatalf("invalid default nickname %q", name)
		}
	}
}

package ble

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeHelperFile(t *testing.T, path string) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte("test helper"), 0755); e != nil {
		t.Fatal(e)
	}
}
func makeCheckout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module btfling\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.MkdirAll(filepath.Join(dir, "internal", "ble", "native"), 0755); e != nil {
		t.Fatal(e)
	}
	return dir
}
func TestResolveHelperForGoRun(t *testing.T) {
	for _, goos := range []string{"darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			dir := makeCheckout(t)
			name := "btfling-ble"
			if goos == "windows" {
				name += ".exe"
			}
			executable := filepath.Join(t.TempDir(), "go-build", "c7", "cached-d", "btfling")
			if _, e := resolveHelper(executable, dir, "", goos); e == nil || !strings.Contains(e.Error(), "go run ./internal/buildhelper -out .") {
				t.Fatal("missing source-build instructions", e)
			}
			dist := filepath.Join(dir, "dist", name)
			makeHelperFile(t, dist)
			got, e := resolveHelper(executable, dir, "", goos)
			if e != nil || got != dist {
				t.Fatalf("dist fallback: %q %v", got, e)
			}
			root := filepath.Join(dir, name)
			makeHelperFile(t, root)
			got, e = resolveHelper(executable, dir, "", goos)
			if e != nil || got != root {
				t.Fatalf("source helper: %q %v", got, e)
			}
			sibling := filepath.Join(filepath.Dir(executable), name)
			makeHelperFile(t, sibling)
			got, e = resolveHelper(executable, dir, "", goos)
			if e != nil || got != sibling {
				t.Fatalf("sibling preference: %q %v", got, e)
			}
			override := filepath.Join(t.TempDir(), name)
			makeHelperFile(t, override)
			got, e = resolveHelper(executable, dir, override, goos)
			if e != nil || got != override {
				t.Fatalf("override: %q %v", got, e)
			}
			if _, e = resolveHelper(executable, dir, override+"-missing", goos); e == nil {
				t.Fatal("ignored bad explicit override")
			}
		})
	}
}
func TestInstalledCommandDoesNotLoadWorkingDirectoryHelper(t *testing.T) {
	dir := makeCheckout(t)
	makeHelperFile(t, filepath.Join(dir, "btfling-ble"))
	installed := filepath.Join(t.TempDir(), "bin", "btfling")
	if _, e := resolveHelper(installed, dir, "", "darwin"); e == nil {
		t.Fatal("installed command trusted working directory helper")
	}
	cached := filepath.Join(t.TempDir(), "go-build", "cache", "btfling")
	if _, e := resolveHelper(cached, t.TempDir(), "", "darwin"); e == nil {
		t.Fatal("accepted directory without source markers")
	}
}
func TestGoBuildExecutable(t *testing.T) {
	for _, p := range []string{"/tmp/go-build123/b001/exe/btfling", "/Users/test/Library/Caches/go-build/c7/cached-d/btfling"} {
		if !goBuildExecutable(p) {
			t.Fatal("missed cache path", p)
		}
	}
	for _, p := range []string{"/usr/local/bin/btfling", "/apps/go-build-tools/btfling"} {
		if goBuildExecutable(p) {
			t.Fatal("treated installed path as Go cache", p)
		}
	}
}

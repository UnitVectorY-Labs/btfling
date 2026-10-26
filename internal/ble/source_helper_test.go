package ble

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestSourceHelperCache(t *testing.T) {
	root, cache := t.TempDir(), t.TempDir()
	for _, file := range []string{"internal/buildhelper/main.go", "internal/ble/native/adapter.swift", "internal/ble/native/Info.plist"} {
		makeHelperFile(t, filepath.Join(root, file))
	}
	builds := 0
	build := func(out string) error {
		builds++
		return os.WriteFile(filepath.Join(out, "btfling-ble"), []byte("fresh helper"), 0700)
	}
	var wg sync.WaitGroup
	paths := make(chan string, 8)
	for range 8 {
		wg.Go(func() {
			path, err := cachedSourceHelper(root, cache, "darwin", "arm64", build)
			if err != nil {
				t.Error(err)
				return
			}
			paths <- path
		})
	}
	wg.Wait()
	close(paths)
	if builds != 1 {
		t.Fatalf("concurrent scans/listeners triggered %d builds", builds)
	}
	var first string
	for path := range paths {
		if first != "" && path != first {
			t.Fatal("different cache paths for identical sources")
		}
		first = path
	}
	if err := os.WriteFile(filepath.Join(root, "internal/ble/native/adapter.swift"), []byte("updated source"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := cachedSourceHelper(root, cache, "darwin", "arm64", build)
	if err != nil || first == second || builds != 2 {
		t.Fatalf("stale source helper reused: %s %v builds=%d", second, err, builds)
	}
	failed := errors.New("compiler failed")
	if _, err := cachedSourceHelper(root, cache, "darwin", "amd64", func(string) error { return failed }); !errors.Is(err, failed) {
		t.Fatalf("lost build error: %v", err)
	}
	if _, err := cachedSourceHelper(root, cache, "darwin", "amd64", build); err != nil {
		t.Fatalf("failed build poisoned cache: %v", err)
	}
}

func TestSourceHelperStarts(t *testing.T) {
	if os.Getenv("BTFLING_TEST_NATIVE_BUILD") != "1" {
		t.Skip("set BTFLING_TEST_NATIVE_BUILD=1 to verify automatic source builds")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("no native helper on this OS")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	path, cleanup, err := prepareHelper(filepath.Join(t.TempDir(), "go-build123", "exe", "btfling"), root, "", runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	out, err := exec.Command(path, "--version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "btfling-ble wire-v1" {
		t.Fatalf("automatic helper startup: %v %s", err, out)
	}
}

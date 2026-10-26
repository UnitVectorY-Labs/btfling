package ble

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/UnitVectorY-Labs/btfling/internal/diagnostics"
)

var sourceBuildGate = make(chan struct{}, 1)

// Source runs build from the current sources rather than trusting an old helper
// left in the checkout. The cache survives go run's temporary executable.
func sourceHelper(ctx context.Context, workdir, goos string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return cachedSourceHelperContext(ctx, workdir, cache, goos, runtime.GOARCH, func(out string) error {
		cmd := exec.CommandContext(ctx, "go", "run", "./internal/buildhelper", "-os", goos, "-arch", runtime.GOARCH, "-out", out)
		cmd.WaitDelay = 2 * time.Second
		cmd.Dir = workdir
		var logs bytes.Buffer
		cmd.Stdout, cmd.Stderr = &logs, &logs
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("automatically build Bluetooth adapter (requires Xcode Command Line Tools on macOS or .NET SDK on Windows): %w\n%s", err, logs.String())
		}
		return nil
	})
}

func cachedSourceHelper(workdir, cache, goos, arch string, build func(string) error) (string, error) {
	return cachedSourceHelperContext(context.Background(), workdir, cache, goos, arch, build)
}
func cachedSourceHelperContext(ctx context.Context, workdir, cache, goos, arch string, build func(string) error) (string, error) {
	select {
	case sourceBuildGate <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-sourceBuildGate }()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	hash := sha256.New()
	fmt.Fprintf(hash, "%s\x00%s\x00%s\x00", goos, arch, runtime.Version())
	files := []string{"internal/buildhelper/main.go"}
	switch goos {
	case "darwin":
		files = append(files, "internal/ble/native/adapter.swift", "internal/ble/native/Info.plist")
	case "windows":
		entries, err := os.ReadDir(filepath.Join(workdir, "internal/ble/native"))
		if err != nil {
			return "", err
		}
		for _, entry := range entries {
			if ext := filepath.Ext(entry.Name()); !entry.IsDir() && (ext == ".cs" || ext == ".csproj") {
				files = append(files, filepath.Join("internal/ble/native", entry.Name()))
			}
		}
	default:
		return "", fmt.Errorf("unsupported native adapter platform: %s", goos)
	}
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(workdir, file))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(hash, "%s\x00%d\x00", file, len(data))
		hash.Write(data)
	}
	root := filepath.Join(cache, "btfling", "native", goos+"-"+arch)
	dir := filepath.Join(root, fmt.Sprintf("%x", hash.Sum(nil)))
	name := "btfling-ble"
	if goos == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	if err := checkHelper(path); err == nil {
		diagnostics.Emit(ctx, "info", "helper", "cache.hit", fmt.Sprintf("Cached source helper %s/%s source=%x", goos, arch, hash.Sum(nil)[:8]))
		return path, nil
	}
	diagnostics.Emit(ctx, "info", "helper", "build.start", fmt.Sprintf("Building source helper %s/%s source=%x", goos, arch, hash.Sum(nil)[:8]))
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(root, "build-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	if err := build(stage); err != nil {
		return "", err
	}
	if err := checkHelper(filepath.Join(stage, name)); err != nil {
		return "", fmt.Errorf("native adapter build output: %w", err)
	}
	if err := os.Rename(stage, dir); err != nil {
		// Another source process may have published the same build first.
		if existing := checkHelper(path); existing != nil {
			return "", fmt.Errorf("cache native adapter: %w", err)
		}
	}
	return path, nil
}

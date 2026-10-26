package ble

import (
	"fmt"
	"os"
	"path/filepath"
)

// Each helper gets a private directory so concurrent scanners/listeners cannot
// overwrite or delete an executable that another process is using.
func extractHelper(data []byte, name string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "btfling-ble-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("extract Bluetooth adapter: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0700); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("extract Bluetooth adapter: %w", err)
	}
	return path, cleanup, nil
}

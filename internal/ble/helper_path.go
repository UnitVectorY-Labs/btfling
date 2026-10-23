package ble

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Installed commands use their sibling helper. Only Go build-cache executables
// may also use a helper from the source checkout in the working directory.
func resolveHelper(executable, workdir, override, goos string) (string, error) {
	name := "btfling-ble"
	if goos == "windows" {
		name += ".exe"
	}
	if override != "" {
		path, err := filepath.Abs(override)
		if err != nil {
			return "", err
		}
		if err = checkHelper(path); err != nil {
			return "", fmt.Errorf("BTFLING_BLE_HELPER: %w", err)
		}
		return path, nil
	}
	sibling := filepath.Join(filepath.Dir(executable), name)
	if err := checkHelper(sibling); err == nil {
		return sibling, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if goBuildExecutable(executable) && sourceCheckout(workdir) {
		for _, path := range []string{filepath.Join(workdir, name), filepath.Join(workdir, "dist", name)} {
			if err := checkHelper(path); err == nil {
				return path, nil
			} else if !os.IsNotExist(err) {
				return "", err
			}
		}
		return "", fmt.Errorf("native Bluetooth adapter is missing; from this repository run `go run ./internal/buildhelper -out .`, then retry `go run .`; alternatively set BTFLING_BLE_HELPER to the adapter's absolute path")
	}
	return "", fmt.Errorf("native Bluetooth adapter is missing at %s; install %s beside the command, or set BTFLING_BLE_HELPER to its absolute path", sibling, name)
}
func checkHelper(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("native Bluetooth adapter is not a regular file: %s", path)
	}
	return nil
}
func sourceCheckout(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return false
	}
	fields := strings.Fields(string(b))
	if len(fields) < 2 || fields[0] != "module" || (fields[1] != "btfling" && fields[1] != "github.com/UnitVectorY-Labs/btfling") {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, "internal", "ble", "native"))
	return err == nil && info.IsDir()
}
func goBuildExecutable(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == "go-build" {
			return true
		} // newer persistent Go build cache
		if strings.HasPrefix(part, "go-build") {
			suffix := strings.TrimPrefix(part, "go-build")
			digits := len(suffix) > 0
			for _, r := range suffix {
				if r < '0' || r > '9' {
					digits = false
					break
				}
			}
			if digits {
				return true
			} // older temporary go-buildNNNN directories
		}
	}
	return false
}

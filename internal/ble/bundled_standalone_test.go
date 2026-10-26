//go:build (windows || darwin) && standalone

package ble

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Start the actual embedded native adapter without requesting Bluetooth access.
// This catches missing runtime libraries and incorrect target architectures.
func TestBundledHelperStarts(t *testing.T) {
	dir := t.TempDir()
	path, cleanup, err := prepareHelper(filepath.Join(dir, "btfling"), dir, "", runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	out, err := exec.Command(path, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("embedded adapter startup: %v: %s", err, out)
	}
	if strings.TrimSpace(string(out)) != "btfling-ble wire-v1" {
		t.Fatalf("unexpected version: %s", out)
	}
}

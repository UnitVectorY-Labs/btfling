package ble

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractHelperIsolationAndCleanup(t *testing.T) {
	for _, name := range []string{"btfling-ble", "btfling-ble.exe"} {
		t.Run(name, func(t *testing.T) {
			payload := []byte("embedded adapter")
			first, cleanFirst, err := extractHelper(payload, name)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cleanFirst)
			second, cleanSecond, err := extractHelper(payload, name)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cleanSecond)
			if first == second || filepath.Base(first) != name {
				t.Fatal("helper paths are not isolated")
			}
			cleanFirst()
			if _, err := os.Stat(filepath.Dir(first)); !os.IsNotExist(err) {
				t.Fatalf("directory not removed: %v", err)
			}
			got, err := os.ReadFile(second)
			if err != nil || string(got) != string(payload) {
				t.Fatalf("other helper damaged: %q %v", got, err)
			}
		})
	}
}

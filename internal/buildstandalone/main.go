// Build a single executable, including the platform native BLE helper.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func main() {
	out := flag.String("out", "", "output executable (default dist/btfling-OS-ARCH[.exe])")
	platform := flag.String("os", runtime.GOOS, "target OS: darwin or windows")
	arch := flag.String("arch", runtime.GOARCH, "target architecture: amd64 or arm64")
	flag.Parse()
	if err := build(*out, *platform, *arch); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func build(out, platform, arch string) error {
	if platform != "darwin" && platform != "windows" {
		return fmt.Errorf("standalone builds support darwin and windows")
	}
	if arch != "amd64" && arch != "arm64" {
		return fmt.Errorf("standalone builds support amd64 and arm64")
	}
	if platform == "darwin" && runtime.GOOS != "darwin" {
		return fmt.Errorf("macOS builds require macOS and Xcode Command Line Tools")
	}
	name, ext := "btfling-ble", ""
	if platform == "windows" {
		name += ".exe"
		ext = ".exe"
	}
	if out == "" {
		out = filepath.Join("dist", "btfling-"+platform+"-"+arch+ext)
	}

	// Do not overwrite a concurrent build's embedded payload.
	const bundle = "internal/ble/bundle"
	if err := os.Mkdir(bundle, 0700); err != nil {
		return fmt.Errorf("create staging directory (run from the repository root; remove stale %s if no build is running): %w", bundle, err)
	}
	defer os.RemoveAll(bundle)
	helperOut, err := os.MkdirTemp("", "btfling-standalone-build-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(helperOut)
	cmd := exec.Command("go", "run", "./internal/buildhelper", "-os", platform, "-arch", arch, "-out", helperOut)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build native Bluetooth adapter: %w", err)
	}
	payload, err := os.ReadFile(filepath.Join(helperOut, name))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(bundle, name), payload, 0600); err != nil {
		return err
	}
	if platform == runtime.GOOS && arch == runtime.GOARCH {
		check := exec.Command("go", "test", "-count=1", "-tags", "standalone", "./internal/ble", "-run", "^TestBundledHelperStarts$")
		check.Stdout, check.Stderr = os.Stdout, os.Stderr
		if err := check.Run(); err != nil {
			return fmt.Errorf("verify embedded adapter startup: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		return err
	}
	cmd = exec.Command("go", "build", "-trimpath", "-tags", "standalone", "-ldflags=-s -w", "-o", out, ".")
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "GOOS=") && !strings.HasPrefix(env, "GOARCH=") && !strings.HasPrefix(env, "CGO_ENABLED=") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "GOOS="+platform, "GOARCH="+arch, "CGO_ENABLED=0")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build standalone executable: %w", err)
	}
	fmt.Println("Standalone executable:", out)
	return nil
}

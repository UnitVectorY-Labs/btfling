// Build the native BLE adapter next to the btfling Go executable.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

func main() {
	out := flag.String("out", ".", "output directory")
	platform := flag.String("os", runtime.GOOS, "darwin or windows")
	arch := flag.String("arch", runtime.GOARCH, "target architecture")
	flag.Parse()
	abs, e := filepath.Abs(*out)
	if e != nil {
		panic(e)
	}
	if e = os.MkdirAll(abs, 0755); e != nil {
		panic(e)
	}
	var cmd *exec.Cmd
	switch *platform {
	case "darwin":
		cmd = exec.Command("swiftc", "-O", "-framework", "CoreBluetooth", "-framework", "Foundation", "internal/ble/native/adapter.swift", "-Xlinker", "-sectcreate", "-Xlinker", "__TEXT", "-Xlinker", "__info_plist", "-Xlinker", "internal/ble/native/Info.plist", "-o", filepath.Join(abs, "btfling-ble"))
	case "windows":
		rid := "win-x64"
		if *arch == "arm64" {
			rid = "win-arm64"
		} else if *arch != "amd64" {
			panic("Windows requires amd64 or arm64")
		}
		cmd = exec.Command("dotnet", "publish", "internal/ble/native/Adapter.csproj", "-c", "Release", "-r", rid, "--self-contained", "true", "-p:PublishSingleFile=true", "-p:IncludeNativeLibrariesForSelfExtract=true", "-o", abs)
	default:
		panic("native Bluetooth backend available for darwin and windows")
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if e = cmd.Run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}

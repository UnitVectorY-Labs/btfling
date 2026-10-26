---
layout: default
title: Installation
nav_order: 2
permalink: /install
---

# Installation

btfling requires macOS or Windows with a supported Bluetooth adapter. Standalone
builds embed the native Bluetooth helper, so users need only one executable.

## Build a standalone binary

Use Go 1.26+ and run from the repository root. macOS also needs Xcode Command Line
Tools (`xcode-select --install`):

```sh
go run ./internal/buildstandalone
./dist/btfling-darwin-arm64 tui
```

The default target matches the build machine. Use `-arch amd64` for an Intel Mac
or `-arch arm64` for Apple Silicon. macOS builds require a Mac. Cross-building
between Mac architectures also requires Swift compatibility libraries for the
target CPU; if your Command Line Tools omit them, build on a Mac of that
architecture. Build Windows x64 from either macOS or Windows with a .NET SDK
capable of targeting .NET 8:

```sh
go run ./internal/buildstandalone -os windows -arch amd64
```

Copy only `dist/btfling-windows-amd64.exe` to the Windows computer, then run:

```powershell
.\btfling-windows-amd64.exe tui
```

Use `-arch arm64` for Windows ARM64. Set `-out PATH` to choose the output filename.
Go and .NET are not required on target machines. The native helper is extracted
to a private temporary directory for each Bluetooth process and removed after
that process exits. A forced application termination may leave temporary files.
Windows also extracts the embedded .NET runtime to its runtime cache. The system
temporary directory must be writable and allow execution. No files need to be
installed beside the distributed binary. Build-time package restore may download
dependencies; runtime transfer uses only Bluetooth.

Native builds verify that the embedded helper starts without accessing Bluetooth.
Cross-builds compile the target binary but cannot run that startup check.

## Separate command and helper builds

For development, the original two-file layout is still supported:

```sh
go build -o dist/ .
go run ./internal/buildhelper -out dist
```

Keep the command and helper together in this layout. `go install .` alone does
not install or embed the helper. Use `buildstandalone` for single-file distribution.

Enable Bluetooth and grant terminal/helper access in macOS Privacy & Security
settings. macOS standalone builds target macOS 12+ (also subject to the Go runtime minimum).
Windows requires Windows 10 version 2004+ or Windows 11 with BLE
peripheral support. Pair mode needs concurrent peripheral advertising and central
scanning for full mutual discovery; if advertising fails on one device, pairing
can continue by discovering and initiating from that device. A transfer receiver
still needs working peripheral advertising. Adapter/driver support must be
validated. Manual OS bonding should not
be necessary. Physical two-device interoperability remains unverified.

## Run directly from source

Run from the repository root:

```sh
go run . tui
```

Source runs automatically compile the native helper when Bluetooth is first
used. The helper is cached under the OS user cache directory in
`btfling/native/`, keyed by the native source contents, build script, platform,
architecture, and Go runtime version. Unchanged sources reuse the cached build;
native source changes trigger a new build. No separate helper command is needed,
and stale helpers in the checkout root or `dist/` are ignored. The build machine
still needs Xcode Command Line Tools on macOS or the .NET SDK on Windows.

Separate installed binaries use their sibling helper and do not build source
automatically. Standalone binaries use their embedded helper. An explicit
override is supported for all layouts:

```sh
BTFLING_BLE_HELPER="$PWD/dist/btfling-ble" go run . pair
```

For separate builds, keep the command and helper from the same build together.
`BTFLING_BLE_HELPER` also overrides the embedded helper in standalone builds.


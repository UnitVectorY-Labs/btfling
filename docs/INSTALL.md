---
layout: default
title: Installation
nav_order: 2
permalink: /install
---

# Installation

btfling requires macOS or Windows with a supported Bluetooth adapter. Build the
Go command and its native BLE helper together; `go install` alone is insufficient.

## Build from source

Use Go 1.26+. Build from the repository root. macOS also needs Xcode Command Line
Tools (`xcode-select --install`):

```sh
go build -o dist/btfling .
go run ./internal/buildhelper -out dist
./dist/btfling tui
```

Windows builds need the .NET 8 SDK, or a newer SDK capable of targeting .NET 8:

```powershell
go build -o dist/btfling.exe .
go run ./internal/buildhelper -out dist
.\dist\btfling.exe tui
```

Keep the Go command and `btfling-ble` helper together (`.exe` on Windows). The
Windows helper is self-contained; target machines do not need .NET. Build-time
package restore may download dependencies; runtime transfer uses only Bluetooth.
`go install .` does not install the helper.

The locally built Windows x64 command may be named `windows.exe`; run it as
`.\windows.exe` or rename it to `btfling.exe`. Keep `btfling-ble.exe` beside it.
Generated binaries are not committed to Git.

Cross-build Windows x64 from macOS:

```sh
GOOS=windows GOARCH=amd64 go build -o windows.exe .
go run ./internal/buildhelper -os windows -arch amd64 -out .
```

Use `GOARCH=arm64` and `-arch arm64` for Windows ARM64. Build the macOS helper on
macOS, where Swift defaults to the host CPU architecture.

Enable Bluetooth and grant terminal/helper access in macOS Privacy & Security
settings. Windows requires Windows 10 version 2004+ or Windows 11 with BLE
peripheral support. Pair mode needs concurrent peripheral advertising and central
scanning; adapter/driver support must be validated. Manual OS bonding should not
be necessary. Physical two-device interoperability remains unverified.

## Run directly from source

Build the native helper once, then use `go run .` from the repository root:

```sh
go run ./internal/buildhelper -out .
go run . tui
```

Source runs look for a sibling helper, then a helper in the checkout root or
`dist/`. Installed binaries use their sibling helper and do not trust arbitrary
working-directory helpers. An explicit override is supported:

```sh
BTFLING_BLE_HELPER="$PWD/dist/btfling-ble" go run . pair
```

Rebuild the native helpers on both devices when updating pairing functionality.


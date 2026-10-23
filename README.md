[![License](https://img.shields.io/badge/license-MIT-blue.svg)](https://opensource.org/licenses/MIT) [![Work In Progress](https://img.shields.io/badge/Status-Work%20In%20Progress-yellow)](https://guide.unitvectorylabs.com/bestpractices/status/#work-in-progress) 

# btfling

Send files and binary streams between nearby computers over encrypted Bluetooth.

Pair once, remember the other device by name, and transfer from a terminal or
shell pipeline. Payloads travel exclusively over Bluetooth—no Wi-Fi, accounts, Internet
relays, or cloud storage.

- **One Pair mode:** both devices advertise and discover each other. Names are learned automatically.
- **Always encrypted:** mutual TLS 1.3 authenticates saved Ed25519 keys and uses ephemeral X25519 session keys.
- **A terminal you can navigate:** an orange-accented Bubble Tea / Lip Gloss interface with Client, Pair, and Transfer tabs, blue Bluetooth indicators, file browsing, and progress.
- **Scriptable:** stdin/stdout, explicit files, useful exit codes, and diagnostics on stderr.
- **Automatic setup:** first run creates a local identity and key under `~/.config/btfling/`.

Native BLE adapters are implemented for **macOS and Windows**. This is an initial
implementation: physical two-device interoperability, including concurrent
advertising/scanning, still needs verification on real adapters. BLE's conservative
transport favors compatibility over speed. Termux Bluetooth is not implemented.

## Quick Start

Build the Go command and the native helper together. On macOS, install Go 1.26+
and Xcode Command Line Tools, then from the repository root:

```sh
go build -o dist/btfling .
go run ./internal/buildhelper -out dist
./dist/btfling tui
```

On Windows, build with Go 1.26+ and a .NET SDK capable of targeting .NET 8:

```powershell
go build -o dist/btfling.exe .
go run ./internal/buildhelper -out dist
.\dist\btfling.exe tui
```

Keep both executables together. The Windows helper is self-contained; end users
do not need .NET. Generated binaries are excluded from this source repository.

For headless setup, run the same command on both devices:

```sh
btfling pair
```

Both advertise and scan; the CLI selects the first nearby peer in pairing mode.
Use the Pair tab to choose from a list, or `btfling pair DEVICE_ID` to target one.
Pairing uses first-use trust by default. Compare key fingerprints through a trusted
channel, or supply `-trust FINGERPRINT` to require a known fingerprint before saving.
Every subsequent transfer verifies the saved key; plaintext transfers are disabled.

For devices saved locally as `alice` and `bob`:

```sh
# Bob's device
btfling receive alice -out report.pdf

# Alice's device
btfling send bob -in report.pdf
```

## Documentation

- [Overview](docs/README.md) — purpose and execution flow
- [Installation](docs/INSTALL.md) — platform requirements, building, and helper deployment
- [Usage](docs/USAGE.md) — commands, flags, terminal interface, and exit codes
- [Examples](docs/EXAMPLES.md) — pairing, file transfers, and pipelines
- [Configuration](docs/CONFIG.md) — identity, keys, peers, and storage
- [Protocol and Security](docs/PROTOCOL.md) — wire format, trust model, and physical-device testing
- [Development](docs/DEVELOPMENT.md) — validation, repository standards, and documentation maintenance

## Development

```sh
go test -race ./...
go vet ./...
go build ./...
```

Tests include simultaneous mutual pairing, learned names, optional fingerprint
verification, key pinning, mandatory encryption, binary pipelines, destination
cleanup, TLS tampering, cancellation, and minimum-MTU GATT mailboxes. See the
[physical-device checklist](docs/PROTOCOL.md#physical-device-testing) for the
remaining radio/OS acceptance tests.

## License

MIT License — see [LICENSE](LICENSE) for details.

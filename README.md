# btfling

**Nearby files. One command. Encrypted Bluetooth.**

btfling sends files and binary streams directly between nearby computers. Pair
once, remember the other device by name, and transfer from a terminal or shell
pipeline. Payloads travel exclusively over Bluetooth—no Wi-Fi, accounts, Internet
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

## Get started

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

See [usage and building](docs/USAGE.md), [examples](docs/EXAMPLE.md),
[configuration](docs/CONFIG.md), and the [protocol and security model](docs/PROTOCOL.md).

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

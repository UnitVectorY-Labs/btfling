---
layout: default
title: Usage
nav_order: 3
permalink: /usage
---

# Command-line and terminal usage

See [installation](INSTALL.md) for platform requirements, building the command and
native helper, and running from source.

## First run and command structure

```text
btfling COMMAND [ARGUMENTS] [FLAGS]
```

The default configuration is `~/.config/btfling/config.yaml` on every platform,
including Windows. Missing configuration is created automatically with a
hostname-based nickname, random stable device ID, and Ed25519 key. Existing
identities, keys, and peers are preserved. Invalid/unreadable files cause an error
rather than being replaced. Initialization notices go to stderr.

Running `btfling` with no arguments initializes configuration and displays help.
`help`, `--help`, and `-h` display help without creating configuration. To choose
an explicit name before first use, run `btfling init NAME`; it refuses an existing
file. Later, change your client name in the TUI's Client tab or the configuration.

An optional directory override goes once before the command:

```sh
btfling --config /private/profile pair
```

`-config` is also accepted. See [configuration](CONFIG.md) for details.

## Commands

| Command | Purpose |
| --- | --- |
| `init NAME` | Create an identity/key with an explicit local name. |
| `keygen` | Print the local public-key fingerprint. |
| `pair [ID] [flags]` | Advertise and discover, exchange keys, and save the other device. |
| `discover [-timeout 10s]` | Scan for advertising peers and print their IDs/names. This diagnostic command only scans. |
| `peers` | Print saved local aliases, device IDs, and public-key fingerprints. |
| `rename OLD NEW` | Change a saved peer's local alias. |
| `forget NAME` | Remove a saved peer/trust record. |
| `send NAME [-in FILE] [-timeout 0]` | Send a file or stdin to a paired peer. |
| `receive NAME [-out FILE] [-timeout 0]` | Receive from a paired peer into a new file or stdout. |
| `tui` | Open Client, Pair, and Transfer tabs. |

Nicknames are case-sensitive, limited to 64 UTF-8 bytes, and cannot be empty,
start/end with whitespace, or contain control characters or slashes. Quote names
with spaces. Peer names are learned on first pairing; `-as` and `rename` override
the local alias without changing the other device's own name.

## One pairing mode

```text
btfling pair [ID] [-as NAME] [-trust FINGERPRINT] [-timeout DURATION]
```

Run `pair` on both devices. Each advertises a pairing-enabled identity, scans for
others, and can accept an incoming pairing connection. Without an ID, the CLI
connects to the first nearby peer in pairing mode. With an ID, it targets that
specific peer and rejects a different incoming peer. Use the TUI to browse and
choose a device while remaining discoverable.

| Flag | Meaning |
| --- | --- |
| `-as NAME` | Override the peer name learned automatically from its hello. |
| `-trust FINGERPRINT` | Optional 64-hex-digit SHA-256 fingerprint to verify before saving trust. Hex case is ignored. |
| `-timeout DURATION` | Overall pairing deadline; default `0` waits indefinitely. |

Pairing defaults to trust on first use. Both sides prove possession of their
offered keys through TLS, but first-use key substitution by an active attacker is
possible. Compare the printed fingerprints out of band, or obtain them first with
`keygen` and supply `-trust`. A fingerprint is the hash of the raw public key, not
a signature or a certificate fingerprint.

Pairing an already known ID with the same key preserves its alias and avoids
duplicate records. `-as` can change the alias. A changed known key is rejected;
forget the relationship explicitly before replacing trust. Name collisions across
different IDs require an alias override.

`discover` prints `DEVICE_ID<TAB>NAME` once per ID. Pair-mode devices and waiting
receivers advertise; idle installations do not. Scan before a transfer rather than
during it. Only one application pairing/transfer operation per adapter is supported;
a pairing operation internally runs both radio roles.

## Send, receive, and binary streams

```text
btfling send NAME [-in FILE] [-timeout DURATION]
btfling receive NAME [-out FILE] [-timeout DURATION]
```

Use a saved peer alias. The receiver advertises; the sender scans and connects.
Either may start first. **All transfers use mutual TLS 1.3 and pinned peer keys.**
The connection must authenticate the saved peer key before transferring data.

`send` reads stdin until EOF unless `-in FILE` is supplied. `receive` writes payload
bytes to stdout unless `-out FILE` is supplied. Status/diagnostics go to stderr;
`peers`, discovery rows, and `keygen` fingerprints use stdout. Explicit files avoid
Windows shell versions that decode/re-encode native-command pipelines.

Flags may follow the leading nickname or all precede it. Do not interleave a
trailing nickname with more flags. Durations accept `500ms`, `30s`, `2m`, etc.
Negative values are rejected. `-timeout` limits waiting, connection, and transfer
together. Default zero waits indefinitely. Discovery's scan duration defaults to
10 seconds and must be positive. After packet exchange starts, 45 seconds of radio
silence cancels a stalled session. Ctrl-C cancels.

`-out` refuses existing files, directories, and symlinks. Bytes go to a private
same-directory temporary file; length and SHA-256 are verified before fsync and
publication using a hard link. The destination filesystem must support hard links
(e.g. APFS or NTFS, not FAT/exFAT). Normal failure cleanup removes the temporary
file; crashes can leave `.btfling-*` files. Redirected stdout can leave partial
output on failure. Check the exit status. Transfers do not resume automatically.
A lost final acknowledgement can cause failure after a destination was published;
inspect it before retrying.

## Terminal interface

```sh
btfling tui
```

The ASCII header and orange tab/selection styling stay visible across operations.
Blue spinners indicate active Bluetooth work. Use Tab/Shift+Tab or 1/2/3 to switch
tabs, Page Up/Down to scroll, Esc to cancel, and q/Ctrl-C to exit.

In terminals that support mouse reporting, click tabs and action labels to activate
those controls. Click a device or file row to select it, then click the Enter
label to continue. In the file browser, click the arrow labels to open a folder or
return to its parent. The wheel moves the selection when over device/file rows
and scrolls the panel elsewhere. Save/cancel form labels and the footer's
cancel/quit labels are clickable; entering names and paths still uses the keyboard.
Switching tabs by mouse also cancels the current operation.

- **Client:** local name, device ID, encryption details, your fingerprint, and saved
  peer fingerprints. Press n to edit your client name.
- **Pair:** entering the tab starts advertising and scanning. Nearby devices appear
  by name; previously paired devices are marked. Select a nearby pairing peer and
  press Enter, or accept an incoming selection. Press p to pause/restart, a for an
  optional nickname override, and v for an optional expected fingerprint. Editing
  these options pauses/restarts discovery.
- **Transfer:** s selects sending and r selects receiving. Choose a paired peer,
  browse for a sending file or destination folder, and supply a receiving filename.
  Right arrow opens folders, Enter selects, and f accepts a typed full path. Review
  the chosen peer/path, then Enter starts the transfer. Progress shows bytes and,
  when sending a known-size file, a progress bar.

Switching away cancels an active operation so it does not silently keep using the
adapter. The TUI requires explicit files; use CLI commands for stdin/stdout streams.

## Exit codes and troubleshooting

| Code | Meaning |
| --- | --- |
| `0` | Success, including discovery that found no peers. |
| `1` | Configuration, Bluetooth, trust, protocol, or I/O failure. |
| `2` | Invalid CLI usage. |
| `130` | Interrupted through the CLI signal context. |

If no devices appear, open Pair on the other device, verify Bluetooth permissions,
and check peripheral/concurrent-role adapter support. Keep the application and native
helper from the same build together. Missing-helper errors mean the helper executable
is absent or incorrectly located. For a trusted-key error, verify the device
fingerprint and inspect the saved peer relationship.

Bluetooth operations require macOS or Windows.

## Development

```sh
go test -race ./...
go vet ./...
go build ./...
```

The remaining [physical-device checklist](PROTOCOL.md#physical-device-testing)
includes simultaneous discovery, two-sided selection, and different OS pairings.

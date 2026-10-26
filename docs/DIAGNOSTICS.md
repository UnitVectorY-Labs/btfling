---
layout: default
title: Pairing Diagnostics
nav_order: 8
permalink: /diagnostics
---

# Pairing diagnostics

Run the same diagnostic command on both computers. It tries advertising and
discovery, automatically pairs with another diagnostic client, exchanges reports,
and tests data in both directions over the same mutually authenticated Bluetooth
connection. No network connection is used to forward logs.

On the Mac, from the source checkout:

```sh
go run . diagnose
```

Or use the standalone Mac binary:

```sh
./btfling-darwin-arm64 diagnose
```

On Windows, with the updated standalone executable:

```powershell
.\btfling-windows-amd64.exe diagnose
```

Stop any other active btfling pairing or transfer operation on each computer
first. Both machines should run this command; a normal Pair tab or `pair` command
does not join diagnostic mode. Diagnostics use a separate GATT service UUID
(`8462b101-8c34-4f19-a9ef-8ac72d7312b6`), a diagnostic identity marker, and the
`diagnose-v1` session operation.

## Connection coordination

Both devices advertise and scan. The lower device ID initiates immediately when
it discovers its peer. The higher ID waits four seconds for that incoming
connection before initiating as a fallback. This permits a device whose
advertising failed to discover and connect to the other computer.

If both nevertheless initiate, the existing authenticated `ready`/`yield` votes
select one connection using device IDs. Losing connections are cancelled;
discovery stops before reports are exchanged. Workers and native helper stderr
are collected before the log closes. Pairing uses the existing identity and
saves the peer normally, preserving key pinning and optional fingerprint checks.

## What the command verifies

After both devices confirm saving each other's keys:

1. Each sends its initial diagnostic report over TLS.
2. Each sends a fresh 4096-byte random probe.
3. Each acknowledges the other probe's SHA-256 digest; the sender checks it.
4. Each sends a completed report with transport counters when available.
5. Both acknowledge completion and shut down the session.

Reports are bounded to 24 KiB and include device identity, public-key fingerprint,
OS, architecture, Go version, available build revision, recent events, and a
separate retained set of warnings/errors. Native logs include radio state,
advertising status, identity-read failures, subscriptions, and negotiated sizes.
Adapter/OS events are retained separately from recent events so noisy discovery
does not evict Windows capability information before it reaches the Mac.
Private keys, file contents, and raw stream/probe payloads are not logged.

`PASS` means this pairing, report exchange, bidirectional integrity test, and
session shutdown succeeded. It does not establish every transfer direction or
adapter capability. In particular, a successful outgoing diagnostic connection
can coexist with unavailable local advertising.

## One Mac log contains the received Windows reports

The command prints the full log filename. By default it creates a new
`btfling-diagnose-OS-TIMESTAMP-RUN.log` in the current directory. The file contains
one JSON object per line, so partial results remain readable after failure.

| Log phase | Meaning |
| --- | --- |
| `local.info` | This computer's run and public identity metadata. |
| Native/helper phases | Local radio lifecycle, discovery, errors, and cleanup. |
| `election.*` | Preferred direction and authenticated connection votes. |
| `remote.paired` | Initial report received from the authenticated peer, including its earlier failures. |
| `probe.verified` | Received the peer's data and verified its receipt for our data. |
| `remote.complete` | Peer report after its own bidirectional checks. |
| `passed` / `failed` | Final local outcome. |
| `remote.unavailable` | No remote report arrived; the log contains only local evidence. |

The Mac log therefore includes Windows reports once communication succeeds.
Remote timestamps are the peer's own clock; use device/run IDs and sequence
numbers rather than assuming the clocks are synchronized. Imported reports are
not recursively included in outgoing reports.

If no connection or report exchange is possible, the Mac cannot retrieve Windows
logs over that failed Bluetooth link. The command records this explicitly; an
absence of Windows reports does not identify the Windows failure cause. A report
received before a later failure is retained in the Mac log.

## Options

```text
btfling diagnose [-timeout 90s] [-log FILE] [-peer ID] [-trust FINGERPRINT]
```

The timeout must be positive and covers the entire run, including automatic
source-helper setup. `-log` selects a filename; existing files are never
overwritten. `-peer` restricts pairing to a discovered device ID, while `-trust`
requires its expected public-key fingerprint. As with ordinary pairing, default
first-use trust does not prove the identity of the person operating the peer.

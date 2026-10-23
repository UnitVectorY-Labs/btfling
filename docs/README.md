---
layout: default
title: btfling
nav_order: 1
permalink: /
---

# btfling

**Nearby files. One command. Encrypted Bluetooth.**

Send a report to the laptop beside you. Pipe bytes from your favorite command.
Receive a file without opening a browser. btfling brings the feel of a small Unix
utility to direct Bluetooth transfers.

Open **Pair** on both devices: each makes itself available while discovering the
other. Pick a device in the terminal interface or let the CLI connect to the first
available pairing peer. Names are learned automatically and can be overridden.

Once paired, transferring data takes a short command. Every transfer is encrypted
and authenticates the saved peer key. First-use pairing is convenient by default;
optional fingerprint verification lets you check trust before saving a device.

The terminal interface puts **Client**, **Pair**, and **Transfer** in distinct tabs,
with a btfling ASCII header, orange accents, blue Bluetooth activity indicators,
file browsing, and progress. Headless commands remain available for pipelines and
scripts.

Native Bluetooth adapters are implemented for macOS and Windows. This initial
version still needs physical two-device interoperability testing. Termux Bluetooth
is not yet implemented.

## Execution flow

1. An operational command creates missing configuration, including the device ID
   and private key, while preserving an existing identity.
2. Pair mode advertises and scans on both devices. Selecting a peer exchanges keys
   and saves its name and trust record. Verify fingerprints through a trusted
   channel or supply `-trust` before saving.
3. A receiver advertises while a sender scans and connects. Mutual TLS authenticates
   saved peer keys before transferring file or pipeline bytes over Bluetooth.
4. The receiver verifies the byte count and checksum before acknowledging success.
   File destinations are published only after verification.

## Documentation

- [Installation](INSTALL.md) — supported platforms, native helpers, and source builds
- [Usage](USAGE.md) — command reference, flags, TUI controls, and exit codes
- [Examples](EXAMPLES.md) — practical pairing and transfer workflows
- [Configuration](CONFIG.md) — identities, peer trust, YAML schema, and recovery
- [Protocol and Security](PROTOCOL.md) — transport details, security limits, and testing
- [Development](DEVELOPMENT.md) — validation, repository standards, and documentation maintenance

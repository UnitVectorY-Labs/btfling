---
layout: default
title: Protocol and Security
nav_order: 6
permalink: /protocol
---

# Protocol and security

This describes wire protocol **version 2**, with mandatory encryption and mutual
pairing discovery. Both applications and native helpers must be updated together.
Configuration schema version remains 1. Physical two-device interoperability is
still unverified; this document is not an independent security audit.

## Stack and Bluetooth roles

```text
Files / stdin / stdout
       ↕ bounded transfer records
Mutually authenticated TLS 1.3
       ↕ reliable Bluetooth byte stream
Acknowledged 20-byte GATT mailboxes
       ↕ BLE ATT read / write
CoreBluetooth (macOS) / WinRT (Windows)
```

A transfer uses a central/client sender and a peripheral/server receiver. Pairing
runs both roles on each device: one native helper advertises a pairing service and
accepts incoming stream bytes, while another scans and reads nearby identities.
Selection starts a central connection while the local pairing service remains
available. Native helper processes communicate with Go through anonymous local
pipes. **All inter-device traffic travels through Bluetooth GATT.** No TCP/IP,
Wi-Fi, network relay, or fallback transport exists. A `net.Conn` interface adapter
is used by TLS without opening a socket.

Native implementations are Swift/CoreBluetooth and C#/WinRT. GATT attributes permit
unpaired access; btfling authenticates the encrypted application session above
Bluetooth. Pairing requires adapters/drivers capable of peripheral advertising and
central scanning concurrently. Termux needs an unimplemented Android companion APK.

## GATT discovery and stream characteristics

Service: `c9e0b001-49f0-4c98-9c83-3ad797a40d10`.

| Characteristic UUID | Properties | Purpose |
| --- | --- | --- |
| `c9e0b002-49f0-4c98-9c83-3ad797a40d10` | Read | Public JSON identity: `id`, `name`, `pairing`. |
| `c9e0b003-49f0-4c98-9c83-3ad797a40d10` | Write with response | Central request mailbox. |
| `c9e0b004-49f0-4c98-9c83-3ad797a40d10` | Read | Peripheral response mailbox. |

A `pairing: true` identity indicates active pairing mode. Waiting file receivers
advertise with pairing false. Discovery briefly connects to read identity metadata.
Application IDs persist independently of OS Bluetooth addresses. Discovery is not
authentication: names, IDs, and the pairing indicator can be spoofed.

Within one listener process, the first central writing the mailbox owns that stream.
The design is single-session, not a multi-client file server.

## Mailbox framing and acknowledgement

Each ATT value is 8–20 bytes, fitting the mandatory 23-byte ATT MTU:

| Offset | Bytes | Meaning |
| --- | --- | --- |
| 0 | 4 | Big-endian uint32 data sequence for this direction. |
| 4 | 4 | Big-endian uint32 last accepted opposite-direction sequence. |
| 8 | 0–12 | Stream fragment; absent for a poll/acknowledgement only. |

Sequences start at zero; the first data fragment is sequence 1. Writers retain one
fragment until acknowledged, then advance. The most recent duplicate fragment is
not delivered twice. An invalid packet length, unexpected nonduplicate data
sequence, or acknowledgement beyond the sent sequence fails the stream.

The central writes its request with ATT write-with-response, then reads the response
without caching. The peripheral forwards the request to Go, which updates its
response value. A read may see the previous value while an update is in flight;
stop-and-wait repetition handles that race. The central uses a 5ms ticker, but radio
and ATT latency usually determine a slower effective rate. No notifications or
larger MTU are needed. These are reliability measures, not cryptographic defenses.

A 64-fragment inbound queue bounds memory and withholds acknowledgements when full.
Application records are also bounded. Sequences never wrap: at most `2^32 - 1`
fragments per direction give an upper bound just under 48 GiB of stream content
when every fragment is full. TLS/record overhead and short writes reduce the maximum
actual file size. This is a conservative transport, not a high-throughput design.

Before packet activity, a listener waits until its overall context expires. Once
packet exchange starts, 45 seconds without activity cancels a stalled stream; the
central starts its timer when ready. This is not a payload inactivity deadline.
Central close attempts a final acknowledgement exchange with a two-second grace
period. No automatic reconnection or resume is implemented.

## Hello and TLS

Records begin with a four-byte big-endian body length. Incoming zero lengths or
bodies above 32,769 bytes are rejected; initial JSON hellos have a stricter 4,096-byte
receive limit. The initiating endpoint sends hello first; the listener validates
it and responds. Representative structure (public key is a placeholder):

```json
{
  "version": 2,
  "id": "0123456789abcdef0123456789abcdef",
  "name": "office-laptop",
  "public_key": "BASE64_OF_32_BYTE_ED25519_PUBLIC_KEY",
  "operation": "pair",
  "encrypted": true
}
```

`operation` is `pair` for setup and `send` at both transfer endpoints. Version 2,
valid nonlocal ID/name, a 32-byte offered public key, expected operation, and
`encrypted: true` are required. Transfers also compare the saved peer ID and key.
A peer without a saved key cannot transfer. There is no plaintext branch.

The hello itself is public and not independently signed or bound as a separate
transcript to TLS. Tampering can cause failure; it cannot make an endpoint downgrade
to plaintext. After hello, both pairing and transfer establish TLS directly on the
Bluetooth byte stream:

| Component | Selection |
| --- | --- |
| Version | TLS 1.3 only. |
| Ephemeral key agreement | X25519. |
| Identity authentication | Ed25519 certificate/public key. |
| Record encryption and integrity | TLS 1.3 AEAD suites selected by Go's `crypto/tls`. |
| Certificates | One self-signed certificate per endpoint/session. |
| Session tickets | Disabled. |

Certificates are generated in memory from the stored private seed with a random
serial. Their nominal validity is from one hour before creation through 24 hours
after creation. Verification intentionally replaces public PKI, DNS, and validity
checks with an exact Ed25519 public-key comparison, requiring one peer certificate.
TLS still verifies the handshake signature and possession of the corresponding key.

For transfers, the comparison uses the previously saved public key. For initial
pairing, it uses the key from that connection's hello; optional fingerprint and
known-key policy checks run before accepting/saving the relationship. TLS on first
pairing proves possession of an offered key, not that it belongs to the person you
intended to pair with. Fresh ephemeral sessions protect record ordering/integrity
and prevent replaying captured ciphertext into a new session.

## Mutual pairing, trust, and collision handling

Both sides advertise and scan. The CLI with no target selects the first discovered
pairing-enabled peer. The TUI remains available while presenting candidates and
starts an outgoing attempt when a user selects one; an incoming selection can also
complete setup. An explicit target restricts accepted candidates to that ID. The
remote client name is learned unless a local alias overrides it.

If both sides initiate simultaneously, the lower application ID's outgoing
connection wins. Candidate sessions exchange TLS-protected length-prefixed ASCII
votes, `ready` or `yield`, with the initiator writing first. A listener that already
has a preferred outgoing attempt yields its competing incoming session; the other
side cancels its losing outgoing attempt when accepting the preferred incoming one.
A one-sided selection works in either ID order.

Default first-use pairing accepts an offered key and assumes no active interception.
Users can compare fingerprints out of band afterward, or provide `-trust` to require
a known fingerprint before saving. Fingerprints are hex SHA-256 of raw public keys,
not signatures/certificate fingerprints. Existing saved keys cannot silently change.
Re-pairing a known ID with the same key is idempotent and preserves its local alias;
legacy identity-only relationships can acquire keys. Conflicting peer aliases
require an explicit unique override.

After agreement and local configuration persistence, endpoints exchange encrypted
ASCII `saved` records, initiator first. These confirm application-level success,
not an absolute guarantee about remote storage across crashes. An error can leave
one side saved without the other; the command reports confirmation failure. Private
seeds are never transmitted.

## Transfer records and completion

Inside TLS, each length-prefixed body starts with a type byte:

| Type | Remaining body | Meaning |
| --- | --- | --- |
| `0x01` | 1–32,768 raw bytes | Binary payload fragment. |
| `0x02` | uint64 byte count and 32 raw SHA-256 bytes | Sender's end record; 41 total body bytes. |
| `0x03` | Same count and digest | Receiver's completion acknowledgement; 41 total body bytes. |

Counts are big-endian. SHA-256 covers concatenated payload bytes only. Empty input
sends no data record, then an end record with zero count and the empty-stream digest.
Empty data records are invalid. The sender succeeds only on an exact acknowledgement
of its byte count and digest.

The receiver streams output while hashing. After checking count/digest, it commits
the destination before acknowledging. `-out` fsyncs/closes a private temporary file
beside the destination, then publishes using a hard link that refuses an existing
path. This requires APFS/NTFS or another hard-link-capable filesystem. The containing
directory is not separately fsynced; completion is not a guarantee against all
power-loss scenarios. With stdout, completed writes do not guarantee downstream
consumer durability.

No structured remote error records, filenames, remote paths, compression, remote
execution, or resume metadata are transmitted. Truncation, malformed records,
changed keys, failed output/commit, and missing completion cause failure/closure.
A destination may already be committed when the final acknowledgement is lost;
exactly-once delivery across crashes is not promised.

## Security model and limits

All transfers provide application encryption and authenticate saved keys. Initial
first-use trust remains vulnerable to active nearby key substitution unless the
right fingerprints are checked. Protecting the private seed and installed
command/helper is essential. A saved nickname/ID alone is not authenticated identity.

Discovery and hello expose names, IDs, public keys, and pairing/operation metadata.
Traffic timing, volume, proximity, and service advertisements are not hidden. Nearby
attackers can spoof discovery, occupy a listener, disrupt handshakes, or jam the
radio. Denial-of-service defenses are limited.

Private seeds are stored unencrypted in `config.yaml`. Unix file modes restrict
normal access; Windows depends on ACLs. Compromised backups or local malware can
expose identity keys. There is no distributed revocation or automatic rotation:
forget and re-pair affected relationships. There is no plaintext escape hatch,
even for older identity-only peers; they must pair again.

## Physical-device testing

Hardware-free tests exercise automatic/two-sided pairing, one-sided selection in
both ID orders, simultaneous selection, learned/overridden names, optional
fingerprints, changed-key refusal, mandatory encryption, binary/empty streams,
malformed/truncated records, TLS tampering, CLI files/pipelines, destination cleanup,
mailbox sequencing/backpressure, and a full encrypted minimum-MTU stream. Native
macOS and Windows adapters compile. The following physical checks remain unrun.

For macOS↔Windows, macOS↔macOS, and Windows↔Windows, test both transfer directions:

1. Install current command/helpers. Enable Bluetooth, grant permissions, verify
   peripheral and concurrent-role support, remove prior OS bonding, and disable Wi-Fi.
2. Open Pair on both devices; confirm both appear, can be selected from either side,
   and simultaneous selections save a single relationship. Repeat with multiple
   nearby peers, CLI auto-selection, and explicit IDs.
3. Check learned names, alias overrides, known-peer badges, optional fingerprint
   checks, wrong fingerprints, and changed-key refusal.
4. Test both transfer startup orders, empty/one-byte/random/32 KiB-boundary/large
   files, pipelines, receiving destinations, checksums, throughput, and memory.
5. Try unexpected senders, missing keys, and old unencrypted protocol hellos. Confirm
   no plaintext payload is accepted or silently enabled.
6. Disable/unplug Bluetooth, kill/cancel processes, and use short deadlines. Check
   native helper cleanup and incomplete destination cleanup.
7. Test full disks, existing/unwritable paths, unsupported hard links, and final
   acknowledgement/disconnect races; verify the sender reports commit failures.
8. Exercise all TUI tabs, file/folder selection, destination naming, progress, resize,
   cancellation, and restarting operations after switching tabs.

---
layout: default
title: Protocol and Security
nav_order: 6
permalink: /protocol
---

# Protocol and security

This is the canonical, experimental btfling streaming protocol. The protocol
version remains **1** by design; its previous unreleased definition has been
replaced completely. The new UUID namespace isolates this definition. There is
no old-protocol discovery, parsing, negotiation, fallback, or migration.

## Stack and Bluetooth roles

```text
Files / stdin / stdout
          ↕ bounded file records and SHA-256 completion
Mutually authenticated TLS 1.3 (pinned Ed25519 keys)
          ↕ net.Conn: ordered, bounded, full-duplex bytes
Streaming transport: offsets, cumulative ACK, receive limit
          ↕                                 ↕
Write Without Response                    Notify
 central → peripheral               peripheral → central
          └──────────── BLE GATT ────────────┘
```

A transfer uses a central/client sender and peripheral/server receiver. Each BLE
connection is full duplex regardless of file direction. Pairing advertises and
scans concurrently using separate native helpers; selection starts an outgoing
central connection while the incoming listener remains available. A listener is
single-session: the first TX subscriber owns it. Writes from other centrals are
ignored, notifications target the owner, and advertising stops on subscription.
Restart the helper for a new stream. No reconnect/resume is attempted.

Swift/CoreBluetooth and C#/WinRT handle platform BLE mechanics. Go communicates
with these local child processes using bounded queues over anonymous pipes. The
local IPC uses JSON lines with base64 packet values; BLE transport frames are
binary. No TCP/IP, Wi-Fi, socket, relay, or network fallback is involved.

## Discovery and GATT attributes

Service UUID: **`8462b001-8c34-4f19-a9ef-8ac72d7312b6`**.

| Characteristic UUID | Properties | Purpose |
| --- | --- | --- |
| `8462b002-8c34-4f19-a9ef-8ac72d7312b6` | Read | Public identity JSON. |
| `8462b003-8c34-4f19-a9ef-8ac72d7312b6` | Write Without Response | Central → peripheral stream. |
| `8462b004-8c34-4f19-a9ef-8ac72d7312b6` | Notify | Peripheral → central stream. |

The service UUID is advertised. Discovery connects briefly, discovers the
characteristics and reads identity. Example:

```json
{"version":1,"id":"0123456789abcdef0123456789abcdef","name":"office-laptop","pairing":true,"accepting":true}
```

`pairing` distinguishes application pairing from a waiting file receiver;
`accepting` becomes false once a subscriber owns the listener. Discovery events
show ID, name and pairing state. A central connecting for a session verifies the
required characteristic properties and subscribes to TX before starting transport.
Subscription establishment uses the usual CCCD operation; it is setup, not the
streaming data path. No stream data is fetched using Read, and no stream frame is
sent with Write With Response. GATT attributes permit access without OS bonding.

**Everything above is public and unauthenticated**, including UUIDs, name,
application ID, version, accepting/pairing state, and OS Bluetooth addresses.
Discovery chooses where to attempt a connection; only TLS authenticates a peer.

## Binary transport frame

One complete frame occupies one characteristic operation; there is no BLE-level
length prefix. All integers are unsigned and big-endian.

| Offset | Size | Field |
| --- | --- | --- |
| 0 | 1 | Protocol version, `1`. |
| 1 | 1 | Flags: OPEN `0x01`, FIN `0x02`, FIN_ACK `0x04`. |
| 2 | 2 | Reserved, must be zero. |
| 4 | 8 | Nonzero random sender session identifier. |
| 12 | 8 | Offset of this frame's first payload byte in sender's direction. |
| 20 | 8 | ACK: exclusive contiguous byte offset received in opposite direction. |
| 28 | 8 | LIMIT: exclusive opposite-direction offset permitted by receiver. |
| 36 | 0–476 | Stream payload; maximum depends on platform value size. |

Offsets and ACKs start at zero and count bytes, excluding headers. Byte offsets
allow variable payload lengths without packet-size assumptions or packet history.
The stream refuses offset exhaustion before uint64 wrapping. The randomly generated
64-bit sender identifier changes for each local stream and is fixed for its
lifetime. It rejects stale frames after session establishment; it is not an
authentication token. TLS supplies cryptographic integrity, replay resistance and
authenticity.

Each direction sends OPEN first: flags exactly `0x01`, offset=ACK=0, no payload,
and `0 < LIMIT <= 65536`. The implementation advertises 65536. A peer cannot send
data until it has received OPEN. An identical duplicate OPEN is ignored; a changed
session identifier or changed OPEN limit fails the connection. Subsequent frames
contain any combination of FIN/FIN_ACK, or flags=0. FIN can share the final data
frame. FIN_ACK acknowledges FIN receipt, with ACK equal to the entire opposite
stream length. Neither FIN nor FIN_ACK consumes an offset.

Data must start at the next expected offset and fit the advertised receive limit.
A fully duplicated range is discarded without delivery or retraction of newer
ACK/credit state. Partial overlaps, gaps, stale session IDs, data after FIN,
malformed headers, unknown flags, nonzero reserved fields, and impossible FIN_ACK
fail the connection. Duplicate old control updates are ignored when their offset,
ACK and limit are all behind accepted state. Repeated FIN at the already known end
is harmless. Otherwise cumulative ACK and LIMIT must not decrease; ACK must not
exceed bytes submitted; LIMIT must be at least ACK and at most ACK+65536. Rejecting
violations stops both directions and unblocks readers/writers with an error.

## Payload sizing and native backpressure

Every frame fits the current platform value limit and the 512-byte GATT attribute
bound. Application fragment capacity is `min(platform_limit, 512) - 36`, not a
fixed MTU. For example, a platform limit of 244 yields 208 stream bytes. Setup
requires a value limit of at least **37 bytes**; small-MTU-only links fail clearly
rather than operating through a legacy mode. Windows allows ten seconds for size
negotiation after subscription. Safe size increases apply to subsequent frames;
if an already queued frame no longer fits a shrunken native limit, fail rather
than allow truncation. An unsafe size change also fails the stream.

macOS central uses `maximumWriteValueLength(for: .withoutResponse)`. It only calls
`writeValue(..., type: .withoutResponse)` when `canSendWriteWithoutResponse` is true,
resuming on `peripheralIsReady(toSendWriteWithoutResponse:)`. The peripheral uses
the subscribed `CBCentral.maximumUpdateValueLength`; `updateValue` returning false
retains the frame and waits for `peripheralManagerIsReady(toUpdateSubscribers:)`.
It targets just the owning central. Native size limits are rechecked at submission.
See [Apple write readiness](https://developer.apple.com/documentation/corebluetooth/cbperipheral/cansendwritewithoutresponse)
and [notification readiness](https://developer.apple.com/documentation/corebluetooth/cbperipheralmanagerdelegate/peripheralmanagerisready(toupdatesubscribers:)).

Windows central uses `GattSession.MaxPduSize - 3`, capped at 512, and listens for
`MaxPduSizeChanged`. It awaits `WriteValueAsync(..., WriteWithoutResponse)` before
starting the next native operation. The peripheral uses the owning
`GattSubscribedClient.MaxNotificationSize`, observes size changes, and awaits
targeted `NotifyValueAsync(value, subscriber)`, checking the result. These are local
OS queue/admission completions, **not remote application ACKs or indications**.
There is no portable CoreBluetooth-style WinRT readiness property; bounded awaited
operations avoid unbounded outstanding WinRT tasks. Whether a driver completes
these operations promptly enough to sustain many radio packets is a hardware
validation requirement. See [Microsoft PDU size](https://learn.microsoft.com/en-us/uwp/api/windows.devices.bluetooth.genericattributeprofile.gattsession.maxpdusize)
and [GATT server notifications](https://learn.microsoft.com/en-us/windows/uwp/devices-sensors/gatt-server).

Go admits at most sixteen outstanding native send commands. CoreBluetooth may
submit several immediately, then pause on its native readiness signal. Windows
processes the commands serially while awaiting OS completion. Both allow multiple
frames in flight over the radio without waiting for peer transport ACKs. Local
IPC and native callback queues are bounded; no lock is held across Bluetooth calls.

## Receiver-driven flow control and reliability

Each endpoint has a 64 KiB receive ring and a 64 KiB transmit ring. The receiver
tracks `received` and `consumed`; it advertises `ACK=received` and
`LIMIT=consumed+65536`. The sender may submit up to `LIMIT-sent` more bytes. ACK
acknowledges buffering, not application consumption; it does **not** replenish
credit. Only advancing LIMIT does. Absolute limits avoid double-counting repeated
credit advertisements. A sender cannot force the receiver to allocate storage
beyond its ring. A blocked writer eventually fills its own ring, then sleeps until
credit or cancellation; partial writes report bytes already accepted on error.

ACKs and credits piggyback on outgoing data. Standalone updates batch at 16 KiB of
new receipt or consumed capacity; FIN forces an immediate update. A one-shot
10 ms delayed update guarantees progress for smaller batches and `Flush`, even if
the application stops reading. It is not a polling loop or a per-fragment send
delay. ACK-only frames do not solicit ACK-only replies.

```text
Central                            Peripheral (64 KiB receive window)
  OPEN, limit=65536  ────────────────►
                      ◄───────────── OPEN, limit=65536
  DATA off=0,    208B ───────────────►
  DATA off=208,  208B ───────────────►
  DATA off=416,  208B ───────────────►
  DATA off=624,  208B ───────────────►
       ... many more frames ...
                      ◄───────────── cumulative ACK=16640, limit=65536
                                     application consumes 16384 bytes
                      ◄───────────── ACK=16640, limit=81920
  DATA continues within new limit ──►
```

No frame is retained for retransmission, and ACK does not gate every fragment.
ATT Write Commands and notifications ride BLE's ordered reliable link. The chosen
APIs preserve ordered submissions on this single stream/bearer; local failure,
disconnect, or a detected application-boundary gap aborts the stream. If a platform
drops a final frame silently, outstanding-data timeout fails the session. Reconnect
requires a fresh hello and TLS handshake. Adding replay buffers would obscure
native failures and duplicate link reliability without evidence it is needed.

## Connection and transfer flow

```text
Central / initiator                      Peripheral / listener
 discover service, read public identity ──►
 subscribe TX notifications ───────────────► claim single subscriber
                 ◄──── OPEN / OPEN ──────►
 public length-prefixed hello ────────────► validate operation and version
                 ◄──────────────────────── public hello
                 ◄── TLS 1.3 handshake ───► prove pinned Ed25519 keys
 TLS data records pipelined ──────────────► stream to temporary file + SHA-256
 TLS END(count,digest) ───────────────────► verify, commit destination
                 ◄──────────────────────── TLS CONFIRM(count,digest)
                 ◄── TLS close_notify ────►
                 ◄── FIN / FIN_ACK ───────►
 stop local helper / release GATT
```

Both directions remain active throughout; TLS never sees BLE frames, credits,
notifications, or MTU sizes. The Go transport implements `net.Conn` directly,
including functional read/write deadlines. Other in-memory test transports may
use an interface adapter; unsupported deadlines return an explicit error.

## Hello and TLS

Records begin with a four-byte big-endian body length. Incoming zero lengths or
bodies above 32,769 bytes are rejected; initial JSON hellos have a stricter 4,096-byte
receive limit. The initiating endpoint sends hello first; the listener validates
it and responds. Representative structure (public key is a placeholder):

```json
{
  "version": 1,
  "id": "0123456789abcdef0123456789abcdef",
  "name": "office-laptop",
  "public_key": "BASE64_OF_32_BYTE_ED25519_PUBLIC_KEY",
  "operation": "pair"
}
```

`operation` is `pair` for setup and `send` at both transfer endpoints. Version 1,
a valid nonlocal ID/name, a 32-byte offered public key, and the expected operation
are required. Transfers also compare the saved peer ID and key.

The hello itself is public and not independently signed or bound as a separate
transcript to TLS. Tampering can cause failure. After hello, both pairing and
transfer establish TLS directly on the Bluetooth byte stream:

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
Re-pairing a known ID with the same key is idempotent and preserves its local alias.
Conflicting peer aliases require an explicit unique override.

After agreement and local configuration persistence, endpoints exchange encrypted
ASCII `saved` records, initiator first. These confirm application-level success,
not an absolute guarantee about remote storage across crashes. An error can leave
one side saved without the other; the command reports confirmation failure. Pairing sessions finish with the same TLS
close-notify and transport shutdown exchange as transfers, ensuring buffered votes
and saved confirmations are delivered before cancellation. Private seeds are never
transmitted.

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
forget and re-pair affected relationships.

## Shutdown, errors and timeouts

Application success requires the encrypted completion count/digest record. Raw
transport EOF, physical disconnection, or TLS close alone never means success.
After completion, file endpoints exchange TLS `close_notify`, then use transport
`Shutdown`: drain queued bytes, send FIN, receive FIN_ACK and peer FIN, submit the
peer's FIN_ACK, and drain outstanding local native admissions before helper exit.
Go TLS expires its underlying write deadline after `CloseWrite`; btfling restores
the shutdown deadline before finishing the transport.

`CloseWrite` waits for FIN_ACK and preserves the reverse direction. After FIN,
Read delivers buffered bytes then EOF. `Close` is immediate local cancellation,
not a promise to flush; error shutdown tears down the helper and cancels waiters.
EOF is independent of the file completion record. A protocol violation or native
failure propagates an error; bytes already buffered may be read before that error.
TLS still rejects truncated records and missing `close_notify` during normal file
shutdown. The final OS admission is not an over-the-air delivery receipt; physical
shutdown races remain part of hardware acceptance testing.

The operation context bounds setup, TLS, pairing and transfer and closes the
transport on cancellation. Read/write deadlines wake blocked I/O and may be reset.
A native health watchdog checks once per second: setup after subscription,
unacknowledged sent data, and pending FIN fail after 45 seconds without cumulative
ACK progress. Idle sessions and a peer deliberately holding consumed-byte credits
are permitted, bounded by the operation context/deadlines. Successful file
shutdown has a five-second deadline. Discovery has ten-second candidate connection
bounds; Windows advertisement startup has a twenty-second bound. No per-fragment
polling, sleeps, automatic reconnection, or file resumption exists.

## Performance and diagnostics

The 64 KiB window is large enough to hide substantial host/connection latency
without excessive memory. Each stream uses 128 KiB for the two rings, one reusable
512-byte encoder buffer, and at most sixteen local native commands. The helper
receive channel has 64 entries, each frame at most 512 decoded bytes; process pipes
and OS/controller queues add bounded platform buffering. Windows caps deferred
write callbacks at 64; discovery candidates are capped at 256. Tiny or empty
control frames do not allocate unbounded application storage. There is one writer
pump and callbacks never wait for the application to consume bytes.

Hot-path stream payloads copy into/out of rings; the pump reuses its frame buffer
and retains no ACK history. Broadcast wake channels are allocated only when a
waiter exists. Local JSON/base64 helper IPC still incurs allocation/encoding costs;
benchmarks below exclude the helpers and do not measure radio speed.

`Stats()` snapshots negotiated payload, sent/received stream bytes, available send
window, receive credit, unacknowledged bytes, frames, controls, GATT writes,
notifications, elapsed time and terminal error. `BTFLING_BLE_DEBUG=1` prints these
on completion/closure, with no per-packet normal logging. Control counts are
outgoing; write/notification counts include local submissions and incoming
callbacks. Sent byte counts mean submitted stream bytes, not radio receipts. Successful transfers
print file byte count, elapsed time and effective KiB/s including completion and
shutdown. Stream counts also include hello and TLS overhead; file counts do not.

Run:

```sh
go test ./...
go test -race ./...
go test ./internal/ble -run '^$' -bench . -benchtime=2x -count=1
go test ./internal/ble -run '^$' -fuzz FuzzFrame -fuzztime=10s
```

The simulator separates bounded OS admission queues from ordered delivery workers.
It can constrain characteristic size, add latency and delayed controls, impose
packets-per-connection-event pacing, duplicate or mutate frames, and disconnect.
Tests cover frame validation, payload limits, ring fragmentation/wrap, duplex
ordering, partial reads/writes, cumulative ACKs, exhausted windows, credit releases,
stale/duplicate/malformed frames, cancellation, native errors, deadlines, half-close,
TLS handshake, large TLS file/digest verification, TLS shutdown and concurrent close.
Application/protocol tests additionally cover TOFU, fingerprints, changed-key
refusal, TLS tampering, bad/truncated checksums and destination commit failures.

See [benchmark results and hardware checklist](design/streaming-results.md) for
recorded simulation results. **No physical Bluetooth throughput has been measured
for this implementation.** Tens of KiB/s or better is the engineering target,
conditional on negotiated value size, connection interval, PHY, controller queues,
OS driver behavior, helper costs and RF conditions; the simulator is not evidence
of a particular physical throughput.

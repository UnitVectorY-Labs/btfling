# Replacement transport implementation design

Architecture inspection: `ble.go` owns native helper discovery/lifecycle; the old
`mailbox.go` owns fragment sequencing and pipes; Swift and C# own GATT setup and
central connections. `protocol.go` owns public hello, TLS adapter, pinned Ed25519
verification, and bounded file records. `pairing.go` owns TOFU/fingerprint policy,
collision arbitration, and persistence. `app.go` owns transfer completion/commit.

Replace the mailbox with an event-driven net.Conn stream. Each direction has a
64 KiB ring. A single write pump reserves offsets under a mutex and submits outside
it; native receive callbacks validate/copy into the bounded receive ring. Reads
return capacity via an absolute exclusive limit (consumed + 65536). Acknowledgements
mean contiguous receipt, not application consumption. Updates batch at 16 KiB,
piggyback where possible, with a 10 ms one-shot delayed update for smaller changes; FIN forces an update.
No periodic data-path timer, retransmission queue, or network fallback.

Every frame: version u8, flags u8, reserved u16, random sender session u64, byte
offset u64, cumulative opposite-direction ACK u64, exclusive opposite-direction
receive limit u64 (36 bytes, big endian), followed by data. OPEN establishes each
session before data. FIN marks the exact end offset; FIN_ACK confirms receipt.
An operation must fit at least 37 bytes or setup fails clearly. Offsets do not wrap.
A fresh BLE connection/helper is required for each stream; full duplicate payload
ranges and stale cumulative controls are discarded, overlap/gaps and stale session identifiers fail closed.
TLS remains responsible for authenticity and cryptographic stream integrity.

New UUID namespace: 8462b001/2/3/4-8c34-4f19-a9ef-8ac72d7312b6, respectively service,
readable public identity, Write Without Response RX, and Notify TX. Identity JSON
adds version=1 and accepting=true. Subscribe before OPEN. Peripheral claims one
subscriber, and stops advertising when claimed. No OS bonding requirement.

macOS: maximumWriteValueLength(.withoutResponse), CBCentral.maximumUpdateValueLength;
canSendWriteWithoutResponse/peripheralIsReady and updateValue false/readiness.
Windows: session MaxPduSize minus three, min with 512 attribute bound; subscribed
client MaxNotificationSize; awaited WriteWithoutResponse/targeted NotifyValueAsync
completion bounds the native operation queue. Neither completion is a remote ACK.
Go-to-helper admission is bounded at 16 frames. Recheck current platform size on
submission; fail on unsafe shrink, use negotiated increase on subsequent frames.

CloseWrite drains accepted writes, sends FIN, waits FIN_ACK with write deadline.
Close is deterministic cancellation; application success still requires encrypted
count/digest confirmation. Normal application completion explicitly flushes TLS and transport
before native helper teardown. Context cancellation, native errors/disconnects,
malformed frames and deadlines unblock waiters; no lock held over a native call.
Expose snapshots for sizes, counts, windows, outstanding bytes, duration and error.
Hardware-free simulator uses bounded channels, independent delivery workers,
latency/credit delay, OS admission backpressure, duplication, mutation/disconnect.
Validate with TLS/file protocol, race tests, and deterministic large-stream benchmarks.

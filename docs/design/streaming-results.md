# Streaming transport validation

Recorded on 2026-10-23, Linux amd64, AMD Ryzen 7 5825U, Go 1.27.3,
GOMAXPROCS=6 as reported by Go. Protocol version remains 1; the unreleased wire
definition is replaced in a new UUID namespace. No physical Bluetooth hardware
was available to this run.

## Benchmarks (simulation only)

Command:

```sh
go test ./internal/ble -run '^$' -bench . -benchtime=2x -count=1
```

All cases use a 244-byte characteristic value (208 stream bytes per frame).
`BenchmarkStream` sends 1 MiB per iteration through bounded 512-entry simulated
native queues. Each direction has an independent delivery worker; latency is
one-way and does not serialize submission. There is no simulated radio speed
limit in these cases. `BenchmarkTLSStream` uses the same 5 ms link plus mutually
authenticated TLS, file record framing, SHA-256 verification, and receiver
confirmation. Setup/TLS handshake and shutdown are outside its timed section.

`BenchmarkEventLimitedStream` sends 256 KiB per iteration through 128-entry native
queues, limited to eight characteristic operations per 15 ms connection event.
This is a configured synthetic link budget, not an observed Bluetooth connection.
Both directions, including control frames, use event pacing. Effective measured
payload rates were approximately **106.7 KiB/s at 5 ms** and **105.1 KiB/s at
30 ms** additional one-way latency. Maintaining nearly the same rate while adding
25 ms is evidence of windowed submission in this model; it is not a prediction of
particular adapter throughput.

Raw results (MB/s is Go's decimal MB/s):

```text
goos: linux
goarch: amd64
pkg: github.com/UnitVectorY-Labs/btfling/internal/ble
cpu: AMD Ryzen 7 5825U with Radeon Graphics         
BenchmarkStream/0s-6            	       2	   2253899 ns/op	 465.23 MB/s	 1325604 B/op	    5364 allocs/op
BenchmarkStream/5ms-6           	       2	 180971390 ns/op	   5.79 MB/s	 1335548 B/op	    5479 allocs/op
BenchmarkStream/30ms-6          	       2	1040966645 ns/op	   1.01 MB/s	 1362968 B/op	    5835 allocs/op
BenchmarkTLSStream-6            	       2	 181169784 ns/op	   5.79 MB/s	 4136692 B/op	    5650 allocs/op
BenchmarkEventLimitedStream/5ms-6         	       2	2398180618 ns/op	   0.11 MB/s	  495016 B/op	    3204 allocs/op
BenchmarkEventLimitedStream/30ms-6        	       2	2435973480 ns/op	   0.11 MB/s	  504948 B/op	    3261 allocs/op
PASS
ok  	github.com/UnitVectorY-Labs/btfling/internal/ble	18.972s

```

Allocations include the simulator's packet copies and timers and TLS/file framing
where applicable. The production stream has fixed rings, one reusable encoder,
no packet retransmission history, and lazy waiter broadcasts. Unpaced results
measure Go/simulation work plus window/latency effects and must never be presented
as measured BLE speed. Two iterations are a reproducible smoke benchmark, not a
statistical performance distribution.

## Checks completed

- Full `go test ./...` and `go test -race ./...`.
- `go vet ./...` and `git diff --check`.
- Twenty repeated race runs of duplex, cumulative credit, graceful/TLS shutdown,
  small ACK and concurrent-close tests.
- Ten repeated race runs of helper IPC admission, TLS transfer and native drain.
- Ten seconds of frame fuzzing: 456,895 executions; no failures.
- Windows native helper compiled with .NET SDK 8.0.426 targeting
  `net8.0-windows10.0.19041.0`: zero warnings/errors. This is a cross-build on Linux,
  not execution against WinRT or hardware.
- Reviewed fixed receive/transmit storage, bounded native admissions/callbacks,
  deadline/cancellation wakeups, TLS pinned-key verification, digest/commit
  confirmation, and public discovery metadata's lack of trust.

macOS native compilation cannot run on this Linux host. CI now includes macOS and
Windows native-helper builds and Go tests, in addition to the Linux race run.
Those newly added CI jobs have not been executed in this workspace.

## Reported hardware failures (2026-10-26)

A user attempting Mac-to-Windows 11 pairing reported an identity read failure
on macOS (`CBATTErrorDomain Code=14`, "Unlikely error"), followed on a later build
by a Windows advertising abort with `BluetoothError.Success`. The Windows adapter
model and driver version are unknown. Pairing has not been demonstrated on that
hardware; successful compilation and simulated transfers do not establish radio
interoperability.

Changes retain Windows event sources, handle identity read offsets, and let
pairing proceed with one failed advertising/discovery role. Windows now records
advertising status history and adapter/OS capabilities, handles partial startup,
and distinguishes intentional stops from aborts. The one-role pairing path is
covered by mutual TLS tests with both saved keys, including manual selection on
the non-advertising device. The underlying Windows advertising failure remains
undiagnosed; the updated path requires hardware verification. Transfer receiving
still requires working local peripheral advertising.

## Hardware acceptance (unrun)

For macOS↔Windows, macOS↔macOS and Windows↔Windows, in both sender/receiver roles:

1. Build the helpers on their native OS, verify permissions, enable Bluetooth,
   disable Wi-Fi, remove OS bonding, and confirm central/peripheral support.
   Windows adapter/driver peripheral advertising support is required; pairing
   also needs concurrent central scanning and peripheral advertising.
2. Pair with one-sided and simultaneous selections in both ID orders; verify
   learned names, aliases, explicit fingerprints, wrong-fingerprint refusal,
   known-key refusal and both saved confirmations. Exercise competing listeners.
3. Capture debug metrics for actual maximum write/notification value sizes. Check
   subscription-before-OPEN races, notification ownership, CCCD unsubscribe,
   size changes and clear refusal of links unable to carry 37-byte values.
4. Measure 1/10/100 MiB deterministic files, stdin/stdout and empty/one-byte input.
   Record both file throughput and transport counts/window state, CPU and peak
   resident memory. Check destination count/digest and no intermediate plaintext
   payload exposure. Repeat over RF interference and power-saving conditions.
5. Confirm CoreBluetooth pauses and resumes on readiness for both write commands
   and notifications. Verify WinRT awaited Write Without Response/Notify
   completions allow useful radio queue depth on actual drivers. If completions
   serialize to air time, measure before considering a bounded multi-operation
   WinRT queue; do not substitute Write With Response or GATT Read.
6. Stress duplex TLS, delayed readers, long pauses, disconnection, disabled/removed
   adapters, cancellation and deadlines. Verify bounded memory and deterministic
   process cleanup. Check restart always creates fresh sessions.
7. Exercise full disks, existing/unwritable paths, unsupported hard links,
   checksum/commit failures and final completion/close races. A committed file
   with a lost confirmation must not be reported as sender success. Check OS
   admission followed by helper teardown does not lose the final FIN_ACK.
8. Exercise pairing/session cancellation while a native send is blocked, and
   repeat transfers/TUI operations after cancellation or switching views.

Expected real-world performance is **unmeasured**. The engineering target is tens
of KiB/s or better where negotiated sizes, connection parameters, driver queues
and RF conditions permit it. Native IPC JSON/base64, scheduling and per-operation
WinRT completion may limit throughput before the radio. The 37-byte minimum,
Windows peripheral-role support, native callback ordering and teardown behavior
are explicit validation constraints, not compatibility promises.

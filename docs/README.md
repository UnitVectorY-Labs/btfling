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

Explore [usage](USAGE.md), [examples](EXAMPLE.md), [configuration](CONFIG.md), and
[protocol and security](PROTOCOL.md).

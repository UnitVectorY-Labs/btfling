# Configuration

## Location and automatic setup

The default is `~/.config/btfling/config.yaml` on macOS and Windows. Windows `~`
means the user profile; btfling does not switch to AppData or `XDG_CONFIG_HOME`.
Override the directory once before the command:

```sh
btfling --config /private/profile tui
```

The one persistent file holds the local identity, private seed, and known peers.
There is no separate certificate store or peer directory.

Running without arguments or invoking a recognized operation creates missing
configuration automatically. Initial setup generates a random 16-byte application
ID and a 32-byte Ed25519 private seed. The nickname comes from the hostname;
disallowed characters become hyphens, it is limited to 64 UTF-8 bytes, and an
unavailable/empty hostname falls back to `btfling`.

Explicit help commands do not create configuration. Invalid/unreadable existing
files are errors, not reasons to overwrite them. Existing device IDs and keys are
preserved. Legacy configurations without a local key acquire one on operational
startup; legacy peers without keys remain unusable for transfers until re-paired.

Use `btfling init NAME` before automatic setup to choose an initial name. It refuses
an existing configuration. Change the local name later in the TUI's Client tab
(n) or by editing the top-level `name` field while no configuration writes run.

## Schema

Configuration version remains `1`; it is separate from wire protocol version 2.
A structural example, omitting key fields for readability:

```yaml
version: 1
name: office-laptop
id: 0123456789abcdef0123456789abcdef
peers:
  - name: alice
    id: fedcba9876543210fedcba9876543210
```

Actual newly generated configurations also contain `private_key`; paired peers
contain `public_key`. Let btfling generate the IDs and key material rather than
copying these example IDs.

| Field | Type | Meaning |
| --- | --- | --- |
| `version` | Integer | Exactly `1`. |
| `name` | String | This device's name, used in discovery and hello. |
| `id` | String | Stable 16-byte random application ID, as 32 hexadecimal characters. |
| `private_key` | Byte sequence | Local 32-byte Ed25519 private seed. Generated automatically if absent. |
| `peers` | Optional sequence | Saved remote identity/trust records. |
| `peers[].name` | String | Local alias learned from the remote name, optionally overridden. |
| `peers[].id` | String | Stable remote 32-hex-character application ID. |
| `peers[].public_key` | Byte sequence | The trusted remote 32-byte Ed25519 public key. Legacy records may lack it. |

Key bytes are numeric YAML sequences, not PEM or base64 strings. Public keys use
base64 in JSON on the Bluetooth wire; the on-disk encoding differs. The serializer
omits empty peer lists and absent legacy keys. Unknown YAML fields are rejected.

The local public key is derived from the private seed and is not separately stored.
TLS certificates are generated in memory per session and are not persisted.
Fingerprints from `keygen` and `peers` are lowercase hexadecimal SHA-256 of the raw
32-byte public key, not a signature or a certificate fingerprint.

Names are case-sensitive, nonempty, no more than 64 UTF-8 bytes, and contain no
leading/trailing whitespace, control characters, `/`, or `\`. Peer names and IDs
must be unique; a peer ID cannot equal the local ID. Present keys must have 32 bytes.

## Trust lifecycle

```sh
btfling keygen
btfling peers
btfling rename alice personal-laptop
btfling forget personal-laptop
```

`keygen` prints the local fingerprint and preserves an existing key. Every transfer
requires saved keys and mutual TLS. There is no plaintext override.

Pairing learns the remote client name unless `-as NAME` overrides it. The TUI has
an optional alias field. Re-pairing a known ID with the same key is idempotent and
preserves its local alias unless explicitly overridden. An old identity-only
record can acquire a key through re-pairing. Name collisions across different IDs
require a unique alias.

A changed saved key is rejected, including during pairing. To intentionally replace
trust, forget and pair again on both devices, preferably with verified fingerprints.
Default pairing trusts first-use offered keys; compare fingerprints out of band or
supply `-trust FINGERPRINT` before saving.

## Storage protection and recovery

Unix directories use mode `0700` and files use `0600`. On Windows, protect the
containing directory with user-profile ACLs. Private seeds are **unencrypted on
disk**; someone who obtains a seed can impersonate that identity. Protect backups
accordingly. Restoring the whole file preserves names, device identity, and trust.
Do not clone it onto multiple active devices: they would share ID and private key.

Saves use same-directory `.config-*` temporary files and atomic replacement.
Normal cleanup removes temporary files; a crash can leave one behind. There is
no multi-process configuration merge/lock protocol, so avoid concurrent writes.
Run only one application Bluetooth operation per adapter; mutual pairing internally
uses both an advertising helper and a scanning helper.

If a file is damaged, restore a trusted backup or deliberately move it aside and
initialize a fresh identity. Peers then need to replace the old trust relationship.
Automatic setup does not discard malformed configuration. Do not share files
containing private seeds in bug reports or commit them to Git.

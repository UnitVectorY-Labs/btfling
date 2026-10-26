---
layout: default
title: Development
nav_order: 7
permalink: /development
---

# Development

## Validation

```sh
go test -race ./...
go vet ./...
go build ./...
```

The generated Go CI workflow builds and tests the Go packages on Linux. The
separate `ble-transport.yml` workflow adds a race run and native-helper compilation
with Go integration tests and standalone builds on macOS and Windows.
Native standalone builds launch the embedded helper with `--version` to verify
extraction, architecture, and runtime dependencies without using Bluetooth.
Native CI also checks automatic source builds and Windows identity long reads.
Native tests cover partial advertising startup, aborts before/after startup, and
intentional advertising stops. Go pairing tests complete mutual TLS and save
both peer keys with either side's advertising or discovery disabled.
CI does not exercise Bluetooth hardware. Build the complete
application on macOS or Windows using the [installation guide](INSTALL.md), or
run `just build-app`. `just build` validates the Go packages only;
`just build-helper` builds the native helper into `dist/`.

Run the stream/TLS simulations with:

```sh
go test ./internal/ble -run '^$' -bench . -benchtime=2x -count=1
go test ./internal/ble -run '^$' -fuzz FuzzFrame -fuzztime=10s
```

Verify automatic source-helper builds on macOS or Windows with
`BTFLING_TEST_NATIVE_BUILD=1` in the environment and
`go test ./internal/ble -run '^TestSourceHelperStarts$' -count=1`.
Test ATT identity read offset/length handling without Bluetooth hardware:

```sh
dotnet run --project internal/ble/nativetests/IdentityRead.Tests.csproj
```

See the [recorded simulations and physical-device checklist](design/streaming-results.md)
for results, limitations and remaining radio/OS acceptance tests. Native helper
metrics are available with `BTFLING_BLE_DEBUG=1`. On macOS this also logs identity
read failures that discovery skips and retries.
Windows diagnostics include the adapter name, OS build, role capabilities, and
advertising status history. An aborted state with `BluetoothError.Success` is
reported as an abort with no supplied error code; it is never treated as success.

`go run . diagnose` opts into structured logging without setting environment
variables. Tests exercise staggered and forced simultaneous pairing, report
exchange, random probe receipts, imported peer failures, deadlines, log
preservation, and rejection of reports with the wrong authenticated identity.
The diagnostic coordinator and report protocol are Go; the existing Swift/.NET
helpers remain the platform Bluetooth boundary. See [Pairing diagnostics](DIAGNOSTICS.md).

## Repository standards

[GitRepoForge](https://github.com/UnitVectorY-Labs/gitrepoforge) generates shared
repository files from the UnitVectorY-Labs templates. `.gitrepoforge` contains
this repository's settings; `.managedfiles` records which files or sections are
managed. Change settings in `.gitrepoforge` rather than editing generated files.
The README's first five lines and go.mod's first four lines are managed sections;
the remaining content is maintained here.

In the standard workspace, with `btfling`, `gitrepoforge`, and
`uvy-labs-gitrepoforge` as sibling directories, run from the workspace root:

```sh
gitrepoforge report --repo btfling
gitrepoforge apply --repo btfling --action stage
gitrepoforge validate --repo btfling
```

The workspace `.gitrepoforge-config` supplies the template repository path and
named actions. Its `stage` action writes files without committing or pushing.

`release: build-go` enables Go build/test CI. Release publishing is not configured.
Run `go run ./internal/buildstandalone` for a distributable single binary.
Use `-os windows -arch amd64` for Windows x64. The build stages a native helper
under `internal/ble/bundle`, embeds it with the `standalone` build tag, then
removes the staging directory. The original separate-helper build remains
available for development.

Set `config.versions.go` in `.gitrepoforge` to keep the Go version consistent
across go.mod and the generated workflows.

## Documentation

Pages use Just the Docs with Jekyll front matter (`layout`, `title`, `nav_order`,
and `permalink`). Keep the [documentation index](README.md) and root README links
in sync when adding or renaming pages. Update the matching documentation when
changing commands, configuration, or protocol behavior.

`just docs-serve` bootstraps a local Jekyll environment and serves the site at
`http://127.0.0.1:4000`. It requires Ruby and Bundler; local dependencies and site
output are ignored by Git.

The site configuration specifies `btfling.unitvectorylabs.com`. To publish it,
configure GitHub Pages to use the `main` branch's `/docs` directory and configure
the custom domain's DNS.

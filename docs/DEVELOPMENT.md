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

The generated Go CI workflow builds and tests the Go packages on Linux. It does
not compile the native helpers or exercise Bluetooth hardware. Build the complete
application on macOS or Windows using the [installation guide](INSTALL.md), or
run `just build-app`. `just build` validates the Go packages only;
`just build-helper` builds the native helper into `dist/`.

See the [physical-device checklist](PROTOCOL.md#physical-device-testing) for the
remaining radio and operating-system acceptance tests.

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
A distributable application consists of the Go executable and its platform's
native BLE helper in the same directory.

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

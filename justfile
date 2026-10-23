
# Commands for btfling
default:
  @just --list
# Build btfling with Go
build:
  go build ./...

# Run tests for btfling with Go
test:
  go clean -testcache
  go test ./...

# Serve documentation locally for testing (bootstraps local Jekyll setup)
docs-serve:
  #!/usr/bin/env bash
  set -euo pipefail
  cd docs

  if [[ ! -f Gemfile ]]; then
    printf '%s\n' \
      'source "https://rubygems.org"' \
      '' \
      'gem "github-pages", group: :jekyll_plugins' \
      'gem "jekyll-remote-theme"' \
      'gem "webrick"' > Gemfile
  fi

  bundle config set --local path vendor/bundle
  bundle check >/dev/null || bundle install
  bundle exec jekyll serve --host 127.0.0.1 --port 4000 --livereload

# Repository-specific commands
# Build the native BLE helper on macOS or Windows
build-helper:
  go run ./internal/buildhelper -out dist

# Build the Go command and native helper together on macOS or Windows
build-app:
  go build -o dist/ .
  go run ./internal/buildhelper -out dist

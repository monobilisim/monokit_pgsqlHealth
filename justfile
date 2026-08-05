# justfile for a monokit2 plugin — compile & test recipes.
# Run `just` or `just --list` to see the available recipes.
#
# This file is plugin-agnostic: the plugin name (used for the output binary name)
# is derived from the directory name, so the same justfile can be copied verbatim
# into any plugin directory.

# Plugin name == directory name == output binary name.
plugin  := file_name(justfile_directory())
# Podman/OCI image + volume names must be lowercase.
image   := lowercase(plugin) + "-tests"
# Version stamped into the binary via -ldflags. Override with `VERSION=1.0.0 just ...`.
version := env("VERSION", "devel")
# Output directory: this plugin's own ./bin (kept self-contained per plugin/repo).
bindir  := justfile_directory() / "bin"

# Show the available recipes.
default:
    @just --list

# Build the plugin for the host platform into ./bin/<plugin>.
build:
    @echo "Building {{plugin}} {{version}} for the host platform"
    mkdir -p "{{bindir}}"
    rm -f "{{bindir}}/{{plugin}}"
    go build -ldflags "-X main.version={{version}}" -o "{{bindir}}/{{plugin}}"

# Clean, then cross-compile the plugin for every release target.
build-all: clean (build-target "linux" "amd64") (build-target "linux" "arm64")

# Cross-compile the plugin for a single GOOS/GOARCH target.
build-target goos goarch:
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p "{{bindir}}"
    ext=""
    [ "{{goos}}" = "windows" ] && ext=".exe"
    out="{{bindir}}/{{plugin}}_{{version}}_{{goos}}_{{goarch}}${ext}"
    echo "Building {{plugin}} {{version}} for {{goos}} {{goarch}}"
    GOOS={{goos}} GOARCH={{goarch}} go build -ldflags "-X 'main.version={{version}}'" -o "$out"

# Run every test suite one by one.
test: test-postgres test-walg test-patroni test-consul test-haproxy

# Connection, activity, long-query and uptime tests against PostgreSQL 12, 15 and 18.
test-postgres filter="": (test-suite "postgres" filter)

# WAL-G verification tests against a stubbed wal-g binary.
test-walg filter="": (test-suite "walg" filter)

# Patroni cluster tests against a stubbed Patroni REST API.
test-patroni filter="": (test-suite "patroni" filter)

# Consul tests against a stubbed Consul HTTP API.
test-consul filter="": (test-suite "consul" filter)

# HAProxy tests against the real haproxy service via systemd.
test-haproxy filter="": (test-suite "haproxy" filter)

# Run one test suite inside a Podman container (boots systemd as PID 1).
# Each suite has its own Containerfile.<suite> so CI can run them in
# parallel: postgres (PG 12/15/18 matrix), walg, patroni, consul, haproxy.
# `just test-suite postgres TestConnectPSQL` narrows the suite to one test.
# Tests ALWAYS run inside Podman — never directly on the host.
test-suite suite filter="":
    #!/usr/bin/env bash
    set -euo pipefail

    suite={{ quote(suite) }}
    case "$suite" in
        postgres) default_run='TestConnectPSQL|TestCheckActivity|TestCheckLongRunningQueries|TestGetUptime' ;;
        walg)     default_run='TestCheckWalG|TestParseWalVerifyStatus' ;;
        patroni)  default_run='TestCheckPatroni' ;;
        consul)   default_run='TestCheckConsul' ;;
        haproxy)  default_run='TestCheckHAProxy|TestParseHAProxyBindPorts' ;;
        *) echo "unknown test suite: $suite" >&2; exit 1 ;;
    esac

    run="{{ filter }}"
    [ -z "$run" ] && run="$default_run"

    mkdir -p "{{ justfile_directory() }}/logs"

    podman build -t "{{ image }}-$suite" -f "Containerfile.$suite" .
    podman run --rm -t \
        --systemd=always \
        --tmpfs /run \
        --tmpfs /run/lock \
        -v {{ image }}-go-mod-cache:/go/pkg/mod \
        -v {{ image }}-go-build-cache:/root/.cache/go-build \
        -v "{{ justfile_directory() }}/logs":/artifacts \
        -e TEST_RUN="$run" \
        -e TEST_SUITE="$suite" \
        -e HOST_UID="$(id -u)" \
        -e HOST_GID="$(id -g)" \
        "{{ image }}-$suite"

# Build then run the plugin, forwarding any extra ARGS (e.g. `just run -v`).
run *args: build
    "{{bindir}}/{{plugin}}" {{args}}

# Update the monokit_lib dependency to the latest commit and tidy go.mod.
update-lib:
    go get github.com/monobilisim/monokit_lib@latest
    go mod tidy

# Remove this plugin's build artifacts from ./bin.
clean:
    #!/usr/bin/env bash
    set -euo pipefail
    echo "Removing {{plugin}} artifacts from {{bindir}}"
    rm -rf "{{bindir}}"

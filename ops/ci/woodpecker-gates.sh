#!/bin/bash
# The CI gate for cts-lite on Woodpecker (ci.metabolomics.us).
#
# It replaces two TeamCity build configurations:
#
#   backend_CtsLite            build + test + coverage (+ the E2E suite, which
#                              does NOT migrate -- see the `e2e` note below)
#   backend_CtsLiteOtelConfig  otelcol-contrib validate of the collector config
#
# RUN IT LOCALLY: `bash ops/ci/woodpecker-gates.sh` from a clean checkout runs
# everything; `... go` or `... otel` runs one half. It takes no Woodpecker
# input, so a green CI run is reproducible on a laptop.
#
# WHAT THE COVERAGE STEP IS. TeamCity did not merely print the coverage
# numbers: the build carried a `BuildFailureOnMetric` feature, metricKey
# CodeCoverageL, moreOrLess=less, metricThreshold=75 -- i.e. the build FAILED
# when line coverage fell below 75%. Woodpecker has no such feature, so the
# threshold is enforced here, in the repository, by the same arithmetic
# TeamCity's step used to publish the numbers. Dropping it to an echo would
# have turned a gate into a printout while leaving the tick green.
# TeamCity measured 654/756 = 86.5% on every build since #163; this script's
# arithmetic on the same tree reports 835/945 = 88.4% -- the denominators
# differ with the Go toolchain's own block accounting, the ratio does not
# meaningfully. Either way the floor starts with >11 points of headroom,
# exactly as it did under TeamCity.
#
# WHY THE E2E SUITE IS NOT HERE. playwright/ pins `channel: 'chrome'` -- real
# Google Chrome, which on Linux Playwright installs through apt. The exec
# hosts are unprivileged Slurm allocations inside a read-only CI image: there
# is no apt, no sudo, and no Chrome. See the issue linked from the migration
# PR. Independently of that, links.spec.js asserts HTTP < 400 against
# third-party sites, which is what actually reddened the last TeamCity build
# (a 403 from pmc.ncbi.nlm.nih.gov, not a defect in this repository).
set -euo pipefail

SELECTION="${1:-all}"

# Pinned to the version the TeamCity step used (its SIF was
# otelcol-contrib-0.153.0.sif). The checksum is the upstream release one.
OTEL_VERSION="0.153.0"
OTEL_SHA256="f7be4acf2c04058875073afc2e74ef885a66ef4fac8a4bfc93faa7235cb1c174"
OTEL_TARBALL="otelcol-contrib_${OTEL_VERSION}_linux_amd64.tar.gz"
OTEL_URL="https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v${OTEL_VERSION}/${OTEL_TARBALL}"

# The TeamCity threshold, transplanted. Overridable so the number can be
# raised in one place, not so a red build can be waved through.
COVERAGE_FLOOR="${CTS_COVERAGE_FLOOR:-75}"

journal=""
for candidate in "${WOODPECKER_JOURNAL_DIR:-}" /home/wohlgemuth/woodpecker/logs; do
  [ -n "$candidate" ] || continue
  if mkdir -p "$candidate" 2>/dev/null && [ -w "$candidate" ]; then
    journal="$candidate/cts-lite-gates-${CI_COMMIT_SHA:-local}-$(date +%s).log"
    break
  fi
done

step() { echo; echo "=== $* ==="; }

find_go() {
  if ! command -v go >/dev/null 2>&1; then
    for d in /usr/local/go/bin /opt/go/bin "${GOROOT:-}/bin" /usr/lib/golang/bin; do
      if [ -n "$d" ] && [ -x "$d/go" ]; then PATH="$d:$PATH"; export PATH; break; fi
    done
  fi
  if ! command -v go >/dev/null 2>&1; then
    echo "FATAL: no Go toolchain on this agent." >&2
    echo "Looked on PATH and in: /usr/local/go/bin /opt/go/bin \$GOROOT/bin /usr/lib/golang/bin" >&2
    exit 1
  fi
  go version
}

go_gate() {
  step "go toolchain"
  find_go
  # rdkit/ is cgo against a prebuilt static archive and needs a C++ linker.
  # Without one `go build ./...` fails deep inside the toolchain with a
  # link error that reads like a broken module, so say it plainly here.
  if ! command -v g++ >/dev/null 2>&1; then
    echo "FATAL: no g++ on this agent; rdkit/ is cgo and links -lstdc++." >&2
    exit 1
  fi

  step "build and test (with coverage)"
  # `go clean -testcache` and `go mod tidy` are carried over from the
  # TeamCity steps verbatim. tidy runs as a mutation there; the check that
  # it changes nothing is a separate step below.
  go clean -testcache
  go mod tidy
  go build ./...
  rm -rf .coveragedata coverage.out index.html
  mkdir .coveragedata
  # `-json` is dropped: it existed only to feed TeamCity's `golang` build
  # feature, which parsed the stream into its test report. Woodpecker has no
  # such parser, and the raw JSON makes the log unreadable. No assertion
  # changes -- the same tests run with the same coverage flags.
  go test ./... -covermode=atomic -coverpkg=./... -test.gocoverdir="$PWD/.coveragedata"

  step "go.mod / go.sum are tidy"
  # Strengthening, not parity: TeamCity ran `go mod tidy` and silently built
  # whatever it produced, so a drifted go.mod could never go red. Both files
  # are clean today, so this starts green.
  if ! git diff --quiet -- go.mod go.sum; then
    echo "FATAL: 'go mod tidy' changed go.mod/go.sum; commit the result." >&2
    git --no-pager diff -- go.mod go.sum >&2
    exit 1
  fi
  echo "unchanged by go mod tidy"

  step "coverage floor (>= ${COVERAGE_FLOOR}%)"
  go tool covdata textfmt -i=.coveragedata -o=coverage.out
  go tool cover -html=coverage.out -o=index.html
  # Same awk as the TeamCity step: column 2 is the statement count of a
  # block, column 3 its hit count.
  covered=$(awk 'NR>1 && $3>0 {c += $2} END {print c+0}' coverage.out)
  total=$(awk 'NR>1 {t += $2} END {print t+0}' coverage.out)
  echo "Covered statements: $covered"
  echo "Total statements:   $total"
  # A zero total is the failure mode that would otherwise pass this gate
  # loudest: an empty .coveragedata yields 0/0, and any "percentage" built
  # from it is a lie. covdata would have to have produced an empty profile.
  if [ "$total" -le 0 ]; then
    echo "FATAL: coverage profile is empty (total=$total); -test.gocoverdir produced nothing." >&2
    exit 1
  fi
  pct=$(awk -v c="$covered" -v t="$total" 'BEGIN { printf "%.4f", (c * 100) / t }')
  echo "Line coverage:      ${pct}%"
  if awk -v p="$pct" -v f="$COVERAGE_FLOOR" 'BEGIN { exit !(p < f) }'; then
    echo "FATAL: line coverage ${pct}% is below the ${COVERAGE_FLOOR}% floor" \
         "inherited from TeamCity's BuildFailureOnMetric(CodeCoverageL)." >&2
    exit 1
  fi
  echo "coverage floor met"
}

otel_gate() {
  step "otel collector config validate (otelcol-contrib ${OTEL_VERSION})"
  # TeamCity ran this out of a SIF staged on quobyte under the TeamCity
  # agent images directory. That path is not visible from the Woodpecker
  # exec hosts and is on its way out with TeamCity itself, so the release
  # binary is fetched into a temp dir instead and pinned by checksum.
  #
  # The binary, not the distroless container, also disposes of the trap the
  # TeamCity step documents at length: apptainer shell-expands --env values,
  # so passing the config through an environment variable emptied its own
  # ${env:...} placeholders in transit and failed a config docker accepted.
  # Here the collector reads the file and resolves the placeholders itself,
  # exactly as it does at runtime.
  local cfg="telemetry/collector/config.yaml"
  test -f "$cfg" || { echo "FATAL: $cfg not found" >&2; exit 1; }

  local cache="${CTS_OTEL_CACHE:-/tmp/woodpecker-cache/otelcol}"
  mkdir -p "$cache" || cache="$(mktemp -d)"
  local tgz="$cache/$OTEL_TARBALL"

  # Verify whatever is cached before trusting it, and re-download if it does
  # not match: concurrent agents share /tmp on a node.
  if ! [ -f "$tgz" ] || ! echo "$OTEL_SHA256  $tgz" | sha256sum -c - >/dev/null 2>&1; then
    echo "downloading $OTEL_URL"
    local tmp="$tgz.$$.part"
    curl -sSfL --retry 3 --retry-delay 5 --max-time 600 -o "$tmp" "$OTEL_URL"
    echo "$OTEL_SHA256  $tmp" | sha256sum -c - >/dev/null \
      || { echo "FATAL: checksum mismatch for $OTEL_TARBALL" >&2; rm -f "$tmp"; exit 1; }
    mv -f "$tmp" "$tgz"
  fi
  echo "$OTEL_SHA256  $tgz" | sha256sum -c -

  # Not a RETURN trap: it fires after the function's locals are gone, and
  # under `set -u` the cleanup itself then aborts the script -- after a
  # successful gate, which is the confusing direction to fail in.
  local bindir
  bindir="$(mktemp -d)"
  tar -xzf "$tgz" -C "$bindir" otelcol-contrib
  "$bindir/otelcol-contrib" --version

  # Placeholder-free dummies, as TeamCity used. They only have to satisfy
  # the exporter's schema; nothing is dialled by `validate`.
  GRAFANA_CLOUD_OTLP_ENDPOINT=http://dummy \
  GRAFANA_CLOUD_OTLP_AUTH_HEADER=dummy \
    "$bindir/otelcol-contrib" validate --config="$cfg"
  rm -rf "$bindir"
  echo "collector config valid"
}

main() {
  echo "commit:  ${CI_COMMIT_SHA:-<local>}"
  echo "selection: $SELECTION"
  case "$SELECTION" in
    all)  go_gate; otel_gate ;;
    go)   go_gate ;;
    otel) otel_gate ;;
    *)    echo "usage: $0 [all|go|otel]" >&2; exit 2 ;;
  esac
  echo
  echo "GATES PASSED ($SELECTION)"
}

# The run goes through a PIPELINE, not `exec > >(tee ...)`.
#
# Process substitution does not make the shell wait for the reader: a script
# that fails in seconds exits before tee drains its pipe, and the agent
# records nothing at all -- which loses exactly the runs that need
# explaining. A pipeline is waited on, and PIPESTATUS carries the body's
# status past tee, which would otherwise mask it with its own.
if [ -n "$journal" ]; then
  echo "journal: $journal"
  main 2>&1 | tee -a "$journal"
  exit "${PIPESTATUS[0]}"
fi
main

#!/usr/bin/env bash
# standalone.sh — prove that pod, shepherd and song each build, vet and test on
# their own, with no shared code and nothing outside the standard library.
#
# The platform claims these three "run fully standalone". This script is the gate
# for that claim. It is a script rather than a Go test because the thing being
# verified is the module boundary itself: a Go test inside one module cannot see
# what the other modules require, and a test that passed while the boundary
# rotted would be worse than no test.
#
# stenella/web/standalone_test.go asserts the same properties for the vendored
# trees from inside the Go toolchain, so `go test ./...` alone still catches a
# regression. The script exists for the case where you want the answer without
# running the test suite, and because it can report per-module detail.
#
# Usage: scripts/standalone.sh [--quiet]
set -uo pipefail

quiet=0
for arg in "$@"; do
  case "$arg" in
    --quiet) quiet=1 ;;
    -h|--help) sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown flag: $arg" >&2; exit 2 ;;
  esac
done

# Run from the repository root regardless of where the script was invoked.
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/.." && pwd)"
atp="$root/atp"

MODULES=(pod shepherd song)
fail=0

say()    { [ "$quiet" -eq 1 ] || printf '%s\n' "$*"; }
ok()     { say "  ok    $*"; }
bad()    { say "  FAIL  $*"; fail=1; }
oneline(){ printf '%s' "$*" | tr '\n' ' '; printf '\n'; }

# external_imports lists every non-stdlib package in the import graph, minus the
# module's own. `.Standard` is the toolchain's own answer to "is this standard
# library?", so this needs no allowlist to drift out of date — and unlike a
# "does the first path element contain a dot" heuristic it catches a domain-less
# module path, which is exactly what a sibling import looks like
# ("azzurrotech/pod" has no dot anywhere in its first element).
external_imports() {
  local dir="$1" mod="$2"
  ( cd "$dir" && go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./... 2>/dev/null ) \
    | grep -v '^$' \
    | grep -v "^${mod}\(/\|$\)" \
    | sort -u
}

required_deps() {
  # Module requirements that point at anything other than this module itself.
  local dir="$1"
  ( cd "$dir" && go list -m -f '{{if not .Main}}{{.Path}}{{end}}' all 2>/dev/null ) \
    | grep -v '^$' | sort -u
}

for m in "${MODULES[@]}"; do
  dir="$atp/$m"
  say ""
  say "$m"
  if [ ! -d "$dir" ]; then
    bad "$dir does not exist"
    continue
  fi

  mod="$( cd "$dir" && go list -m 2>/dev/null )"
  if [ -z "$mod" ]; then
    bad "no go.mod, or it does not parse"
    continue
  fi
  ok "module $mod"

  # 1. It builds on its own. This is the real standalone claim: nothing in this
  #    module's import graph resolves to a sibling.
  if out="$( cd "$dir" && go build ./... 2>&1 )"; then
    ok "go build"
  else
    bad "go build"
    say "$out" | sed 's/^/        /'
  fi

  # 2. It vets clean.
  if out="$( cd "$dir" && go vet ./... 2>&1 )"; then
    ok "go vet"
  else
    bad "go vet"
    say "$out" | sed 's/^/        /'
  fi

  # 3. Its tests pass on their own.
  if [ -n "$( cd "$dir" && go list ./... 2>/dev/null | grep _test || true )" ] \
     || ( cd "$dir" && go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./... 2>/dev/null | grep -q . ); then
    if out="$( cd "$dir" && go test -count=1 -timeout 120s ./... 2>&1 )"; then
      ok "go test"
    else
      bad "go test"
      say "$out" | sed 's/^/        /'
    fi
  else
    ok "go test (no test files)"
  fi

  # 4. Nothing outside the standard library is in the import graph. A grep that
  #    filters everything out exits 1, so the emptiness test is on the value and
  #    never on the command's status.
  deps="$( external_imports "$dir" "$mod" )"
  if [ -z "$deps" ]; then
    ok "standard library only"
  else
    bad "non-stdlib imports: $(oneline "$deps")"
  fi

  # 5. go.mod requires nothing. This is the check that would fail first if
  #    someone reached for a sibling module to share a type.
  deps="$( required_deps "$dir" )"
  if [ -z "$deps" ]; then
    ok "no module requirements"
  else
    bad "go.mod requires: $(oneline "$deps")"
  fi
done

say ""
if [ "$fail" -ne 0 ]; then
  say "standalone: FAILED — a core component is not standalone"
  exit 1
fi
say "standalone: pod, shepherd and song each build, vet and test on their own, stdlib only"

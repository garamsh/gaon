#!/usr/bin/env bash
# Reads the event stream `go test -json` writes on standard input and prints
# what plain `go test` hides for a package that passes: the package's own
# output, the output of every test that failed, and the tests that were
# skipped, per package and in total. It exits 0 whatever the stream holds; the
# exit status of the test run is the producer's, which the caller keeps with
# `set -o pipefail`.
set -euo pipefail

log=$(mktemp)
trap 'rm -f "$log"' EXIT
cat >"$log"

# A line that is not an event is something go test wrote outside the stream.
jq -Rr '. as $l | try (fromjson | empty) catch $l' "$log"

# Output of a package outside any test: the ok and FAIL lines, and whatever a
# TestMain prints.
jq -Rr 'try (fromjson | select(.Action == "output" and .Test == null) | .Output | rtrimstr("\n")) catch empty' "$log"

# Output of the build of a package (compiler errors, vet findings): since Go
# 1.24 it arrives as build-output events keyed by ImportPath, not as output.
jq -Rr 'try (fromjson | select(.Action == "build-output") | .Output | rtrimstr("\n")) catch empty' "$log"

# Output of each test that failed.
jq -Rrs '
  [split("\n")[] | try fromjson catch empty] as $events
  | ($events | map(select(.Action == "fail" and .Test != null) | [.Package, .Test]) | unique)[]
  | . as [$p, $t]
  | "--- output of failed test \($t) in \($p)",
    ($events[] | select(.Action == "output" and .Package == $p and .Test == $t) | .Output | rtrimstr("\n"))
' "$log"

# What was skipped, and the counts the numbers come from.
jq -Rrs '
  [split("\n")[] | try fromjson catch empty | select(.Test != null)] as $tests
  | ($tests | map(select(.Action == "skip")) | group_by(.Package) | map({p: .[0].Package, n: length})) as $skips
  | ($skips | map(.n) | add // 0) as $total
  | ($tests | map(select(.Action == "pass")) | length) as $passed
  | ($tests | map(select(.Action == "fail")) | length) as $failed
  | ($tests | map(select(.Action == "pass" or .Action == "fail" or .Action == "skip") | .Package) | unique | length) as $pkgs
  | ($skips[] | "test: \(.n) skipped in \(.p)"),
    (if $total == 0 then "test: no test was skipped" else "test: \($total) skipped in total" end),
    "test: \($passed) passed, \($failed) failed, \($total) skipped, in \($pkgs) packages with tests, counted from the events of go test -json"
' "$log"

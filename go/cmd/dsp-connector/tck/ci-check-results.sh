#!/usr/bin/env bash
# Checks a DSP TCK run's raw log against ci-expected-passing.txt - the CI
# workflow's own allowlist of currently-verified-passing tests (#83's CI
# workflow, go-tests.yml). A test id not listed in the allowlist is not
# checked either way (e.g. TP's 12 other sub-tests, which hang against a
# real connector - a TCK harness limitation, see tck.properties:122-158 -
# only TP:03-01/TP:03-02 are listed) - every id that IS listed must show
# SUCCESSFUL in the log, or the job fails.
#
# Usage: ./ci-check-results.sh <log-file> <allowlist-file>
set -euo pipefail

LOG_FILE="$1"
ALLOWLIST_FILE="$2"

missing=0
checked=0
while IFS= read -r test_id; do
  [ -z "$test_id" ] && continue
  checked=$((checked + 1))
  if grep -qF "SUCCESSFUL: ${test_id}" "$LOG_FILE"; then
    echo "PASS  $test_id"
  else
    echo "FAIL  $test_id (not SUCCESSFUL in $LOG_FILE)"
    missing=$((missing + 1))
  fi
done < "$ALLOWLIST_FILE"

echo
if [ "$missing" -gt 0 ]; then
  echo "$missing of $checked expected-passing test(s) did not pass."
  exit 1
fi

echo "All $checked expected-passing tests passed."

#!/usr/bin/env bash
set -euo pipefail
: "${CORDBRIEF_RUNTIME_DIR:?}" "${CORDBRIEF_CORE_DATA_DIR:?}"
# Setup already owns FD 9. Reuse only a demonstrably locked descriptor on this inode.
if ! { test /proc/self/fd/9 -ef "$CORDBRIEF_RUNTIME_DIR/runtime.lock" && grep -Eq 'FLOCK[[:space:]]+ADVISORY[[:space:]]+WRITE' /proc/self/fdinfo/9; } 2>/dev/null; then
    exec 9>>"$CORDBRIEF_RUNTIME_DIR/runtime.lock"
    flock -n 9 || { echo 'Collector runtime is busy' >&2; exit 1; }
fi
exec 8>>"$CORDBRIEF_CORE_DATA_DIR/commit.lock"
flock -n 8 || { echo 'Core is busy' >&2; exit 1; }
exec node "$(dirname "$0")/retention-publish.mjs" "$@"

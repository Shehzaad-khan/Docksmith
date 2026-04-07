#!/usr/bin/env bash
set -uo pipefail

# Docksmith feature test matrix
# Usage:
#   chmod +x scripts/test-matrix.sh
#   ./scripts/test-matrix.sh
#
# Optional env vars:
#   DOCKSMITH_BIN=./docksmith
#   USE_SUDO=1

DOCKSMITH_BIN="${DOCKSMITH_BIN:-./docksmith}"
USE_SUDO="${USE_SUDO:-1}"

PASS=0
FAIL=0
TMP_ROOT="$(mktemp -d -t docksmith-matrix-XXXXXX)"
BASE_TAG="matrix-$(date +%s)"

cleanup() {
  rm -rf "$TMP_ROOT"
}
trap cleanup EXIT

log() {
  printf '%s\n' "$*"
}

pass() {
  PASS=$((PASS + 1))
  printf '[PASS] %s\n' "$1"
}

fail() {
  FAIL=$((FAIL + 1))
  printf '[FAIL] %s\n' "$1"
  if [ -n "${2:-}" ]; then
    printf '       %s\n' "$2"
  fi
}

ds_cmd() {
  if [ "$USE_SUDO" = "1" ]; then
    sudo HOME="$HOME" "$DOCKSMITH_BIN" "$@"
  else
    "$DOCKSMITH_BIN" "$@"
  fi
}

assert_contains() {
  local haystack="$1"
  local needle="$2"
  printf '%s' "$haystack" | grep -Fq "$needle"
}

assert_matches() {
  local haystack="$1"
  local regex="$2"
  printf '%s' "$haystack" | grep -Eq "$regex"
}

run_test() {
  local name="$1"
  shift
  if "$@"; then
    pass "$name"
  else
    fail "$name" "See output above."
  fi
}

ensure_prereqs() {
  if [ ! -x "$DOCKSMITH_BIN" ]; then
    log "docksmith binary not found or not executable at: $DOCKSMITH_BIN"
    log "Build it first: go build -o docksmith ."
    exit 1
  fi

  if [ "$USE_SUDO" = "1" ]; then
    sudo -v || exit 1
  fi

  local imgs
  imgs="$(ds_cmd images 2>&1)"
  if ! assert_matches "$imgs" '^alpine[[:space:]]+latest'; then
    log "Missing base image alpine:latest in local Docksmith store."
    log "Import Alpine first, then rerun this script."
    exit 1
  fi
}

# 1) FROM success + basic sample build
# Expects sample-app context to build successfully.
test_from_success_sample_build() {
  local out rc
  out="$(ds_cmd build -t "${BASE_TAG}-sample:latest" ./sample-app 2>&1)"
  rc=$?
  [ $rc -eq 0 ] || { printf '%s\n' "$out"; return 1; }
  assert_contains "$out" "Successfully built"
}

# 2) FROM failure for missing base image
test_from_failure_missing_base() {
  local ctx out rc
  ctx="$TMP_ROOT/from-missing"
  mkdir -p "$ctx"
  cat > "$ctx/Docksmithfile" <<'EOF'
FROM doesnotexist:latest
CMD ["/bin/sh"]
EOF

  out="$(ds_cmd build -t "${BASE_TAG}-from-miss:latest" "$ctx" 2>&1)"
  rc=$?
  [ $rc -ne 0 ] && assert_contains "$out" "image not found in local store"
}

# 3) COPY + RUN chmod metadata change + runtime execution
test_copy_run_chmod_exec() {
  local ctx out rc run_out
  ctx="$TMP_ROOT/chmod"
  mkdir -p "$ctx"

  cat > "$ctx/app.sh" <<'EOF'
#!/bin/sh
echo RUN_OK
EOF

  cat > "$ctx/Docksmithfile" <<'EOF'
FROM alpine:latest
WORKDIR /app
COPY app.sh /app/app.sh
RUN chmod +x /app/app.sh
CMD ["/app/app.sh"]
EOF

  out="$(ds_cmd build --no-cache -t "${BASE_TAG}-chmod:latest" "$ctx" 2>&1)"
  rc=$?
  [ $rc -eq 0 ] || { printf '%s\n' "$out"; return 1; }

  run_out="$(ds_cmd run "${BASE_TAG}-chmod:latest" 2>&1)"
  rc=$?
  [ $rc -eq 0 ] && assert_contains "$run_out" "RUN_OK"
}

# 4) WORKDIR behavior
test_workdir() {
  local ctx out rc run_out
  ctx="$TMP_ROOT/workdir"
  mkdir -p "$ctx"

  cat > "$ctx/Docksmithfile" <<'EOF'
FROM alpine:latest
WORKDIR /app
CMD ["/bin/pwd"]
EOF

  out="$(ds_cmd build -t "${BASE_TAG}-workdir:latest" "$ctx" 2>&1)"
  rc=$?
  [ $rc -eq 0 ] || { printf '%s\n' "$out"; return 1; }

  run_out="$(ds_cmd run "${BASE_TAG}-workdir:latest" 2>&1)"
  rc=$?
  [ $rc -eq 0 ] && assert_matches "$run_out" '^/app$'
}

# 5) ENV default + -e override
test_env_override() {
  local ctx out rc run_default run_override
  ctx="$TMP_ROOT/env"
  mkdir -p "$ctx"

  cat > "$ctx/Docksmithfile" <<'EOF'
FROM alpine:latest
ENV GREETING=hello
CMD ["/bin/sh", "-c", "echo $GREETING"]
EOF

  out="$(ds_cmd build -t "${BASE_TAG}-env:latest" "$ctx" 2>&1)"
  rc=$?
  [ $rc -eq 0 ] || { printf '%s\n' "$out"; return 1; }

  run_default="$(ds_cmd run "${BASE_TAG}-env:latest" 2>&1)"
  rc=$?
  [ $rc -eq 0 ] || { printf '%s\n' "$run_default"; return 1; }

  run_override="$(ds_cmd run -e GREETING=bye "${BASE_TAG}-env:latest" 2>&1)"
  rc=$?
  [ $rc -eq 0 ] || { printf '%s\n' "$run_override"; return 1; }

  assert_matches "$run_default" '^hello$' && assert_matches "$run_override" '^bye$'
}

# 6) CMD default + command override
test_cmd_override() {
  local ctx out rc default_out override_out
  ctx="$TMP_ROOT/cmd"
  mkdir -p "$ctx"

  cat > "$ctx/Docksmithfile" <<'EOF'
FROM alpine:latest
CMD ["/bin/echo", "default-cmd"]
EOF

  out="$(ds_cmd build -t "${BASE_TAG}-cmd:latest" "$ctx" 2>&1)"
  rc=$?
  [ $rc -eq 0 ] || { printf '%s\n' "$out"; return 1; }

  default_out="$(ds_cmd run "${BASE_TAG}-cmd:latest" 2>&1)"
  rc=$?
  [ $rc -eq 0 ] || { printf '%s\n' "$default_out"; return 1; }

  override_out="$(ds_cmd run "${BASE_TAG}-cmd:latest" /bin/echo override-cmd 2>&1)"
  rc=$?
  [ $rc -eq 0 ] || { printf '%s\n' "$override_out"; return 1; }

  assert_contains "$default_out" "default-cmd" && assert_contains "$override_out" "override-cmd"
}

# 7) Cache hit/miss behavior
test_cache_hit_miss() {
  local ctx out1 out2 rc
  ctx="$TMP_ROOT/cache"
  mkdir -p "$ctx"

  cat > "$ctx/app.sh" <<'EOF'
#!/bin/sh
echo cache-test
EOF

  cat > "$ctx/Docksmithfile" <<'EOF'
FROM alpine:latest
WORKDIR /app
COPY app.sh /app/app.sh
RUN chmod +x /app/app.sh
CMD ["/app/app.sh"]
EOF

  out1="$(ds_cmd build -t "${BASE_TAG}-cache:latest" "$ctx" 2>&1)"
  rc=$?
  [ $rc -eq 0 ] || { printf '%s\n' "$out1"; return 1; }

  out2="$(ds_cmd build -t "${BASE_TAG}-cache:latest" "$ctx" 2>&1)"
  rc=$?
  [ $rc -eq 0 ] || { printf '%s\n' "$out2"; return 1; }

  assert_contains "$out2" "[CACHE HIT]"
}

# 8) images + rmi
test_images_rmi() {
  local ctx out rc imgs_after_build imgs_after_rmi
  ctx="$TMP_ROOT/rmi"
  mkdir -p "$ctx"

  cat > "$ctx/Docksmithfile" <<'EOF'
FROM alpine:latest
CMD ["/bin/sh"]
EOF

  out="$(ds_cmd build -t "${BASE_TAG}-rmi:latest" "$ctx" 2>&1)"
  rc=$?
  [ $rc -eq 0 ] || { printf '%s\n' "$out"; return 1; }

  imgs_after_build="$(ds_cmd images 2>&1)"
  assert_contains "$imgs_after_build" "${BASE_TAG}-rmi" || return 1

  out="$(ds_cmd rmi "${BASE_TAG}-rmi:latest" 2>&1)"
  rc=$?
  [ $rc -eq 0 ] || { printf '%s\n' "$out"; return 1; }

  imgs_after_rmi="$(ds_cmd images 2>&1)"
  ! assert_contains "$imgs_after_rmi" "${BASE_TAG}-rmi"
}

main() {
  ensure_prereqs

  log "Running Docksmith feature matrix..."
  run_test "FROM success + sample build" test_from_success_sample_build
  run_test "FROM failure on missing base" test_from_failure_missing_base
  run_test "COPY/RUN chmod metadata preserved" test_copy_run_chmod_exec
  run_test "WORKDIR applied at runtime" test_workdir
  run_test "ENV default and -e override" test_env_override
  run_test "CMD default and runtime override" test_cmd_override
  run_test "Cache hit on identical rebuild" test_cache_hit_miss
  run_test "images list and rmi remove" test_images_rmi

  log ""
  log "Summary: PASS=$PASS FAIL=$FAIL"
  if [ "$FAIL" -ne 0 ]; then
    exit 1
  fi
}

main "$@"

#!/usr/bin/env bash
set -euo pipefail

# One-command Docksmith demo flow.
# It prints the actual code/files, then runs commands in a clean sequence.
# Usage:
#   chmod +x scripts/demo-flow.sh
#   ./scripts/demo-flow.sh
# Optional env:
#   DOCKSMITH_BIN=./docksmith
#   USE_SUDO=1
#   DEMO_TAG=demo

DOCKSMITH_BIN="${DOCKSMITH_BIN:-./docksmith}"
USE_SUDO="${USE_SUDO:-1}"
DEMO_TAG="${DEMO_TAG:-demo}"
IMAGE_TAG="sample-app:${DEMO_TAG}"

say() {
  printf '\n%s\n' "$*"
}

print_rule() {
  printf '%s\n' "------------------------------------------------------------"
}

docksmith() {
  if [ "$USE_SUDO" = "1" ]; then
    sudo HOME="$HOME" "$DOCKSMITH_BIN" "$@"
  else
    "$DOCKSMITH_BIN" "$@"
  fi
}

run_step() {
  local title="$1"
  shift
  print_rule
  printf 'STEP: %s\n' "$title"
  printf 'CMD : %s\n' "$*"
  print_rule
  "$@"
}

ensure_prereqs() {
  if [ ! -x "$DOCKSMITH_BIN" ]; then
    echo "Error: docksmith binary not found or not executable at $DOCKSMITH_BIN"
    echo "Build it first: go build -o docksmith ."
    exit 1
  fi

  if [ "$USE_SUDO" = "1" ]; then
    sudo -v >/dev/null
  fi

  if [ ! -f "sample-app/Docksmithfile" ] || [ ! -f "sample-app/app.sh" ]; then
    echo "Error: run this from repo root so sample-app files are available."
    exit 1
  fi
}

show_source_files() {
  run_step "Print sample Docksmithfile" cat sample-app/Docksmithfile
  run_step "Print sample app script" cat sample-app/app.sh
}

main() {
  say "Docksmith demo flow starting..."
  ensure_prereqs

  show_source_files

  run_step "Show current local images (before build)" docksmith images

  run_step "Build sample image" docksmith build -t "$IMAGE_TAG" ./sample-app

  run_step "Show local images (after build)" docksmith images

  run_step "Run image with default CMD" docksmith run "$IMAGE_TAG"

  run_step "Run image with runtime env override (-e GREETING=Hi_from_runtime)" \
    docksmith run -e GREETING=Hi_from_runtime "$IMAGE_TAG"

  run_step "Run image with command override (ignore default CMD)" \
    docksmith run "$IMAGE_TAG" /bin/sh -c 'echo override-command-works; pwd; id'

  run_step "Rebuild same image to demonstrate cache hits" docksmith build -t "$IMAGE_TAG" ./sample-app

  run_step "Build with --no-cache (forces recompute)" docksmith build --no-cache -t "$IMAGE_TAG" ./sample-app

  say "Demo completed successfully."
  say "Optional cleanup command:"
  echo "  ${DOCKSMITH_BIN} rmi ${IMAGE_TAG}"
}

main "$@"

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

# say prints one message with a leading newline so each major section
# in the demo output is visually separated and easier to follow.
say() {
  printf '\n%s\n' "$*"
}

# print_rule draws the separator line used between demo steps.
# It keeps the terminal output structured for live presentation.
print_rule() {
  printf '%s\n' "------------------------------------------------------------"
}

# docksmith runs the Docksmith binary with optional sudo passthrough.
# This keeps all command invocations consistent in one helper.
docksmith() {
  if [ "$USE_SUDO" = "1" ]; then
    sudo HOME="$HOME" "$DOCKSMITH_BIN" "$@"
  else
    "$DOCKSMITH_BIN" "$@"
  fi
}

# run_step prints a labeled step header, echoes the exact command,
# and then executes it so users can follow the flow in real time.
run_step() {
  local title="$1"
  shift
  print_rule
  printf 'STEP: %s\n' "$title"
  printf 'CMD : %s\n' "$*"
  print_rule
  "$@"
}

# ensure_prereqs validates binary availability, permissions, sample files,
# and Alpine base-layer integrity before running the main demo flow.
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

  # Validate alpine:latest backing layer exists.
  # rmi behavior can leave alpine manifest present while its layer file is missing.
  local images_dir layers_dir manifest_path layer_digest layer_path
  images_dir="$HOME/.docksmith/images"
  layers_dir="$HOME/.docksmith/layers"
  manifest_path="$images_dir/alpine_latest.json"

  if [ ! -f "$manifest_path" ]; then
    echo "Error: missing base image manifest: $manifest_path"
    echo "Import Alpine base image first, then rerun this demo."
    exit 1
  fi

  layer_digest="$(awk '
    /"layers"[[:space:]]*:/ {in_layers=1}
    in_layers && /"digest"[[:space:]]*:/ {
      gsub(/[",]/, "", $2)
      print $2
      exit
    }
  ' "$manifest_path")"

  if [ -z "$layer_digest" ]; then
    echo "Error: could not parse alpine layer digest from $manifest_path"
    echo "Re-import Alpine base image, then rerun this demo."
    exit 1
  fi

  layer_path="$layers_dir/$layer_digest"
  if [ ! -f "$layer_path" ]; then
    echo "Error: alpine manifest exists but layer file is missing:"
    echo "  $layer_path"
    echo "Re-import Alpine (manifest + layer), then rerun this demo."
    exit 1
  fi
}

# show_source_files prints the sample Docksmithfile and app script first,
# so the audience sees the input files before build and run steps.
show_source_files() {
  run_step "Print sample Docksmithfile" cat sample-app/Docksmithfile
  run_step "Print sample app script" cat sample-app/app.sh
}

# main executes the full happy-path demo in sequence: inspect, build, run,
# override env/command, then show cache hit and no-cache behavior.
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

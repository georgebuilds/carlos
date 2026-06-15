#!/usr/bin/env bash
# usershell-demo-env.sh - launch carlos in a throwaway HOME for the VHS demo.
#
# Creates a fresh temp HOME and a minimal-but-complete config (so the TUI
# skips onboarding and lands straight in the chat view), then execs the
# pre-built carlos binary against it. The user-shell `!` feature never calls
# the LLM, so the fake API key below is fine: the demo only types shell
# commands, it never sends a chat turn.
#
# The binary must be built first (the tape does this):
#   go build -o /tmp/carlos-demo ./cmd/carlos
set -euo pipefail

BIN="${CARLOS_DEMO_BIN:-/tmp/carlos-demo}"
if [ ! -x "$BIN" ]; then
  echo "carlos demo binary not found at $BIN (build it with: go build -o /tmp/carlos-demo ./cmd/carlos)" >&2
  exit 1
fi

DEMO_HOME="$(mktemp -d "${TMPDIR:-/tmp}/carlos-demo.XXXXXX")"
mkdir -p "$DEMO_HOME/.carlos"
CONFIG_PATH="$DEMO_HOME/.carlos/config.yaml"

cat > "$CONFIG_PATH" <<'YAML'
user_name: Boss
providers:
  anthropic:
    api_key: sk-ant-demo-not-a-real-key
    default_model: claude-sonnet-4-6
default_provider: anthropic
daemon:
  enabled: false
skills:
  convention: agents
YAML
chmod 600 "$CONFIG_PATH"

# Clean up the throwaway HOME when the demo session ends.
trap 'rm -rf "$DEMO_HOME"' EXIT

export HOME="$DEMO_HOME"
export CARLOS_CONFIG="$CONFIG_PATH"
exec "$BIN"

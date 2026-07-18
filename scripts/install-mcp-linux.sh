#!/usr/bin/env bash
set -euo pipefail

BINARY_PATH="${1:-}"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"
INSTALL_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/scopenest/mcp"
INSTALLED_BINARY="$INSTALL_DIR/scopenest-mcp"

mkdir -p "$INSTALL_DIR"
chmod 700 "$INSTALL_DIR"

if [[ -n "$BINARY_PATH" ]]; then
  SOURCE_BINARY="$(realpath "$BINARY_PATH")"
  [[ -f "$SOURCE_BINARY" ]] || { echo "MCP binary not found: $SOURCE_BINARY" >&2; exit 1; }
  install -m 700 "$SOURCE_BINARY" "$INSTALLED_BINARY"
else
  command -v go >/dev/null 2>&1 || { echo "Go 1.25+ is required, or pass a prebuilt binary as the first argument." >&2; exit 1; }
  GO_VERSION="$(go version)"
  [[ "$GO_VERSION" =~ go1\.([0-9]+) ]] || { echo "Could not determine the installed Go version." >&2; exit 1; }
  (( BASH_REMATCH[1] >= 25 )) || { echo "Go 1.25+ is required; found $GO_VERSION." >&2; exit 1; }
  TEMP_BINARY="$(mktemp "$INSTALL_DIR/.scopenest-mcp.XXXXXX")"
  trap 'rm -f "$TEMP_BINARY"' EXIT
  (cd "$REPO_ROOT/native-host" && go build -buildvcs=false -trimpath -ldflags='-s -w' -o "$TEMP_BINARY" ./cmd/scopenest-mcp)
  chmod 700 "$TEMP_BINARY"
  mv -f "$TEMP_BINARY" "$INSTALLED_BINARY"
  trap - EXIT
fi

[[ -x "$INSTALLED_BINARY" ]] || { echo "Installed MCP executable is not executable." >&2; exit 1; }
echo "ScopeNest MCP installed at $INSTALLED_BINARY"
echo "No browser registration or AI-client configuration was changed."
printf 'Register with Codex: codex mcp add scopenest -- %q\n' "$INSTALLED_BINARY"

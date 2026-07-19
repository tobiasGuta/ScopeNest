#!/usr/bin/env bash
set -euo pipefail

EXTENSION_ID="${1:-nnmpnmnmmfoedjeionoopgnbjnepfolh}"
BINARY_PATH="${2:-}"
HOST_NAME="com.scopenest.host"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"
INSTALL_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/scopenest/native-host"
INSTALLED_BINARY="$INSTALL_DIR/scopenest-host"

json_escape() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  printf '%s' "$value"
}

if [[ ! "$EXTENSION_ID" =~ ^[a-p]{32}$ ]]; then
  echo "Invalid extension ID. Expected 32 characters in the range a-p." >&2
  exit 1
fi

mkdir -p "$INSTALL_DIR"
chmod 700 "$INSTALL_DIR"

if [[ -n "$BINARY_PATH" ]]; then
  SOURCE_BINARY="$(realpath "$BINARY_PATH")"
  [[ -f "$SOURCE_BINARY" ]] || { echo "Native host binary not found: $SOURCE_BINARY" >&2; exit 1; }
  install -m 700 "$SOURCE_BINARY" "$INSTALLED_BINARY"
else
  command -v go >/dev/null 2>&1 || { echo "Go 1.25+ is required, or pass a prebuilt binary as the second argument." >&2; exit 1; }
  (cd "$REPO_ROOT/native-host" && go build -buildvcs=false -trimpath -ldflags='-s -w' -o "$INSTALLED_BINARY" ./cmd/scopenest-host)
  chmod 700 "$INSTALLED_BINARY"
fi

MANIFEST_DIRS=(
  "${XDG_CONFIG_HOME:-$HOME/.config}/google-chrome/NativeMessagingHosts"
  "${XDG_CONFIG_HOME:-$HOME/.config}/chromium/NativeMessagingHosts"
  "${XDG_CONFIG_HOME:-$HOME/.config}/BraveSoftware/Brave-Browser/NativeMessagingHosts"
  "${XDG_CONFIG_HOME:-$HOME/.config}/microsoft-edge/NativeMessagingHosts"
)

for dir in "${MANIFEST_DIRS[@]}"; do
  mkdir -p "$dir"
  manifest="$dir/$HOST_NAME.json"
  temp="$(mktemp "$dir/.scopenest-manifest.XXXXXX")"
  escaped_binary="$(json_escape "$INSTALLED_BINARY")"
  printf '%s\n' \
    '{' \
    "  \"name\": \"$HOST_NAME\"," \
    '  "description": "ScopeNest native messaging companion",' \
    "  \"path\": \"$escaped_binary\"," \
    '  "type": "stdio",' \
    "  \"allowed_origins\": [\"chrome-extension://$EXTENSION_ID/\"]" \
    '}' > "$temp"
  chmod 600 "$temp"
  mv -f "$temp" "$manifest"
  [[ -s "$manifest" ]] || { echo "Manifest validation failed: $manifest" >&2; exit 1; }
  echo "Installed $manifest"
done

[[ -x "$INSTALLED_BINARY" ]] || { echo "Installed host is not executable." >&2; exit 1; }
echo "ScopeNest native host installed at $INSTALLED_BINARY"
echo "Authorized extension ID: $EXTENSION_ID"
echo "Restart your browser, then reopen ScopeNest."

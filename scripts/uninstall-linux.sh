#!/usr/bin/env bash
set -euo pipefail

HOST_NAME="com.scopenest.host"
INSTALL_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/scopenest/native-host"
MANIFEST_DIRS=(
  "${XDG_CONFIG_HOME:-$HOME/.config}/google-chrome/NativeMessagingHosts"
  "${XDG_CONFIG_HOME:-$HOME/.config}/chromium/NativeMessagingHosts"
  "${XDG_CONFIG_HOME:-$HOME/.config}/BraveSoftware/Brave-Browser/NativeMessagingHosts"
  "${XDG_CONFIG_HOME:-$HOME/.config}/microsoft-edge/NativeMessagingHosts"
)

for dir in "${MANIFEST_DIRS[@]}"; do
  manifest="$dir/$HOST_NAME.json"
  if [[ -f "$manifest" ]]; then rm -f -- "$manifest"; echo "Removed $manifest"; fi
done
if [[ -d "$INSTALL_DIR" ]]; then rm -rf -- "$INSTALL_DIR"; echo "Removed $INSTALL_DIR"; fi

if [[ "${1:-}" == "--remove-data" ]]; then
  DATA_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/ScopeNest"
  case "$DATA_DIR" in
    "$HOME"/*) rm -rf -- "$DATA_DIR"; echo "Removed ScopeNest container data." ;;
    *) echo "Refusing to remove unexpected data path: $DATA_DIR" >&2; exit 1 ;;
  esac
else
  echo "Container data was preserved. Pass --remove-data to remove it explicitly."
fi
echo "ScopeNest native-host registration removed."

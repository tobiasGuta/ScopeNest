#!/usr/bin/env bash
set -euo pipefail

INSTALL_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/scopenest/mcp"
INSTALLED_BINARY="$INSTALL_DIR/scopenest-mcp"

rm -f -- "$INSTALLED_BINARY"
if [[ -d "$INSTALL_DIR" ]]; then
  rmdir -- "$INSTALL_DIR" 2>/dev/null || true
fi

echo "ScopeNest MCP executable removed. Containers, certificates, proxies, templates, and metadata were preserved."
echo "Remove the scopenest entry from each MCP client configuration separately."

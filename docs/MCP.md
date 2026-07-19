# ScopeNest MCP

ScopeNest includes an optional provider-neutral Model Context Protocol server, `scopenest-mcp`. A local MCP client can create, inspect, launch, and close a deliberately limited set of ScopeNest browser containers. The server cannot operate pages inside those browsers.

## Architecture and security boundary

```text
Codex Desktop or another MCP client
        | MCP JSON-RPC over stdin/stdout
        v
scopenest-mcp (one local process, no listener)
        | serialized internal Go calls
        v
host.Host
        | existing validation and ownership controls
        v
locked store / browser launcher / certificate manager
```

The MCP executable initializes the same data directory, store migrations, certificate manager, browser launcher, and long-lived `host.Host` used by `scopenest-host`. MCP inputs are strictly decoded by the MCP layer and mapped through a separate allowlist. Ordinary commands pass through `Host.Handle`; launch uses the dedicated `Host.LaunchForMCP` path so MCP-only launch restrictions remain inside the host's launch-reservation transaction. There is no generic command tool or remotely supplied launch-policy argument.

The server uses the official [`github.com/modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk) v1.6.1 stable release and its newline-delimited JSON `StdioTransport`. It does not start HTTP, TCP, WebSocket, SSE, named-pipe, Unix-socket, or other listeners.

## What it cannot do

The first MCP version cannot:

- browse, click, read page content, or run security tests;
- read cookies, browser profiles, credentials, or local browsing state;
- execute arbitrary commands, executables, Chromium arguments, or ScopeNest commands;
- delete or update containers;
- create, update, or delete proxy profiles or environment templates;
- import, delete, install, remove, or acknowledge certificate trust;
- access arbitrary files;
- add extension permissions or communicate through the extension;
- directly use a cloud AI API, telemetry service, or remote configuration service.

Use launch operations only for systems you own or are authorized to test.

## Model-provider privacy boundary

ScopeNest MCP runs locally, but the selected MCP client may transmit tool names, arguments, and sanitized results to its model provider. Container names, proxy names and listener metadata, template names and descriptions, certificate IDs, browser types, and running-state metadata may therefore leave the device. ScopeNest excludes proxy bypass rules from MCP summaries, but other engagement-sensitive labels and infrastructure metadata remain visible to the client. Review the client's privacy, retention, and training settings before using real engagement names or confidential infrastructure details.

The statement that ScopeNest does not directly use a cloud AI API describes the local MCP server, not the behavior of Codex, Claude, Gemini, or another MCP client.

## Requirements and builds

Go 1.25 or newer is required. The selected stable MCP Go SDK also declares Go 1.25, matching `native-host/go.mod` and CI.

Windows, from the repository root:

```powershell
Set-Location native-host
go test -count=1 ./...
go build -buildvcs=false -trimpath -o ..\bin\scopenest-mcp.exe .\cmd\scopenest-mcp
```

Linux:

```bash
mkdir -p bin
(cd native-host && go test -count=1 ./...)
(cd native-host && go build -buildvcs=false -trimpath -o ../bin/scopenest-mcp ./cmd/scopenest-mcp)
```

The existing `scopenest-host` and the new `scopenest-mcp` are separate executables. Installing one does not install or register the other.

## Per-user installation

The MCP installers do not require administrator/root access, write registry keys or browser native-host manifests, or edit any AI-client configuration.

Windows, build from source:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install-mcp-windows.ps1
```

Windows, install a prebuilt binary:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install-mcp-windows.ps1 -BinaryPath .\bin\scopenest-mcp.exe
```

The destination is `%LOCALAPPDATA%\ScopeNest\MCP\scopenest-mcp.exe`. The installer restricts the MCP directory ACL to the current user.

Linux, build from source or install a prebuilt binary:

```bash
chmod +x scripts/install-mcp-linux.sh scripts/uninstall-mcp-linux.sh
./scripts/install-mcp-linux.sh
./scripts/install-mcp-linux.sh ./bin/scopenest-mcp
```

The destination is `${XDG_DATA_HOME:-$HOME/.local/share}/scopenest/mcp/scopenest-mcp`, with mode `0700` on the directory and executable.

## Register with Codex

The commands below were verified against the locally installed `codex mcp add --help`, whose stdio syntax is `codex mcp add <NAME> -- <COMMAND>...`.

Windows PowerShell:

```powershell
codex mcp add scopenest -- "$env:LOCALAPPDATA\ScopeNest\MCP\scopenest-mcp.exe"
```

Linux:

```bash
codex mcp add scopenest -- "${XDG_DATA_HOME:-$HOME/.local/share}/scopenest/mcp/scopenest-mcp"
```

Verify registration:

```text
codex mcp list
codex mcp get scopenest
```

Restart Codex Desktop after changing MCP configuration. In a new task, ask Codex to list the available ScopeNest tools or call `scopenest_ping`. Tool discovery should show exactly the 11 tools below.

Codex stores durable MCP configuration in its shared `config.toml`. The equivalent generic Codex TOML shape is:

```toml
[mcp_servers.scopenest]
command = "C:\\Users\\YOUR_USER\\AppData\\Local\\ScopeNest\\MCP\\scopenest-mcp.exe"
args = []
enabled = true
```

Prefer `codex mcp add` so Codex writes the configuration using its current supported format.

## Other MCP clients

The server is provider-neutral and is designed for standards-compliant clients that can start a local stdio MCP process. A commonly used generic configuration shape is:

```json
{
  "mcpServers": {
    "scopenest": {
      "command": "/home/YOUR_USER/.local/share/scopenest/mcp/scopenest-mcp",
      "args": []
    }
  }
}
```

Configuration keys and restart behavior are client-specific. ScopeNest has not been manually tested with Claude or Gemini, so this document does not claim product-specific compatibility with them.

## Tools

| MCP tool | ScopeNest host command | Behavior |
| --- | --- | --- |
| `scopenest_ping` | `ping` | Versions, platform, protocol, and cleanup state |
| `scopenest_get_status` | `get_status` | Sanitized health, counts, browser types, references, and trust capabilities |
| `scopenest_list_containers` | `list_containers` | Sanitized saved and temporary containers |
| `scopenest_list_running_containers` | `get_running_containers` | Sanitized running subset |
| `scopenest_list_proxy_profiles` | `list_proxy_profiles` | Existing non-secret loopback proxy metadata |
| `scopenest_list_environment_templates` | `list_environment_templates` | Existing template metadata and references |
| `scopenest_get_container_readiness` | `get_container_readiness` | Effective network, listener, certificate states, warnings, and readiness |
| `scopenest_create_container` | `create_container` | Create a persistent isolated profile |
| `scopenest_create_temporary_container` | `create_temporary_container` | Create a disposable profile cleaned after safe owned-process exit |
| `scopenest_launch_container` | `launch_container` | Identity-confirmed browser launch at an optional HTTP(S) URL |
| `scopenest_close_container` | `close_container` | Identity-confirmed close, limited to this MCP process's owned process tree |

Read-only tools are annotated read-only, idempotent, and closed-world. Create tools are additive, non-idempotent, and closed-world. Close is annotated destructive and closed-world. Launch is conservatively annotated destructive, non-idempotent, and open-world because opening an arbitrary HTTP(S) URL can contact an external entity and cause side effects.

### Examples

Check readiness before launching a proxy/template container:

```json
{"id":"0123456789abcdef0123456789abcdef"}
```

Create a direct Chrome container. MCP creation accepts only `chrome`, `chromium`, `edge`, or `brave`; ScopeNest resolves a detected executable and the MCP schema does not accept `browserExecutable`:

```json
{
  "name": "Target - User A",
  "color": "#725cff",
  "icon": "A",
  "browserType": "chrome",
  "networkMode": "direct"
}
```

Launch requires the exact current name as an identity and staleness check:

```json
{
  "id": "0123456789abcdef0123456789abcdef",
  "expectedName": "Target - User A",
  "url": "https://example.com/authorized-test"
}
```

`expectedName` is not human approval: an MCP client can obtain the name from `scopenest_list_containers`. If the ID is absent or the name has changed, the browser is not launched. The expected name and standard browser type are checked against the current container record while the shared store lock is held in the same transaction that creates the launch reservation. The host also validates URL scheme, credentials, length, browser path, proxy/template/certificate state, profile locks, and duplicate-launch state.

Containers configured with `browserType: "custom"` through the human-operated extension cannot be launched by MCP and return `CUSTOM_BROWSER_REQUIRES_HUMAN_LAUNCH`. Launch them explicitly through the extension instead.

## Output privacy and errors

MCP responses use explicit output models. They never return profile paths, ScopeNest data-directory paths, full browser executable paths, PIDs, launch tokens/reservations, certificate DER/Base64, private keys, lock paths, operating-system trust-store paths, raw metadata records, internal Go errors, or stack traces.

Proxy profiles expose only their name, enabled state, protocol, loopback host/port, certificate IDs, health-check configuration, and unavailable behavior. Bypass rules are excluded from MCP output. ScopeNest's loopback-only proxy validation is unchanged.

A rejected host operation is an MCP tool error, not a successful tool result containing an error string. Its structured body preserves the stable ScopeNest `errorCode` with a fixed redacted message, for example:

```json
{
  "success": false,
  "command": "launch_container",
  "errorCode": "PROXY_LISTENER_UNAVAILABLE",
  "message": "The configured proxy listener is unavailable."
}
```

Input schemas set `additionalProperties: false`, and MCP handlers independently require exactly one JSON object and strictly decode it before invoking the corresponding host method. Commands routed through `Host.Handle` are then strictly decoded again.

Standard output is reserved for MCP protocol traffic. The server is silent by default and does not enable the SDK logging capability.

## Separate-process ownership

The extension and MCP server share locked metadata, but never process ownership:

```text
Chrome extension -> scopenest-host process A -> ownership map A
AI client        -> scopenest-mcp  process B -> ownership map B
```

After refresh/reopen, the extension can observe containers created or launched through MCP because both processes use the same store. It cannot kill MCP-owned browser processes. MCP cannot kill extension-owned browser processes. `PROCESS_NOT_OWNED` is expected across that boundary, and a persisted PID never grants termination authority.

The MCP process can close a browser it launched while that same MCP process remains the owner. On Windows, the owner's kill-on-close Job Object means exiting the MCP server may close its browser trees. On Linux, an unexpected owner exit may leave the process group running; close that browser normally. Chromium profile-lock markers continue to prevent unsafe deletion or relaunch.

This integration deliberately does not add a shared broker or daemon.

## Troubleshooting

### Codex does not show the tools

Run `codex mcp get scopenest`, confirm the executable path exists, then restart Codex Desktop. Call `scopenest_ping` in a new task.

### The MCP process exits immediately

Run the executable only through an MCP client; it expects MCP JSON-RPC on stdin. Reinstall if the binary is missing. ScopeNest intentionally emits no detailed initialization diagnostics that could expose local paths.

### `INVALID_BROWSER_PATH`

Install a supported Chromium-family browser. Standard `chrome`, `chromium`, `edge`, and `brave` selections are resolved from ScopeNest's existing detected-browser list. For `custom`, pass an existing recognized Chromium-family executable and let the host validate it.

### `PROCESS_NOT_OWNED`

The browser was launched by the extension, another MCP server process, or an earlier owner process. Close the browser window normally. Do not use a persisted PID as kill authority.

### `PROXY_LISTENER_UNAVAILABLE` or readiness warnings

Start the existing loopback proxy listener, correct the proxy through the human-operated extension, or follow the configured unavailable behavior. MCP cannot mutate the proxy profile.

### Temporary cleanup remains pending

Close all windows using that profile. Startup cleanup runs asynchronously once after the first valid MCP command and uses the existing safe cleanup and profile-lock checks.

## Removal

Remove the Codex registration first:

```text
codex mcp remove scopenest
```

Then remove only the MCP executable:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\uninstall-mcp-windows.ps1
```

```bash
./scripts/uninstall-mcp-linux.sh
```

These scripts preserve ScopeNest containers, profiles, certificates, proxies, templates, metadata, extension installation, native-host executable, browser manifests, and registry entries. An unexpected extra file in the MCP install directory is preserved rather than recursively deleted.

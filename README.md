# ScopeNest

**Isolated browser contexts for testing, research, and development.**

ScopeNest is a local Chrome Manifest V3 extension and Go native-messaging companion for opening Chromium-based browsers in genuinely separate user-data directories. It is designed for security researchers, developers, QA engineers, and bug-bounty hunters who need to use the same site as several roles without mixing login state.

Each saved or temporary container opens as a separate browser window backed by its own profile directory. Cookies, local storage, IndexedDB, cache, service workers, authentication sessions, history, permissions, and profile settings are therefore separated by the browser itself.

## Why a native companion is necessary

Chrome does not expose Firefox's `contextualIdentities` API, and an extension cannot create arbitrary cookie stores for ordinary tabs. Swapping cookies would be incomplete and unsafe because modern session state also lives in storage, service workers, cache, and browser-managed databases. ScopeNest does not imitate isolation inside one window. It launches a browser with:

```text
--user-data-dir=<managed ScopeNest container>/profile
--profile-directory=Default
--new-window
```

No shell is involved. The Go host passes every argument separately to the operating system.

## Features

- Create, edit, duplicate, search, filter, sort, and permanently delete named contexts.
- Assign a color and optional emoji/icon to each container.
- Launch a blank window, a manually entered HTTP(S) URL, or the current page.
- See running state, last launch, selected browser, and exact profile path.
- Create fresh temporary contexts that are removed after their launched process exits when safe.
- Retry failed temporary cleanup at host startup or with the cleanup protocol command.
- Use the polished action popup or the wider Chrome side panel.
- Detect Chrome, Chromium, Edge, and Brave on Windows and Linux, with a custom executable option.
- Keep preferences in `chrome.storage.local` and authoritative container metadata in the local host.
- Operate with no analytics, advertising, telemetry, external service, or page-content access.

## Architecture

```text
┌──────────────────────────────── Chrome / Edge ────────────────────────────────┐
│ ScopeNest popup or side panel                                                │
│   └─ chrome.storage.local (sort/filter/last browser preference only)         │
│         │ validated, versioned commands                                      │
│ MV3 service worker ── persistent chrome.runtime.connectNative port           │
└───────────────────────────────│───────────────────────────────────────────────┘
                                │ 4-byte little-endian length + strict JSON
┌───────────────────────────────▼───────────────────────────────────────────────┐
│ Go native host (stdio only; no HTTP listener)                                │
│ validation → command allowlist → metadata store → process manager            │
│                                      │ exec(executable, separate args)        │
└──────────────────────────────────────│────────────────────────────────────────┘
                                       ▼
                    Chromium window using a dedicated user-data directory
                    ScopeNest/containers/<random-id>/profile
```

The persistent native port lets the host observe browser-process exit while the connection remains alive. A new host invocation reconciles stale process metadata and retries cleanup. ScopeNest will never kill a PID it did not launch during the current native-host process; after a host restart, close that container's browser window normally.

## Repository layout

```text
extension/                 MV3 manifest, UI, service worker, assets, tests
native-host/cmd/           native host entry point
native-host/internal/      protocol, validation, browser, storage, host logic
native-host/manifest/      development manifest template
scripts/                   per-user Windows and Linux installers/uninstallers
docs/                      protocol and development details
tools/                     deterministic icon generator
```

## Requirements

- Chrome, Chromium, Microsoft Edge, or Brave.
- Go 1.22 or newer to build the native companion.
- Node.js 20 or newer to run extension checks/tests and regenerate icons. The extension itself has no npm runtime dependencies.

## Build

From the repository root:

```powershell
npm.cmd run build
Set-Location native-host
go test ./...
go build -buildvcs=false -trimpath -o ..\bin\scopenest-host.exe .\cmd\scopenest-host
```

On Linux:

```bash
npm run build
(cd native-host && go test ./...)
mkdir -p bin
(cd native-host && go build -buildvcs=false -trimpath -o ../bin/scopenest-host ./cmd/scopenest-host)
```

`npm run assets` deterministically creates the four PNG icons from the original nested-compartment design. No CDN or downloaded asset is used.

## Install for development

The public key in `extension/manifest.json` pins the unpacked development extension ID to:

```text
nnmpnmnmmfoedjeionoopgnbjnepfolh
```

1. Open `chrome://extensions` (or `edge://extensions`).
2. Enable **Developer mode**.
3. Choose **Load unpacked** and select the repository's `extension` directory.
4. Install the native host with the matching ID.
5. Fully restart the browser and open ScopeNest from the toolbar.

### Windows native-host install

Run this from the repository root in PowerShell; it builds and registers the host for Chrome and Edge under the current user:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install-windows.ps1 -ExtensionId nnmpnmnmmfoedjeionoopgnbjnepfolh
```

To use a prebuilt binary:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install-windows.ps1 -BinaryPath .\bin\scopenest-host.exe -ExtensionId nnmpnmnmmfoedjeionoopgnbjnepfolh
```

The installer copies/builds the host under `%LOCALAPPDATA%\ScopeNest\NativeHost`, writes a restricted-origin manifest, and creates only these per-user registrations:

- `HKCU\Software\Google\Chrome\NativeMessagingHosts\com.scopenest.host`
- `HKCU\Software\Microsoft\Edge\NativeMessagingHosts\com.scopenest.host`

No administrator privileges are required.

### Linux native-host install

```bash
chmod +x scripts/install-linux.sh scripts/uninstall-linux.sh
./scripts/install-linux.sh nnmpnmnmmfoedjeionoopgnbjnepfolh
```

The script builds to `${XDG_DATA_HOME:-$HOME/.local/share}/scopenest/native-host` and installs per-user manifests for Google Chrome, Chromium, Brave, and Microsoft Edge under their standard directories in `${XDG_CONFIG_HOME:-$HOME/.config}`. It does not require root.

Pass a prebuilt binary as the second argument if Go is not installed:

```bash
./scripts/install-linux.sh nnmpnmnmmfoedjeionoopgnbjnepfolh ./bin/scopenest-host
```

## Stable packaged installation

Chrome Web Store and enterprise packaging may assign or require a different extension ID. Find the installed ID on the browser's extensions page, then rerun the relevant installer with that exact ID. The native manifest must contain only:

```json
"allowed_origins": ["chrome-extension://YOUR_32_CHARACTER_ID/"]
```

Do not add wildcards. If a signed package should retain the development ID, retain the same extension key as part of the packaging process. Store-managed keys may override local packaging expectations, so always verify the final installed ID.

## Using ScopeNest

1. Create a container and select a detected browser executable. Suggested names include “Target — Anonymous”, “Target — User A”, “Target — Administrator”, and “Invited Member”.
2. Select **Launch**, **Current page**, or enter an explicit URL in the quick-open box.
3. Use a different container for each role. Every container gets a cryptographically random internal ID and a separate profile path.
4. Close the isolated browser window normally. Running state is reconciled automatically.
5. Use **Temporary** for a fresh disposable context. Its profile is removed on process exit when files are no longer held open; otherwise cleanup is marked pending and retried.

Duplicate copies only the visible configuration (name, color, icon, and browser selection). It intentionally creates a fresh empty profile and never copies cookies or profile databases.

## Browser-path configuration

ScopeNest safely detects common locations:

- Windows: per-user and Program Files installations of Google Chrome, Microsoft Edge, and Brave.
- Linux: `google-chrome`, `chromium`, `chromium-browser`, `microsoft-edge`, and `brave-browser` discovered through `PATH`.

Choose **Custom executable…** to enter another Chromium-based browser. The native host canonicalizes the value, requires an existing regular file, and requires the executable bit on Linux. A configured path is validated again at every launch. Arbitrary arguments are not accepted.

## Local data and privacy

The native host uses the OS user configuration directory:

- Windows: `%APPDATA%\ScopeNest`
- Linux: `${XDG_CONFIG_HOME:-$HOME/.config}/ScopeNest`

`containers.json` stores names, colors, icons, timestamps, generated IDs, profile paths, browser selection, and process status. Browser profile content is stored below `containers/<id>/profile`. Metadata is written with restrictive permissions and temp-file-plus-atomic-replace semantics.

The extension stores only UI preferences in `chrome.storage.local`. ScopeNest does not maintain URL history. A URL is sent locally to the native host only when the user explicitly launches it. There is no telemetry, remote listener, analytics, advertising, or external data transfer. The extension does not inject scripts and does not read page content.

## Threat model

The native host treats extension messages as untrusted. It protects against malformed framing, oversized payloads, unknown commands, extra JSON properties, path traversal, symlink escapes, unsafe URL schemes, browser-argument injection, untrusted profile names, and accidental termination of unrelated processes. See [SECURITY.md](SECURITY.md) for boundaries and reporting guidance.

In scope are local message validation, containment within the managed data root, safe browser invocation, and avoiding unauthorized process termination. Out of scope are a compromised operating-system account, a malicious or compromised browser executable selected by the user, browser vulnerabilities, malware able to edit ScopeNest files, and sites correlating isolated profiles by IP address or other network-level signals.

## Testing and development

```powershell
npm.cmd test
npm.cmd run build
Set-Location native-host
go fmt ./...
go vet ./...
go test -race ./...
```

Linux uses `npm` instead of `npm.cmd`. Go tests cover native-message framing and limits, strict requests, command rejection, URL and input validation, traversal and symlink boundaries, browser argument construction, container creation, metadata persistence/atomic replacement, temporary cleanup, and stale process reconciliation. Extension tests cover protocol construction/parsing, validation, storage, filtering/sorting, and unavailable-host UI state. Tests never launch the user's browser.

When editing the protocol, update both `extension/src/shared/protocol.js` and the Go protocol/host packages, then update [docs/native-protocol.md](docs/native-protocol.md). Never write diagnostic text to native-host standard output; stdout is reserved for framed JSON.

## Troubleshooting

### “Specified native messaging host not found”

- Confirm the installer completed without errors.
- Confirm the browser was fully restarted.
- Confirm the manifest is in the correct browser directory/registry key.
- Confirm its `allowed_origins` exactly matches the ID shown on the extensions page.
- Rerun the installer after an ID or install-path change.

### “Access to the specified native messaging host is forbidden”

The manifest was found, but the extension ID is not authorized. Rerun the installer with the actual installed ID. Do not add a wildcard origin.

### No browsers detected

Choose **Custom executable…** and provide the full path. On Linux, ensure it is a regular executable file. The host returns a specific `INVALID_BROWSER_PATH` error if validation fails.

### Container says running after its window closed

Press **Retry** or reopen ScopeNest; list/status operations reconcile dead PIDs. Some Chromium launchers hand work to another process and exit early, so state may clear even while a browser window is open. The isolated profile remains valid.

### Temporary cleanup is pending

Close every window using that temporary profile and reopen ScopeNest. The host retries on startup. On Windows, antivirus scanners and the browser can briefly retain handles; cleanup remains bounded to the generated container directory.

### Browser opens the existing non-isolated window

Verify the configured executable is Chromium-based and that no one has manually launched a process with the same ScopeNest profile directory. Never reuse or pass a ScopeNest profile to another process.

## Limitations

- Containers normally open in separate browser windows. They are not isolated tabs in the same window.
- ScopeNest is not Firefox's `contextualIdentities` API.
- A full browser profile consumes more memory and disk than a normal tab.
- Isolation depends on distinct `--user-data-dir` values. Never manually reuse one profile directory across containers.
- ScopeNest separates browser storage, not network identity. Sites can still correlate sessions through IP address, TLS/network properties, browser fingerprinting, or account behavior.
- Extensions, policies, certificate stores, OS keychains, DNS caches, and other system-wide facilities may exist outside an individual profile's isolation boundary.
- Process ownership does not survive a native-host restart. ScopeNest reconciles status but refuses to kill a process it did not launch in the current host session.
- Chromium launchers may hand off to another process, which can make precise running-state and immediate temporary cleanup platform-dependent. Cleanup is retried safely.

## Uninstallation

Unload/remove the extension from the browser, then unregister the native host.

Windows (preserves container data):

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\uninstall-windows.ps1
```

Windows, including all profiles and metadata:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\uninstall-windows.ps1 -RemoveData
```

Linux (preserves container data):

```bash
./scripts/uninstall-linux.sh
```

Linux, including all profiles and metadata:

```bash
./scripts/uninstall-linux.sh --remove-data
```

The uninstallers remove only ScopeNest's native-host manifests/registrations and installed binary. Container data is preserved unless explicitly requested.

## License

[MIT](LICENSE)

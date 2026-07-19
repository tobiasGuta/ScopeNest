# ScopeNest security policy

## Supported versions

Security fixes are provided for the latest released major version. Version 1.x receives fixes on its current release line. Development snapshots are supported on a best-effort basis and should not be used for high-risk work without review.

## Reporting a vulnerability

Do not open a public issue for an unpatched vulnerability. Send a private report to the security contact published with the repository or use the repository host's private security-advisory feature. Include the affected version, operating system/browser, impact, reproduction steps, and a minimal proof of concept. Do not include live credentials, session cookies, or third-party private data.

Maintainers should acknowledge a report within seven days, provide a status update within fourteen days, coordinate remediation and disclosure, and credit the reporter unless anonymity is requested. These targets are not a guarantee.

## Security boundaries

ScopeNest isolates Chromium browser state by giving each container a different managed user-data directory. It does not claim to isolate network identity, operating-system resources, compromised browser binaries, browser exploits, globally installed policy, or malware running as the same user.

The Chrome extension is an authorized client, not a trusted source of input. The native companion validates every request even though Chrome enforces `allowed_origins`. The native host has the same filesystem and process privileges as the local user; installing or selecting an untrusted host/browser binary defeats that boundary.

GitHub Actions dependencies are pinned to immutable full commit SHAs. Dependabot checks the `github-actions` ecosystem weekly so action upgrades arrive as explicit, reviewable pull requests instead of mutable tag changes.

The optional `scopenest-mcp` executable is a separate local stdio process. It exposes only dedicated allowlisted tools, strictly rejects unknown arguments, redacts filesystem/process/certificate internals, and delegates all mutations to the existing `host.Host`. It does not expose deletion, certificate trust changes, proxy/template mutations, arbitrary commands, or page access. MCP-created containers use detected standard browsers only, and existing custom-browser containers require a human launch through the extension. For MCP launches, the expected container name and standard browser type are checked against the current record under the shared store lock in the same transaction that creates the launch reservation. Process ownership remains isolated between each native-host or MCP process; persisted PIDs are never termination authority. The MCP client may send sanitized arguments and results to its model provider; see [docs/MCP.md](docs/MCP.md) for that privacy boundary.

## Native-host protections

- Standard input/output Native Messaging only; no HTTP server, remote listener, telemetry, or updater.
- One-megabyte message limit checked before payload allocation.
- Versioned request and response envelopes with request/command correlation.
- Strict JSON decoding, required types, rejection of unknown fields, and exact one-value input.
- Fixed command allowlist; no arbitrary command, environment, argument, or filesystem operation.
- Structured error codes without secrets or raw profile content.
- Standard output is reserved exclusively for length-prefixed JSON. The host currently emits no operational logs.

The native manifest must restrict `allowed_origins` to the installed ScopeNest extension ID. Wildcards and unrelated extension IDs are unsupported.

## Filesystem protections

- Container IDs are generated from 128 bits of cryptographically secure randomness and are never derived from user names.
- IDs use a fixed lowercase hexadecimal form before they enter a path.
- Managed paths are made absolute, canonicalized where possible, and checked with `filepath.Rel` against the ScopeNest root.
- Existing symlinks are evaluated so they cannot redirect a profile or deletion outside the managed root.
- Destructive deletion operates only on `ScopeNest/containers/<validated-id>`.
- Directories use owner-only permissions where supported; metadata uses mode `0600`.
- Metadata updates write a protected temporary file, sync it, then atomically replace the destination (`rename` on Unix; `MoveFileExW` with replace/write-through on Windows).
- Every metadata read or read-modify-write transaction holds `containers.lock` with `flock` on Unix or `LockFileEx` on Windows. Lock acquisition times out instead of waiting indefinitely.
- Launches first persist a cryptographically random `launching` reservation. Only the matching token may commit the running PID or roll back a failed launch.
- Stale launch reservations are recovered only after a bounded timeout and only when Chromium profile-lock markers show that the profile is not in use.
- Permanent and temporary deletion recheck lifecycle state and remove metadata/profile data inside the same locked transaction, preventing launch-versus-delete races.
- Failed temporary deletion is recorded as pending and retried; the host never broadens the deletion target.
- Startup cleanup is scheduled only after the first valid native response is written, runs asynchronously, and reports bounded state metadata without exposing filesystem errors.
- Chromium `SingletonLock`, `SingletonSocket`, and `SingletonCookie` markers block deletion even if a launcher PID has already exited.

Anyone with access to the user's operating-system account may still read or modify browser profiles. Full disk encryption and a protected OS account are recommended.

## Process-launch protections

- Executables must resolve to existing regular files; Unix files must be executable.
- Only known browser types are accepted. A custom type still accepts only one executable path, not arguments.
- URLs are limited to absolute, credential-free `http` and `https` URLs up to 8192 bytes.
- Browser arguments are fixed and passed separately through Go's `exec.Command`; no shell command is built or invoked.
- Proxy arguments are built from validated loopback profiles only. ScopeNest never accepts arbitrary Chromium arguments, arbitrary startup commands, interception-tool launch commands, or `--ignore-certificate-errors`.
- Container name/icon metadata is converted to one bounded, single-line `--window-name=<label>` argument. Existing native validation remains authoritative; label normalization prevents control characters, line separators, or embedded text from becoming separate Chromium arguments.
- Effective networking is resolved under the metadata lock during launch reservation. Explicit `direct` launches direct, explicit container `proxy` overrides a template proxy, and `template` inherits the template proxy. Broken or disabled references are rejected; ScopeNest does not silently fall back to direct networking.
- ScopeNest refuses duplicate launches while a recorded process is alive.
- On Windows, the browser is created suspended, assigned to a private Job Object with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`, and resumed only after assignment succeeds. Closing uses that owned Job Object.
- Windows visual styling is asynchronous and best effort. Candidate ownership comes only from `QueryInformationJobObject(JobObjectBasicProcessIdList)` on that exact private Job Object; ScopeNest then considers only visible, unowned top-level windows whose current PID is in that list. Persisted PIDs, process names, titles, and window classes are never styling authority. The poll stops after one eligible window attempt, process exit, or a ten-second timeout, and DWM failures do not affect launch or process lifetime.
- On Linux, the browser is created in a dedicated process group. Closing signals only that owned group, first with `SIGTERM` and then with `SIGKILL` after a bounded grace period.
- Process authority exists only in the current host's in-memory managed-process object. Persisted and reconciled PIDs are never reopened or killed, even if the numeric PID currently exists.
- A persisted PID is never sufficient evidence that an unowned container is running. Reconciliation, relaunch, and deletion use Chromium profile-lock markers as the authoritative signal, preventing unrelated PID reuse from preserving stale state.
- The process watcher waits for the owned Job Object or process group to empty before changing lifecycle metadata. It also verifies both the current in-memory owner and persisted PID before committing a stopped state.

ScopeNest never uses broad process-name killing or heuristic descendant discovery. Chromium descendants created within the owned Job Object or process group remain controlled, including ordinary launcher handoff. A transfer to an already-running external Chromium process cannot be adopted safely; profile-lock markers continue to block deletion, and closing that browser window normally is the fallback.

## Proxy and certificate protections

- Proxy profiles are validated by the native host for protocol, strict local host (`127.0.0.0/8`, `::1`, or `localhost` normalized to a loopback literal), port, bypass-list length, duplicate bypass rules, duplicate certificate IDs, null/control characters, and argument-injection patterns.
- A disabled proxy profile returns `PROXY_PROFILE_DISABLED`. Health-check-disabled profiles still launch with the proxy; they only skip listener reachability checks and unavailable-listener behavior.
- Required template and proxy certificates are merged without duplicates. The native host verifies required managed DER files and trust state before launch; the extension is never trusted as the source of certificate readiness.
- Windows trust operations are scoped to `CurrentUser\Root`, persist `installing`/`removing`/`trust_error` operation state, verify exact fingerprint and encoded DER after native trust-store changes, and reconcile incomplete operations at startup.
- Linux manual trust acknowledgment is never represented as verified trust. It is unverified, fingerprint-bound metadata only.
- Certificate deletion is refused while ScopeNest ownership is recorded, a trust operation is pending, or a proxy/template still references the certificate. Pre-existing trusted certificates can be removed from the ScopeNest library without changing Windows trust. Deletion uses a persisted tombstone so startup can restore or finish interrupted filesystem cleanup.
- ScopeNest does not download remote certificates, handle private keys, launch proxy tools, install browser extensions automatically, or create a remote listener.

## Extension permissions

- `nativeMessaging`: communicate with the local companion.
- `storage`: retain non-sensitive UI preferences.
- `activeTab`: read the current tab URL only after the user invokes the extension; no page content is requested.
- `sidePanel`: provide the optional side-panel interface.

There are no host permissions, content scripts, externally connectable endpoints, or remotely hosted code.

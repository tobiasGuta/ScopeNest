# ScopeNest security policy

## Supported versions

Security fixes are provided for the latest released major version. Version 1.x receives fixes on its current release line. Development snapshots are supported on a best-effort basis and should not be used for high-risk work without review.

## Reporting a vulnerability

Do not open a public issue for an unpatched vulnerability. Send a private report to the security contact published with the repository or use the repository host's private security-advisory feature. Include the affected version, operating system/browser, impact, reproduction steps, and a minimal proof of concept. Do not include live credentials, session cookies, or third-party private data.

Maintainers should acknowledge a report within seven days, provide a status update within fourteen days, coordinate remediation and disclosure, and credit the reporter unless anonymity is requested. These targets are not a guarantee.

## Security boundaries

ScopeNest isolates Chromium browser state by giving each container a different managed user-data directory. It does not claim to isolate network identity, operating-system resources, compromised browser binaries, browser exploits, globally installed policy, or malware running as the same user.

The Chrome extension is an authorized client, not a trusted source of input. The native companion validates every request even though Chrome enforces `allowed_origins`. The native host has the same filesystem and process privileges as the local user; installing or selecting an untrusted host/browser binary defeats that boundary.

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
- Failed temporary deletion is recorded as pending and retried; the host never broadens the deletion target.
- Chromium `SingletonLock`, `SingletonSocket`, and `SingletonCookie` markers block deletion even if a launcher PID has already exited.

Anyone with access to the user's operating-system account may still read or modify browser profiles. Full disk encryption and a protected OS account are recommended.

## Process-launch protections

- Executables must resolve to existing regular files; Unix files must be executable.
- Only known browser types are accepted. A custom type still accepts only one executable path, not arguments.
- URLs are limited to absolute, credential-free `http` and `https` URLs up to 8192 bytes.
- Browser arguments are fixed and passed separately through Go's `exec.Command`; no shell command is built or invoked.
- ScopeNest refuses duplicate launches while a recorded process is alive.
- A close request can kill only the exact `os.Process` object launched by the current host instance. Reconciled PIDs from older instances are never killed.

Chromium can create subprocesses or transfer work to an existing process. ScopeNest intentionally avoids broad process-name killing or PID-tree heuristics that could terminate unrelated work. Closing the isolated browser window normally is the safest fallback.

## Extension permissions

- `nativeMessaging`: communicate with the local companion.
- `storage`: retain non-sensitive UI preferences.
- `activeTab`: read the current tab URL only after the user invokes the extension; no page content is requested.
- `sidePanel`: provide the optional side-panel interface.

There are no host permissions, content scripts, externally connectable endpoints, or remotely hosted code.

# ChatGPT control of isolated ScopeNest Chrome profiles

ScopeNest creates a separate Chrome user-data directory for every container. Chrome extensions are profile-specific, so an extension installed in Chrome's ordinary `Default` profile is not automatically available inside a new ScopeNest profile.

On Windows, ScopeNest includes an optional and reversible policy script that asks Google Chrome to normally install the official ChatGPT browser-control extension in every Chrome profile for the current Windows user. This enables the ChatGPT/Codex Chrome-control plugin to connect to separately isolated ScopeNest windows such as Browser A and Browser B.

This integration is distinct from [`scopenest-mcp`](MCP.md):

| Integration | How it connects | Container setting |
| --- | --- | --- |
| ChatGPT Chrome-control plugin | Official Chrome extension installed in each profile | **Allow local agent/browser automation** is not required |
| ScopeNest MCP browser tools | ScopeNest-owned ephemeral loopback DevTools endpoint | **Allow local agent/browser automation** must be enabled |

Do not enable both merely as a prerequisite. Choose the integration whose permissions and controls match the task.

## Requirements

- Windows and Google Chrome. The supplied policy scripts do not configure Edge, Brave, or Chromium.
- ScopeNest's unpacked extension and native host installed as described in the [README quick start](../README.md#quick-start).
- The ChatGPT/Codex application and its Chrome-control plugin available for the current account.
- Permission to install a per-user Chrome policy. A device administrator may enforce a conflicting policy that the script will not overwrite.

## Install

1. Save work in Chrome and close every Chrome window, including ordinary and ScopeNest windows.
2. From the ScopeNest repository root, optionally preview the registry change:

   ```powershell
   powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\enable-chatgpt-control-windows.ps1 -WhatIf
   ```

3. Apply the per-user policy:

   ```powershell
   powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\enable-chatgpt-control-windows.ps1
   ```

4. Start Chrome again and launch a ScopeNest container that uses `chrome`. Chrome installs the official extension into that isolated profile from the Chrome Web Store update service. A first-run extension page may open; it can be left open or closed normally.
5. In the ChatGPT/Codex application, make sure Chrome control is enabled in **Settings -> Computer use** if that setting is present for the installed version. Product UI and account availability can vary; follow any in-product connection prompt.

The script is idempotent when the exact expected policy already exists. It preserves unrelated `ExtensionSettings` entries and refuses to overwrite invalid JSON or a different policy for the same extension ID.

## Run one isolated browser

1. Open ScopeNest from the browser toolbar or side panel.
2. Create a saved or temporary container with a clear name, color, and icon, and select Google Chrome.
3. Leave **Allow local agent/browser automation** off unless the same container will also be controlled through `scopenest-mcp`.
4. Launch the container at the required HTTP(S) page.
5. Ask ChatGPT/Codex to use Chrome and identify the container by its visible name, for example, "In Browser A, click Learn more."
6. Watch the requested action in the isolated window and verify the resulting page before continuing.

## Run multiple isolated browsers

Create and launch each container separately, for example Browser A and Browser B. Every window retains its own cookies, local storage, cache, service workers, authentication state, history, permissions, and installed-extension state because each uses a different managed user-data directory.

Use unique names, colors, and icons. In each request, name the target container explicitly. A single task can coordinate actions across both connected Chrome instances, but page state and tabs remain attached to their originating profile.

Closing a temporary container normally allows ScopeNest to remove its profile after the owned Chrome process tree exits and files are released. Closing one container does not close the other.

## Verify the setup

For each isolated container:

1. Open `chrome://extensions` inside that container and confirm **ChatGPT for Chrome** is present and enabled.
2. Open a harmless page such as `https://example.com/`.
3. Ask ChatGPT/Codex to read the page title or click **Learn more** in that named container.
4. Confirm the visible window reaches `https://www.iana.org/help/example-domains`.

For an isolation check, set different non-sensitive test values in two containers and verify each reads only its own value. Do not use production secrets for a setup test.

## Troubleshooting

### The extension is missing from a new ScopeNest profile

- Close every Chrome window and run the enable script again.
- Open `chrome://policy` in Chrome, select **Reload policies**, and confirm that `ExtensionSettings` is active.
- Confirm the ScopeNest container uses Google Chrome rather than Edge, Brave, Chromium, or a custom executable.
- Check `chrome://extensions` inside the isolated container in case the normally installed extension was disabled by the user.

### The enable script refuses the change

The script intentionally stops when `ExtensionSettings` contains malformed JSON or the official extension ID already has a different policy. Inspect the current device policy or ask the administrator to resolve the conflict; do not replace a managed policy blindly.

### ChatGPT/Codex cannot see the isolated window

- Confirm the extension is enabled inside that exact ScopeNest profile, not only in Chrome's ordinary profile.
- Confirm Chrome control is enabled and connected in the ChatGPT/Codex application.
- Fully restart Chrome after changing policy.
- Use a distinct container name and mention that name in the request when more than one Chrome instance is connected.

### ScopeNest MCP tools report that automation is unavailable

That message concerns the separate ScopeNest MCP integration. Enable **Allow local agent/browser automation** and relaunch the container only when using the tools documented in [MCP.md](MCP.md). The ChatGPT Chrome-control extension path does not depend on that setting.

## Security and privacy

The policy is browser-wide for the current Windows user, not ScopeNest-only. It applies to ordinary and isolated Google Chrome profiles. The extension remains user-disableable because the policy uses `normal_installed`, but its permissions are separate from ScopeNest's base extension permissions.

Browser-control tasks may expose page URLs, visible page content, typed values, and interaction results to the selected model provider. Review the application's privacy, retention, and account settings before using authenticated or engagement-sensitive profiles. Use browser control only on systems you own or are authorized to test.

## Remove

Close every Chrome window, then remove only the policy entry managed by ScopeNest:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\disable-chatgpt-control-windows.ps1
```

The removal script preserves unrelated `ExtensionSettings` entries and refuses to remove a conflicting value it does not own. Removing the policy does not delete ScopeNest containers or their browsing data. Chrome may retain an already-installed, user-disableable extension; remove it manually from `chrome://extensions` in any profile where it should no longer remain.

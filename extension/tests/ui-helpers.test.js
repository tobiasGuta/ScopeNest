import test from "node:test";
import assert from "node:assert/strict";
import { buildProxyProfilePayload, DEFAULT_PROXY_VALUES } from "../src/ui/proxies.js";
import { certificateActionView, dialogClosedState } from "../src/ui/state.js";

test("builds proxy payload with native-required defaults", () => {
  assert.deepEqual(buildProxyProfilePayload({ name: "Local" }), {
    name: "Local",
    ...DEFAULT_PROXY_VALUES,
    healthCheck: { ...DEFAULT_PROXY_VALUES.healthCheck },
  });
});

test("builds proxy edit payload with health check, behavior, and certificates", () => {
  assert.deepEqual(buildProxyProfilePayload({
    name: "Proxy",
    enabled: false,
    protocol: "https",
    host: "::1",
    port: "8443",
    bypassRules: "localhost\n\n127.0.0.1",
    certificateIds: ["cert-a", "cert-b"],
    healthCheck: { enabled: false, timeoutMs: "2500" },
    unavailableBehavior: "block",
  }), {
    name: "Proxy",
    enabled: false,
    protocol: "https",
    host: "::1",
    port: 8443,
    bypassRules: ["localhost", "127.0.0.1"],
    certificateIds: ["cert-a", "cert-b"],
    healthCheck: { enabled: false, timeoutMs: 2500 },
    unavailableBehavior: "block",
  });
});

test("renders Linux capability certificate actions without Windows install button", () => {
  const host = { capabilities: { trustInstallation: false, manualTrustAcknowledgment: true } };
  assert.equal(certificateActionView({ trustState: "untrusted" }, host).kind, "acknowledge");
  const acknowledged = certificateActionView({ trustState: "manual_trust_acknowledged_unverified" }, host);
  assert.equal(acknowledged.kind, "manual");
  assert.match(acknowledged.label, /not verified/);
});

test("renders Windows and pending certificate trust states", () => {
  assert.deepEqual(certificateActionView({ trustState: "trusted" }, { capabilities: { trustInstallation: true } }), { kind: "remove", label: "Remove Trust", disabled: false });
  assert.deepEqual(certificateActionView({ trustState: "untrusted" }, { capabilities: { trustInstallation: true } }), { kind: "install", label: "Install Trust", disabled: false });
  assert.deepEqual(certificateActionView({ trustState: "installing" }, { capabilities: { trustInstallation: true } }), { kind: "status", label: "Installing...", disabled: true });
  assert.deepEqual(certificateActionView({ trustState: "removing" }, { capabilities: { trustInstallation: true } }), { kind: "status", label: "Removing...", disabled: true });
  assert.deepEqual(certificateActionView({ trustState: "trust_error" }, { capabilities: { trustInstallation: true } }), { kind: "status", label: "Recovery required", disabled: true });
});

test("exposes dialog close reset state for DOM binding tests", () => {
  assert.deepEqual(dialogClosedState(), { errorHidden: true, formReset: true, returnFocus: true });
});

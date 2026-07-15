import test from "node:test";
import assert from "node:assert/strict";
import { certificateTrustView, connectionView, visibleContainers } from "../src/ui/state.js";

const containers = [
  { name: "Admin", browserType: "chrome", icon: "A", running: true, temporary: false, createdAt: "2026-01-01", lastLaunchedAt: "2026-03-01" },
  { name: "Guest", browserType: "edge", icon: "G", running: false, temporary: true, createdAt: "2026-02-01", lastLaunchedAt: null },
];

test("filters and searches UI state", () => {
  assert.deepEqual(visibleContainers(containers, { filter: "running" }).map((c) => c.name), ["Admin"]);
  assert.deepEqual(visibleContainers(containers, { filter: "temporary" }).map((c) => c.name), ["Guest"]);
  assert.deepEqual(visibleContainers(containers, { query: "edge" }).map((c) => c.name), ["Guest"]);
});

test("shows native host unavailable state", () => {
  assert.deepEqual(connectionView({ loading: false, connected: false, error: "Host not found" }), { tone: "danger", label: "Host not found" });
});

test("shows connected and loading states", () => {
  assert.equal(connectionView({ loading: true }).tone, "loading");
  assert.equal(connectionView({ connected: true, version: "1.0.0" }).tone, "success");
});

test("labels Linux manual trust acknowledgment as unverified rather than trusted", () => {
  assert.deepEqual(certificateTrustView({ trustState: "manual_trust_acknowledged_unverified" }), {
    handled: true,
    verified: false,
    label: "Manual trust acknowledged (unverified)",
  });
});

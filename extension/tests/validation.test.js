import test from "node:test";
import assert from "node:assert/strict";
import { sortContainers, validateContainer, validateWebURL } from "../src/shared/validation.js";

const valid = { name: "Target — User A", color: "#725cff", icon: "🔐", browserType: "chrome", browserExecutable: "/opt/google/chrome" };

test("normalizes a valid container", () => {
  assert.equal(validateContainer({ ...valid, name: "  Target — User A  " }).name, valid.name);
});

test("rejects invalid names, colors, icons, browsers, and paths", () => {
  assert.throws(() => validateContainer({ ...valid, name: "\u0000bad" }));
  assert.throws(() => validateContainer({ ...valid, color: "red" }));
  assert.throws(() => validateContainer({ ...valid, icon: "123456789" }));
  assert.throws(() => validateContainer({ ...valid, browserType: "firefox" }));
  assert.throws(() => validateContainer({ ...valid, browserExecutable: "" }));
});

test("allows only absolute credential-free HTTP(S) URLs", () => {
  assert.equal(validateWebURL("https://example.com/test"), "https://example.com/test");
  assert.throws(() => validateWebURL("javascript:alert(1)"));
  assert.throws(() => validateWebURL("https://user:pass@example.com"));
  assert.throws(() => validateWebURL("example.com", { allowEmpty: false }));
});

test("sorts containers by name, recency, and creation", () => {
  const items = [
    { name: "Zulu", createdAt: "2025-01-01", lastLaunchedAt: null },
    { name: "Alpha", createdAt: "2026-01-01", lastLaunchedAt: "2026-02-01" },
  ];
  assert.equal(sortContainers(items, "name")[0].name, "Alpha");
  assert.equal(sortContainers(items, "lastUsed")[0].name, "Alpha");
  assert.equal(sortContainers(items, "created")[0].name, "Alpha");
});

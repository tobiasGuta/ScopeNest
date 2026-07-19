import test from "node:test";
import assert from "node:assert/strict";
import { containerCommandData, MAX_WINDOW_LABEL_RUNES, sortContainers, validateContainer, validateWebURL, visualIdentityLabel } from "../src/shared/validation.js";

const valid = { name: "Target — User A", color: "#725cff", icon: "🔐", browserType: "chrome", browserExecutable: "/opt/google/chrome" };

test("normalizes a valid container", () => {
  assert.equal(validateContainer({ ...valid, name: "  Target — User A  " }).name, valid.name);
});

test("builds a safe visual identity label for the browser preview", () => {
  assert.equal(visualIdentityLabel({ name: "Research", icon: "🔬" }), "[🔬] ScopeNest — Research");
  assert.equal(visualIdentityLabel({ name: " Work " }), "ScopeNest — Work");
  assert.equal(visualIdentityLabel(), "ScopeNest");
  assert.equal(visualIdentityLabel({ name: "red\tteam\u2028window", icon: " 🧪 " }), "[🧪] ScopeNest — red team window");
});

test("bounds the visual identity label without splitting Unicode code points", () => {
  const label = visualIdentityLabel({ name: "界".repeat(200), icon: "🧪" });
  assert.equal([...label].length, MAX_WINDOW_LABEL_RUNES);
  assert.equal(label.includes("�"), false);
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

test("validates explicit direct, template inheritance, and proxy override modes", () => {
  assert.equal(validateContainer({ ...valid, networkMode: "template", environmentTemplateId: "template-id" }).networkMode, "template");
  assert.equal(validateContainer({ ...valid, networkMode: "proxy", proxyProfileId: "proxy-id", environmentTemplateId: "template-id" }).proxyProfileId, "proxy-id");
  assert.throws(() => validateContainer({ ...valid, networkMode: "direct", environmentTemplateId: "template-id" }));
  assert.throws(() => validateContainer({ ...valid, networkMode: "template" }));
  assert.throws(() => validateContainer({ ...valid, networkMode: "template", environmentTemplateId: "template-id", proxyProfileId: "proxy-id" }));
});

test("omits default direct network fields from container command data", () => {
  assert.deepEqual(containerCommandData(validateContainer({ ...valid, networkMode: "direct" })), valid);
});

test("keeps non-default network fields in container command data", () => {
  assert.deepEqual(containerCommandData(validateContainer({ ...valid, networkMode: "template", environmentTemplateId: "template-id" })), { ...valid, networkMode: "template", environmentTemplateId: "template-id" });
  assert.deepEqual(containerCommandData(validateContainer({ ...valid, networkMode: "proxy", proxyProfileId: "proxy-id", environmentTemplateId: "template-id" })), { ...valid, networkMode: "proxy", proxyProfileId: "proxy-id", environmentTemplateId: "template-id" });
});

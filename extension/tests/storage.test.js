import test from "node:test";
import assert from "node:assert/strict";
import { DEFAULT_PREFERENCES, loadPreferences, savePreferences } from "../src/shared/storage.js";

function memoryStorage(initial = {}) {
  const data = { ...initial };
  return {
    async get(key) { return { [key]: data[key] }; },
    async set(values) { Object.assign(data, values); },
    data,
  };
}

test("loads safe defaults when storage is empty", async () => {
  assert.deepEqual(await loadPreferences(memoryStorage()), DEFAULT_PREFERENCES);
});

test("merges and persists preferences", async () => {
  const storage = memoryStorage({ preferences: { sort: "created", filter: "all" } });
  const saved = await savePreferences(storage, { filter: "running" });
  assert.equal(saved.sort, "created"); assert.equal(storage.data.preferences.filter, "running");
});

test("ignores malformed stored preference values", async () => {
  assert.deepEqual(await loadPreferences(memoryStorage({ preferences: "broken" })), DEFAULT_PREFERENCES);
});

export const DEFAULT_PREFERENCES = Object.freeze({ sort: "name", filter: "all", lastBrowser: null });

export async function loadPreferences(storageArea) {
  const stored = await storageArea.get("preferences");
  const value = stored?.preferences;
  return { ...DEFAULT_PREFERENCES, ...(value && typeof value === "object" ? value : {}) };
}

export async function savePreferences(storageArea, patch) {
  const current = await loadPreferences(storageArea);
  const next = { ...current, ...patch };
  await storageArea.set({ preferences: next });
  return next;
}

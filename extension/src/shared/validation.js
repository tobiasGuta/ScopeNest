export const DEFAULT_COLOR = "#725cff";

export function validateContainer(input) {
  const value = {
    name: typeof input?.name === "string" ? input.name.trim() : "",
    color: typeof input?.color === "string" ? input.color : DEFAULT_COLOR,
    icon: typeof input?.icon === "string" ? input.icon.trim() : "",
    browserType: typeof input?.browserType === "string" ? input.browserType : "",
    browserExecutable: typeof input?.browserExecutable === "string" ? input.browserExecutable.trim() : "",
    networkMode: typeof input?.networkMode === "string" ? input.networkMode : "direct",
    proxyProfileId: typeof input?.proxyProfileId === "string" ? input.proxyProfileId : undefined,
    environmentTemplateId: typeof input?.environmentTemplateId === "string" ? input.environmentTemplateId : undefined,
  };
  if (!value.name || [...value.name].length > 80 || /[\u0000-\u001f\u007f]/.test(value.name)) throw new Error("Name must contain 1–80 visible characters.");
  if (!/^#[0-9a-fA-F]{6}$/.test(value.color)) throw new Error("Choose a valid color.");
  if ([...value.icon].length > 8 || /[\u0000-\u001f\u007f]/.test(value.icon)) throw new Error("Icon must contain at most 8 visible characters.");
  if (!["chrome", "chromium", "edge", "brave", "custom"].includes(value.browserType)) throw new Error("Choose a supported browser.");
  if (!value.browserExecutable) throw new Error("Choose a browser executable.");
  if (!["direct", "proxy"].includes(value.networkMode)) throw new Error("Invalid network mode.");
  if (value.networkMode === "proxy" && !value.proxyProfileId) throw new Error("Select a proxy profile.");
  return value;
}

export function validateWebURL(raw, { allowEmpty = true } = {}) {
  const value = typeof raw === "string" ? raw.trim() : "";
  if (!value && allowEmpty) return "";
  if (!value || value.length > 8192) throw new Error("Enter a valid URL.");
  let parsed;
  try { parsed = new URL(value); } catch { throw new Error("Enter an absolute http or https URL."); }
  if (!["http:", "https:"].includes(parsed.protocol) || !parsed.hostname || parsed.username || parsed.password) throw new Error("Only http and https URLs without embedded credentials are supported.");
  return parsed.href;
}

export function sortContainers(containers, order) {
  const items = [...containers];
  if (order === "lastUsed") return items.sort((a, b) => (b.lastLaunchedAt || "").localeCompare(a.lastLaunchedAt || "") || a.name.localeCompare(b.name));
  if (order === "created") return items.sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  return items.sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: "base" }));
}

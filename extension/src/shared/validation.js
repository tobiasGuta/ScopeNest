export const DEFAULT_COLOR = "#725cff";
export const MAX_WINDOW_LABEL_RUNES = 120;

const labelWhitespaceOrControl = /[\p{Cc}\p{Zl}\p{Zp}\s]/u;
const bidiControl = /[\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069]/u;

function normalizeWindowLabelPart(value) {
  let result = "";
  let pendingSpace = false;
  for (const rune of String(value || "").trim()) {
    if (bidiControl.test(rune)) continue;
    if (labelWhitespaceOrControl.test(rune)) {
      pendingSpace = result.length > 0;
      continue;
    }
    if (pendingSpace) result += " ";
    pendingSpace = false;
    result += rune;
  }
  return result;
}

export function visualIdentityLabel({ name = "", icon = "" } = {}) {
  const safeName = normalizeWindowLabelPart(name);
  const safeIcon = normalizeWindowLabelPart(icon);
  let label = safeIcon ? `[${safeIcon}] ScopeNest` : "ScopeNest";
  if (safeName) label += ` — ${safeName}`;
  return [...label].slice(0, MAX_WINDOW_LABEL_RUNES).join("");
}

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
  if (bidiControl.test(value.name)) throw new Error("Name cannot contain bidirectional formatting characters.");
  if (!/^#[0-9a-fA-F]{6}$/.test(value.color)) throw new Error("Choose a valid color.");
  if ([...value.icon].length > 8 || /[\u0000-\u001f\u007f]/.test(value.icon)) throw new Error("Icon must contain at most 8 visible characters.");
  if (bidiControl.test(value.icon)) throw new Error("Icon cannot contain bidirectional formatting characters.");
  if (!["chrome", "chromium", "edge", "brave", "custom"].includes(value.browserType)) throw new Error("Choose a supported browser.");
  if (!value.browserExecutable) throw new Error("Choose a browser executable.");
  if (!["direct", "template", "proxy"].includes(value.networkMode)) throw new Error("Invalid network mode.");
  if (value.networkMode === "direct" && (value.proxyProfileId || value.environmentTemplateId)) throw new Error("Direct mode cannot include proxy or template references.");
  if (value.networkMode === "template" && !value.environmentTemplateId) throw new Error("Select an environment template.");
  if (value.networkMode === "template" && value.proxyProfileId) throw new Error("Template mode cannot include a proxy override.");
  if (value.networkMode === "proxy" && !value.proxyProfileId) throw new Error("Select a proxy profile.");
  return value;
}

export function containerCommandData(container) {
  const data = {
    name: container.name,
    color: container.color,
    icon: container.icon,
    browserType: container.browserType,
    browserExecutable: container.browserExecutable,
  };
  if (container.networkMode && container.networkMode !== "direct") data.networkMode = container.networkMode;
  if (container.proxyProfileId) data.proxyProfileId = container.proxyProfileId;
  if (container.environmentTemplateId) data.environmentTemplateId = container.environmentTemplateId;
  return data;
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

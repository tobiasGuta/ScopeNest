import { sortContainers } from "../shared/validation.js";

export function visibleContainers(containers, { query = "", filter = "all", sort = "name" } = {}) {
  const needle = query.trim().toLocaleLowerCase();
  const filtered = containers.filter((container) => {
    if (filter === "running" && !container.running) return false;
    if (filter === "temporary" && !container.temporary) return false;
    if (filter === "saved" && container.temporary) return false;
    return !needle || `${container.name} ${container.browserType} ${container.icon || ""}`.toLocaleLowerCase().includes(needle);
  });
  return sortContainers(filtered, sort);
}

export function connectionView(status) {
  if (status.loading) return { tone: "loading", label: "Connecting to native host…" };
  if (status.connected) return { tone: "success", label: `Native host ${status.version || "connected"}` };
  return { tone: "danger", label: status.error || "Native host unavailable" };
}

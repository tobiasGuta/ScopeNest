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

export function certificateTrustView(certificate) {
  if (certificate?.trustState === "trusted") return { handled: true, verified: true, label: "Trusted" };
  if (certificate?.trustState === "installing") return { handled: false, verified: false, label: "Installing" };
  if (certificate?.trustState === "removing") return { handled: false, verified: false, label: "Removing" };
  if (certificate?.trustState === "trust_error") return { handled: false, verified: false, label: "Recovery required" };
  if (certificate?.trustState === "manual_trust_acknowledged_unverified") {
    return { handled: true, verified: false, label: "Manual trust acknowledged (unverified)" };
  }
  return { handled: false, verified: false, label: "Untrusted" };
}

export function certificateActionView(certificate, host) {
  const state = certificate?.trustState || "untrusted";
  const capabilities = host?.capabilities || {};
  if (state === "installing") return { kind: "status", label: "Installing...", disabled: true };
  if (state === "removing") return { kind: "status", label: "Removing...", disabled: true };
  if (state === "trust_error") return { kind: "status", label: "Recovery required", disabled: true };
  if (capabilities.trustInstallation === false) {
    const acknowledged = state === "manual_trust_acknowledged_unverified";
    return {
      kind: capabilities.manualTrustAcknowledgment === true && !acknowledged ? "acknowledge" : "manual",
      label: acknowledged
        ? "Manual trust acknowledged for this exact fingerprint. ScopeNest has not verified browser or operating-system trust."
        : "Manually trust this exact certificate, then acknowledge its fingerprint.",
      disabled: false,
    };
  }
  if (state === "trusted") return { kind: "remove", label: "Remove Trust", disabled: false };
  return { kind: "install", label: "Install Trust", disabled: false };
}

export function dialogClosedState() {
  return { errorHidden: true, formReset: true, returnFocus: true };
}

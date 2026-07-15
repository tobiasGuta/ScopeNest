import { request } from "./api.js";

export async function checkReadiness(containerId, state) {
  const container = state.containers.find(c => c.id === containerId);
  if (!container) throw new Error("Container not found");
  const readiness = await request("get_container_readiness", { id: containerId });
  if (!readiness.ready) {
    throw new Error(readiness.warnings?.[0] || "Container is not ready to launch.");
  }
  return readiness;
}

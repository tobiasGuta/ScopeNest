import { certificateTrustView } from "./state.js";

export async function checkReadiness(containerId, state) {
  const container = state.containers.find(c => c.id === containerId);
  if (!container) throw new Error("Container not found");

  // If there's an environment template, ensure all certs are trusted.
  if (container.environmentTemplateId) {
    const template = state.templates.find(t => t.id === container.environmentTemplateId);
    if (template && template.certificateIds?.length > 0) {
      for (const certId of template.certificateIds) {
        const cert = state.certificates.find(c => c.id === certId);
        if (!cert) throw new Error(`Template requires a certificate that is missing.`);
        if (!certificateTrustView(cert).handled) {
          throw new Error(`Template requires certificate "${cert.displayName}" trust handling. Please go to Certificates.`);
        }
      }
    }
  }

  // The native host performs the authoritative listener check atomically with launch
  // and applies warn/block/direct without the UI overriding the configured policy.
}

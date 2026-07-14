import { request, toast } from "./api.js";
import { button, toggleMenu, confirmDelete, bindDialogControls } from "./common.js";
import { certificateTrustView } from "./state.js";

const $ = (selector) => document.querySelector(selector);

export function initCertificates(state, refreshApp) {
  function renderCard(cert) {
    const card = document.createElement("article");
    card.className = "container-card";
    card.style.setProperty("--container-color", "#e91e63");
    card.dataset.id = cert.id;

    const main = document.createElement("div"); main.className = "card-main";
    const head = document.createElement("div"); head.className = "card-head";
    const icon = document.createElement("div"); icon.className = "card-icon"; icon.textContent = "Certificate";
    const title = document.createElement("div"); title.className = "card-title";
    const heading = document.createElement("h3"); heading.textContent = cert.displayName; title.append(heading);
    const meta = document.createElement("div"); meta.className = "meta";
    const fingerprint = document.createElement("span");
    fingerprint.textContent = `SHA-256: ${cert.sha256Fingerprint}`;
    meta.append(fingerprint);
    const trustView = certificateTrustView(cert);
    if (cert.trustState === "trusted") {
      const badge = document.createElement("span"); badge.className = "badge running"; badge.textContent = "Trusted"; meta.append(badge);
    } else if (cert.trustState === "manual_trust_acknowledged_unverified") {
      const badge = document.createElement("span"); badge.className = "badge temporary"; badge.textContent = trustView.label; meta.append(badge);
    }
    title.append(meta); head.append(icon, title);
    const menuButton = button("Actions", "card-menu", (event) => toggleMenu(card, cert, event.currentTarget, buildMenu), `Actions for ${cert.displayName}`);
    menuButton.setAttribute("aria-expanded", "false"); head.append(menuButton); main.append(head); card.append(main);

    const actions = document.createElement("div"); actions.className = "card-actions";
    if (state.host?.capabilities?.trustInstallation === false) {
      const message = document.createElement("p"); message.className = "meta";
      message.textContent = cert.trustState === "manual_trust_acknowledged_unverified"
        ? "Manual trust acknowledged for this exact fingerprint. ScopeNest has not verified browser or operating-system trust."
        : "Manually trust this exact certificate, then acknowledge its fingerprint.";
      actions.append(message);
      if (state.host?.capabilities?.manualTrustAcknowledgment === true && cert.trustState !== "manual_trust_acknowledged_unverified") {
        actions.append(button("Acknowledge exact fingerprint", "button secondary", async () => {
          if (!window.confirm(`Acknowledge manual Linux trust for ${cert.sha256Fingerprint}? ScopeNest cannot verify it.`)) return;
          try {
            await request("acknowledge_manual_certificate_trust", { id: cert.id, sha256Fingerprint: cert.sha256Fingerprint, platform: "linux" });
            toast("Manual trust acknowledged but unverified."); await refreshApp();
          } catch (error) { toast(error.message, true); }
        }));
      }
    } else if (cert.trustState === "trusted") {
      actions.append(button("Remove Trust", "button ghost", async () => {
        try { await request("remove_certificate_trust", { id: cert.id }); toast("Trust removed."); await refreshApp(); }
        catch (error) { toast(error.message, true); }
      }));
    } else {
      actions.append(button("Install Trust", "button primary", async () => {
        try { await request("install_certificate_trust", { id: cert.id }); toast("Certificate installed."); await refreshApp(); }
        catch (error) { toast(error.message, true); }
      }));
    }
    card.append(actions);
    return card;
  }

  function buildMenu(menu, cert) {
    if (!cert) return;
    menu.append(button("Delete", "delete", () => confirmDelete("Delete certificate?", `This removes ${cert.displayName} from ScopeNest.`, async () => {
      try { await request("delete_certificate", { id: cert.id }); toast("Certificate deleted."); await refreshApp(); }
      catch (error) { toast(error.message, true); }
    })));
  }

  async function saveForm(event) {
    event.preventDefault();
    if (event.submitter?.value === "cancel") { $("#cert-dialog").close(); return; }
    const file = $("#cert-file").files[0];
    if (!file || file.size > 128 * 1024) {
      $("#cert-form-error").textContent = !file ? "Please select a file." : "File is too large (max 128KB).";
      $("#cert-form-error").hidden = false; return;
    }
    try {
      $("#cert-save").disabled = true;
      const bytes = new Uint8Array(await file.arrayBuffer());
      let binary = "";
      for (const byte of bytes) binary += String.fromCharCode(byte);
      await request("import_certificate", { displayName: $("#cert-name").value, contentBase64: btoa(binary), expectedSize: file.size });
      toast("Certificate imported."); certDialog.close(); await refreshApp();
    } catch (error) {
      $("#cert-form-error").textContent = error.message; $("#cert-form-error").hidden = false;
    } finally { $("#cert-save").disabled = false; }
  }

  function renderCertificates() {
    const certificates = state.certificates || [];
    if (certificates.length === 0) $("#certificate-list").innerHTML = '<div class="state-card"><h3>No certificates</h3><p>Import CA certificates to trust interception proxies.</p></div>';
    else $("#certificate-list").replaceChildren(...certificates.map(renderCard));
  }

  const certDialog = bindDialogControls($("#cert-dialog"), {
    form: $("#cert-form"),
    error: $("#cert-form-error"),
    opener: () => document.activeElement,
    initialFocus: () => $("#cert-name"),
  });

  $("#import-certificate").addEventListener("click", () => certDialog.open());
  $("#cert-form")?.addEventListener("submit", saveForm);
  return { renderCertificates };
}

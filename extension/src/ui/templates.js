import { request, toast } from "./api.js";
import { button, toggleMenu, confirmDelete } from "./common.js";

const $ = (selector) => document.querySelector(selector);

export function initTemplates(state, refreshApp) {
  function renderCard(template) {
    const card = document.createElement("article");
    card.className = "container-card";
    card.style.setProperty("--container-color", "#8bc34a");
    card.dataset.id = template.id;
    
    const main = document.createElement("div"); main.className = "card-main";
    const head = document.createElement("div"); head.className = "card-head";
    const icon = document.createElement("div"); icon.className = "card-icon"; icon.textContent = "📑"; icon.setAttribute("aria-hidden", "true");
    
    const title = document.createElement("div"); title.className = "card-title";
    const h3 = document.createElement("h3"); h3.textContent = template.name; title.append(h3);
    const meta = document.createElement("div"); meta.className = "meta";
    
    const proxyName = template.proxyProfileId ? state.proxies?.find(p => p.id === template.proxyProfileId)?.name || "Unknown Proxy" : "Direct";
    const proxySpan = document.createElement("span"); proxySpan.textContent = `Network: ${proxyName}`; meta.append(proxySpan);
    
    const certCount = template.certificateIds?.length || 0;
    const certSpan = document.createElement("span"); certSpan.textContent = `${certCount} Certificate(s)`; meta.append(certSpan);
    
    title.append(meta); head.append(icon, title);
    
    const menuButton = button("⋯", "card-menu", (event) => toggleMenu(card, template, event.currentTarget, buildMenu), `Actions for ${template.name}`);
    menuButton.setAttribute("aria-expanded", "false"); head.append(menuButton); main.append(head);
    
    card.append(main);
    return card;
  }

  function buildMenu(menu, template) {
    if (!template) return;
    
    menu.append(button("Edit", "", () => { menu.remove(); openDialog(template); }));
    menu.append(button("Delete", "delete", () => confirmDelete("Delete environment template?", `This permanently removes “${template.name}”.`, async () => {
      try { await request("delete_environment_template", { id: template.id }); toast("Template deleted."); await refreshApp(); }
      catch (error) { toast(error.message, true); }
    })));
  }

  function openDialog(template = null) {
    $("#template-form").reset();
    $("#template-id").value = template?.id || "";
    $("#template-dialog-title").textContent = template ? "Edit Template" : "Create Template";
    $("#template-save").textContent = template ? "Save changes" : "Create template";
    
    $("#template-name").value = template?.name || "";
    
    const proxySelect = $("#template-proxy");
    proxySelect.replaceChildren(Object.assign(document.createElement("option"), {value: "", textContent: "None"}));
    for (const p of state.proxies || []) {
      proxySelect.append(Object.assign(document.createElement("option"), {value: p.id, textContent: p.name}));
    }
    proxySelect.value = template?.proxyProfileId || "";
    
    const certsSelect = $("#template-certs");
    certsSelect.replaceChildren();
    for (const c of state.certificates || []) {
      const opt = document.createElement("option");
      opt.value = c.id;
      opt.textContent = c.name;
      if (template?.certificateIds?.includes(c.id)) {
        opt.selected = true;
      }
      certsSelect.append(opt);
    }
    
    $("#template-form-error").hidden = true;
    $("#template-dialog").showModal();
    $("#template-name").focus();
  }

  async function saveForm(event) {
    event.preventDefault();
    const submitter = event.submitter; if (submitter?.value === "cancel") { $("#template-dialog").close(); return; }
    
    const selectedCerts = Array.from($("#template-certs").selectedOptions).map(o => o.value);
    
    const input = {
      name: $("#template-name").value,
      proxyProfileId: $("#template-proxy").value || undefined,
      certificateIds: selectedCerts,
    };
    
    const id = $("#template-id").value;
    try {
      $("#template-save").disabled = true;
      if (id) {
        await request("update_environment_template", { id, ...input });
        toast("Template updated.");
      } else {
        await request("create_environment_template", input);
        toast("Template created.");
      }
      $("#template-dialog").close();
      await refreshApp();
    } catch (error) {
      $("#template-form-error").textContent = error.message;
      $("#template-form-error").hidden = false;
    } finally {
      $("#template-save").disabled = false;
    }
  }

  function renderTemplates() {
    const templates = state.templates || [];
    if (templates.length === 0) {
      $("#template-list").innerHTML = '<div class="state-card"><h3>No templates</h3><p>Combine proxies and certificates into reusable templates for containers.</p></div>';
    } else {
      $("#template-list").replaceChildren(...templates.map(renderCard));
    }
  }

  $("#new-template").addEventListener("click", () => openDialog());
  $("#template-form")?.addEventListener("submit", saveForm);

  return { renderTemplates };
}

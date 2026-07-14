import { request, toast } from "./api.js";
import { button, formatDate, toggleMenu, confirmDelete } from "./common.js";

const $ = (selector) => document.querySelector(selector);

export function initProxies(state, refreshApp) {
  function renderCard(proxy) {
    const card = document.createElement("article");
    card.className = "container-card";
    card.style.setProperty("--container-color", "#00bcd4");
    card.dataset.id = proxy.id;
    
    const main = document.createElement("div"); main.className = "card-main";
    const head = document.createElement("div"); head.className = "card-head";
    const icon = document.createElement("div"); icon.className = "card-icon"; icon.textContent = "🌐"; icon.setAttribute("aria-hidden", "true");
    
    const title = document.createElement("div"); title.className = "card-title";
    const h3 = document.createElement("h3"); h3.textContent = proxy.name; title.append(h3);
    const meta = document.createElement("div"); meta.className = "meta";
    
    const type = document.createElement("span"); type.textContent = proxy.protocol.toUpperCase(); meta.append(type);
    const addr = document.createElement("span"); addr.textContent = `${proxy.host}:${proxy.port}`; meta.append(addr);
    
    title.append(meta); head.append(icon, title);
    
    const menuButton = button("⋯", "card-menu", (event) => toggleMenu(card, proxy, event.currentTarget, buildMenu), `Actions for ${proxy.name}`);
    menuButton.setAttribute("aria-expanded", "false"); head.append(menuButton); main.append(head);
    
    if (proxy.bypassRules?.length) {
      const path = document.createElement("p"); path.className = "profile-path"; 
      path.textContent = `Bypass: ${proxy.bypassRules.join(", ")}`; main.append(path);
    }
    
    card.append(main);
    
    const actions = document.createElement("div"); actions.className = "card-actions";
    actions.append(button("Test Listener", "button secondary", async () => {
      try {
        const res = await request("test_proxy_listener", { host: proxy.host, port: proxy.port });
        toast(res.reachable ? `Listener is open (${res.latencyMs}ms).` : `Listener is closed or unreachable (${res.errorCode}).`);
      } catch (err) {
        toast(`Test failed: ${err.message}`, true);
      }
    }));
    card.append(actions);
    return card;
  }

  function buildMenu(menu, proxy) {
    if (!proxy) return;
    
    menu.append(button("Edit", "", () => { menu.remove(); openDialog(proxy); }));
    menu.append(button("Delete", "delete", () => confirmDelete("Delete proxy profile?", `This permanently removes “${proxy.name}”.`, async () => {
      try { await request("delete_proxy_profile", { id: proxy.id }); toast("Proxy profile deleted."); await refreshApp(); }
      catch (error) { toast(error.message, true); }
    })));
  }

  function openDialog(proxy = null) {
    $("#proxy-form").reset();
    $("#proxy-id").value = proxy?.id || "";
    $("#proxy-dialog-title").textContent = proxy ? "Edit proxy profile" : "Create proxy profile";
    $("#proxy-save").textContent = proxy ? "Save changes" : "Create proxy";
    
    $("#proxy-name").value = proxy?.name || "";
    $("#proxy-protocol").value = proxy?.protocol || "http";
    $("#proxy-host").value = proxy?.host || "127.0.0.1";
    $("#proxy-port").value = proxy?.port || 8080;
    $("#proxy-bypass").value = (proxy?.bypassRules || []).join("\n");
    
    $("#proxy-form-error").hidden = true;
    $("#proxy-dialog").showModal();
    $("#proxy-name").focus();
  }

  async function saveForm(event) {
    event.preventDefault();
    const submitter = event.submitter; if (submitter?.value === "cancel") { $("#proxy-dialog").close(); return; }
    
    const input = {
      name: $("#proxy-name").value,
      protocol: $("#proxy-protocol").value,
      host: $("#proxy-host").value,
      port: parseInt($("#proxy-port").value, 10),
      bypassRules: $("#proxy-bypass").value.split("\n").map(s => s.trim()).filter(Boolean),
    };
    
    const id = $("#proxy-id").value;
    try {
      $("#proxy-save").disabled = true;
      if (id) {
        await request("update_proxy_profile", { id, ...input });
        toast("Proxy profile updated.");
      } else {
        await request("create_proxy_profile", input);
        toast("Proxy profile created.");
      }
      $("#proxy-dialog").close();
      await refreshApp();
    } catch (error) {
      $("#proxy-form-error").textContent = error.message;
      $("#proxy-form-error").hidden = false;
    } finally {
      $("#proxy-save").disabled = false;
    }
  }

  function renderProxies() {
    const proxies = state.proxies || [];
    if (proxies.length === 0) {
      $("#proxy-list").innerHTML = '<div class="state-card"><h3>No proxies</h3><p>Create a proxy profile to route traffic through interception tools.</p></div>';
    } else {
      $("#proxy-list").replaceChildren(...proxies.map(renderCard));
    }
  }

  $("#new-proxy").addEventListener("click", () => openDialog());
  $("#proxy-form")?.addEventListener("submit", saveForm);

  return { renderProxies };
}

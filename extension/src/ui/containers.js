import { request, toast } from "./api.js";
import { button, formatDate, toggleMenu, confirmDelete, bindDialogControls } from "./common.js";
import { containerCommandData, validateContainer, validateWebURL } from "../shared/validation.js";
import { connectionView, visibleContainers } from "./state.js";
import { savePreferences } from "../shared/storage.js";
import { checkReadiness } from "./readiness.js";

const $ = (selector) => document.querySelector(selector);

export function initContainers(state, refreshApp) {
  function browserLabel(container) {
    return ({ chrome: "Chrome", chromium: "Chromium", edge: "Edge", brave: "Brave", custom: "Custom" })[container.browserType] || container.browserType;
  }

  function renderCard(container) {
    const launching = container.state === "launching";
    const card = document.createElement("article");
    card.className = "container-card";
    card.style.setProperty("--container-color", container.color);
    card.dataset.id = container.id;
    
    const main = document.createElement("div"); main.className = "card-main";
    const head = document.createElement("div"); head.className = "card-head";
    const icon = document.createElement("div"); icon.className = "card-icon"; icon.textContent = container.icon || "▣"; icon.setAttribute("aria-hidden", "true");
    
    const title = document.createElement("div"); title.className = "card-title";
    const h3 = document.createElement("h3"); h3.textContent = container.name; title.append(h3);
    const meta = document.createElement("div"); meta.className = "meta";
    
    const browser = document.createElement("span"); browser.textContent = browserLabel(container); meta.append(browser);
    const used = document.createElement("span"); used.textContent = `• ${formatDate(container.lastLaunchedAt)}`; meta.append(used);
    
    if (container.running) { const running = document.createElement("span"); running.className = "badge running"; running.textContent = "Running"; meta.append(running); }
    if (launching) { const launchingBadge = document.createElement("span"); launchingBadge.className = "badge launching"; launchingBadge.textContent = "Launching"; meta.append(launchingBadge); }
    if (container.temporary) { const temporary = document.createElement("span"); temporary.className = "badge temporary"; temporary.textContent = container.pendingCleanup ? "Cleanup pending" : "Temporary"; meta.append(temporary); }
    
    title.append(meta); head.append(icon, title);
    
    const menuButton = button("⋯", "card-menu", (event) => toggleMenu(card, container, event.currentTarget, buildMenu), `Actions for ${container.name}`);
    menuButton.setAttribute("aria-expanded", "false"); head.append(menuButton); main.append(head);
    
    const path = document.createElement("p"); path.className = "profile-path"; path.title = container.profilePath; path.textContent = container.profilePath; main.append(path);
    card.append(main);
    
    const actions = document.createElement("div"); actions.className = "card-actions";
    actions.append(button(container.running ? "Running" : launching ? "Launching" : "Launch", "button primary", () => launch(container.id, ""), `Launch ${container.name}`), button("Current page", "button secondary", () => openCurrent(container.id), `Open current page in ${container.name}`));
    actions.firstChild.disabled = container.running || launching; actions.lastChild.disabled = launching;
    card.append(actions);
    return card;
  }

  function buildMenu(menu, container) {
    if (!container) return;
    
    menu.append(button("Edit", "", () => { menu.remove(); openDialog(container); }));
    menu.append(button("Duplicate", "", () => duplicate(container)));
    if (container.running) menu.append(button("Close window", "", () => closeContainer(container.id)));
    menu.append(button("Delete", "delete", () => confirmDelete("Delete container?", `This permanently removes “${container.name}” and all data in its isolated profile.`, async () => {
      try { await request("delete_container", { id: container.id }); toast("Container deleted."); await refreshApp(); }
      catch (error) { toast(error.message, true); }
    })));
  }

  async function currentURL() {
    const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
    return validateWebURL(tab?.url || "", { allowEmpty: false });
  }

  async function launch(id, url) {
    try {
      await checkReadiness(id, state);
      const launched = await request("launch_container", { id, url: validateWebURL(url) });
      if (launched.directFallbackUsed) toast("Proxy listener unavailable; launched using the explicitly configured direct fallback.");
      else if (launched.proxyWarning) toast(launched.proxyWarning.message, true);
      else toast("Container launched in an isolated window.");
      await refreshApp();
    } catch (error) { toast(error.message, true); }
  }

  async function openCurrent(id) {
    try { await launch(id, await currentURL()); } catch (error) { toast(error.message, true); }
  }

  function fillBrowsers(container) {
    const select = $("#browser"); select.replaceChildren();
    for (const item of state.browsers) { const option = document.createElement("option"); option.value = `${item.type}|${item.path}`; option.textContent = `${item.name} — ${item.path}`; select.append(option); }
    const custom = document.createElement("option"); custom.value = "custom|"; custom.textContent = "Custom executable…"; select.append(custom);
    if (container?.browserExecutable) {
      const match = [...select.options].find((option) => option.value === `${container.browserType}|${container.browserExecutable}`);
      if (match) select.value = match.value; else { custom.value = `${container.browserType}|${container.browserExecutable}`; select.value = custom.value; }
      $("#browser-path").value = container.browserExecutable;
    } else if (state.preferences.lastBrowser) {
      const match = [...select.options].find((option) => option.value === `${state.preferences.lastBrowser.type}|${state.preferences.lastBrowser.path}`); if (match) select.value = match.value;
    }
    syncBrowserPath();
  }

  function syncBrowserPath() {
    const [type, path] = $("#browser").value.split("|");
    $("#browser-path").value = path || $("#browser-path").value;
    $("#browser-path-wrap").hidden = Boolean(path);
    $("#browser").dataset.type = type;
  }

  function openDialog(container = null, temporary = false) {
    const isTemp = temporary || container?.temporary;
    containerDialog.open();
    $("#container-id").value = container?.id || ""; $("#temporary").value = String(isTemp);
    $("#dialog-kind").textContent = isTemp ? "TEMPORARY CONTEXT" : "CONTAINER";
    $("#dialog-title").textContent = container ? "Edit container" : isTemp ? "Create temporary container" : "Create container";
    $("#save").textContent = container ? "Save changes" : isTemp ? "Create & launch" : "Create container";
    $("#name").value = container?.name || (isTemp ? `Temporary ${new Date().toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}` : "");
    $("#color").value = container?.color || (isTemp ? "#d28b26" : "#725cff"); $("#icon").value = container?.icon || (isTemp ? "⚡" : "");
    $("#launch-after").checked = isTemp; $("#launch-after").closest("label").hidden = isTemp;
    
    // Fill network mode
    $("#network-mode").value = container?.networkMode || "direct";
    fillProxies(container?.proxyProfileId);
    fillTemplates(container?.environmentTemplateId);
    syncNetworkMode();

    $("#form-error").hidden = true; fillBrowsers(container); $("#name").focus();
  }

  function syncNetworkMode() {
    const mode = $("#network-mode").value;
    $("#proxy-profile-wrap").hidden = mode !== "proxy";
    $("#environment-template-wrap").hidden = mode === "direct";
    if (mode !== "proxy") $("#proxy-profile").value = "";
    if (mode === "direct") $("#environment-template").value = "";
  }

  function fillProxies(selectedId) {
    const select = $("#proxy-profile");
    select.replaceChildren();
    const defaultOpt = document.createElement("option"); defaultOpt.value = ""; defaultOpt.textContent = "Select proxy..."; select.append(defaultOpt);
    for (const p of state.proxies || []) {
      const opt = document.createElement("option"); opt.value = p.id; opt.textContent = p.name; select.append(opt);
    }
    if (selectedId) select.value = selectedId;
  }

  function fillTemplates(selectedId) {
    const select = $("#environment-template");
    select.replaceChildren();
    const defaultOpt = document.createElement("option"); defaultOpt.value = ""; defaultOpt.textContent = "None"; select.append(defaultOpt);
    for (const t of state.templates || []) {
      const opt = document.createElement("option"); opt.value = t.id; opt.textContent = t.name; select.append(opt);
    }
    if (selectedId) select.value = selectedId;
  }

  async function saveForm(event) {
    event.preventDefault();
    const submitter = event.submitter; if (submitter?.value === "cancel") { $("#container-dialog").close(); return; }
    const [browserType, selectedPath] = $("#browser").value.split("|");
    try {
      const input = validateContainer({
        name: $("#name").value, color: $("#color").value, icon: $("#icon").value,
        browserType, browserExecutable: selectedPath || $("#browser-path").value,
        networkMode: $("#network-mode").value,
        proxyProfileId: $("#proxy-profile").value,
        environmentTemplateId: $("#environment-template").value
      });
      const id = $("#container-id").value, temporary = $("#temporary").value === "true";
      $("#save").disabled = true;
      const commandData = containerCommandData(input);
      const saved = id ? await request("update_container", { id, ...commandData }) : await request(temporary ? "create_temporary_container" : "create_container", commandData);
      state.preferences = await savePreferences(chrome.storage.local, { lastBrowser: { type: input.browserType, path: input.browserExecutable } });
      containerDialog.close(); toast(id ? "Container updated." : "Container created.");
      if (temporary || $("#launch-after").checked) await launch(saved.id, ""); else await refreshApp();
    } catch (error) { $("#form-error").textContent = error.message; $("#form-error").hidden = false; }
    finally { $("#save").disabled = false; }
  }

  async function duplicate(container) {
    try {
      const input = validateContainer({
        name: `${container.name} copy`.slice(0, 80),
        color: container.color,
        icon: container.icon || "",
        browserType: container.browserType,
        browserExecutable: container.browserExecutable,
        networkMode: container.networkMode || "direct",
        proxyProfileId: container.proxyProfileId,
        environmentTemplateId: container.environmentTemplateId
      });
      await request("create_container", containerCommandData(input));
      toast("Container duplicated with a fresh profile.");
      await refreshApp();
    }
    catch (error) { toast(error.message, true); }
  }

  async function closeContainer(id) { try { await request("close_container", { id }); toast("Close requested."); setTimeout(refreshApp, 500); } catch (error) { toast(error.message, true); } }

  function renderContainers() {
    const view = connectionView(state.status), el = $("#connection");
    el.className = `connection ${view.tone}`; el.querySelector("span:nth-child(2)").textContent = view.label;
    $("#retry").hidden = state.status.loading || state.status.connected;
    $("#host-summary").textContent = view.label;

    const available = state.containers.filter((c) => !c.running && c.state !== "launching");
    const options = ["<option value=\"\">Choose container</option>", ...available.map((c) => `<option value="${c.id}"></option>`)].join("");
    $("#quick-container").innerHTML = options;
    [...$("#quick-container").options].slice(1).forEach((option, i) => { option.textContent = available[i].name; });
    
    const visible = visibleContainers(state.containers, { query: $("#search").value, filter: $("#filter").value, sort: $("#sort").value });
    $("#container-count").textContent = `${state.containers.length} total`;
    $("#container-list").replaceChildren(...visible.map(renderCard));
    
    $("#loading").hidden = !state.status.loading;
    $("#empty").hidden = state.status.loading || state.containers.length > 0 || !state.status.connected;
    $("#no-results").hidden = state.status.loading || state.containers.length === 0 || visible.length > 0;
  }

  // Bind events
  const containerDialog = bindDialogControls($("#container-dialog"), {
    form: $("#container-form"),
    error: $("#form-error"),
    opener: () => document.activeElement,
    initialFocus: () => $("#name"),
  });

  $("#new-container").addEventListener("click", () => openDialog()); $("#new-temporary").addEventListener("click", () => openDialog(null, true)); $("#empty-create").addEventListener("click", () => openDialog());
  $("#container-form").addEventListener("submit", saveForm); $("#browser").addEventListener("change", syncBrowserPath); $("#retry").addEventListener("click", refreshApp);
  $("#search").addEventListener("input", renderContainers);
  $("#filter").addEventListener("change", async (event) => { state.preferences = await savePreferences(chrome.storage.local, { filter: event.target.value }); renderContainers(); });
  $("#sort").addEventListener("change", async (event) => { state.preferences = await savePreferences(chrome.storage.local, { sort: event.target.value }); renderContainers(); });
  $("#open-url").addEventListener("click", () => { const id = $("#quick-container").value; if (!id) return toast("Choose a container.", true); launch(id, $("#quick-url").value); });
  $("#open-current").addEventListener("click", () => { const id = $("#quick-container").value; if (!id) return toast("Choose a container.", true); openCurrent(id); });
  $("#host-toggle").addEventListener("click", () => { const expanded = $("#host-toggle").getAttribute("aria-expanded") === "true"; $("#host-toggle").setAttribute("aria-expanded", String(!expanded)); $("#host-content").hidden = expanded; });
  $("#cleanup-temporary").addEventListener("click", async () => {
    try {
      const result = await request("cleanup_temporary_containers");
      const cleaned = result.cleaned?.length || 0, pending = result.pending?.length || 0;
      toast(pending ? `Cleaned ${cleaned}; ${pending} profile${pending === 1 ? " is" : "s are"} still in use.` : `Temporary cleanup complete. ${cleaned} profile${cleaned === 1 ? "" : "s"} removed.`);
      await refreshApp();
    } catch (error) { toast(error.message, true); }
  });
  if (document.querySelector("#network-mode")) {
    document.querySelector("#network-mode").addEventListener("change", syncNetworkMode);
  }

  return { renderContainers };
}

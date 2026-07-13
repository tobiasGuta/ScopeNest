import { loadPreferences, savePreferences } from "../shared/storage.js";
import { validateContainer, validateWebURL } from "../shared/validation.js";
import { connectionView, visibleContainers } from "./state.js";

const $ = (selector) => document.querySelector(selector);
const state = { containers: [], status: { loading: true, connected: false }, preferences: { sort: "name", filter: "all" }, browsers: [] };

async function request(command, data) {
  const response = await chrome.runtime.sendMessage({ type: "native-request", command, data });
  if (!response?.success) { const error = new Error(response?.error || "Native host unavailable"); error.code = response?.errorCode; throw error; }
  return response.data;
}

function toast(message, error = false) {
  const item = document.createElement("div"); item.className = `toast${error ? " error" : ""}`; item.textContent = message;
  $("#toast-region").append(item); setTimeout(() => item.remove(), 4200);
}

function formatDate(value) {
  if (!value) return "Never used";
  const date = new Date(value); if (Number.isNaN(date.getTime())) return "Unknown";
  const delta = Date.now() - date.getTime();
  if (delta < 60_000) return "Just now";
  if (delta < 3_600_000) return `${Math.floor(delta / 60_000)}m ago`;
  if (delta < 86_400_000) return `${Math.floor(delta / 3_600_000)}h ago`;
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", year: date.getFullYear() === new Date().getFullYear() ? undefined : "numeric" }).format(date);
}

function setConnection() {
  const view = connectionView(state.status), el = $("#connection");
  el.className = `connection ${view.tone}`; el.querySelector("span:nth-child(2)").textContent = view.label;
  $("#retry").hidden = state.status.loading || state.status.connected;
  $("#host-summary").textContent = view.label;
}

function browserLabel(container) {
  return ({ chrome: "Chrome", chromium: "Chromium", edge: "Edge", brave: "Brave", custom: "Custom" })[container.browserType] || container.browserType;
}

function button(text, className, action, label = text) {
  const el = document.createElement("button"); el.type = "button"; el.className = className; el.textContent = text; el.setAttribute("aria-label", label); el.addEventListener("click", action); return el;
}

function renderCard(container) {
  const card = document.createElement("article"); card.className = "container-card"; card.style.setProperty("--container-color", container.color); card.dataset.id = container.id;
  const main = document.createElement("div"); main.className = "card-main";
  const head = document.createElement("div"); head.className = "card-head";
  const icon = document.createElement("div"); icon.className = "card-icon"; icon.textContent = container.icon || "▣"; icon.setAttribute("aria-hidden", "true");
  const title = document.createElement("div"); title.className = "card-title";
  const h3 = document.createElement("h3"); h3.textContent = container.name; title.append(h3);
  const meta = document.createElement("div"); meta.className = "meta";
  const browser = document.createElement("span"); browser.textContent = browserLabel(container); meta.append(browser);
  const used = document.createElement("span"); used.textContent = `• ${formatDate(container.lastLaunchedAt)}`; meta.append(used);
  if (container.running) { const running = document.createElement("span"); running.className = "badge running"; running.textContent = "Running"; meta.append(running); }
  if (container.temporary) { const temporary = document.createElement("span"); temporary.className = "badge temporary"; temporary.textContent = container.pendingCleanup ? "Cleanup pending" : "Temporary"; meta.append(temporary); }
  title.append(meta); head.append(icon, title);
  const menuButton = button("⋯", "card-menu", (event) => toggleMenu(card, container, event.currentTarget), `Actions for ${container.name}`); menuButton.setAttribute("aria-expanded", "false"); head.append(menuButton); main.append(head);
  const path = document.createElement("p"); path.className = "profile-path"; path.title = container.profilePath; path.textContent = container.profilePath; main.append(path); card.append(main);
  const actions = document.createElement("div"); actions.className = "card-actions";
  actions.append(button(container.running ? "Running" : "Launch", "button primary", () => launch(container.id, ""), `Launch ${container.name}`), button("Current page", "button secondary", () => openCurrent(container.id), `Open current page in ${container.name}`));
  actions.firstChild.disabled = container.running; card.append(actions); return card;
}

function toggleMenu(card, container, anchor) {
  const existing = card.querySelector(".action-menu");
  document.querySelectorAll(".action-menu").forEach((menu) => menu.remove());
  document.querySelectorAll(".card-menu").forEach((button) => button.setAttribute("aria-expanded", "false"));
  if (existing) return;
  const menu = document.createElement("div"); menu.className = "action-menu"; menu.setAttribute("role", "menu"); anchor.setAttribute("aria-expanded", "true");
  menu.append(button("Edit", "", () => { menu.remove(); openDialog(container); }));
  menu.append(button("Duplicate", "", () => duplicate(container)));
  if (container.running) menu.append(button("Close window", "", () => closeContainer(container.id)));
  menu.append(button("Delete", "delete", () => confirmDelete(container)));
  card.append(menu); menu.querySelector("button").focus();
}

function render() {
  setConnection();
  const options = ["<option value=\"\">Choose container</option>", ...state.containers.filter((c) => !c.running).map((c) => `<option value="${c.id}"></option>`)].join("");
  $("#quick-container").innerHTML = options;
  [...$("#quick-container").options].slice(1).forEach((option, i) => { option.textContent = state.containers.filter((c) => !c.running)[i].name; });
  const visible = visibleContainers(state.containers, { query: $("#search").value, filter: $("#filter").value, sort: $("#sort").value });
  $("#container-count").textContent = `${state.containers.length} total`;
  $("#container-list").replaceChildren(...visible.map(renderCard));
  $("#loading").hidden = !state.status.loading;
  $("#empty").hidden = state.status.loading || state.containers.length > 0 || !state.status.connected;
  $("#no-results").hidden = state.status.loading || state.containers.length === 0 || visible.length > 0;
}

async function refresh() {
  state.status = { loading: true, connected: false }; render();
  try {
    const [status, containers] = await Promise.all([request("get_status"), request("list_containers")]);
    state.containers = containers; state.browsers = status.detectedBrowsers || [];
    state.status = { loading: false, connected: true, version: status.hostVersion };
    $("#data-directory").textContent = status.dataDirectory; $("#extension-id").textContent = chrome.runtime.id;
  } catch (error) { state.status = { loading: false, connected: false, error: error.message }; }
  render();
}

async function currentURL() {
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  return validateWebURL(tab?.url || "", { allowEmpty: false });
}

async function launch(id, url) {
  try { await request("launch_container", { id, url: validateWebURL(url) }); toast("Container launched in an isolated window."); await refresh(); }
  catch (error) { toast(error.message, true); }
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
  $("#container-form").reset(); $("#container-id").value = container?.id || ""; $("#temporary").value = String(isTemp);
  $("#dialog-kind").textContent = isTemp ? "TEMPORARY CONTEXT" : "CONTAINER";
  $("#dialog-title").textContent = container ? "Edit container" : isTemp ? "Create temporary container" : "Create container";
  $("#save").textContent = container ? "Save changes" : isTemp ? "Create & launch" : "Create container";
  $("#name").value = container?.name || (isTemp ? `Temporary ${new Date().toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}` : "");
  $("#color").value = container?.color || (isTemp ? "#d28b26" : "#725cff"); $("#icon").value = container?.icon || (isTemp ? "⚡" : "");
  $("#launch-after").checked = isTemp; $("#launch-after").closest("label").hidden = isTemp;
  $("#form-error").hidden = true; fillBrowsers(container); $("#container-dialog").showModal(); $("#name").focus();
}

async function saveForm(event) {
  event.preventDefault();
  const submitter = event.submitter; if (submitter?.value === "cancel") { $("#container-dialog").close(); return; }
  const [browserType, selectedPath] = $("#browser").value.split("|");
  try {
    const input = validateContainer({ name: $("#name").value, color: $("#color").value, icon: $("#icon").value, browserType, browserExecutable: selectedPath || $("#browser-path").value });
    const id = $("#container-id").value, temporary = $("#temporary").value === "true";
    $("#save").disabled = true;
    const saved = id ? await request("update_container", { id, ...input }) : await request(temporary ? "create_temporary_container" : "create_container", input);
    state.preferences = await savePreferences(chrome.storage.local, { lastBrowser: { type: input.browserType, path: input.browserExecutable } });
    $("#container-dialog").close(); toast(id ? "Container updated." : "Container created.");
    if (temporary || $("#launch-after").checked) await launch(saved.id, ""); else await refresh();
  } catch (error) { $("#form-error").textContent = error.message; $("#form-error").hidden = false; }
  finally { $("#save").disabled = false; }
}

async function duplicate(container) {
  try { await request("create_container", { name: `${container.name} copy`.slice(0, 80), color: container.color, icon: container.icon || "", browserType: container.browserType, browserExecutable: container.browserExecutable }); toast("Container duplicated with a fresh profile."); await refresh(); }
  catch (error) { toast(error.message, true); }
}

function confirmDelete(container) {
  $("#confirm-message").textContent = `This permanently removes “${container.name}” and all data in its isolated profile.`;
  const dialog = $("#confirm-dialog"); dialog.showModal();
  dialog.addEventListener("close", async function onClose() { dialog.removeEventListener("close", onClose); if (dialog.returnValue !== "confirm") return; try { await request("delete_container", { id: container.id }); toast("Container deleted."); await refresh(); } catch (error) { toast(error.message, true); } }, { once: true });
}

async function closeContainer(id) { try { await request("close_container", { id }); toast("Close requested."); setTimeout(refresh, 500); } catch (error) { toast(error.message, true); } }

function bind() {
  $("#new-container").addEventListener("click", () => openDialog()); $("#new-temporary").addEventListener("click", () => openDialog(null, true)); $("#empty-create").addEventListener("click", () => openDialog());
  $("#container-form").addEventListener("submit", saveForm); $("#browser").addEventListener("change", syncBrowserPath); $("#retry").addEventListener("click", refresh);
  $("#search").addEventListener("input", render); $("#filter").addEventListener("change", async (event) => { state.preferences = await savePreferences(chrome.storage.local, { filter: event.target.value }); render(); });
  $("#sort").addEventListener("change", async (event) => { state.preferences = await savePreferences(chrome.storage.local, { sort: event.target.value }); render(); });
  $("#open-url").addEventListener("click", () => { const id = $("#quick-container").value; if (!id) return toast("Choose a container.", true); launch(id, $("#quick-url").value); });
  $("#open-current").addEventListener("click", () => { const id = $("#quick-container").value; if (!id) return toast("Choose a container.", true); openCurrent(id); });
  $("#host-toggle").addEventListener("click", () => { const expanded = $("#host-toggle").getAttribute("aria-expanded") === "true"; $("#host-toggle").setAttribute("aria-expanded", String(!expanded)); $("#host-content").hidden = expanded; });
  $("#cleanup-temporary").addEventListener("click", async () => {
    try {
      const result = await request("cleanup_temporary_containers");
      const cleaned = result.cleaned?.length || 0, pending = result.pending?.length || 0;
      toast(pending ? `Cleaned ${cleaned}; ${pending} profile${pending === 1 ? " is" : "s are"} still in use.` : `Temporary cleanup complete. ${cleaned} profile${cleaned === 1 ? "" : "s"} removed.`);
      await refresh();
    } catch (error) { toast(error.message, true); }
  });
  $("#open-side-panel").addEventListener("click", async () => { try { const [tab] = await chrome.tabs.query({ active: true, currentWindow: true }); await chrome.sidePanel.open({ windowId: tab.windowId }); window.close(); } catch (error) { toast(error.message, true); } });
  document.addEventListener("click", (event) => { if (!event.target.closest(".card-menu, .action-menu")) document.querySelectorAll(".action-menu").forEach((menu) => menu.remove()); });
}

async function init() {
  bind(); state.preferences = await loadPreferences(chrome.storage.local); $("#filter").value = state.preferences.filter; $("#sort").value = state.preferences.sort;
  chrome.runtime.onMessage.addListener((message) => { if (message?.type === "native-state" && !message.connected) { state.status = { loading: false, connected: false, error: message.error }; render(); } });
  await refresh();
}

init().catch((error) => { state.status = { loading: false, connected: false, error: error.message }; render(); });

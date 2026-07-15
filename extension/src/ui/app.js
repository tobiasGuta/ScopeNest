import { request, toast } from "./api.js";
import { initNavigation } from "./navigation.js";
import { initContainers } from "./containers.js";
import { initProxies } from "./proxies.js";
import { initCertificates } from "./certificates.js";
import { initTemplates } from "./templates.js";
import { loadPreferences } from "../shared/storage.js";

const state = { 
  containers: [], 
  proxies: [],
  certificates: [],
  templates: [],
  host: null,
  status: { loading: true, connected: false }, 
  preferences: { sort: "name", filter: "all" }, 
  browsers: [] 
};

const $ = (selector) => document.querySelector(selector);

let renderContainers, renderProxies, renderCertificates, renderTemplates;

async function refreshApp() {
  state.status = { loading: true, connected: false }; 
  renderContainers?.();
  renderProxies?.();
  renderCertificates?.();
  renderTemplates?.();

  try {
    const [status, containers, proxies, certificates, templates] = await Promise.all([
      request("get_status"), 
      request("list_containers"),
      request("list_proxy_profiles").catch(() => []),
      request("list_certificates").catch(() => []),
      request("list_environment_templates").catch(() => [])
    ]);
    state.containers = containers; 
    state.proxies = proxies;
    state.certificates = certificates;
    state.templates = templates;
    state.host = status;
    state.browsers = status.detectedBrowsers || [];
    state.status = { loading: false, connected: true, version: status.hostVersion };
    $("#data-directory").textContent = status.dataDirectory; 
    $("#extension-id").textContent = chrome.runtime.id;
  } catch (error) { 
    state.host = null;
    state.status = { loading: false, connected: false, error: error.message }; 
  }
  
  renderContainers?.();
  renderProxies?.();
  renderCertificates?.();
  renderTemplates?.();
}

async function init() {
  initNavigation();
  state.preferences = await loadPreferences(chrome.storage.local); 
  $("#filter").value = state.preferences.filter; 
  $("#sort").value = state.preferences.sort;

  const containersMod = initContainers(state, refreshApp);
  renderContainers = containersMod.renderContainers;

  const proxiesMod = initProxies(state, refreshApp);
  renderProxies = proxiesMod.renderProxies;

  const certsMod = initCertificates(state, refreshApp);
  renderCertificates = certsMod.renderCertificates;

  const templatesMod = initTemplates(state, refreshApp);
  renderTemplates = templatesMod.renderTemplates;

  chrome.runtime.onMessage.addListener((message) => { 
    if (message?.type === "native-state" && !message.connected) { 
      state.status = { loading: false, connected: false, error: message.error }; 
      renderContainers?.();
    } 
  });
  
  await refreshApp();
}

init().catch((error) => { 
  state.status = { loading: false, connected: false, error: error.message }; 
  renderContainers?.(); 
});

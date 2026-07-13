import { createRequest, HOST_NAME, parseResponse, timeoutForCommand } from "./shared/protocol.js";

let nativePort = null;
let connectionError = null;
const pending = new Map();

function rejectPending(message, code = "NATIVE_HOST_UNAVAILABLE") {
  for (const { reject, timer } of pending.values()) {
    clearTimeout(timer);
    const error = new Error(message); error.code = code; reject(error);
  }
  pending.clear();
}

function connect() {
  if (nativePort) return nativePort;
  try {
    const port = chrome.runtime.connectNative(HOST_NAME);
    nativePort = port;
    connectionError = null;
    port.onMessage.addListener((response) => {
      const item = pending.get(response?.requestId);
      if (!item) return;
      pending.delete(response.requestId); clearTimeout(item.timer);
      try { item.resolve(parseResponse(response, item.request)); } catch (error) { item.reject(error); }
    });
    port.onDisconnect.addListener(() => {
      const message = chrome.runtime.lastError?.message || "Native host disconnected.";
      if (nativePort === port) nativePort = null;
      connectionError = message;
      rejectPending(message);
      chrome.runtime.sendMessage({ type: "native-state", connected: false, error: message }).catch(() => {});
    });
    return port;
  } catch (error) {
    connectionError = error.message;
    throw error;
  }
}

function nativeRequest(command, data) {
  const request = createRequest(command, data);
  const timeoutMs = timeoutForCommand(command);
  return new Promise((resolve, reject) => {
    let port;
    try { port = connect(); } catch (error) { reject(error); return; }
    const timer = setTimeout(() => {
      pending.delete(request.requestId);
      const seconds = timeoutMs / 1000;
      const error = new Error(`Native host request timed out after ${seconds} seconds.`); error.code = "TIMEOUT"; reject(error);
    }, timeoutMs);
    pending.set(request.requestId, { request, resolve, reject, timer });
    try { port.postMessage(request); } catch (error) { clearTimeout(timer); pending.delete(request.requestId); reject(error); }
  });
}

chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
  if (message?.type === "native-request") {
    nativeRequest(message.command, message.data)
      .then((data) => sendResponse({ success: true, data }))
      .catch((error) => sendResponse({ success: false, error: error.message, errorCode: error.code || "NATIVE_ERROR" }));
    return true;
  }
  if (message?.type === "connection-state") {
    sendResponse({ connected: Boolean(nativePort), error: connectionError });
  }
  return false;
});

chrome.runtime.onInstalled.addListener(() => {
  chrome.sidePanel.setPanelBehavior({ openPanelOnActionClick: false }).catch(() => {});
  nativeRequest("cleanup_temporary_containers").catch(() => {});
});

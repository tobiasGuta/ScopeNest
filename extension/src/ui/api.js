export async function request(command, data = {}) {
  const response = await chrome.runtime.sendMessage({ type: "native-request", command, data });
  if (!response?.success) {
    const error = new Error(response?.error || "Native host unavailable");
    error.code = response?.errorCode;
    throw error;
  }
  return response.data;
}

export function toast(message, error = false) {
  const item = document.createElement("div");
  item.className = `toast${error ? " error" : ""}`;
  item.textContent = message;
  document.querySelector("#toast-region").append(item);
  setTimeout(() => item.remove(), 4200);
}

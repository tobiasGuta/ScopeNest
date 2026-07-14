import { request, toast } from "./api.js";

export function button(text, className, action, label = text) {
  const el = document.createElement("button");
  el.type = "button";
  el.className = className;
  el.textContent = text;
  el.setAttribute("aria-label", label);
  el.addEventListener("click", action);
  return el;
}

export function formatDate(value) {
  if (!value) return "Never used";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "Unknown";
  const delta = Date.now() - date.getTime();
  if (delta < 60_000) return "Just now";
  if (delta < 3_600_000) return `${Math.floor(delta / 60_000)}m ago`;
  if (delta < 86_400_000) return `${Math.floor(delta / 3_600_000)}h ago`;
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    year: date.getFullYear() === new Date().getFullYear() ? undefined : "numeric"
  }).format(date);
}

export function toggleMenu(card, container, anchor, buildMenu) {
  const existing = card.querySelector(".action-menu");
  document.querySelectorAll(".action-menu").forEach((menu) => menu.remove());
  document.querySelectorAll(".card-menu").forEach((button) => button.setAttribute("aria-expanded", "false"));
  if (existing) return;
  const menu = document.createElement("div");
  menu.className = "action-menu";
  menu.setAttribute("role", "menu");
  anchor.setAttribute("aria-expanded", "true");
  buildMenu(menu, container);
  card.append(menu);
  menu.querySelector("button")?.focus();
}

export function confirmDelete(title, message, onConfirm) {
  const dialog = document.querySelector("#confirm-dialog");
  dialog.querySelector("#confirm-title").textContent = title;
  dialog.querySelector("#confirm-message").textContent = message;
  dialog.showModal();
  dialog.addEventListener("close", async function onClose() {
    dialog.removeEventListener("close", onClose);
    if (dialog.returnValue === "confirm") {
      await onConfirm();
    }
  }, { once: true });
}

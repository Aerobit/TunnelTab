// dialogs.js — modal dialogs (native <dialog>), confirmations, form fields
// and toast messages.

import { h } from "./dom.js";

/**
 * Opens a modal dialog.
 *   title   heading text
 *   body    node(s) for the dialog body
 *   actions [{label, kind: "primary"|"danger"|"secondary", onClick}]
 *           onClick may be async; return false (or throw) to keep the dialog
 *           open. Buttons are disabled while it runs.
 *   wide    use a wider dialog
 * Resolves with the label of the action that closed it, or null on Escape.
 */
export function openDialog({ title, body, actions = [], wide = false, danger = false }) {
  return new Promise((resolve) => {
    const error = h("p", { class: "dialog-error", role: "alert", hidden: true });
    const buttons = actions.map((a) =>
      h("button", { type: a.submit ? "submit" : "button", class: ["btn", a.kind || "secondary"] }, a.label),
    );
    const form = h(
      "form",
      { method: "dialog", class: "dialog-form", novalidate: true },
      h("header", { class: "dialog-header" }, h("h2", {}, title)),
      h("div", { class: "dialog-body" }, body),
      error,
      h("footer", { class: "dialog-actions" }, buttons),
    );
    const dialog = h("dialog", { class: ["dialog", wide && "wide", danger && "danger"] }, form);
    let result = null;

    async function run(action, button) {
      error.hidden = true;
      if (!action.onClick) return close(action.label);
      buttons.forEach((b) => (b.disabled = true));
      button.classList.add("busy");
      try {
        const keepOpen = (await action.onClick()) === false;
        if (!keepOpen) close(action.label);
      } catch (err) {
        error.textContent = err.message || String(err);
        error.hidden = false;
      } finally {
        buttons.forEach((b) => (b.disabled = false));
        button.classList.remove("busy");
      }
    }
    function close(label) {
      result = label;
      dialog.close();
    }

    actions.forEach((a, i) => buttons[i].addEventListener("click", (e) => {
      e.preventDefault();
      run(a, buttons[i]);
    }));
    // Enter in a text field triggers the primary action.
    form.addEventListener("submit", (e) => {
      e.preventDefault();
      const i = actions.findIndex((a) => a.submit);
      if (i >= 0 && !buttons[i].disabled) run(actions[i], buttons[i]);
    });
    dialog.addEventListener("close", () => {
      dialog.remove();
      resolve(result);
    });
    document.body.appendChild(dialog);
    dialog.showModal();
    const first = dialog.querySelector("input:not([type=hidden]):not([disabled]), select, textarea");
    (first || buttons[buttons.length - 1])?.focus();
  });
}

/** Asks a yes/no question. Resolves true when confirmed. */
export async function confirmDialog(title, message, { confirmLabel = "OK", danger = false } = {}) {
  const label = await openDialog({
    title,
    body: h("p", {}, message),
    danger,
    actions: [
      { label: "Cancel" },
      { label: confirmLabel, kind: danger ? "danger" : "primary", submit: true },
    ],
  });
  return label === confirmLabel;
}

let fieldCounter = 0;

/**
 * A labelled form field. input is an <input>, <select> or <textarea>; hint
 * is optional help text shown underneath.
 */
export function field(label, input, hint) {
  const id = "f" + ++fieldCounter;
  input.id = id;
  const hintEl = hint ? h("small", { class: "hint", id: id + "-hint" }, hint) : null;
  if (hintEl) input.setAttribute("aria-describedby", hintEl.id);
  return h("div", { class: "field" }, h("label", { for: id }, label), input, hintEl);
}

/** A checkbox with its label to the right. */
export function checkbox(label, checked, hint) {
  const input = h("input", { type: "checkbox", checked });
  const el = h(
    "div",
    { class: "field check" },
    h("label", {}, input, " ", label),
    hint ? h("small", { class: "hint" }, hint) : null,
  );
  return { el, input };
}

const toastArea = () =>
  document.getElementById("toasts") || document.body.appendChild(h("div", { id: "toasts", "aria-live": "polite" }));

/**
 * Shows a short message. kind: "info" | "success" | "error". extra may be a
 * node (for example a link) appended to the message.
 */
export function toast(message, kind = "info", extra = null, ms = 6000) {
  const el = h(
    "div",
    { class: ["toast", kind], role: kind === "error" ? "alert" : "status" },
    h("span", {}, message),
    extra,
    h("button", { type: "button", class: "toast-close", "aria-label": "Dismiss", onclick: () => el.remove() }, "×"),
  );
  toastArea().appendChild(el);
  setTimeout(() => el.remove(), kind === "error" ? ms * 2 : ms);
}

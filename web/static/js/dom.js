// dom.js — tiny helpers for building DOM safely. Text is always inserted as
// text nodes (never innerHTML), so names and hostnames can't inject markup.

/**
 * h("button", {class: "btn", onclick: fn, disabled: true}, "Save")
 * Attributes starting with "on" become event listeners; true/false toggle
 * boolean attributes; null/undefined are skipped. Children may be nodes,
 * strings, numbers, arrays or null.
 */
export function h(tag, attrs = {}, ...children) {
  const el = document.createElement(tag);
  for (const [key, value] of Object.entries(attrs || {})) {
    if (value === null || value === undefined || value === false) continue;
    if (key.startsWith("on") && typeof value === "function") {
      el.addEventListener(key.slice(2), value);
    } else if (key === "class") {
      el.className = Array.isArray(value) ? value.filter(Boolean).join(" ") : value;
    } else if (key === "dataset") {
      Object.assign(el.dataset, value);
    } else if (key === "value") {
      el.value = value;
    } else if (key === "checked") {
      el.checked = Boolean(value);
    } else if (value === true) {
      el.setAttribute(key, "");
    } else {
      el.setAttribute(key, String(value));
    }
  }
  append(el, children);
  return el;
}

function append(el, children) {
  for (const child of children) {
    if (child === null || child === undefined || child === false) continue;
    if (Array.isArray(child)) append(el, child);
    else if (child instanceof Node) el.appendChild(child);
    else el.appendChild(document.createTextNode(String(child)));
  }
}

/** Replaces all children of el. */
export function replace(el, ...children) {
  el.replaceChildren();
  append(el, children);
}

/** Returns a debounced version of fn. */
export function debounce(fn, ms) {
  let t;
  return (...args) => {
    clearTimeout(t);
    t = setTimeout(() => fn(...args), ms);
  };
}

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

// Redrawing a page replaces its elements, which drops the keyboard focus to
// the top of the page. focusKey remembers the focused control by what it is
// (tag, label, attributes) and by where it is; restoreFocus focuses the
// matching control in the redrawn page, or the one in the same place if the
// control changed (e.g. Start became Stop).

const FOCUSABLE = "a[href], button, input, select, textarea, summary, [tabindex]";

function signature(el) {
  const attrs = [...el.attributes]
    .filter((a) => ["id", "name", "type", "href", "role", "aria-label"].includes(a.name) || a.name.startsWith("data-"))
    .map((a) => `${a.name}=${a.value}`);
  const text = el.tagName === "TEXTAREA" ? "" : el.textContent.trim().slice(0, 80);
  return `${el.tagName}|${attrs.join("|")}|${text}`;
}

function sameAs(container, tag, sig) {
  return [...container.querySelectorAll(tag)].filter((e) => signature(e) === sig);
}

/** Describes the focused element inside container, or returns null. */
export function focusKey(container) {
  const el = document.activeElement;
  if (!el || el === document.body || !container.contains(el)) return null;
  const sig = signature(el);
  const path = [];
  for (let n = el; n !== container; n = n.parentElement) path.unshift([...n.parentElement.children].indexOf(n));
  let caret = null;
  try {
    if (typeof el.selectionStart === "number") caret = [el.selectionStart, el.selectionEnd];
  } catch { /* inputs like checkboxes have no caret */ }
  const same = sameAs(container, el.tagName, sig);
  return { el, sig, path, caret, nth: same.indexOf(el), count: same.length };
}

/** Focuses the element matching key (from focusKey) inside container. */
export function restoreFocus(container, key) {
  if (!key) return;
  let atPath = container;
  for (const i of key.path) atPath = atPath?.children[i];
  if (atPath && !(atPath.matches(FOCUSABLE) && !atPath.disabled)) atPath = null;
  const same = sameAs(container, key.el.tagName, key.sig);
  // The same element (kept across the redraw), the same control in the same
  // place, the same control elsewhere (only if it is unambiguous: as many
  // identical ones as before), or whatever control is now in its place.
  const el = [
    key.el.isConnected && container.contains(key.el) ? key.el : null,
    atPath && signature(atPath) === key.sig ? atPath : null,
    same.length === key.count ? same[key.nth] : null,
    atPath,
  ].find(Boolean);
  if (!el) return;
  el.focus({ preventScroll: true });
  if (key.caret && signature(el) === key.sig) {
    try { el.setSelectionRange(...key.caret); } catch { /* not a text field */ }
  }
}

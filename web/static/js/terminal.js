// terminal.js — the terminal page (terminal.html#<serverId>/<terminalId>),
// used for "Pop out" from the dashboard and for terminals opened on their
// own. The session itself is a TermView (termview.js).
//
// The page keeps an event stream open that names its session, so the session
// is kept while this tab exists (even while locked); closing the tab ends it.
// Reloading re-attaches. See docs/ARCHITECTURE.md → "Terminals".

import { sayClosing, streamEvents } from "./api.js";
import { TERM_STATES, TermView } from "./termview.js";

const [serverId, savedTerminalId] = location.hash.slice(1).split("/").map(decodeURIComponent);

const titleEl = document.getElementById("term-title");
const statusEl = document.getElementById("term-status");
const toastEl = document.getElementById("term-toast");
const main = document.getElementById("terminal");

let toastTimer = null;
function toast(text) {
  toastEl.textContent = text;
  toastEl.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (toastEl.hidden = true), 1200);
}

let watchedId;
let stopEvents = null;

const view = new TermView({
  serverId, terminalId: savedTerminalId || null, reopenIfGone: true, onToast: toast,
  onChange(v) {
    const [cls, label] = TERM_STATES[v.state];
    statusEl.className = "pill " + cls;
    statusEl.textContent = label;
    if (v.serverName) {
      titleEl.textContent = v.serverName;
      document.title = `${v.serverName} — TunnelTab`;
    }
    if (v.terminalId !== watchedId) remember(v.terminalId);
    if (v.state === "connected") v.focus();
  },
});
main.appendChild(view.el);

function remember(id) {
  watchedId = id;
  // Kept in the address so a reload re-attaches to the same session.
  history.replaceState(null, "", `#${encodeURIComponent(serverId || "")}${id ? "/" + encodeURIComponent(id) : ""}`);
  watchEvents();
}

// The event stream tells us when TunnelTab unlocks, and — because it names
// our session — keeps the session alive for as long as this tab is open.
function watchEvents() {
  stopEvents?.();
  stopEvents = streamEvents((ev) => {
    if (ev.type === "stopping") {
      stopEvents?.();
      view.stopped();
      return;
    }
    const unlocked = (ev.type === "vault" && ev.state === "unlocked") || ev.type === "resync";
    if (unlocked) view.resume();
  }, () => {}, watchedId ? `?terminal=${encodeURIComponent(watchedId)}` : "");
}

window.addEventListener("pagehide", sayClosing);

if (!serverId) {
  view.setState("ended", "Open terminals from the TunnelTab dashboard.");
  view.msgButton.hidden = true;
} else {
  watchEvents();
  view.start();
}

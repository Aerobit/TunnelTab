// terminal.js — the terminal page (terminal.html#<serverId>/<terminalId>).
//
// A terminal session lives in the TunnelTab program, independent of this
// page: the page attaches to it over a WebSocket. Locking TunnelTab detaches
// the page (the shell keeps running); after unlocking, the page re-attaches
// by itself and the recent output is replayed. Reloading the page also
// re-attaches. See docs/ARCHITECTURE.md → "Terminals".

import { Terminal } from "../vendor/xterm/xterm.mjs";
import { FitAddon } from "../vendor/xterm/addon-fit.mjs";
import { api, streamEvents } from "./api.js";
import { withHostKeys } from "./forms.js";

const [serverId, savedTerminalId] = location.hash.slice(1).split("/").map(decodeURIComponent);
let terminalId = savedTerminalId || null;

const titleEl = document.getElementById("term-title");
const statusEl = document.getElementById("term-status");
const messageEl = document.getElementById("term-message");
const reconnectBtn = document.getElementById("term-reconnect");
const view = document.getElementById("terminal");

const term = new Terminal({
  cursorBlink: true,
  fontFamily: 'ui-monospace, "Cascadia Mono", Consolas, "DejaVu Sans Mono", monospace',
  fontSize: 14,
  scrollback: 5000,
  theme: {
    background: "#0d1117", foreground: "#e6edf3", cursor: "#58a6ff", cursorAccent: "#0d1117",
    selectionBackground: "rgba(56, 139, 253, 0.35)",
    black: "#484f58", red: "#ff7b72", green: "#3fb950", yellow: "#d29922",
    blue: "#58a6ff", magenta: "#bc8cff", cyan: "#39c5cf", white: "#b1bac4",
    brightBlack: "#6e7681", brightRed: "#ffa198", brightGreen: "#56d364", brightYellow: "#e3b341",
    brightBlue: "#79c0ff", brightMagenta: "#d2a8ff", brightCyan: "#56d4dd", brightWhite: "#ffffff",
  },
});
const fit = new FitAddon();
term.loadAddon(fit);
term.open(document.getElementById("terminal"));
fit.fit();

// Copy and paste work like Windows Terminal:
// - Ctrl+C copies when text is selected; otherwise it stops the running
//   command as usual. Ctrl+Shift+C and Ctrl+Insert copy too.
// - Ctrl+V, Ctrl+Shift+V and Shift+Insert paste. The browser does the
//   pasting (xterm.js just mustn't send the key), so reading the clipboard
//   needs no permission.
// - Right-click copies the selection; with nothing selected it shows the
//   browser's menu, which has Paste.
// preventDefault matters: without it Firefox opens its Inspector on
// Ctrl+Shift+C.
const toastEl = document.getElementById("term-toast");
let toastTimer = null;
function toast(text) {
  toastEl.textContent = text;
  toastEl.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (toastEl.hidden = true), 1200);
}

function copySelection() {
  const text = term.getSelection();
  if (!text) return;
  navigator.clipboard.writeText(text).then(
    () => { term.clearSelection(); toast("Copied"); },
    () => toast("Couldn't copy"),
  );
}

term.attachCustomKeyEventHandler((e) => {
  if (e.type !== "keydown") return true;
  const ctrl = e.ctrlKey && !e.altKey && !e.metaKey; // AltGr is Ctrl+Alt on Windows
  const copy = ctrl && ((e.code === "KeyC" && (e.shiftKey || term.hasSelection())) || (e.code === "Insert" && !e.shiftKey));
  if (copy) {
    e.preventDefault();
    copySelection();
    return false;
  }
  const paste = (ctrl && e.code === "KeyV") || (e.shiftKey && !e.ctrlKey && !e.altKey && e.code === "Insert");
  return !paste;
});

view.addEventListener("contextmenu", (e) => {
  if (!term.hasSelection()) return;
  e.preventDefault();
  copySelection();
});

// state: "connecting" | "connected" | "locked" | "disconnected" | "ended"
let state = "ended";
let ws = null;
let retryTimer = null;
const encoder = new TextEncoder();

function setStatus(next, label, message = "") {
  state = next;
  const kind = { connecting: "connecting", connected: "connected", locked: "paused", disconnected: "reconnecting", ended: "ended" }[next];
  statusEl.className = "pill " + kind;
  statusEl.textContent = label;
  messageEl.textContent = message;
  messageEl.hidden = !message;
  reconnectBtn.hidden = !(next === "ended" || next === "disconnected");
  reconnectBtn.textContent = next === "ended" ? "New session" : "Reconnect";
  requestAnimationFrame(() => fit.fit());
}

function send(data) {
  if (ws?.readyState === WebSocket.OPEN) ws.send(data);
}

term.onData((data) => send(encoder.encode(data)));
term.onBinary((data) => send(Uint8Array.from(data, (c) => c.charCodeAt(0))));
term.onResize(({ cols, rows }) => send(JSON.stringify({ type: "resize", cols, rows })));
new ResizeObserver(() => fit.fit()).observe(document.getElementById("terminal"));

function rememberSession(id) {
  terminalId = id;
  // Kept in the address so a reload re-attaches to the same session.
  history.replaceState(null, "", `#${encodeURIComponent(serverId)}${id ? "/" + encodeURIComponent(id) : ""}`);
  watchEvents();
}

// The event stream tells us when TunnelTab unlocks, and — because it names
// our session — keeps the session alive for as long as this tab is open,
// even while locked or in the background. Closing the tab ends the session.
let stopEvents = null;
function watchEvents() {
  stopEvents?.();
  stopEvents = streamEvents((ev) => {
    const unlocked = (ev.type === "vault" && ev.state === "unlocked") || ev.type === "resync";
    if (unlocked && state === "locked") terminalId ? attachSession() : openSession();
  }, () => {}, terminalId ? `?terminal=${encodeURIComponent(terminalId)}` : "");
}

/** Opens a new shell on the server. */
async function openSession() {
  setStatus("connecting", "Connecting…");
  let opened;
  try {
    opened = await withHostKeys(() => api("POST", "/terminals", { serverId, cols: term.cols, rows: term.rows }));
  } catch (err) {
    return showError(err);
  }
  if (!opened) return setStatus("ended", "Not connected", "The server's fingerprint was not confirmed.");
  term.reset();
  rememberSession(opened.terminalId);
  connect(opened);
}

/** Re-attaches to the existing session (after a lock, reload or blip). */
async function attachSession() {
  setStatus("connecting", "Reconnecting…");
  try {
    connect(await api("POST", `/terminals/${encodeURIComponent(terminalId)}/attach`));
  } catch (err) {
    if (err.status === 404) {
      // The session ended while we were away; start a fresh one.
      rememberSession(null);
      return openSession();
    }
    showError(err);
  }
}

function showError(err) {
  if (err.code === "locked") {
    // Hide what was on screen: someone at the PC shouldn't be able to read
    // it while TunnelTab is locked. It's replayed when the page re-attaches.
    term.reset();
    view.classList.add("hidden-while-locked");
    return setStatus("locked", "Locked",
      terminalId
        ? "TunnelTab is locked. Your session keeps running — unlock TunnelTab in the dashboard and this terminal reconnects by itself."
        : "TunnelTab is locked. Unlock it in the dashboard; this terminal connects by itself.");
  }
  if (err.code === "offline") {
    setStatus("disconnected", "Disconnected", "Can't reach TunnelTab. Retrying…");
    return retrySoon();
  }
  setStatus("ended", "Not connected", err.message);
}

function retrySoon() {
  clearTimeout(retryTimer);
  retryTimer = setTimeout(() => (terminalId ? attachSession() : openSession()), 3000);
}

function connect({ ticket, serverName }) {
  titleEl.textContent = serverName;
  document.title = `${serverName} — TunnelTab`;

  let exitInfo = null;
  let locked = false;
  const socket = new WebSocket(`ws://${location.host}/api/terminals/connect?ticket=${encodeURIComponent(ticket)}`);
  socket.binaryType = "arraybuffer";
  ws = socket;
  socket.onmessage = (ev) => {
    if (typeof ev.data !== "string") {
      term.write(new Uint8Array(ev.data));
      return;
    }
    let msg;
    try {
      msg = JSON.parse(ev.data);
    } catch {
      return;
    }
    if (msg.type === "attached") {
      term.reset(); // the recent output is replayed next
      view.classList.remove("hidden-while-locked");
      setStatus("connected", "Connected");
      send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
      term.focus();
    } else if (msg.type === "locked") {
      locked = true;
    } else if (msg.type === "exit") {
      exitInfo = msg;
    }
  };
  socket.onclose = (ev) => {
    if (ws !== socket) return; // replaced by a newer connection
    ws = null;
    if (exitInfo) {
      rememberSession(null);
      const why = exitInfo.message
        ? `Session ended: ${exitInfo.message}.`
        : exitInfo.code ? `The shell exited with code ${exitInfo.code}.` : "";
      setStatus("ended", "Session ended", why);
      term.write("\r\n\x1b[2m[session ended — press Enter or click New session]\x1b[0m\r\n");
    } else if (locked) {
      showError({ code: "locked" });
    } else if (ev.reason === "opened in another tab") {
      setStatus("disconnected", "Opened elsewhere", "This terminal was opened in another tab. Click Reconnect to use it here.");
    } else {
      setStatus("disconnected", "Disconnected", "Lost the connection to TunnelTab. Retrying…");
      retrySoon();
    }
  };
}


// After the session ends, Enter starts a new one.
term.onKey(({ domEvent }) => {
  if (state === "ended" && domEvent.key === "Enter") openSession();
});
reconnectBtn.addEventListener("click", () => {
  clearTimeout(retryTimer);
  if (state === "ended" || !terminalId) openSession();
  else attachSession();
});

watchEvents();
if (!serverId) {
  setStatus("ended", "No server", "Open terminals from the TunnelTab dashboard.");
  reconnectBtn.hidden = true;
} else if (terminalId) {
  attachSession();
} else {
  openSession();
}

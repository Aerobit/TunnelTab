// terminal.js — the terminal page (terminal.html#<serverId>). Opens an SSH
// shell through the TunnelTab API and connects xterm.js to it over a
// WebSocket. See docs/ARCHITECTURE.md → "Terminals".

import { Terminal } from "../vendor/xterm/xterm.mjs";
import { FitAddon } from "../vendor/xterm/addon-fit.mjs";
import { api } from "./api.js";
import { withHostKeys } from "./forms.js";

const serverId = decodeURIComponent(location.hash.slice(1));
const titleEl = document.getElementById("term-title");
const statusEl = document.getElementById("term-status");
const messageEl = document.getElementById("term-message");
const reconnectBtn = document.getElementById("term-reconnect");

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

// Ctrl+Shift+C copies the selection. Ctrl+Shift+V is left to the browser,
// which pastes into the terminal. Plain Ctrl+C still sends an interrupt.
term.attachCustomKeyEventHandler((e) => {
  if (e.type === "keydown" && e.ctrlKey && e.shiftKey && e.code === "KeyC") {
    const text = term.getSelection();
    if (text) navigator.clipboard?.writeText(text).catch(() => {});
    return false;
  }
  if (e.type === "keydown" && e.ctrlKey && e.shiftKey && e.code === "KeyV") return false;
  return true;
});

let ws = null;
let ended = true;
const encoder = new TextEncoder();

function setStatus(kind, text, message = "") {
  statusEl.className = "pill " + kind;
  statusEl.textContent = text;
  messageEl.textContent = message;
  messageEl.hidden = !message;
  reconnectBtn.hidden = kind !== "ended";
  requestAnimationFrame(() => fit.fit());
}

function send(data) {
  if (ws?.readyState === WebSocket.OPEN) ws.send(data);
}

term.onData((data) => send(encoder.encode(data)));
term.onBinary((data) => send(Uint8Array.from(data, (c) => c.charCodeAt(0))));
term.onResize(({ cols, rows }) => send(JSON.stringify({ type: "resize", cols, rows })));
new ResizeObserver(() => fit.fit()).observe(document.getElementById("terminal"));

async function connect() {
  if (!ended) return;
  ended = false;
  setStatus("connecting", "Connecting…");
  let opened;
  try {
    opened = await withHostKeys(() =>
      api("POST", "/terminals", { serverId, cols: term.cols, rows: term.rows }));
  } catch (err) {
    ended = true;
    const msg = err.code === "locked"
      ? "TunnelTab is locked. Unlock it in the dashboard, then reconnect."
      : err.message;
    return setStatus("ended", "Not connected", msg);
  }
  if (!opened) {
    ended = true;
    return setStatus("ended", "Not connected", "The server's fingerprint was not confirmed.");
  }
  titleEl.textContent = opened.serverName;
  document.title = `${opened.serverName} — TunnelTab`;

  let exitInfo = null;
  ws = new WebSocket(`ws://${location.host}/api/terminals/connect?ticket=${encodeURIComponent(opened.ticket)}`);
  ws.binaryType = "arraybuffer";
  ws.onopen = () => {
    setStatus("connected", "Connected");
    term.focus();
  };
  ws.onmessage = (ev) => {
    if (typeof ev.data !== "string") {
      term.write(new Uint8Array(ev.data));
      return;
    }
    try {
      const msg = JSON.parse(ev.data);
      if (msg.type === "exit") exitInfo = msg;
    } catch {
      // ignore unknown control messages
    }
  };
  ws.onclose = () => {
    ended = true;
    ws = null;
    if (exitInfo?.message) {
      setStatus("ended", "Disconnected", `Session ended: ${exitInfo.message}.`);
    } else if (exitInfo) {
      setStatus("ended", "Session ended", exitInfo.code ? `The shell exited with code ${exitInfo.code}.` : "");
    } else {
      setStatus("ended", "Disconnected", "Lost the connection to TunnelTab. Is it still running?");
    }
    term.write("\r\n\x1b[2m[session ended — press Enter or click Reconnect]\x1b[0m\r\n");
  };
}

// After the session ends, Enter reconnects.
term.onKey(({ domEvent }) => {
  if (ended && domEvent.key === "Enter") connect();
});
reconnectBtn.addEventListener("click", connect);

if (!serverId) {
  setStatus("ended", "No server", "Open terminals from the TunnelTab dashboard.");
  reconnectBtn.hidden = true;
} else {
  connect();
}

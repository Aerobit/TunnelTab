// termview.js — one terminal session shown in an element: xterm.js plus the
// WebSocket to the session in the TunnelTab program. Used by the terminal page
// (terminal.html) and by the dashboard's Terminals tab.
//
// The session lives in the program, independent of any page: a view attaches
// to it (replaying the recent output) and can be detached and re-attached.
// Locking TunnelTab detaches it and blanks it; resume() re-attaches after
// unlocking. Only one page shows a session at a time: attaching elsewhere
// (Pop out, or a second tab) moves it there, and this view offers
// "Bring back here". See docs/ARCHITECTURE.md → "Terminals".

import { Terminal } from "../vendor/xterm/xterm.mjs";
import { FitAddon } from "../vendor/xterm/addon-fit.mjs";
import { api } from "./api.js";
import { h } from "./dom.js";
import { withHostKeys } from "./forms.js";

const THEME = {
  background: "#0d1117", foreground: "#e6edf3", cursor: "#58a6ff", cursorAccent: "#0d1117",
  selectionBackground: "rgba(56, 139, 253, 0.35)",
  black: "#484f58", red: "#ff7b72", green: "#3fb950", yellow: "#d29922",
  blue: "#58a6ff", magenta: "#bc8cff", cyan: "#39c5cf", white: "#b1bac4",
  brightBlack: "#6e7681", brightRed: "#ffa198", brightGreen: "#56d364", brightYellow: "#e3b341",
  brightBlue: "#79c0ff", brightMagenta: "#d2a8ff", brightCyan: "#56d4dd", brightWhite: "#ffffff",
};

// Status pill class and label for each state.
export const TERM_STATES = {
  connecting: ["connecting", "Connecting…"],
  connected: ["connected", "Connected"],
  locked: ["paused", "Locked"],
  disconnected: ["reconnecting", "Disconnected"],
  reconnecting: ["reconnecting", "Reconnecting…"],
  elsewhere: ["paused", "In another tab"],
  ended: ["ended", "Ended"],
};

const encoder = new TextEncoder();

// Undoes what a program in the lost shell may have left switched on (full
// screen, hidden cursor, mouse reporting, colours…) without clearing the
// screen or moving the cursor. Not a soft reset (ESC [ ! p): in xterm.js that
// moves the cursor to the top. Leaving full screen (ESC [ ? 1049 l) is added
// only when in it, as it also moves the cursor back to where it was saved.
// Origin mode and the scroll region move the cursor home: saved around them.
const RESET_MODES = "\x1b[0m\x1b[4l" + "\x1b[?1l\x1b[?7h\x1b[?25h\x1b[?2004l" +
  "\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l" + "\x1b7\x1b[?6l\x1b[r\x1b8";

export class TermView {
  /**
   * serverId: the server; terminalId: an existing session to attach to (or
   * null to open a new one); client: the dashboard tab's ID, if shown there;
   * reopenIfGone: open a new session if the saved one has ended (the page);
   * onChange(view): state, title or session changed; onToast(text).
   */
  constructor({ serverId, terminalId = null, client = "", reopenIfGone = false, onChange = () => {}, onToast = () => {} }) {
    Object.assign(this, { serverId, terminalId, client, reopenIfGone, onChange, onToast });
    this.state = "connecting";
    this.message = "";
    this.serverName = "";
    this.ws = null;
    this.retryTimer = null;
    this.disposed = false;

    this.msgText = h("span", { class: "grow" });
    this.msgButton = h("button", { type: "button", class: "btn primary small" });
    this.msgButton.addEventListener("click", () => this.reconnect());
    this.msgBar = h("div", { class: "termview-msg", role: "status", hidden: true }, this.msgText, this.msgButton);
    this.screen = h("div", { class: "termview-screen" });
    this.el = h("div", { class: "termview" }, this.msgBar, this.screen);

    this.term = new Terminal({
      cursorBlink: true,
      fontFamily: 'ui-monospace, "Cascadia Mono", Consolas, "DejaVu Sans Mono", monospace',
      fontSize: 14,
      scrollback: 5000,
      theme: THEME,
    });
    this.fitAddon = new FitAddon();
    this.term.loadAddon(this.fitAddon);
    this.term.open(this.screen);
    this.setupClipboard();

    this.term.onData((data) => this.send(encoder.encode(data)));
    this.term.onBinary((data) => this.send(Uint8Array.from(data, (c) => c.charCodeAt(0))));
    this.term.onResize(({ cols, rows }) => this.send(JSON.stringify({ type: "resize", cols, rows })));
    // After the session ends, Enter starts a new one.
    this.term.onKey(({ domEvent }) => {
      if (this.state === "ended" && domEvent.key === "Enter") this.open();
    });
    this.resizer = new ResizeObserver(() => this.fit());
    this.resizer.observe(this.screen);
  }

  /** Attaches to the saved session, or opens a new one. */
  start() {
    return this.terminalId ? this.attach() : this.open();
  }

  // --- Copy and paste ---------------------------------------------------------
  // They work like Windows Terminal:
  // - Ctrl+C copies when text is selected; otherwise it stops the running
  //   command as usual. Ctrl+Shift+C and Ctrl+Insert copy too.
  // - Ctrl+V, Ctrl+Shift+V and Shift+Insert paste. The browser does the
  //   pasting (xterm.js just mustn't send the key), so reading the clipboard
  //   needs no permission.
  // - Right-click copies the selection; with nothing selected it shows the
  //   browser's menu, which has Paste.
  // preventDefault matters: without it Firefox opens its Inspector on
  // Ctrl+Shift+C.
  setupClipboard() {
    const term = this.term;
    const copySelection = () => {
      const text = term.getSelection();
      if (!text) return;
      navigator.clipboard.writeText(text).then(
        () => { term.clearSelection(); this.onToast("Copied"); },
        () => this.onToast("Couldn't copy"),
      );
    };
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
    this.screen.addEventListener("contextmenu", (e) => {
      if (!term.hasSelection()) return;
      e.preventDefault();
      copySelection();
    });
  }

  // --- State ----------------------------------------------------------------

  setState(state, message = "") {
    this.state = state;
    this.message = message;
    this.msgText.textContent = message;
    const action = { ended: "New session", disconnected: "Reconnect", elsewhere: "Bring back here" }[state];
    this.msgButton.hidden = !action;
    this.msgButton.textContent = action || "";
    this.msgBar.hidden = !message;
    this.msgBar.className = ["termview-msg", state === "ended" || state === "disconnected" ? "bad" : ""].join(" ");
    // Hide what was on screen while locked: someone at the PC shouldn't be
    // able to read it. It's replayed when the view re-attaches.
    this.screen.classList.toggle("hidden-while-locked", state === "locked");
    requestAnimationFrame(() => this.fit());
    this.onChange(this);
  }

  fit() {
    if (this.disposed || !this.screen.isConnected || this.screen.clientWidth === 0) return;
    try {
      this.fitAddon.fit();
    } catch {
      // not laid out yet
    }
  }

  focus() {
    this.term.focus();
  }

  send(data) {
    if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(data);
  }

  // --- Sessions ---------------------------------------------------------------

  /** Opens a new shell on the server. */
  async open() {
    clearTimeout(this.retryTimer);
    this.setState("connecting");
    let opened;
    try {
      opened = await withHostKeys(() => api("POST", "/terminals", {
        serverId: this.serverId, cols: this.term.cols, rows: this.term.rows, client: this.client || undefined,
      }));
    } catch (err) {
      return this.showError(err);
    }
    if (this.disposed) {
      if (opened) api("DELETE", `/terminals/${encodeURIComponent(opened.terminalId)}`).catch(() => {});
      return;
    }
    if (!opened) return this.setState("ended", "The server's fingerprint was not confirmed.");
    this.term.reset();
    this.terminalId = opened.terminalId;
    this.connect(opened);
  }

  /** Re-attaches to the existing session (after a lock, reload or blip). */
  async attach() {
    clearTimeout(this.retryTimer);
    this.setState("connecting");
    try {
      this.connect(await api("POST", `/terminals/${encodeURIComponent(this.terminalId)}/attach`));
    } catch (err) {
      if (err.status === 404) {
        // The session ended while we were away.
        if (this.reopenIfGone) return this.open();
        return this.setState("ended", "This terminal session has ended.");
      }
      this.showError(err);
    }
  }

  /** The message bar's button: new session, reconnect or bring back. */
  reconnect() {
    clearTimeout(this.retryTimer);
    if (this.state === "ended" || !this.terminalId) this.open();
    else this.attach();
  }

  /** Re-attaches after TunnelTab was unlocked. */
  resume() {
    if (this.state === "locked") this.start();
  }

  showError(err) {
    if (err.code === "locked") return this.showLocked();
    if (err.code === "offline") {
      this.setState("disconnected", "Can't reach TunnelTab. Retrying…");
      return this.retrySoon();
    }
    this.setState("ended", err.message);
  }

  showLocked() {
    this.term.reset();
    this.setState("locked", this.terminalId
      ? "TunnelTab is locked. The session keeps running; it reconnects after you unlock."
      : "TunnelTab is locked. Unlock it to connect.");
  }

  retrySoon() {
    clearTimeout(this.retryTimer);
    this.retryTimer = setTimeout(() => this.start(), 3000);
  }

  connect({ ticket, serverName }) {
    if (this.disposed) return;
    this.serverName = serverName;
    let exitInfo = null;
    let locked = false;
    const socket = new WebSocket(`ws://${location.host}/api/terminals/connect?ticket=${encodeURIComponent(ticket)}`);
    socket.binaryType = "arraybuffer";
    this.ws = socket;
    socket.onmessage = (ev) => {
      if (typeof ev.data !== "string") {
        this.term.write(new Uint8Array(ev.data));
        return;
      }
      let msg;
      try {
        msg = JSON.parse(ev.data);
      } catch {
        return;
      }
      if (msg.type === "attached") {
        this.term.reset(); // the recent output is replayed next
        this.setState("connected");
        this.send(JSON.stringify({ type: "resize", cols: this.term.cols, rows: this.term.rows }));
      } else if (msg.type === "reconnecting") {
        // The connection to the server dropped; the program keeps the
        // session and opens a new shell in it when the connection is back.
        const fullScreen = this.term.buffer.active.type === "alternate" ? "\x1b[?1049l" : "";
        this.term.write(fullScreen + RESET_MODES);
        this.setState("reconnecting", "Lost the connection to the server. Reconnecting…");
      } else if (msg.type === "reconnected") {
        this.setState("connected");
        this.send(JSON.stringify({ type: "resize", cols: this.term.cols, rows: this.term.rows }));
      } else if (msg.type === "locked") {
        locked = true;
      } else if (msg.type === "exit") {
        exitInfo = msg;
      }
    };
    socket.onclose = (ev) => {
      if (this.ws !== socket || this.disposed) return; // replaced, or gone
      this.ws = null;
      if (exitInfo) {
        const why = exitInfo.message
          ? `Session ended: ${exitInfo.message}.`
          : exitInfo.code ? `The shell exited with code ${exitInfo.code}.` : "The session ended.";
        this.setState("ended", why);
        this.term.write("\r\n\x1b[2m[session ended — press Enter or click New session]\x1b[0m\r\n");
      } else if (locked) {
        this.showLocked();
      } else if (ev.reason === "opened in another tab") {
        this.setState("elsewhere", "This terminal is open in another tab.");
      } else {
        this.setState("disconnected", "Lost the connection to TunnelTab. Retrying…");
        this.retrySoon();
      }
    };
  }

  /** Ends the session on the server and removes the view. */
  async close() {
    const id = this.terminalId;
    this.dispose();
    if (!id) return;
    try {
      await api("DELETE", `/terminals/${encodeURIComponent(id)}`);
    } catch (err) {
      if (err.status !== 404) throw err;
    }
  }

  /** Disconnects and frees the view; the session itself keeps running. */
  dispose() {
    this.disposed = true;
    clearTimeout(this.retryTimer);
    this.resizer.disconnect();
    const ws = this.ws;
    this.ws = null;
    ws?.close();
    this.term.dispose();
    this.el.remove();
  }
}

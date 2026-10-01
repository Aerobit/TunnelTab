// app.js — TunnelTab dashboard. Decides which screen to show (signed out,
// first-run setup, unlock, dashboard), renders the dashboard from /api/data,
// and keeps it live with the event stream.

import { api, ApiError, signIn, streamEvents, whenSignedOut } from "./api.js";
import { debounce, h, replace } from "./dom.js";
import { confirmDialog, field, toast } from "./dialogs.js";
import {
  projectDialog, serverDialog, serviceDialog, settingsDialog, testServer, withHostKeys,
} from "./forms.js";

const root = document.getElementById("app");

const state = {
  vault: null, // "none" | "locked" | "unlocked"
  version: "",
  minPasswordLen: 8,
  data: null, // PublicData
  forwards: new Map(), // serviceId → ForwardStatus
  servers: new Map(), // serverId → ServerStatus
  connected: true, // event stream
};

let stopEvents = null;
let currentScreen = ""; // which screen is showing, so it isn't redrawn needlessly

// --- Startup ----------------------------------------------------------------

whenSignedOut(showSignedOut);

async function start() {
  if (!(await signIn())) return showSignedOut();
  try {
    await loadState();
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) return showSignedOut();
    return showMessage("Can't reach TunnelTab", err.message);
  }
  if (!stopEvents) stopEvents = streamEvents(onEvent, onStreamStatus);
  await route();
}

async function loadState() {
  const st = await api("GET", "/state");
  state.vault = st.vault;
  state.version = st.version;
  state.minPasswordLen = st.minPasswordLen;
  return st;
}

async function route() {
  // Don't redraw a form the user may be typing into.
  if (state.vault === "none") return currentScreen === "setup" || showSetup();
  if (state.vault === "locked") return currentScreen === "unlock" || showUnlock();
  await loadData();
}

// --- Simple screens ---------------------------------------------------------

function screen(title, ...content) {
  currentScreen = "other";
  replace(root, h("section", { class: "screen" },
    h("div", { class: "brand big" }, logo(), h("span", {}, "TunnelTab")),
    title ? h("h1", {}, title) : null, ...content));
}

function logo() {
  return h("img", { src: "icon.svg", alt: "", width: 28, height: 28 });
}

function showMessage(title, message) {
  screen(title, h("p", {}, message));
}

function showSignedOut() {
  stopEvents?.();
  stopEvents = null;
  screen("Not signed in",
    h("p", {}, "Start TunnelTab again to open the dashboard."),
    h("p", { class: "hint" }, "For your security, each dashboard link works only once and only for a couple of minutes."));
}

function showStopped() {
  stopEvents?.();
  stopEvents = null;
  screen("TunnelTab has stopped", h("p", {}, "All tunnels are closed. You can close this tab."));
}

function showUpdating(version) {
  stopEvents?.();
  stopEvents = null;
  closeDialogs();
  screen("Updating TunnelTab",
    h("p", {}, `TunnelTab ${version} is starting and opens in a new tab. You can close this tab.`),
    h("p", { class: "hint" }, "If it doesn't open within a minute, start TunnelTab again. If the new version can't start, the previous one comes back by itself."));
}

/** Rough password strength: 0 (weak) to 4 (strong). */
function strength(pw) {
  let score = 0;
  if (pw.length >= 8) score++;
  if (pw.length >= 12) score++;
  if (pw.length >= 16) score++;
  const classes = [/[a-z]/, /[A-Z]/, /[0-9]/, /[^A-Za-z0-9]/].filter((r) => r.test(pw)).length;
  if (classes >= 3 || (pw.length >= 20 && pw.includes(" "))) score++;
  return Math.min(score, 4);
}

function showSetup() {
  const pw = h("input", { type: "password", autocomplete: "new-password" });
  const pw2 = h("input", { type: "password", autocomplete: "new-password" });
  const meter = h("meter", { min: 0, max: 4, low: 2, high: 3, optimum: 4, value: 0 });
  const meterText = h("small", { class: "hint" }, " ");
  const error = h("p", { class: "form-error", role: "alert" });
  const button = h("button", { type: "submit", class: "btn primary" }, "Create vault");
  pw.addEventListener("input", () => {
    const s = strength(pw.value);
    meter.value = s;
    meterText.textContent = pw.value ? ["Very weak", "Weak", "Fair", "Good", "Strong"][s] : " ";
  });
  const form = h("form", { class: "card narrow" },
    h("p", {}, "Choose a master password. It encrypts everything TunnelTab stores: servers, keys and passwords."),
    field("Master password", pw),
    h("div", { class: "meter" }, meter, meterText),
    field("Repeat it", pw2),
    h("p", { class: "warning" }, "There is no way to recover a forgotten master password. A long passphrase of several words is easy to remember and hard to guess."),
    error, button);
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    error.textContent = "";
    if (pw.value.length < state.minPasswordLen) return (error.textContent = `Use at least ${state.minPasswordLen} characters.`);
    if (pw.value !== pw2.value) return (error.textContent = "The passwords don't match.");
    button.disabled = true;
    button.textContent = "Creating…";
    try {
      await api("POST", "/vault/create", { password: pw.value });
      state.vault = "unlocked";
      await route();
    } catch (err) {
      error.textContent = err.message;
      button.disabled = false;
      button.textContent = "Create vault";
    }
  });
  screen("Welcome", form);
  currentScreen = "setup";
  pw.focus();
}

function showUnlock() {
  const pw = h("input", { type: "password", autocomplete: "current-password" });
  const error = h("p", { class: "form-error", role: "alert" });
  const button = h("button", { type: "submit", class: "btn primary" }, "Unlock");
  let timer = null;

  function waitFor(ms) {
    clearInterval(timer);
    const until = Date.now() + ms;
    button.disabled = true;
    const tick = () => {
      const left = Math.ceil((until - Date.now()) / 1000);
      if (left <= 0) {
        clearInterval(timer);
        button.disabled = false;
        button.textContent = "Unlock";
      } else {
        button.textContent = `Wait ${left}s`;
      }
    };
    tick();
    timer = setInterval(tick, 250);
  }

  const form = h("form", { class: "card narrow" }, field("Master password", pw), error, button);
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    error.textContent = "";
    button.disabled = true;
    button.textContent = "Unlocking…";
    try {
      await api("POST", "/vault/unlock", { password: pw.value });
      clearInterval(timer);
      state.vault = "unlocked";
      await route();
    } catch (err) {
      pw.select();
      error.textContent = err.code === "too_many_attempts" ? "Too many wrong passwords. Please wait." : err.message;
      button.textContent = "Unlock";
      button.disabled = false;
      if (err.body?.retryAfterMs > 0) waitFor(err.body.retryAfterMs);
    }
  });
  screen("Unlock TunnelTab", form,
    h("p", { class: "hint center" }, "Tunnels that were running keep their ports; they reconnect once unlocked."));
  currentScreen = "unlock";
  pw.focus();
}

// --- Data and events --------------------------------------------------------

async function loadData() {
  try {
    const res = await api("GET", "/data");
    state.data = res.data;
    state.forwards = new Map(res.forwards.map((f) => [f.serviceId, f]));
    state.servers = new Map(res.servers.map((s) => [s.id, s]));
    renderDashboard();
  } catch (err) {
    if (err.code === "locked") {
      state.vault = "locked";
      return showUnlock();
    }
    if (err.code === "no_vault") {
      state.vault = "none";
      return showSetup();
    }
    toast(err.message, "error");
  }
}

const reloadSoon = debounce(() => state.vault === "unlocked" && loadData(), 150);
const renderSoon = debounce(() => state.vault === "unlocked" && state.data && renderDashboard(), 50);

function onEvent(ev) {
  switch (ev.type) {
    case "resync":
    case "data":
      if (ev.type === "resync") loadState().then(route).catch(() => {});
      else reloadSoon();
      break;
    case "vault":
      if (ev.state === "locked" && state.vault !== "locked") {
        state.vault = "locked";
        closeDialogs();
        showUnlock();
      } else if (ev.state === "unlocked" && state.vault !== "unlocked") {
        state.vault = "unlocked";
        loadData();
      }
      break;
    case "tunnel":
      if (ev.kind === "forward") {
        if (ev.state === "stopped" || ev.state === "failed") {
          state.forwards.delete(ev.id);
          if (ev.state === "failed" && ev.error) toast(`${serviceLabel(ev.id)}: ${ev.error}`, "error");
        } else {
          const prev = state.forwards.get(ev.id) || {};
          state.forwards.set(ev.id, { ...prev, serviceId: ev.id, serverId: ev.serverId, state: ev.state,
            error: ev.error, localPort: ev.localPort || prev.localPort });
        }
      } else if (ev.kind === "server") {
        if (ev.state === "stopped") state.servers.delete(ev.id);
        else state.servers.set(ev.id, { id: ev.id, state: ev.state, error: ev.error });
      }
      renderSoon();
      break;
  }
}

function onStreamStatus(connected) {
  state.connected = connected;
  document.getElementById("conn")?.classList.toggle("offline", !connected);
  const banner = document.getElementById("offline-banner");
  if (banner) banner.hidden = connected;
}

function closeDialogs() {
  document.querySelectorAll("dialog[open]").forEach((d) => d.close());
}

function serviceLabel(id) {
  return state.data?.services.find((s) => s.id === id)?.label || "Tunnel";
}

// --- Dashboard --------------------------------------------------------------

const byOrder = (a, b) => a.order - b.order || a.name?.localeCompare?.(b.name) || 0;

function renderDashboard() {
  const d = state.data;
  const scroll = window.scrollY;
  const header = h("header", { class: "topbar" },
    h("div", { class: "brand" }, logo(), h("span", {}, "TunnelTab")),
    h("span", { id: "conn", class: ["conn", !state.connected && "offline"], title: "Connection to the TunnelTab program" }),
    h("div", { class: "spacer" }),
    h("button", { class: "btn primary", onclick: () => projectDialog() }, "+ Project"),
    h("button", { class: "btn secondary", onclick: openSettings }, "Settings"),
    h("button", { class: "btn secondary", onclick: lock, title: "Lock now" }, "Lock"),
    h("button", { class: "btn secondary", onclick: quit, title: "Close all tunnels and stop TunnelTab" }, "Quit"));

  const banner = h("div", { id: "offline-banner", class: "banner", hidden: state.connected },
    "Lost connection to the TunnelTab program. Retrying… If you quit it, start it again.");

  const projects = [...d.projects].sort(byOrder);
  const main = projects.length === 0
    ? h("div", { class: "empty card" },
      h("h2", {}, "Add your first project"),
      h("p", {}, "Projects group your servers. Inside each server you add the web apps (services) you want to open, like n8n or Portainer."),
      h("button", { class: "btn primary", onclick: () => projectDialog() }, "+ New project"))
    : h("div", { class: "projects" }, projects.map(renderProject));

  currentScreen = "dashboard";
  replace(root, header, banner, h("main", { class: "dashboard" }, main),
    h("footer", { class: "footer" }, `TunnelTab ${state.version}`));
  window.scrollTo(0, scroll);
  if (focusGripAfterRender) {
    root.querySelector(`[data-grip="${CSS.escape(focusGripAfterRender)}"]`)?.focus();
    focusGripAfterRender = null;
  }
}

// --- Reordering (drag the ⠿ grip, or focus it and press ↑/↓) ---------------

const DRAG_PROJECT = "application/x-tunneltab-project";
const DRAG_SERVER = "application/x-tunneltab-server";
// Services can only be reordered within their own server, so the server ID
// is part of the type (the only thing readable while dragging).
const dragService = (serverId) => `application/x-tunneltab-service-${serverId}`;

let focusGripAfterRender = null;

const idsInOrder = (items) => [...items].sort(byOrder).map((x) => x.id);

/** Returns list with id moved to just before/after target (or to the end). */
function placed(list, id, target, after) {
  const out = list.filter((x) => x !== id);
  let i = target ? out.indexOf(target) : -1;
  i = i < 0 ? out.length : i + (after ? 1 : 0);
  out.splice(i, 0, id);
  return out;
}

/** Moves id one place up (-1) or down (+1) in list. */
function nudged(list, id, delta) {
  const i = list.indexOf(id);
  const j = i + delta;
  if (i < 0 || j < 0 || j >= list.length) return null;
  const out = [...list];
  [out[i], out[j]] = [out[j], out[i]];
  return out;
}

async function saveOrder(path, ids, focusId) {
  focusGripAfterRender = focusId || null;
  try {
    await api("PUT", path, { ids }); // the "data" event re-renders
  } catch (err) {
    toast(err.message, "error");
  }
}

function grip(label, row, type, id, onKeyMove) {
  const g = h("button", {
    type: "button", class: "grip", draggable: "true", dataset: { grip: id },
    title: "Drag to reorder (or focus and press ↑/↓)", "aria-label": label,
  }, "⠿");
  g.addEventListener("dragstart", (e) => {
    e.stopPropagation();
    e.dataTransfer.setData(type, id);
    e.dataTransfer.effectAllowed = "move";
    e.dataTransfer.setDragImage(row, 20, 20);
    row.classList.add("dragging");
  });
  g.addEventListener("dragend", () => row.classList.remove("dragging"));
  g.addEventListener("keydown", (e) => {
    if (e.key !== "ArrowUp" && e.key !== "ArrowDown") return;
    e.preventDefault();
    onKeyMove(e.key === "ArrowUp" ? -1 : 1);
  });
  return g;
}

/** Makes el a drop target for type; onDrop(draggedId, after) on drop. */
function dropTarget(el, type, onDrop) {
  const position = (e) => {
    const r = el.getBoundingClientRect();
    return e.clientY > r.top + r.height / 2;
  };
  const clear = () => el.classList.remove("drop-before", "drop-after");
  el.addEventListener("dragover", (e) => {
    if (!e.dataTransfer.types.includes(type)) return;
    e.preventDefault();
    e.stopPropagation();
    const after = position(e);
    el.classList.toggle("drop-before", !after);
    el.classList.toggle("drop-after", after);
  });
  el.addEventListener("dragleave", (e) => {
    if (!el.contains(e.relatedTarget)) clear();
  });
  el.addEventListener("drop", (e) => {
    if (!e.dataTransfer.types.includes(type)) return;
    e.preventDefault();
    e.stopPropagation();
    const after = position(e);
    clear();
    const id = e.dataTransfer.getData(type);
    if (id) onDrop(id, after);
  });
}

function renderProject(p) {
  const servers = state.data.servers.filter((s) => s.projectId === p.id).sort(byOrder);
  const serverIds = servers.map((s) => s.id);
  const card = h("section", { class: "project card", dataset: { projectId: p.id } });
  replace(card,
    h("div", { class: "project-head" },
      h("div", { class: "title-row" },
        grip(`Move project ${p.name}`, card, DRAG_PROJECT, p.id, (delta) => {
          const ids = nudged(idsInOrder(state.data.projects), p.id, delta);
          if (ids) saveOrder("/projects/order", ids, p.id);
        }),
        h("div", {},
          h("h2", {}, p.name),
          p.description ? h("p", { class: "desc" }, p.description) : null)),
      h("div", { class: "actions" },
        h("button", { class: "chip", onclick: () => projectDialog(p) }, "Edit"),
        h("button", { class: "chip add", onclick: () => serverDialog(p.id) }, "+ Server"))),
    servers.length ? servers.map(renderServer) : h("p", { class: "hint" }, "No servers yet. Add one with “+ Server”."));

  // Reorder projects by dropping one onto another.
  dropTarget(card, DRAG_PROJECT, (id, after) => {
    if (id !== p.id) saveOrder("/projects/order", placed(idsInOrder(state.data.projects), id, p.id, after), id);
  });
  // Dropping a server on the project (not on one of its servers) puts it last.
  card.addEventListener("dragover", (e) => {
    if (!e.dataTransfer.types.includes(DRAG_SERVER)) return;
    e.preventDefault();
    card.classList.add("drag-over");
  });
  card.addEventListener("dragleave", (e) => {
    if (!card.contains(e.relatedTarget)) card.classList.remove("drag-over");
  });
  card.addEventListener("drop", (e) => {
    card.classList.remove("drag-over");
    const id = e.dataTransfer.getData(DRAG_SERVER);
    if (!id) return;
    e.preventDefault();
    saveOrder(`/projects/${p.id}/servers/order`, placed(serverIds, id, null, true), id);
  });
  return card;
}

const STATE_TEXT = {
  connecting: "Connecting…", connected: "Connected", active: "Running", reconnecting: "Reconnecting…",
  paused: "Waiting for unlock", failed: "Failed", stopped: "Stopped",
};

const AUTH_TEXT = { agent: "SSH agent", keyVault: "Stored key", keyFile: "Key file", password: "Password" };

function renderServer(s) {
  const status = state.servers.get(s.id);
  const services = state.data.services.filter((x) => x.serverId === s.id).sort(byOrder);
  const siblings = () => idsInOrder(state.data.servers.filter((x) => x.projectId === s.projectId));
  const el = h("div", { class: "server", dataset: { serverId: s.id } });
  replace(el,
    h("div", { class: "server-head" },
      grip(`Move server ${s.name}`, el, DRAG_SERVER, s.id, (delta) => {
        const ids = nudged(siblings(), s.id, delta);
        if (ids) saveOrder(`/projects/${s.projectId}/servers/order`, ids, s.id);
      }),
      h("span", { class: ["dot", status?.state || "idle"], title: status ? STATE_TEXT[status.state] : "Not connected" }),
      h("div", { class: "server-info" },
        h("strong", {}, s.name),
        h("span", { class: "mono muted" }, `${s.username}@${s.host}${s.port !== 22 ? ":" + s.port : ""}`),
        h("span", { class: "badge" }, AUTH_TEXT[s.auth.type] || s.auth.type)),
      h("div", { class: "actions" },
        h("button", { class: "chip open", onclick: () => openTerminal(s), title: "Open an SSH terminal in a new tab" }, "Terminal ↗"),
        h("button", { class: "chip", onclick: () => testServer(s), title: "Check the connection and login" }, "Test"),
        h("button", { class: "chip", onclick: () => serverDialog(s.projectId, s) }, "Edit"),
        h("button", { class: "chip add", onclick: () => serviceDialog(s.id) }, "+ Service"))),
    status?.error ? h("p", { class: "error-line" }, status.error) : null,
    services.length
      ? h("ul", { class: "services" }, services.map(renderService))
      : h("p", { class: "hint" }, "No services. Add a web app running on this server with “+ Service”."));

  // Dropping a server onto this one places it before/after (moving it into
  // this project if it came from another).
  dropTarget(el, DRAG_SERVER, (id, after) => {
    if (id !== s.id) saveOrder(`/projects/${s.projectId}/servers/order`, placed(siblings(), id, s.id, after), id);
  });
  return el;
}

function serviceURL(svc, fwd) {
  return `${svc.protocol}://127.0.0.1:${fwd.localPort}${svc.path || ""}`;
}

function renderService(svc) {
  const fwd = state.forwards.get(svc.id);
  const running = Boolean(fwd);
  const usable = fwd?.state === "active";
  const localText = fwd ? `localhost:${fwd.localPort}` : svc.localPort ? `localhost:${svc.localPort}` : "auto port";
  const remote = `${svc.remoteHost === "127.0.0.1" ? "" : svc.remoteHost + ":"}${svc.remotePort}`;
  const siblings = () => idsInOrder(state.data.services.filter((x) => x.serverId === svc.serverId));
  const orderPath = `/servers/${svc.serverId}/services/order`;

  const toggle = h("button", { class: ["chip", running ? "stop" : "start"] }, running ? "Stop" : "Start");
  toggle.addEventListener("click", () => (running ? stopService(svc) : startService(svc, false, toggle)));
  const open = h("button", { class: "chip open", title: "Start the tunnel if needed and open it in a new tab" }, "Open ↗");
  open.addEventListener("click", () => openService(svc, open));

  const row = h("li", { class: ["service", running && "running", fwd?.state], dataset: { serviceId: svc.id } });
  replace(row,
    h("div", { class: "service-info" },
      grip(`Move service ${svc.label}`, row, dragService(svc.serverId), svc.id, (delta) => {
        const ids = nudged(siblings(), svc.id, delta);
        if (ids) saveOrder(orderPath, ids, svc.id);
      }),
      h("strong", {}, svc.label),
      h("span", { class: "mono muted" },
        usable ? h("a", { href: serviceURL(svc, fwd), target: "_blank", rel: "noopener noreferrer" }, localText) : localText,
        ` → ${remote}${svc.path || ""}`),
      fwd ? h("span", { class: ["pill", fwd.state] }, STATE_TEXT[fwd.state] || fwd.state) : null,
      svc.autoStart ? h("span", { class: "badge", title: "Starts automatically after unlocking" }, "auto") : null),
    h("div", { class: "actions" },
      toggle, open,
      h("button", { class: "chip", onclick: () => serviceDialog(svc.serverId, svc) }, "Edit")));
  dropTarget(row, dragService(svc.serverId), (id, after) => {
    if (id !== svc.id) saveOrder(orderPath, placed(siblings(), id, svc.id, after), id);
  });
  return row;
}

// --- Actions ----------------------------------------------------------------

async function startService(svc, quiet, button) {
  if (button) button.disabled = true;
  try {
    const res = await withHostKeys(() => api("POST", `/services/${svc.id}/start`));
    if (res) {
      state.forwards.set(svc.id, res.forward);
      renderSoon();
    }
    return res;
  } catch (err) {
    if (!quiet) toast(`${svc.label}: ${err.message}`, "error");
    return null;
  } finally {
    if (button) button.disabled = false;
  }
}

async function stopService(svc) {
  try {
    await api("POST", `/services/${svc.id}/stop`);
    state.forwards.delete(svc.id);
    renderSoon();
  } catch (err) {
    toast(err.message, "error");
  }
}

/** Starts the tunnel if needed, then opens the web UI in a new tab. */
async function openService(svc, button) {
  const fwd = state.forwards.get(svc.id);
  if (fwd?.state === "active") {
    window.open(serviceURL(svc, fwd), "_blank", "noopener,noreferrer");
    return;
  }
  button.disabled = true;
  button.textContent = "Starting…";
  const res = await startService(svc, false, null);
  button.disabled = false;
  button.textContent = "Open ↗";
  if (!res) return;
  // Opening right after the click normally works. If the browser blocked
  // the pop-up (because connecting took a while), offer a link instead.
  // (Not using "noopener" here so a blocked pop-up can be detected; the
  // opener link is cut immediately, and COOP isolates the page anyway.)
  const win = window.open(res.url, "_blank");
  if (win) {
    win.opener = null;
    return;
  }
  const link = h("a", { href: res.url, target: "_blank", rel: "noopener noreferrer" }, " Open it now");
  toast(`${svc.label} is ready.`, "success", link);
}

function openTerminal(server) {
  // Same-origin page; it signs in with the session this tab already has.
  window.open(`/terminal.html#${encodeURIComponent(server.id)}`, "_blank", "noopener");
}

async function openSettings() {
  try {
    await settingsDialog({
      knownHosts: state.data?.knownHosts || [], minPasswordLen: state.minPasswordLen, version: state.version,
      runningTunnels: state.forwards.size, onRestarting: showUpdating,
    });
  } catch (err) {
    toast(err.message, "error");
  }
}

async function lock() {
  try {
    await api("POST", "/vault/lock");
    state.vault = "locked";
    closeDialogs();
    showUnlock();
  } catch (err) {
    toast(err.message, "error");
  }
}

async function quit() {
  const running = state.forwards.size;
  const msg = running
    ? `Stop TunnelTab? ${running} running tunnel${running === 1 ? "" : "s"} will close.`
    : "Stop TunnelTab?";
  if (!(await confirmDialog("Quit TunnelTab", msg, { confirmLabel: "Quit", danger: running > 0 }))) return;
  try {
    await api("POST", "/quit");
  } catch {
    // It may have stopped before answering.
  }
  showStopped();
}

start();

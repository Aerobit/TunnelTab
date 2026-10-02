// app.js — TunnelTab dashboard. Decides which screen to show (signed out,
// first-run setup, unlock, dashboard), renders the dashboard from /api/data,
// and keeps it live with the event stream.

import { api, ApiError, currentSession, probe, signIn, streamEvents, whenSignedOut } from "./api.js";
import { debounce, h, replace } from "./dom.js";
import { confirmDialog, field, toast } from "./dialogs.js";
import {
  projectDialog, serverDialog, serviceDialog, settingsDialog, testServer, withHostKeys,
} from "./forms.js";
import { findServices } from "./discover.js";
import { fmtBytes } from "./format.js";
import { TERM_STATES, TermView } from "./termview.js";

const root = document.getElementById("app");

const state = {
  vault: null, // "none" | "locked" | "unlocked"
  version: "",
  minPasswordLen: 8,
  data: null, // PublicData
  forwards: new Map(), // serviceId → ForwardStatus
  servers: new Map(), // serverId → ServerStatus
  terminals: new Map(), // terminalId → {id, serverId, openedAt}
  activity: [], // recent events, oldest first (kept in memory by the program)
  sidebarOpen: false, // narrow windows: the sidebar is shown over the page
  connected: true, // event stream
  updateAvailable: null, // newer version found by the last "Check for updates" (never checked automatically)
  traffic: new Map(), // serviceId → {todayIn, todayOut, lastHour[60]} (counted by the program, in memory)
  health: new Map(), // serverId → {health?, error?, at} for servers with health switched on
  checks: new Map(), // serviceId → {state, protocol?, status?, at}: does the app answer? (last check)
};

let stopEvents = null;
let currentScreen = ""; // which screen is showing, so it isn't redrawn needlessly

// --- Startup ----------------------------------------------------------------

whenSignedOut(showSignedOut);

// Auto-lock counts requests to the program as activity, but moving between
// pages in the dashboard makes none. So clicks, keys and scrolling in the
// dashboard are reported, at most every 30 s. (Typing in a terminal is
// counted by the program itself.)
let lastTouch = 0;
function noteActivity() {
  if (state.vault !== "unlocked" || currentScreen !== "dashboard" || Date.now() - lastTouch < 30000) return;
  lastTouch = Date.now();
  api("POST", "/touch").catch(() => {});
}
for (const type of ["pointerdown", "keydown", "wheel"]) {
  document.addEventListener(type, noteActivity, { capture: true, passive: true });
}

async function start() {
  if (!(await signIn())) return showSignedOut();
  try {
    await loadState();
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) return showSignedOut();
    return showMessage("Can't reach TunnelTab", err.message);
  }
  if (!stopEvents) stopEvents = streamEvents(onEvent, onStreamStatus, `?client=${CLIENT_ID}`);
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

/** A screen with just a message: the text sits centred in a card. */
function messageScreen(title, ...paragraphs) {
  screen(title, h("div", { class: "card message" }, ...paragraphs));
}

function logo() {
  return h("img", { src: "logo.svg", alt: "", width: 28, height: 28 });
}

function showMessage(title, message) {
  messageScreen(title, h("p", {}, message));
}

function showSignedOut() {
  stopEvents?.();
  stopEvents = null;
  messageScreen("Not signed in",
    h("p", {}, "Start TunnelTab again to open the dashboard."),
    h("p", { class: "hint" }, "For your security, each dashboard link works only once and only for a couple of minutes."));
}

function showStopped() {
  stopEvents?.();
  stopEvents = null;
  dropViews();
  messageScreen("TunnelTab has stopped", h("p", {}, "All tunnels are closed. You can close this tab."));
}

function showUpdating(version) {
  stopEvents?.();
  stopEvents = null;
  dropViews();
  closeDialogs();
  const status = h("p", { class: "update-step", role: "status" },
    h("progress", { class: "update-progress", "aria-label": "Restarting" }), `Starting TunnelTab ${version}…`);
  const hint = h("p", { class: "hint" }, "It opens in a new tab. If the new version can't start, the previous one comes back by itself.");
  messageScreen("Updating TunnelTab", status, hint);
  waitForRestart(version, currentSession(), status).then(() => hint.remove());
}

// After "Update now" this program stops and starts the new version, which
// opens in a new tab (or, if it can't start, the previous version comes
// back, after up to 30 s). The restarted program doesn't know this tab's
// session, so it refuses it: that's how this tab sees the restart. Once the
// new tab has signed in (its session replaces ours in localStorage), the
// new session says which version runs.
async function waitForRestart(version, oldSession, status) {
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  const started = Date.now();
  let restarted = false;
  while (Date.now() - started < 120_000) {
    await sleep(500);
    if (!restarted) {
      const p = await probe(oldSession);
      if (p !== "unknown") continue; // not answering, or still the old program
      restarted = true;
      status.replaceChildren(`TunnelTab has restarted. Waiting for it to open in a new tab…`);
    }
    const s = currentSession();
    if (!s || s === oldSession) continue;
    const p = await probe(s);
    if (typeof p !== "object") continue;
    status.replaceChildren(p.version === version
      ? `TunnelTab ${version} is running and opened in a new tab. You can close this tab.`
      : `TunnelTab ${version} couldn't start, so the previous version (${p.version}) came back. It opened in a new tab. Your data is unchanged.`);
    status.classList.add(p.version === version ? "ok" : "bad");
    return;
  }
  status.replaceChildren(restarted
    ? "TunnelTab has restarted, but no new tab signed in. Open TunnelTab from the link it shows, or start it again."
    : "TunnelTab hasn't come back. Start it again; if the new version couldn't start, the previous one has been restored. Your data is unchanged.");
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
    state.terminals = new Map((res.terminals || []).map((t) => [t.id, t]));
    state.activity = res.activity || [];
    state.health = new Map(Object.entries(res.health || {}));
    state.checks = new Map(Object.entries(res.checks || {}));
    await loadTraffic();
    syncViews(state.terminals.values());
    renderDashboard();
    allViews().forEach((v) => v.resume()); // after an unlock
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
const renderSoon = debounce(() => state.vault === "unlocked" && state.data && currentScreen === "dashboard" && renderDashboard(), 50);

function onEvent(ev) {
  switch (ev.type) {
    case "update":
      // "Update now" progress, for the Settings dialog that started it.
      window.dispatchEvent(new CustomEvent("tunneltab-update", { detail: ev }));
      break;
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
    case "check":
      if (ev.check) state.checks.set(ev.serviceId, ev.check);
      else state.checks.delete(ev.serviceId);
      renderSoon();
      break;
    case "health":
      if (ev.reading) state.health.set(ev.serverId, ev.reading);
      else state.health.delete(ev.serverId);
      renderSoon();
      break;
    case "activity":
      state.activity.push(ev);
      if (state.activity.length > 200) state.activity.shift();
      if (ev.kind === "terminal") {
        if (ev.state === "opened") {
          const view = allViews().find((v) => v.terminalId === ev.id);
          state.terminals.set(ev.id, { id: ev.id, serverId: ev.serverId, openedAt: ev.at, client: view ? CLIENT_ID : undefined });
        } else {
          state.terminals.delete(ev.id);
          // A popped-out terminal that ended: say so here too.
          const view = allViews().find((v) => v.terminalId === ev.id);
          if (view?.state === "elsewhere") view.setState("ended", "The session ended.");
        }
      }
      renderSoon();
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
        else state.servers.set(ev.id, { id: ev.id, state: ev.state, error: ev.error, since: ev.since, reconnects: ev.reconnects, pingMs: ev.pingMs, reason: ev.reason, held: ev.held });
      }
      renderSoon();
      break;
  }
}

function onStreamStatus(connected) {
  state.connected = connected;
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

// The dashboard has a sidebar (projects and servers) and one page at a time,
// chosen by the address: #/ is the Overview, #/server/<id>/<tab> a server.
const SERVER_TABS = ["overview", "services", "terminals", "activity", "notes"];

function currentRoute() {
  const m = location.hash.match(/^#\/server\/([^/]+)(?:\/([a-z]+))?$/);
  if (m) return { page: "server", id: decodeURIComponent(m[1]), tab: SERVER_TABS.includes(m[2]) ? m[2] : "overview" };
  return { page: "overview" };
}

const serverHref = (id, tab = "overview") => `#/server/${encodeURIComponent(id)}${tab === "overview" ? "" : "/" + tab}`;

let lastRouteKey = "";

window.addEventListener("hashchange", () => {
  state.sidebarOpen = false;
  if (currentScreen === "dashboard") renderDashboard();
});

function renderDashboard() {
  const d = state.data;
  let r = currentRoute();
  const server = r.page === "server" ? d.servers.find((s) => s.id === r.id) : null;
  if (r.page === "server" && !server) {
    // The server was deleted (or the link is old): go to the Overview.
    history.replaceState(null, "", "#/");
    r = { page: "overview" };
  }
  const routeKey = server ? `${server.id}/${r.tab}` : "overview";
  const sameRoute = routeKey === lastRouteKey;
  const scroll = sameRoute ? window.scrollY : 0;

  const banner = h("div", { id: "offline-banner", class: "banner", hidden: state.connected },
    "Lost connection to the TunnelTab program. Retrying… If you quit it, start it again.");
  const mobileBar = h("div", { class: "mobile-bar" },
    h("button", {
      type: "button", class: "btn secondary small", "aria-expanded": String(Boolean(state.sidebarOpen)),
      "aria-controls": "sidebar", onclick: () => { state.sidebarOpen = !state.sidebarOpen; renderDashboard(); },
    }, "☰ Menu"),
    h("div", { class: "brand" }, logo(), h("span", {}, "TunnelTab")));

  currentScreen = "dashboard";
  // A terminal being typed into keeps the keyboard across a redraw.
  const typingIn = allViews().find((v) => v.el.contains(document.activeElement));
  const editing = [...noteEditors.values()].find((e) => e.textarea === document.activeElement)?.textarea;
  const caret = editing ? [editing.selectionStart, editing.selectionEnd] : null;
  replace(root, h("div", { class: ["shell", state.sidebarOpen && "nav-open"] },
    renderSidebar(r),
    h("div", { class: "content" },
      mobileBar, banner,
      h("main", { class: "page", id: "main" }, server ? renderServerPage(server, r.tab) : renderOverview()),
      h("footer", { class: "footer" }, `TunnelTab ${state.version}`))));

  window.scrollTo(0, scroll);
  if (typingIn?.el.isConnected) {
    typingIn.focus();
  } else if (editing?.isConnected) {
    editing.focus();
    editing.setSelectionRange(...caret);
  } else if (focusGripAfterRender) {
    root.querySelector(`[data-grip="${CSS.escape(focusGripAfterRender)}"]`)?.focus();
    focusGripAfterRender = null;
  } else if (!sameRoute && lastRouteKey) {
    root.querySelector("main h1")?.focus(); // so screen readers announce the new page
  }
  lastRouteKey = routeKey;
}

// Durations ("connected 2h 14m") are refreshed now and then, unless the
// user is working in the page (a redraw would move the keyboard focus).
setInterval(async () => {
  if (currentScreen !== "dashboard") return;
  await loadTraffic();
  const busy = document.activeElement && document.activeElement !== document.body && root.contains(document.activeElement);
  if (!busy && !document.querySelector("dialog[open]")) renderSoon();
}, 30000);

/** Fetches the traffic figures (bytes through each tunnel). */
async function loadTraffic() {
  try {
    const res = await api("GET", "/traffic");
    state.traffic = new Map(res.services.map((t) => [t.serviceId, t]));
  } catch {
    // shown as unknown until the next try
  }
}

// --- Sidebar ----------------------------------------------------------------

function renderSidebar(r) {
  const projects = [...state.data.projects].sort(byOrder);
  return h("aside", { class: "sidebar", id: "sidebar", "aria-label": "Projects and servers" },
    h("div", { class: "side-brand" },
      logo(), h("span", {}, "TunnelTab")),
    h("nav", { class: "side-nav", "aria-label": "Pages" },
      h("a", { class: "side-link", href: "#/", "aria-current": r.page === "overview" ? "page" : null }, overviewIcon(), "Overview"),
      projects.map((p) => renderSideProject(p, r)),
      projects.length === 0 ? h("p", { class: "hint side-empty" }, "No projects yet.") : null),
    h("div", { class: "side-foot" },
      h("button", { type: "button", class: "btn primary small", onclick: () => projectDialog() }, "+ Project"),
      h("div", { class: "side-actions" },
        h("button", {
          type: "button", class: "btn secondary small", onclick: openSettings,
          title: state.updateAvailable ? `TunnelTab ${state.updateAvailable} is available` : null,
        }, "Settings", state.updateAvailable ? [h("span", { class: "update-dot" }), h("span", { class: "sr-only" }, " (update available)")] : null),
        h("button", { type: "button", class: "btn secondary small", onclick: lock, title: "Lock now" }, "Lock"),
        h("button", { type: "button", class: "btn secondary small", onclick: quit, title: "Close all tunnels and stop TunnelTab" }, "Quit"))));
}

function overviewIcon() {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 16 16");
  svg.setAttribute("width", "14");
  svg.setAttribute("height", "14");
  svg.setAttribute("aria-hidden", "true");
  for (const [x, y] of [[1.5, 1.5], [9.5, 1.5], [1.5, 9.5], [9.5, 9.5]]) {
    const rect = document.createElementNS("http://www.w3.org/2000/svg", "rect");
    for (const [k, v] of Object.entries({ x, y, width: 5, height: 5, rx: 1, fill: "none", stroke: "currentColor", "stroke-width": 1.5 })) {
      rect.setAttribute(k, String(v));
    }
    svg.appendChild(rect);
  }
  return svg;
}

function renderSideProject(p, r) {
  const servers = state.data.servers.filter((s) => s.projectId === p.id).sort(byOrder);
  const serverIds = servers.map((s) => s.id);
  const group = h("section", { class: "side-project", dataset: { projectId: p.id } });
  const head = h("div", { class: "side-project-head" },
    grip(`Move project ${p.name}`, group, DRAG_PROJECT, p.id, (delta) => {
      const ids = nudged(idsInOrder(state.data.projects), p.id, delta);
      if (ids) saveOrder("/projects/order", ids, p.id);
    }),
    h("h2", { class: "side-project-name", title: p.description || null }, p.name),
    moreMenu(`More actions for project ${p.name}`, [
      ["Add server", () => serverDialog(p.id)],
      ["Edit project", () => projectDialog(p)],
    ]));
  replace(group, head,
    servers.length
      ? h("ul", { class: "side-servers" }, servers.map((s) => renderSideServer(s, r)))
      : h("button", { type: "button", class: "side-add", onclick: () => serverDialog(p.id) }, "+ Add a server"));

  // Reorder projects by dropping one onto another.
  dropTarget(group, DRAG_PROJECT, (id, after) => {
    if (id !== p.id) saveOrder("/projects/order", placed(idsInOrder(state.data.projects), id, p.id, after), id);
  });
  // Dropping a server on the project's name puts it last in this project.
  head.addEventListener("dragover", (e) => {
    if (!e.dataTransfer.types.includes(DRAG_SERVER)) return;
    e.preventDefault();
    e.stopPropagation();
    group.classList.add("drag-over");
  });
  head.addEventListener("dragleave", (e) => {
    if (!head.contains(e.relatedTarget)) group.classList.remove("drag-over");
  });
  head.addEventListener("drop", (e) => {
    group.classList.remove("drag-over");
    const id = e.dataTransfer.getData(DRAG_SERVER);
    if (!id) return;
    e.preventDefault();
    e.stopPropagation();
    saveOrder(`/projects/${p.id}/servers/order`, placed(serverIds, id, null, true), id);
  });
  return group;
}

function renderSideServer(s, r) {
  const status = state.servers.get(s.id);
  const siblings = () => idsInOrder(state.data.servers.filter((x) => x.projectId === s.projectId));
  const current = r.page === "server" && r.id === s.id;
  const li = h("li", { class: "side-server", dataset: { serverId: s.id } });
  replace(li,
    grip(`Move server ${s.name}`, li, DRAG_SERVER, s.id, (delta) => {
      const ids = nudged(siblings(), s.id, delta);
      if (ids) saveOrder(`/projects/${s.projectId}/servers/order`, ids, s.id);
    }),
    h("a", { class: "side-link", href: serverHref(s.id, current ? r.tab : "overview"), "aria-current": current ? "page" : null },
      h("span", { class: ["dot", status?.state || "idle"] }),
      h("span", { class: "side-name" }, s.name),
      h("span", { class: ["side-status", attentionStates.has(status?.state) && "warn"] }, shortStatus(s.id))));
  dropTarget(li, DRAG_SERVER, (id, after) => {
    if (id !== s.id) saveOrder(`/projects/${s.projectId}/servers/order`, placed(siblings(), id, s.id, after), id);
  });
  return li;
}

const attentionStates = new Set(["reconnecting", "failed", "paused"]);

/** A few words for the sidebar: "2 running", "reconnecting"… */
function shortStatus(serverId) {
  const st = state.servers.get(serverId)?.state;
  if (st === "reconnecting") return "reconnecting";
  if (st === "failed") return "failed";
  if (st === "paused") return "waiting";
  if (st === "connecting") return "connecting";
  const running = forwardsOf(serverId).length;
  return running ? `${running} running` : "";
}

const forwardsOf = (serverId) => [...state.forwards.values()].filter((f) => f.serverId === serverId);
const terminalsOf = (serverId) => [...state.terminals.values()].filter((t) => t.serverId === serverId);
const serverName = (id) => state.data?.servers.find((s) => s.id === id)?.name || "A server";

// --- Overview ---------------------------------------------------------------

function renderOverview() {
  const d = state.data;
  if (d.projects.length === 0) {
    return h("div", { class: "empty card" },
      h("h1", { tabindex: "-1" }, "Add your first project"),
      h("p", {}, "Projects group your servers. Inside each server you add the web apps (services) you want to open, like n8n or Portainer."),
      h("button", { class: "btn primary", onclick: () => projectDialog() }, "+ New project"));
  }
  const online = d.servers.filter((s) => state.servers.get(s.id)?.state === "connected").length;
  const problems = d.servers.filter((s) => attentionStates.has(state.servers.get(s.id)?.state));

  return [
    h("h1", { tabindex: "-1" }, "Overview"),
    h("div", { class: "tiles" },
      tile("Servers online", String(online), `of ${d.servers.length}`),
      tile("Tunnels running", String(state.forwards.size)),
      tile("Terminals open", String(state.terminals.size)),
      tile("Traffic today", fmtBytes(trafficTodayAll()))),
    problems.length
      ? h("section", { class: "attention", "aria-label": "Needs attention" },
        problems.map((s) => {
          const st = state.servers.get(s.id);
          return h("div", { class: "attention-row" },
            h("span", { class: ["dot", st.state] }),
            h("span", { class: "grow" }, h("strong", {}, s.name), " ", problemText(st)),
            h("a", { class: "chip", href: serverHref(s.id) }, "Show server"));
        }))
      : null,
    h("div", { class: "two-col" },
      h("section", { class: "box" }, h("h2", {}, "Running now"), renderRunningNow()),
      h("section", { class: "box" }, h("h2", {}, "Recent activity"), renderActivity(state.activity, { limit: 8, withServer: true }))),
    h("section", { class: "box" }, h("h2", {}, "All servers"), renderServerTable()),
  ];
}

function tile(label, value, extra) {
  return h("div", { class: "tile" }, h("span", { class: "label" }, label),
    h("span", { class: "tile-value" }, value, extra ? h("span", { class: "muted" }, " ", extra) : null));
}

function problemText(st) {
  if (st.state === "reconnecting") return "lost its connection. Reconnecting…";
  if (st.state === "paused") return "is waiting for TunnelTab to be unlocked.";
  return `couldn't connect${st.error ? ": " + st.error : "."}`;
}

/** Running now: what runs and where on the first line, details below. */
const runningText = (title, details) =>
  h("div", { class: "running-text" }, h("div", {}, title), h("div", { class: "muted" }, details));

function renderRunningNow() {
  const d = state.data;
  const rows = [];
  for (const svc of [...d.services].sort(byOrder)) {
    const fwd = state.forwards.get(svc.id);
    if (!fwd) continue;
    const traffic = todayText(trafficOf([svc.id]));
    const open = h("button", { class: "chip open" }, "Open ↗");
    open.addEventListener("click", () => openService(svc, open));
    rows.push(h("li", { class: "list-row" },
      h("span", { class: ["dot", fwd.state === "active" ? "connected" : fwd.state] }),
      runningText(
        [h("strong", {}, svc.label), " ", h("a", { class: "muted", href: serverHref(svc.serverId, "services") }, serverName(svc.serverId))],
        [fwd.state === "active"
          ? h("a", { class: "mono", href: serviceURL(svc, fwd), target: "_blank", rel: "noopener noreferrer" }, `localhost:${fwd.localPort}`)
          : STATE_TEXT[fwd.state] || fwd.state,
        traffic && ` · ${traffic}`]),
      h("span", { class: "grow" }),
      h("button", { class: "chip stop", onclick: () => stopService(svc) }, "Stop"),
      open));
  }
  for (const t of state.terminals.values()) {
    const view = allViews().find((v) => v.terminalId === t.id);
    rows.push(h("li", { class: "list-row" },
      h("span", { class: "dot connected" }),
      runningText(
        [h("strong", {}, "Terminal"), " ", h("a", { class: "muted", href: serverHref(t.serverId) }, serverName(t.serverId))],
        `opened ${clock(t.openedAt)}`),
      h("span", { class: "grow" }),
      view
        ? h("button", { class: "chip open", onclick: () => showTerminal(t.serverId, view) }, "Show")
        : h("button", { class: "chip stop", onclick: () => closeTerminal(t) }, "Close")));
  }
  return rows.length
    ? h("ul", { class: "list" }, rows)
    : h("p", { class: "hint" }, "Nothing is running. Start a service from a server's Services tab, or open a terminal.");
}

function renderServerTable() {
  const projects = new Map(state.data.projects.map((p) => [p.id, p]));
  const servers = [...state.data.servers].sort((a, b) =>
    byOrder(projects.get(a.projectId) || {}, projects.get(b.projectId) || {}) || byOrder(a, b));
  return h("div", { class: "table-wrap" }, h("table", { class: "table" },
    h("thead", {}, h("tr", {},
      h("th", {}, "Server"), h("th", {}, "Project"), h("th", {}, "Status"), h("th", {}, "Connected for"), h("th", {}, "Ping"),
      h("th", {}, "Services running"), h("th", { class: "sr-only" }, "Actions"))),
    h("tbody", {}, servers.map((s) => {
      const st = state.servers.get(s.id);
      const apps = state.data.services.filter((x) => x.serverId === s.id).length;
      return h("tr", {},
        h("td", {}, h("a", { href: serverHref(s.id) }, s.name)),
        h("td", { class: "muted" }, projects.get(s.projectId)?.name || ""),
        h("td", {}, statusPill(st)),
        h("td", {}, st?.since && st.state === "connected" ? duration(Date.now() - Date.parse(st.since)) : h("span", { class: "muted" }, "—")),
        h("td", {}, st?.pingMs && st.state === "connected" ? fmtPing(st.pingMs) : h("span", { class: "muted" }, "—")),
        h("td", {}, `${forwardsOf(s.id).length} of ${apps}`),
        h("td", { class: "right" }, h("button", { class: "chip open", onclick: () => newTerminal(s) }, "Terminal")));
    }))));
}

function statusPill(st) {
  if (!st) return h("span", { class: "pill idle" }, "Not connected");
  const cls = st.state === "connected" ? "connected" : attentionStates.has(st.state) ? st.state : "idle";
  return h("span", { class: ["pill", cls] }, STATE_TEXT[st.state] || st.state);
}

// --- Activity -----------------------------------------------------------------

const ACTIVITY_DOT = {
  // (a failure that only needs a fingerprint confirmed is shown as waiting)
  connected: "connected", active: "connected", opened: "connected",
  reconnecting: "reconnecting", paused: "paused", failed: "failed",
};

// Plain words for the common connection failures (sshx.ErrorKind).
const FAILURE_TEXT = {
  unknown_host_key: "New server: waiting for you to confirm its fingerprint",
  host_key_changed: "Blocked: the server's fingerprint has changed",
  auth_failed: "Login failed: check the username and password or key",
  key_passphrase: "The key needs its passphrase",
};

function activityText(e) {
  // Errors can be long (a fingerprint, say); the full text is on the server page.
  const err = e.error ? `: ${e.error.length > 90 ? e.error.slice(0, 90) + "…" : e.error}` : "";
  if (e.kind === "server") {
    if (e.state === "failed" && FAILURE_TEXT[e.reason]) return FAILURE_TEXT[e.reason];
    return {
      connected: e.reconnects ? "Reconnected" : "Connected",
      reconnecting: "Lost the connection; reconnecting",
      failed: `Couldn't connect${err}`,
      stopped: "Disconnected",
      paused: "Waiting for unlock to reconnect",
    }[e.state] || e.state;
  }
  if (e.kind === "forward") {
    const label = state.data?.services.find((s) => s.id === e.id)?.label || "A tunnel";
    return `${label}: ${{ active: "tunnel started", stopped: "tunnel stopped", failed: "tunnel failed" }[e.state] || e.state}${e.state === "failed" ? err : ""}`;
  }
  if (e.kind === "discover") return e.state === "searched" ? "Searched for services" : "Couldn't search for services";
  return e.state === "opened" ? "Terminal opened" : "Terminal closed";
}

function renderActivity(entries, { limit = 0, withServer = false } = {}) {
  let list = [...entries].reverse();
  if (limit) list = list.slice(0, limit);
  if (!list.length) return h("p", { class: "hint" }, "Nothing yet. Connections, tunnels and terminals show up here while TunnelTab runs.");
  return h("ul", { class: "activity" }, list.map((e) => h("li", {},
    h("time", { class: "mono muted", datetime: e.at }, clock(e.at)),
    h("span", { class: ["dot", e.reason === "unknown_host_key" ? "paused" : ACTIVITY_DOT[e.state] || "idle"] }),
    h("span", {}, withServer ? [h("a", { href: serverHref(e.serverId) }, serverName(e.serverId)), " · "] : null, activityText(e)))));
}

function clock(iso) {
  return new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

function duration(ms) {
  const min = Math.floor(ms / 60000);
  if (min < 1) return "under a minute";
  if (min < 60) return `${min} min`;
  const hours = Math.floor(min / 60);
  if (hours < 48) return `${hours}h ${min % 60}m`;
  return `${Math.floor(hours / 24)} days`;
}

// --- Traffic ----------------------------------------------------------------

/** Traffic of some services added up: {todayIn, todayOut, lastHour[60]}. */
function trafficOf(serviceIds) {
  const sum = { todayIn: 0, todayOut: 0, lastHour: new Array(60).fill(0) };
  for (const id of serviceIds) {
    const t = state.traffic.get(id);
    if (!t) continue;
    sum.todayIn += t.todayIn;
    sum.todayOut += t.todayOut;
    t.lastHour.forEach((b, i) => (sum.lastHour[i] += b));
  }
  return sum;
}

const todayText = (t) => (t.todayIn + t.todayOut ? `${fmtBytes(t.todayIn + t.todayOut)} today` : "");
const trafficTodayAll = () => {
  const t = trafficOf(state.traffic.keys());
  return t.todayIn + t.todayOut;
};
const fmtPing = (ms) => (ms < 1 ? "under 1 ms" : `${Math.round(ms)} ms`);

/** An area chart of 60 per-minute values (oldest first), drawn with SVG. */
function trafficChart(values) {
  const NS = "http://www.w3.org/2000/svg";
  const W = 300;
  const H = 80;
  const peak = Math.max(...values, 1);
  const pts = values.map((v, i) => [(i / (values.length - 1)) * W, H - 4 - (v / peak) * (H - 12)]);
  const line = pts.map(([x, y], i) => `${i ? "L" : "M"}${x.toFixed(1)} ${y.toFixed(1)}`).join(" ");
  const el = (tag, attrs) => {
    const node = document.createElementNS(NS, tag);
    for (const [k, v] of Object.entries(attrs)) node.setAttribute(k, String(v));
    return node;
  };
  const svg = el("svg", { viewBox: `0 0 ${W} ${H}`, preserveAspectRatio: "none", class: "chart", role: "img",
    "aria-label": `Traffic in the last hour; busiest minute ${fmtBytes(peak)}` });
  svg.append(
    el("path", { d: `M0 ${H / 2}H${W}M0 ${H - 4}H${W}`, class: "chart-grid" }),
    el("path", { d: `${line} L${W} ${H} L0 ${H} Z`, class: "chart-area" }),
    el("path", { d: line, class: "chart-line" }));
  return svg;
}

// --- Server page ------------------------------------------------------------

function renderServerPage(s, tab) {
  const st = state.servers.get(s.id);
  const services = state.data.services.filter((x) => x.serverId === s.id).sort(byOrder);
  const views = termGroup(s.id).views;
  const tabs = [["overview", "Overview"], ["services", "Services", services.length], ["terminals", "Terminals", views.length], ["activity", "Activity"], ["notes", "Notes"]];
  return [
    h("div", { class: "page-head" },
      h("span", { class: ["dot", st?.state || "idle"] }),
      h("h1", { tabindex: "-1" }, s.name),
      h("span", { class: "mono muted" }, address(s)),
      statusPill(st),
      h("span", { class: "grow" }),
      h("button", { class: "btn secondary open-btn", onclick: () => newTerminal(s), title: "Open an SSH terminal on this page" }, "+ New terminal"),
      moreMenu(`More actions for ${s.name}`, [
        ["Test connection", () => testServer(s), "Check the connection and login"],
        ["Edit server", () => serverDialog(s.projectId, s)],
      ])),
    st?.error ? h("p", { class: ["error-line", st.state === "reconnecting" && "quiet"] }, st.state === "reconnecting" ? `Last try: ${st.error}` : st.error) : null,
    h("nav", { class: "page-tabs", "aria-label": `${s.name} sections` },
      tabs.map(([id, label, count]) => h("a", { href: serverHref(s.id, id), "aria-current": id === tab ? "page" : null },
        label, count !== undefined ? h("span", { class: "count" }, String(count)) : null))),
    tab === "services" ? renderServicesTab(s, services)
      : tab === "terminals" ? renderTerminalsTab(s)
        : tab === "notes" ? renderNotesTab(s)
        : tab === "activity" ? h("section", { class: "box" }, renderActivity(state.activity.filter((e) => e.serverId === s.id)))
          : renderServerOverview(s, st, services),
  ];
}

const address = (s) => `${s.username}@${s.host}${s.port !== 22 ? ":" + s.port : ""}`;

function renderServerOverview(s, st, services) {
  const connected = st?.state === "connected";
  const rows = [
    ["Status", statusPill(st)],
    ["Connected", st?.since && connected
      ? `${duration(Date.now() - Date.parse(st.since))}, since ${clock(st.since)}`
      : h("span", { class: "muted" }, "Not right now. Starting a service or a terminal connects, or Connect under Health.")],
    ["Ping", st?.pingMs && connected ? fmtPing(st.pingMs) : "—"],
    ["Reconnects", st?.since ? String(st.reconnects || 0) : "—"],
    ["Address", h("span", { class: "mono" }, address(s))],
    ["Login", AUTH_TEXT[s.auth.type] || s.auth.type],
    ["Terminals open", String(terminalsOf(s.id).length)],
  ];
  const traffic = trafficOf(services.map((x) => x.id));
  const trafficBox = h("section", { class: "box" },
    h("h2", {}, "Traffic, last hour"),
    traffic.todayIn + traffic.todayOut
      ? [
        trafficChart(traffic.lastHour),
        h("div", { class: "chart-axis muted" }, h("span", {}, "60 min ago"), h("span", {}, "now")),
        h("p", { class: "hint" }, `Today: ${fmtBytes(traffic.todayIn)} from the server, ${fmtBytes(traffic.todayOut)} to it. Counted on this PC; starts at zero when TunnelTab starts.`),
      ]
      : h("p", { class: "hint" }, "No traffic through this server's tunnels yet today."));
  const notesBox = h("section", { class: "box" },
    h("div", { class: "box-head" }, h("h2", {}, "Notes"), h("a", { class: "chip", href: serverHref(s.id, "notes") }, s.notes ? "Edit" : "Add notes")),
    s.notes
      ? h("p", { class: "notes-preview" }, s.notes)
      : h("p", { class: "hint" }, "Anything worth remembering about this server: backup times, where passwords are, who to call. Stored in the encrypted vault."));
  return [
    renderServerOverviewTop(s, rows, services),
    h("div", { class: "two-col" }, renderHealthBox(s, st), trafficBox),
    notesBox,
  ];
}

// --- Server health ------------------------------------------------------------
// Shown while the server is connected and either health is switched on in
// Settings, or the user connected it with the card's Connect button (held).

/** Connect: connect to the server and keep it connected, reading its health. */
async function connectServer(server, btn) {
  btn.disabled = true;
  btn.textContent = "Connecting…";
  try {
    await withHostKeys(() => api("POST", `/servers/${encodeURIComponent(server.id)}/connect`));
  } catch (err) {
    toast(err.message, "error");
  } finally {
    renderSoon();
  }
}

async function disconnectServer(server) {
  try {
    await api("DELETE", `/servers/${encodeURIComponent(server.id)}/connect`);
  } catch (err) {
    toast(err.message, "error");
  }
}

/** A labelled bar: label, a <meter> of used/total, and the figures. */
function healthMeter(label, used, total, text) {
  const pct = total > 0 ? Math.round((used / total) * 100) : 0;
  return [
    h("span", { class: "muted" }, label),
    h("meter", { class: "health-meter", min: 0, max: 100, low: 70, high: 90, optimum: 0, value: pct, "aria-label": `${label} ${pct}%` }),
    h("span", { class: "health-figure" }, text),
  ];
}

const kb = (n) => fmtBytes(n * 1024);

function uptimeText(sec) {
  const days = Math.floor(sec / 86400);
  if (days >= 2) return `${days} days`;
  const hours = Math.floor(sec / 3600);
  return hours >= 1 ? `${hours}h ${Math.floor((sec % 3600) / 60)}m` : `${Math.floor(sec / 60)} min`;
}

function renderHealthBox(s, st) {
  const connected = st?.state === "connected";
  const head = h("div", { class: "box-head" }, h("h2", {}, "Health"),
    st?.held ? h("button", { class: "chip", onclick: () => disconnectServer(s),
      title: "Stop reading health. The connection closes unless a service or terminal uses it." }, "Disconnect") : null);
  if (!st?.held && !(s.healthEnabled && connected)) {
    return h("section", { class: "box" }, head,
      h("p", { class: "hint" }, s.healthEnabled
        ? "Shown while this server is connected for a service or a terminal. Connect to see it now."
        : `${connected ? "Show health" : "Connect"} to see this server's load, memory, disk use and uptime, read every 30 seconds until you disconnect or close the dashboard. To see it whenever a service or terminal is open, tick Health for this server in Settings → Servers.`),
      h("p", { class: "hint" }, "It runs one read-only command; Linux servers only."),
      h("div", {}, h("button", { class: "btn secondary small", onclick: (e) => connectServer(s, e.currentTarget) },
        connected ? "Show health" : "Connect")));
  }
  const reading = state.health.get(s.id);
  if (!reading) {
    return h("section", { class: "box" }, head, h("p", { class: "hint" }, connected ? "Reading…" : "Connecting…"));
  }
  if (reading.error || !reading.health) {
    return h("section", { class: "box" }, head, h("p", { class: "hint" }, reading.error || "No reading."));
  }
  const x = reading.health;
  const loadPct = x.cores ? x.load1 / x.cores : 0;
  return h("section", { class: "box" }, head,
    h("div", { class: "health-grid" },
      healthMeter("CPU load", Math.min(loadPct, 1) * 100, 100, `${x.load1.toFixed(2)}${x.cores ? ` · ${x.cores} cores` : ""}`),
      healthMeter("Memory", x.memTotalKB - x.memAvailableKB, x.memTotalKB, `${kb(x.memTotalKB - x.memAvailableKB)} of ${kb(x.memTotalKB)}`),
      x.disks.map((d) => healthMeter(`Disk ${d.mount}`, d.usedKB, d.usedKB + d.availKB, `${kb(d.usedKB)} of ${kb(d.sizeKB)}`))),
    h("p", { class: "hint" }, `Server up ${uptimeText(x.uptimeSec)} · load ${x.load1.toFixed(2)} / ${x.load5.toFixed(2)} / ${x.load15.toFixed(2)} · read at ${clock(reading.at)}`));
}

function renderServerOverviewTop(s, rows, services) {
  return h("div", { class: "two-col" },
    h("section", { class: "box" }, h("h2", {}, "Connection"),
      h("dl", { class: "kv" }, rows.map(([k, v]) => [h("dt", {}, k), h("dd", {}, v)]))),
    h("section", { class: "box" },
      h("div", { class: "box-head" }, h("h2", {}, "Services"), h("a", { class: "chip", href: serverHref(s.id, "services") }, "Manage")),
      services.length
        ? h("ul", { class: "list" }, services.map((svc) => {
          const fwd = state.forwards.get(svc.id);
          const open = h("button", { class: "chip open" }, "Open ↗");
          open.addEventListener("click", () => openService(svc, open));
          return h("li", { class: "list-row" },
            h("strong", {}, svc.label),
            fwd ? h("span", { class: ["pill", fwd.state] }, STATE_TEXT[fwd.state] || fwd.state) : h("span", { class: "pill idle" }, "Stopped"),
            h("span", { class: "grow" }),
            open);
        }))
        : h("p", { class: "hint" }, "No services yet.", " ", h("button", { class: "linklike", onclick: () => serviceDialog(s.id) }, "Add one"))));
}

// One editor per server, kept across redraws so typing isn't lost.
const noteEditors = new Map(); // serverId → { textarea, status, save, saved }

function renderNotesTab(s) {
  let ed = noteEditors.get(s.id);
  if (!ed) {
    const textarea = h("textarea", {
      class: "notes-input", rows: 14, maxlength: 10000, "aria-label": `Notes about ${s.name}`,
      placeholder: "Backup times, where the passwords are, what runs where…",
    });
    const status = h("span", { class: "inline-status", role: "status" });
    const save = h("button", { type: "button", class: "btn primary" }, "Save notes");
    ed = { textarea, status, save, saved: s.notes || "" };
    textarea.value = ed.saved;
    textarea.addEventListener("input", () => {
      status.className = "inline-status";
      status.textContent = textarea.value !== ed.saved ? "Not saved yet" : "";
    });
    save.addEventListener("click", async () => {
      save.disabled = true;
      try {
        await api("PUT", `/servers/${encodeURIComponent(s.id)}/notes`, { notes: textarea.value });
        ed.saved = textarea.value;
        status.className = "inline-status ok";
        status.textContent = "Saved.";
      } catch (err) {
        status.className = "inline-status bad";
        status.textContent = err.message;
      } finally {
        save.disabled = false;
      }
    });
    textarea.addEventListener("keydown", (e) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "s") {
        e.preventDefault();
        save.click();
      }
    });
    noteEditors.set(s.id, ed);
  } else if (ed.textarea.value === ed.saved && (s.notes || "") !== ed.saved) {
    // Changed elsewhere (or tidied when saved) and nothing typed here: show it.
    ed.textarea.value = ed.saved = s.notes || "";
  }
  return h("section", { class: "box" },
    h("h2", {}, "Notes"),
    h("p", { class: "hint" }, "Free text about this server. Stored in the encrypted vault, like its login details. Ctrl+S saves."),
    ed.textarea,
    h("div", { class: "inline-actions" }, ed.save, ed.status));
}

function renderServicesTab(s, services) {
  return h("section", { class: "box" },
    h("div", { class: "box-head" },
      h("h2", {}, "Services"),
      h("span", { class: "grow" }),
      h("button", { class: "chip", onclick: () => findServices(s), title: "List the web apps running on this server and pick which to add" }, "Find services…"),
      h("button", { class: "chip add", onclick: () => serviceDialog(s.id) }, "+ Service")),
    services.length
      ? h("ul", { class: "services" }, services.map(renderService))
      : [
        h("p", { class: "hint" }, "No services yet. TunnelTab can look at this server and list the web apps it runs, or you can add one by hand with “+ Service”."),
        h("div", {}, h("button", { class: "btn secondary small", onclick: () => findServices(s) }, "Find services…")),
      ]);
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

const STATE_TEXT = {
  connecting: "Connecting…", connected: "Connected", active: "Running", reconnecting: "Reconnecting…",
  paused: "Waiting for unlock", failed: "Failed", stopped: "Stopped",
};

const AUTH_TEXT = { agent: "SSH agent", keyVault: "Stored key", keyFile: "Key file", password: "Password" };

/**
 * A "⋯" button with a small menu of [label, action, title?] items.
 * Arrow keys move between items; Escape, Tab or a click elsewhere closes it.
 */
function moreMenu(label, items) {
  const btn = h("button", {
    type: "button", class: "chip more", title: label, "aria-label": label,
    "aria-haspopup": "menu", "aria-expanded": "false",
  }, "⋯");
  const menu = h("div", { class: "menu", role: "menu", hidden: true },
    items.map(([text, action, title]) => h("button", {
      type: "button", role: "menuitem", title,
      onclick: () => { close(true); action(); },
    }, text)));
  const wrap = h("span", { class: "menu-wrap" }, btn, menu);

  const onOutside = (e) => {
    if (!wrap.isConnected || !wrap.contains(e.target)) close(false);
  };
  function open() {
    menu.hidden = false;
    btn.setAttribute("aria-expanded", "true");
    document.addEventListener("pointerdown", onOutside);
    menu.querySelector("button")?.focus();
  }
  function close(focusButton) {
    menu.hidden = true;
    btn.setAttribute("aria-expanded", "false");
    document.removeEventListener("pointerdown", onOutside);
    if (focusButton) btn.focus();
  }
  btn.addEventListener("click", () => (menu.hidden ? open() : close(false)));
  wrap.addEventListener("keydown", (e) => {
    if (menu.hidden) return;
    const buttons = [...menu.querySelectorAll("button")];
    const i = buttons.indexOf(document.activeElement);
    if (e.key === "Escape") {
      e.preventDefault();
      close(true);
    } else if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const step = e.key === "ArrowDown" ? 1 : -1;
      buttons[(i + step + buttons.length) % buttons.length].focus();
    } else if (e.key === "Tab") {
      close(false);
    }
  });
  return wrap;
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
      checkNote(svc),
      h("span", { class: "muted" }, todayText(trafficOf([svc.id]))),
      svc.autoStart ? h("span", { class: "badge", title: "Starts automatically after unlocking" }, "auto") : null),
    h("div", { class: "actions" },
      toggle, open,
      h("button", { class: "chip", title: "Check that the app answers (one request through the server's SSH connection)",
        onclick: (e) => checkService(svc, e.currentTarget) }, "Check"),
      h("button", { class: "chip", onclick: () => serviceDialog(svc.serverId, svc) }, "Edit")));
  dropTarget(row, dragService(svc.serverId), (id, after) => {
    if (id !== svc.id) saveOrder(orderPath, placed(siblings(), id, svc.id, after), id);
  });
  return row;
}

// --- Service checks -----------------------------------------------------------
// Does the app behind a service answer? Checked after Start, on Check, and on
// Open when the last check failed: never in the background.

const checkOK = (svc, c) => c?.state === "responding" && c.protocol === svc.protocol;

/** The last check's result, in words, for a service row. */
function checkNote(svc) {
  const c = state.checks.get(svc.id);
  if (!c) return null;
  const when = `Checked at ${clock(c.at)}`;
  if (c.state === "responding") {
    return c.protocol === svc.protocol
      ? h("span", { class: "svc-check ok", title: `${when}: it answered (HTTP ${c.status}).` }, "✓ App answers")
      : h("span", { class: "svc-check warn", title: when }, `Answers on ${c.protocol}, not ${svc.protocol}: edit the service`);
  }
  return h("span", { class: "svc-check bad", title: when },
    c.state === "no_answer" ? `✗ Nothing answers on port ${svc.remotePort}` : "✗ Answers, but not as a web page");
}

/** The Check button: checks now (this may connect to the server). */
async function checkService(svc, button) {
  button.disabled = true;
  button.textContent = "Checking…";
  try {
    const res = await withHostKeys(() => api("POST", `/services/${svc.id}/check`));
    if (res) state.checks.set(svc.id, res.check);
  } catch (err) {
    toast(`${svc.label}: ${err.message}`, "error");
  } finally {
    renderSoon();
  }
}

/** After Open: if the last check failed, check again and say so if it still does. */
async function recheckOnOpen(svc) {
  const last = state.checks.get(svc.id);
  if (!last || checkOK(svc, last)) return;
  try {
    const { check } = await api("POST", `/services/${svc.id}/check`);
    state.checks.set(svc.id, check);
    renderSoon();
    if (!checkOK(svc, check)) {
      toast(check.state === "responding"
        ? `${svc.label} answers on ${check.protocol}, but the service is set to ${svc.protocol}. Edit it to fix the page.`
        : `${svc.label} isn't answering on the server (port ${svc.remotePort}). Is the app running?`, "error");
    }
  } catch {
    // The page opened anyway; the next Check will tell.
  }
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
    recheckOnOpen(svc);
    return;
  }
  // Starting the tunnel checks the app too (the result shows on the Services tab).
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

// --- Terminals inside the page ---------------------------------------------
//
// Each server's Terminals tab shows the terminals this dashboard tab opened
// (TermView, see termview.js). They belong to this tab: its event stream
// carries CLIENT_ID, which keeps them alive while the tab is open — also
// while locked, or while you look at another server. Pop out moves one to its
// own browser tab (terminal.html); "Bring back here" moves it back.

const CLIENT_ID = (() => {
  const make = () => {
    const bytes = crypto.getRandomValues(new Uint8Array(18));
    return btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_");
  };
  try {
    // Survives a reload of this tab, so its terminals are found again.
    let id = sessionStorage.getItem("tunneltab.client");
    if (!id) sessionStorage.setItem("tunneltab.client", (id = make()));
    return id;
  } catch {
    return make();
  }
})();

const termGroups = new Map(); // serverId → { views: [TermView], active, split }

function termGroup(serverId) {
  if (!termGroups.has(serverId)) termGroups.set(serverId, { views: [], active: null, split: false });
  return termGroups.get(serverId);
}

const allViews = () => [...termGroups.values()].flatMap((g) => g.views);

/** Forgets every terminal view (TunnelTab stopped or is restarting). */
function dropViews() {
  allViews().forEach((v) => v.dispose());
  termGroups.clear();
  noteEditors.clear();
}

function makeView(serverId, terminalId) {
  const view = new TermView({
    serverId, terminalId, client: CLIENT_ID,
    onToast: (text) => toast(text, "success", null, 1500),
    onChange: (v) => {
      // Focus a new terminal once it's connected, if it's the one on screen.
      if (v.state === "connected" && !v.focusedOnce && v.el.isConnected) {
        v.focusedOnce = true;
        v.focus();
      }
      renderSoon();
    },
  });
  termGroup(serverId).views.push(view);
  return view;
}

/** Opens a new terminal on the server's Terminals tab. */
function newTerminal(server) {
  const g = termGroup(server.id);
  const view = makeView(server.id, null);
  g.active = view;
  const href = serverHref(server.id, "terminals");
  if (location.hash === href) renderDashboard();
  else location.hash = href;
  view.start();
}

/** Shows the terminals this tab owns after a (re)load: e.g. after a reload. */
function syncViews(terminals) {
  const known = new Set(allViews().map((v) => v.terminalId));
  for (const t of terminals) {
    if (t.client !== CLIENT_ID || known.has(t.id)) continue;
    const view = makeView(t.serverId, t.id);
    termGroup(t.serverId).active ??= view;
    // One that is popped out stays where it is until brought back.
    if (t.attached) view.setState("elsewhere", "This terminal is open in another tab.");
    else view.start();
  }
}

function showTerminal(serverId, view) {
  termGroup(serverId).active = view;
  const href = serverHref(serverId, "terminals");
  if (location.hash === href) renderDashboard();
  else location.hash = href;
  requestAnimationFrame(() => view.focus());
}

async function closeView(serverId, view) {
  const g = termGroup(serverId);
  if (view.state !== "ended" && !(await confirmDialog("Close terminal?",
    "The terminal ends, along with anything still running in it.", { confirmLabel: "Close terminal", danger: true }))) return;
  g.views = g.views.filter((v) => v !== view);
  if (g.active === view) g.active = g.views[g.views.length - 1] || null;
  if (g.views.length < 2) g.split = false;
  try {
    await view.close();
  } catch (err) {
    toast(err.message, "error");
  }
  renderDashboard();
}

function popOut(view) {
  if (!view?.terminalId) return;
  window.open(`/terminal.html#${encodeURIComponent(view.serverId)}/${encodeURIComponent(view.terminalId)}`, "_blank", "noopener");
}

function toggleSplit(server) {
  const g = termGroup(server.id);
  g.split = !g.split;
  if (g.split && g.views.length < 2) {
    const active = g.active;
    newTerminal(server); // the new one shows next to the current one
    g.active = active;
    renderDashboard();
    return;
  }
  renderDashboard();
}

const TERM_DOT = { connected: "connected", connecting: "connecting", disconnected: "reconnecting", reconnecting: "reconnecting", locked: "paused", elsewhere: "paused", ended: "failed" };

function renderTerminalsTab(s) {
  const g = termGroup(s.id);
  if (!g.views.length) {
    return h("section", { class: "box empty-terminals" },
      h("p", {}, "No terminals open on this server."),
      h("button", { class: "btn primary", onclick: () => newTerminal(s) }, "+ New terminal"),
      h("p", { class: "hint" }, "Terminals stay open while this dashboard tab is open, even while you look at other servers or TunnelTab is locked."));
  }
  if (!g.views.includes(g.active)) g.active = g.views[0];
  const second = g.split ? g.views.find((v) => v !== g.active) : null;
  const shown = second ? [g.active, second] : [g.active];

  const strip = h("div", { class: "term-strip" },
    h("div", { class: "term-tabs", role: "tablist", "aria-label": `Terminals on ${s.name}` },
      g.views.map((v, i) => {
        const label = `Terminal ${i + 1}`;
        return h("div", { class: ["term-tab", v === g.active && "active", v === second && "shown"] },
          h("button", {
            type: "button", role: "tab", class: "term-tab-btn", "aria-selected": String(v === g.active),
            title: TERM_STATES[v.state]?.[1], onclick: () => showTerminal(s.id, v),
          }, h("span", { class: ["dot", TERM_DOT[v.state]] }), label),
          h("button", { type: "button", class: "term-tab-close", "aria-label": `Close ${label}`, onclick: () => closeView(s.id, v) }, "×"));
      })),
    h("button", { type: "button", class: "chip add", onclick: () => newTerminal(s) }, "+ New"),
    h("span", { class: "grow" }),
    h("span", { class: "hint term-hint" }, "Select, then Ctrl+C or right-click to copy · Ctrl+V or right-click to paste"),
    h("button", { type: "button", class: "chip", "aria-pressed": String(g.split), onclick: () => toggleSplit(s), title: "Show two terminals side by side" }, "Split"),
    h("button", { type: "button", class: "chip", disabled: !g.active.terminalId, onclick: () => popOut(g.active), title: "Move this terminal to its own browser tab" }, "Pop out ↗"));

  const panes = h("div", { class: ["term-panes", second && "split"] },
    shown.map((v) => h("div", { class: ["term-pane", second && v === g.active && "active"] }, v.el)));
  requestAnimationFrame(() => shown.forEach((v) => v.fit()));
  return [strip, panes];
}

async function openSettings() {
  try {
    await settingsDialog({
      knownHosts: state.data?.knownHosts || [], minPasswordLen: state.minPasswordLen, version: state.version,
      servers: state.data?.servers || [], projects: state.data?.projects || [],
      runningTunnels: state.forwards.size, onRestarting: showUpdating,
      initialTab: state.updateAvailable ? "Updates" : undefined,
      onChecked: (latest) => {
        state.updateAvailable = latest;
        renderSoon();
      },
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

async function closeTerminal(t) {
  if (!(await confirmDialog("Close terminal?",
    `The terminal on ${serverName(t.serverId)} ends, along with anything still running in it.`,
    { confirmLabel: "Close terminal", danger: true }))) return;
  try {
    await api("DELETE", `/terminals/${encodeURIComponent(t.id)}`);
  } catch (err) {
    if (err.status !== 404) toast(err.message, "error");
  }
}

const plural = (n, word) => `${n} ${word}${n === 1 ? "" : "s"}`;

async function quit() {
  const parts = [];
  if (state.forwards.size) parts.push(plural(state.forwards.size, "running tunnel"));
  if (state.terminals.size) parts.push(plural(state.terminals.size, "terminal"));
  const msg = parts.length ? `Stop TunnelTab? ${parts.join(" and ")} will close.` : "Stop TunnelTab?";
  if (!(await confirmDialog("Quit TunnelTab", msg, { confirmLabel: "Quit", danger: parts.length > 0 }))) return;
  try {
    await api("POST", "/quit");
  } catch {
    // It may have stopped before answering.
  }
  showStopped();
}

start();

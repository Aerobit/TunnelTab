// forms.js — dialogs for editing projects, servers, services and settings,
// and the host-key (fingerprint) confirmation flow.

import { api, ApiError } from "./api.js";
import { h } from "./dom.js";
import { checkbox, confirmDialog, field, openDialog, tabs, toast } from "./dialogs.js";
import { fmtBytes } from "./format.js";

// --- Helpers ----------------------------------------------------------------

const text = (value = "", attrs = {}) => h("input", { type: "text", value, autocomplete: "off", spellcheck: "false", ...attrs });
const number = (value, attrs = {}) => h("input", { type: "number", value: value ?? "", inputmode: "numeric", ...attrs });
const password = (attrs = {}) => h("input", { type: "password", autocomplete: "off", ...attrs });

function select(options, value) {
  return h("select", {}, options.map(([v, label]) => h("option", { value: v, selected: v === value }, label)));
}

/** Parses an optional integer field; "" becomes fallback. */
function int(input, fallback = 0) {
  const v = input.value.trim();
  return v === "" ? fallback : Number.parseInt(v, 10);
}

// --- Host keys --------------------------------------------------------------

/**
 * Runs fn (an API call that connects to a server). If the server's host key
 * needs confirming, asks the user and retries. Returns fn's result, or null
 * if the user declined.
 */
// The confirmed key changed while a question was open (e.g. in another tab),
// so its answer no longer applies. Treat it like an answer: the dialog
// closes and withHostKeys connects again, which asks what fits now.
function confirmHostKey(body) {
  return api("POST", "/hostkeys/confirm", body).catch((err) => {
    if (!(err instanceof ApiError && err.code === "host_key_question_stale")) throw err;
  });
}

export async function withHostKeys(fn) {
  for (let attempt = 0; attempt < 3; attempt++) {
    try {
      return await fn();
    } catch (err) {
      if (!(err instanceof ApiError) || !["unknown_host_key", "host_key_changed"].includes(err.code)) throw err;
      if (!(await hostKeyDialog(err.body))) return null;
    }
  }
  throw new Error("The server's key keeps changing. Not connecting.");
}

async function hostKeyDialog(info) {
  const changed = info.error === "host_key_changed";
  const fp = h("code", { class: "fingerprint" }, `${info.keyType} ${info.fingerprint}`);

  if (!changed) {
    const label = await openDialog({
      title: "Confirm new server",
      body: [
        h("p", {}, "First connection to ", h("strong", {}, info.address), ". Its fingerprint is:"),
        fp,
        h("p", { class: "hint" },
          "Check it matches the one shown by your VPS provider, or by running ",
          h("code", {}, "ssh-keygen -lf /etc/ssh/ssh_host_*_key.pub"),
          " on the server. Nothing (no password or key) is sent until you confirm."),
      ],
      actions: [
        { label: "Cancel" },
        { label: "Trust and connect", kind: "primary", submit: true,
          onClick: () => confirmHostKey({ token: info.token }) },
      ],
    });
    return label === "Trust and connect";
  }

  const understand = checkbox("I know why this server's key changed (for example, it was reinstalled)", false);
  const label = await openDialog({
    title: "⚠ Server identity changed",
    danger: true,
    body: [
      h("p", {}, "The fingerprint of ", h("strong", {}, info.address), " is different from the one you confirmed before."),
      h("p", {}, "This happens when a server is reinstalled — but it is also exactly what an attacker intercepting your connection would look like. TunnelTab refused to connect."),
      h("p", {}, "New fingerprint:"), fp,
      h("p", { class: "hint" }, "Previously confirmed: " + (info.known || []).join(", ")),
      understand.el,
    ],
    actions: [
      { label: "Don't connect", kind: "primary" },
      { label: "Replace key and connect", kind: "danger",
        onClick: async () => {
          if (!understand.input.checked) throw new Error("Tick the box to confirm you know why the key changed.");
          await confirmHostKey({ token: info.token, replace: true });
        } },
    ],
  });
  return label === "Replace key and connect";
}

/** SHA256 fingerprint of an authorized_keys line, like ssh-keygen -l. */
export async function fingerprint(authorizedKey) {
  try {
    const [type, b64] = authorizedKey.split(" ");
    const blob = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
    const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", blob));
    const out = btoa(String.fromCharCode(...digest)).replace(/=+$/, "");
    return `${type} SHA256:${out}`;
  } catch {
    return authorizedKey.slice(0, 40) + "…";
  }
}

// --- Projects ---------------------------------------------------------------

export async function projectDialog(project) {
  const name = text(project?.name, { maxlength: 100, required: true, placeholder: "e.g. Production" });
  const desc = text(project?.description, { maxlength: 1000, placeholder: "Optional" });
  const save = () => {
    const body = { name: name.value, description: desc.value };
    return project ? api("PUT", `/projects/${project.id}`, body) : api("POST", "/projects", body);
  };
  const actions = [];
  if (project) {
    actions.push({
      label: "Delete project", kind: "danger-link",
      onClick: async () => {
        const ok = await confirmDialog("Delete project?",
          `Delete "${project.name}" with all its servers and services? Running tunnels will close.`,
          { confirmLabel: "Delete", danger: true });
        if (!ok) return false;
        await api("DELETE", `/projects/${project.id}`);
      },
    });
  }
  actions.push({ label: "Cancel" }, { label: project ? "Save" : "Create project", kind: "primary", submit: true, onClick: save });
  await openDialog({
    title: project ? "Edit project" : "New project",
    body: [field("Name", name), field("Description", desc)],
    actions,
  });
}

// --- Servers ----------------------------------------------------------------

const AUTH_TYPES = [
  ["agent", "SSH agent (key already loaded, nothing stored)"],
  ["keyVault", "Private key stored in TunnelTab"],
  ["keyFile", "Private key file"],
  ["password", "Password"],
];

/**
 * Add or edit a server. After saving, it tests the connection (which also
 * asks the user to confirm the server's fingerprint the first time).
 */
export async function serverDialog(projectId, server) {
  const a = server?.auth || { type: "keyVault" };
  const name = text(server?.name, { maxlength: 100, placeholder: "e.g. Web VPS" });
  const host = text(server?.host, { maxlength: 253, placeholder: "vps.example.com or 203.0.113.10" });
  const port = number(server?.port ?? 22, { min: 1, max: 65535 });
  const user = text(server?.username, { maxlength: 64, placeholder: "e.g. root" });
  const type = select(AUTH_TYPES, a.type);

  const saved = (has) => (server && has ? "Saved — leave blank to keep it" : "");
  const keyPath = text(a.keyPath, { placeholder: "keys/id_ed25519 (inside the TunnelTab folder) or a full path" });
  const privateKey = h("textarea", {
    rows: 6, spellcheck: "false", class: "mono",
    placeholder: server && a.hasPrivateKey ? "Saved — leave blank to keep it" : "-----BEGIN OPENSSH PRIVATE KEY-----\n…",
  });
  const passphrase = password({ placeholder: saved(a.hasPassphrase) || "Only if the key has one" });
  const pw = password({ placeholder: saved(a.hasPassword) });
  const clearPass = checkbox("Remove the saved passphrase", false);

  const keyPathField = field("Key file", keyPath, "Relative paths are inside the TunnelTab folder, so the key can travel with it.");
  const privateKeyField = field("Private key", privateKey, "Paste the whole key. It is stored only inside your encrypted vault.");
  const passphraseField = field("Key passphrase", passphrase);
  const passwordField = field("Password", pw, "Stored only inside your encrypted vault. SSH keys are safer than passwords.");
  const agentNote = h("p", { class: "hint" },
    "Uses keys loaded in your SSH agent: on Windows the \"OpenSSH Authentication Agent\" service, on Linux ssh-agent. Nothing secret is stored in TunnelTab.");

  function showAuthFields() {
    const t = type.value;
    agentNote.hidden = t !== "agent";
    keyPathField.hidden = t !== "keyFile";
    privateKeyField.hidden = t !== "keyVault";
    passphraseField.hidden = !(t === "keyFile" || t === "keyVault");
    passwordField.hidden = t !== "password";
    clearPass.el.hidden = !(server && a.hasPassphrase && t === a.type && (t === "keyFile" || t === "keyVault"));
  }
  type.addEventListener("change", showAuthFields);
  showAuthFields();

  let savedServer = null;
  const save = async () => {
    const body = {
      projectId, name: name.value, host: host.value, port: int(port, 22), username: user.value,
      auth: { type: type.value },
    };
    if (type.value === "keyFile") body.auth.keyPath = keyPath.value;
    if (type.value === "keyVault") body.auth.privateKey = privateKey.value;
    if (type.value === "keyFile" || type.value === "keyVault") body.auth.passphrase = passphrase.value;
    if (type.value === "password") body.auth.password = pw.value;
    savedServer = server
      ? await api("PUT", `/servers/${server.id}`, body)
      : await api("POST", "/servers", body);
    if (server && clearPass.input.checked && !passphrase.value) {
      await api("POST", `/servers/${server.id}/clear-passphrase`);
    }
  };

  const actions = [];
  if (server) {
    actions.push({
      label: "Delete server", kind: "danger-link",
      onClick: async () => {
        const ok = await confirmDialog("Delete server?",
          `Delete "${server.name}" and its services? Running tunnels will close.`, { confirmLabel: "Delete", danger: true });
        if (!ok) return false;
        await api("DELETE", `/servers/${server.id}`);
      },
    });
  }
  actions.push({ label: "Cancel" }, { label: server ? "Save" : "Add server", kind: "primary", submit: true, onClick: save });

  const result = await openDialog({
    title: server ? "Edit server" : "Add server",
    wide: true,
    body: [
      field("Name", name),
      h("div", { class: "row" }, field("Host", host), field("SSH port", port)),
      field("Username", user),
      field("Log in with", type),
      agentNote, keyPathField, privateKeyField, passphraseField, clearPass.el, passwordField,
    ],
    actions,
  });

  if (savedServer && (result === "Add server" || result === "Save")) {
    await testServer(savedServer, { quiet: false });
  }
}

/** Connects once to check the address, fingerprint and login. */
export async function testServer(server, { quiet = false } = {}) {
  try {
    const ok = await withHostKeys(() => api("POST", `/servers/${server.id}/test`));
    if (ok) toast(`Connected to ${server.name}.`, "success");
    return Boolean(ok);
  } catch (err) {
    if (!quiet) toast(`${server.name}: ${err.message}`, "error");
    return false;
  }
}

// --- Services ---------------------------------------------------------------

export async function serviceDialog(serverId, service) {
  const label = text(service?.label, { maxlength: 100, placeholder: "e.g. n8n, Portainer, Grafana" });
  const remoteHost = text(service?.remoteHost ?? "127.0.0.1", { maxlength: 253 });
  const remotePort = number(service?.remotePort, { min: 1, max: 65535, placeholder: "e.g. 5678" });
  const localPort = number(service?.localPort || "", { min: 1, max: 65535, placeholder: "Auto" });
  const protocol = select([["http", "http"], ["https", "https"]], service?.protocol || "http");
  const path = text(service?.path, { maxlength: 1024, placeholder: "/ (optional, e.g. /admin)" });
  const auto = checkbox("Start this tunnel automatically after unlocking", Boolean(service?.autoStart));

  const save = () => {
    const body = {
      serverId, label: label.value, remoteHost: remoteHost.value, remotePort: int(remotePort),
      localPort: int(localPort), protocol: protocol.value, path: path.value, autoStart: auto.input.checked,
    };
    return service ? api("PUT", `/services/${service.id}`, body) : api("POST", "/services", body);
  };
  const actions = [];
  if (service) {
    actions.push({
      label: "Delete service", kind: "danger-link",
      onClick: async () => {
        const ok = await confirmDialog("Delete service?", `Delete "${service.label}"?`, { confirmLabel: "Delete", danger: true });
        if (!ok) return false;
        await api("DELETE", `/services/${service.id}`);
      },
    });
  }
  actions.push({ label: "Cancel" }, { label: service ? "Save" : "Add service", kind: "primary", submit: true, onClick: save });

  await openDialog({
    title: service ? "Edit service" : "Add service",
    wide: true,
    body: [
      field("Name", label),
      h("div", { class: "row" },
        field("Remote host", remoteHost, "As seen from the server; usually 127.0.0.1"),
        field("Remote port", remotePort)),
      h("div", { class: "row" },
        field("Local port", localPort, "Blank = a stable automatic port"),
        field("Protocol", protocol)),
      field("Path", path),
      auto.el,
    ],
    actions,
  });
}

// --- Settings ---------------------------------------------------------------

/** One "Update now" step (an "update" event) in words; "" for none. */
function updateStepText(p) {
  switch (p.step) {
    case "checking": return "Checking for the latest release…";
    case "verifying": return "Checking the signature…";
    case "downloading":
      return p.total > 0
        ? `Downloading ${fmtBytes(p.done)} of ${fmtBytes(p.total)} (${Math.floor((p.done / p.total) * 100)}%)`
        : `Downloading… ${fmtBytes(p.done)}`;
    case "unpacking": return "Download checked; unpacking…";
    case "installing": return "Installing…";
    case "restarting": return "Restarting…";
    default: return "";
  }
}

export async function settingsDialog({ tray = false, knownHosts, minPasswordLen, version, runningTunnels = 0, onRestarting, onChecked, initialTab, servers = [], projects = [] }) {
  const current = await api("GET", "/settings");
  const lock = select(
    [["0", "Never"], ["5", "5 minutes"], ["15", "15 minutes"], ["30", "30 minutes"], ["60", "1 hour"], ["240", "4 hours"]],
    String(current.autoLockMinutes),
  );
  if (![...lock.options].some((o) => o.selected)) {
    lock.appendChild(h("option", { value: String(current.autoLockMinutes), selected: true }, `${current.autoLockMinutes} minutes`));
  }
  const closeOnLock = checkbox("Close all tunnels when TunnelTab locks", current.closeTunnelsOnLock,
    "Otherwise tunnels keep running while locked, and reconnect after you unlock if they drop.");
  const port = number(current.port, { min: 1024, max: 65535 });
  const closeAction = select(
    [["quit", "Quit TunnelTab"], ["tray", tray ? "Keep TunnelTab running in the system tray" : "Keep TunnelTab running"]],
    current.closeAction || "quit",
  );

  const oldPw = password({ autocomplete: "current-password" });
  const newPw = password({ autocomplete: "new-password" });
  const newPw2 = password({ autocomplete: "new-password" });
  const changePw = h("button", { type: "button", class: "btn secondary" }, "Change master password");
  // Shown inside the dialog: toasts would sit behind the modal backdrop.
  const pwStatus = h("span", { class: "inline-status", role: "status" });
  changePw.addEventListener("click", async () => {
    pwStatus.className = "inline-status";
    pwStatus.textContent = "";
    try {
      if (newPw.value !== newPw2.value) throw new Error("The new passwords don't match.");
      if (newPw.value.length < minPasswordLen) throw new Error(`Use at least ${minPasswordLen} characters.`);
      changePw.disabled = true;
      await api("POST", "/vault/password", { old: oldPw.value, new: newPw.value });
      oldPw.value = newPw.value = newPw2.value = "";
      pwStatus.classList.add("ok");
      pwStatus.textContent = "Master password changed.";
    } catch (err) {
      pwStatus.classList.add("bad");
      pwStatus.textContent = err.message;
    } finally {
      changePw.disabled = false;
    }
  });

  // Updates: checked, and installed, only when the user clicks (TunnelTab
  // never checks by itself). "Update now" installs only signed releases.
  const updateBtn = h("button", { type: "button", class: "btn secondary" }, "Check for updates");
  const updateStatus = h("span", { class: "inline-status", role: "status" });
  updateBtn.addEventListener("click", async () => {
    updateBtn.disabled = true;
    updateStatus.className = "inline-status";
    updateStatus.replaceChildren("Checking…");
    try {
      const r = await api("POST", "/updates/check");
      onChecked?.(r.newer ? r.latest : null);
      const releaseLink = (text) =>
        typeof r.url === "string" && r.url.startsWith("https://github.com/Aerobit/TunnelTab/releases/")
          ? h("a", { href: r.url, target: "_blank", rel: "noopener noreferrer" }, text)
          : null;
      if (r.noRelease) {
        updateStatus.replaceChildren("No release has been published yet.");
      } else if (r.newer) {
        const date = r.publishedAt ? ` (released ${new Date(r.publishedAt).toLocaleDateString()})` : "";
        updateStatus.classList.add("ok");
        updateStatus.replaceChildren(r.testOf ? `You're running a test build made after TunnelTab ${r.testOf}. ` : "",
          `TunnelTab ${r.latest} is available${date}. `, releaseLink("Release notes and download"));
        if (r.canInstall) {
          const installBtn = h("button", { type: "button", class: "btn primary small" }, "Update now");
          installBtn.addEventListener("click", () => installUpdate(r.latest, installBtn));
          updateStatus.append(" ", installBtn);
        }
      } else if (r.testOf) {
        updateStatus.replaceChildren(`This is a test build made after TunnelTab ${r.testOf}. There's no newer release yet. `,
          `(To go back to ${r.testOf} itself, download it by hand.) `, releaseLink("Releases"));
      } else if (r.devBuild) {
        updateStatus.replaceChildren(`This is a development build. The latest release is ${r.latest}. `, releaseLink("View it"));
      } else {
        updateStatus.classList.add("ok");
        updateStatus.replaceChildren(`You're up to date (${r.current}).`);
      }
    } catch (err) {
      updateStatus.classList.add("bad");
      updateStatus.replaceChildren(err.message);
    } finally {
      updateBtn.disabled = false;
    }
  });

  async function installUpdate(latest, installBtn) {
    const tunnels = runningTunnels
      ? `${runningTunnels} running tunnel${runningTunnels === 1 ? "" : "s"} and any open terminals will close. `
      : "Any open terminals will close. ";
    if (!(await confirmDialog("Update TunnelTab",
      `TunnelTab will download ${latest}, check that it's signed by TunnelTab, and restart. ` +
      tunnels + "Afterwards, unlock with your master password as usual. Your data isn't changed.",
      { confirmLabel: "Update now" }))) return;
    installBtn.disabled = updateBtn.disabled = true;
    updateStatus.className = "inline-status";
    const bar = h("progress", { class: "update-progress", "aria-label": "Update progress" }); // no value: busy
    const stepText = h("span", {}, "Checking for the latest release…");
    updateStatus.replaceChildren(h("span", { class: "update-step" }, bar, stepText));
    const onProgress = (e) => {
      const p = { done: 0, total: 0, ...e.detail }; // zeros are left out of the event
      const text = updateStepText(p);
      if (!text) return;
      stepText.textContent = text;
      if (p.step === "downloading" && p.total > 0) {
        bar.max = p.total;
        bar.value = Math.min(p.done, p.total);
      } else if (p.step !== "downloading") {
        bar.removeAttribute("value"); // busy, without a known end
      }
    };
    window.addEventListener("tunneltab-update", onProgress);
    try {
      const res = await api("POST", "/updates/install");
      onRestarting?.(res.version);
    } catch (err) {
      updateStatus.classList.add("bad");
      updateStatus.replaceChildren(err.message);
      installBtn.disabled = updateBtn.disabled = false;
    } finally {
      window.removeEventListener("tunneltab-update", onProgress);
    }
  }

  await openDialog({
    title: "Settings",
    wide: true,
    body: tabs([
      { label: "General", content: [
        h("h3", {}, "Security"),
        field("Lock after inactivity", lock),
        closeOnLock.el,
        h("h3", {}, "Dashboard"),
        field("Dashboard port", port, "Takes effect the next time TunnelTab starts."),
        field("When the dashboard tab is closed", closeAction, tray
          ? "Quit: TunnelTab stops a few seconds after the last TunnelTab tab closes (reloading is fine). Keep running: tunnels and terminals stay up; click the TunnelTab icon in the tray to open the dashboard again."
          : "Quit: TunnelTab stops a few seconds after the last TunnelTab tab closes (reloading is fine). Keep running: tunnels and terminals stay up; start TunnelTab again to open the dashboard (this desktop shows no tray icon)."),
      ] },
      { label: "Password", content: [
        h("p", { class: "hint" }, "Changes the password that unlocks TunnelTab. It can't be recovered, so keep it safe."),
        h("div", { class: "row" }, field("Current", oldPw), field("New", newPw), field("Repeat new", newPw2)),
        h("div", { class: "inline-actions" }, changePw, pwStatus),
      ] },
      { label: "Updates", content: [
        h("p", { class: "hint" }, `You're running TunnelTab ${version}. Checking asks GitHub for the latest release. An update is downloaded only when you click Update now, and installed only if it's signed by TunnelTab. TunnelTab never checks on its own.`),
        h("div", { class: "inline-actions" }, updateBtn, updateStatus),
      ] },
      { label: "Servers", content: renderServerSettings(servers, projects, knownHosts) },
    ], initialTab),
    actions: [
      { label: "Close" },
      { label: "Save settings", kind: "primary", submit: true,
        onClick: async () => {
          const res = await api("PUT", "/settings", {
            port: int(port, current.port), autoLockMinutes: Number(lock.value), closeTunnelsOnLock: closeOnLock.input.checked,
            closeAction: closeAction.value,
          });
          toast(res.restartRequired ? "Settings saved. The new port applies after restarting TunnelTab." : "Settings saved.", "success");
        } },
    ],
  });
}

/** The known_hosts address of a server, as model.HostKeyAddress makes it. */
const hostKeyAddress = (host, port) => (port === 22 ? host : `[${host}]:${port}`);

/**
 * Settings → Servers: per server, its health switch (off by default) and the
 * fingerprint confirmed for its address, with Forget. Fingerprints no server
 * uses any more are listed below.
 */
function renderServerSettings(servers, projects, knownHosts) {
  const status = h("span", { class: "inline-status", role: "status" });
  const say = (ok, text) => {
    status.className = `inline-status ${ok ? "ok" : "bad"}`;
    status.textContent = text;
  };
  const projectName = new Map(projects.map((p) => [p.id, p.name]));
  const keyOf = new Map(knownHosts.map((kh) => [kh.host, kh.key]));
  // Rows showing each address's fingerprint: several servers can share one.
  const shown = new Map(); // address → [{fp, forget}]

  async function forget(address, label) {
    if (!(await confirmDialog("Forget server key?",
      `The next connection to ${label} will ask you to confirm its fingerprint again.`, { confirmLabel: "Forget" }))) return;
    try {
      await api("POST", "/hostkeys/forget", { host: address });
      for (const row of shown.get(address) || []) {
        row.fp.textContent = "Fingerprint not confirmed yet";
        row.forget.remove();
      }
      say(true, `${label}: fingerprint forgotten.`);
    } catch (err) {
      say(false, err.message);
    }
  }

  function fingerprintRow(address, label) {
    const key = keyOf.get(address);
    const fp = h("code", {}, key ? "…" : "Fingerprint not confirmed yet");
    if (!key) return { fp, forget: null };
    fingerprint(key).then((f) => (fp.textContent = f));
    const btn = h("button", { type: "button", class: "btn small secondary", "aria-label": `Forget the fingerprint of ${label}` }, "Forget");
    btn.addEventListener("click", () => forget(address, label));
    const row = { fp, forget: btn };
    shown.set(address, [...(shown.get(address) || []), row]);
    return row;
  }

  const list = h("ul", { class: "host-list server-settings" },
    servers.length
      ? servers.map((srv) => {
        const health = checkbox("Health", srv.healthEnabled);
        health.input.setAttribute("aria-label", `Health for ${srv.name}`);
        health.input.addEventListener("change", async () => {
          health.input.disabled = true;
          try {
            await api("PUT", `/servers/${encodeURIComponent(srv.id)}/health`, { enabled: health.input.checked });
            say(true, `${srv.name}: health ${health.input.checked ? "on" : "off"}.`);
          } catch (err) {
            health.input.checked = !health.input.checked;
            say(false, err.message);
          } finally {
            health.input.disabled = false;
          }
        });
        const { fp, forget: forgetBtn } = fingerprintRow(hostKeyAddress(srv.host, srv.port), srv.name);
        return h("li", {},
          h("div", { class: "server-settings-name" },
            h("strong", {}, srv.name), " ", h("span", { class: "muted" }, projectName.get(srv.projectId) || ""), h("br"), fp),
          h("div", { class: "inline-actions" }, health.el, forgetBtn));
      })
      : h("li", { class: "hint" }, "No servers yet."));

  const used = new Set(servers.map((srv) => hostKeyAddress(srv.host, srv.port)));
  const others = knownHosts.filter((kh) => !used.has(kh.host));
  const otherList = others.length
    ? [h("h3", {}, "Other fingerprints"),
      h("p", { class: "hint" }, "Confirmed for addresses no server uses any more."),
      h("ul", { class: "host-list" }, others.map((kh) => {
        const { fp, forget: forgetBtn } = fingerprintRow(kh.host, kh.host);
        return h("li", {}, h("div", {}, h("strong", {}, kh.host), h("br"), fp), forgetBtn);
      }))]
    : null;

  return [
    h("p", { class: "hint" }, "Health: ticked, a server's CPU load, memory, disk use and uptime are read every 30 seconds while it's already connected (for a service or a terminal) and this dashboard is open. It never connects just for this; to look at a server without one, use Connect on its Health box. It runs one fixed, read-only command (Linux servers): ",
      h("code", {}, "cat /proc/loadavg /proc/meminfo /proc/uptime; nproc; df -P -k"),
      ". Readings stay in memory."),
    h("p", { class: "hint" }, "Fingerprint: the server key you confirmed, stored in the encrypted vault. Forget makes the next connection ask you to confirm it again."),
    list,
    otherList,
    status,
  ];
}

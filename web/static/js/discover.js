// discover.js — "Find services": asks before scanning a server, shows what
// the scan found, and adds only the services the user ticks.

import { api } from "./api.js";
import { h } from "./dom.js";
import { openDialog, toast } from "./dialogs.js";
import { withHostKeys } from "./forms.js";

const DOCKER_NOTE = {
  denied: "Docker is installed, but your user on this server may not use it, so containers aren't named. Adding the user to the docker group would show them.",
  stopped: "Docker is installed but not running, so no containers are listed.",
  error: "Couldn't list Docker containers.",
};

/** Asks first, then scans the server and shows the results. */
export async function findServices(server) {
  let result = null;
  const progress = scanProgress(server);
  await openDialog({
    title: `Find services on ${server.name}`,
    body: [
      h("p", {}, "TunnelTab will connect to ", h("strong", {}, server.name),
        " and run one read-only command that lists its open ports and Docker containers (",
        h("code", {}, "ss"), " or ", h("code", {}, "netstat"), ", and ", h("code", {}, "docker ps"), ")."),
      h("p", { class: "hint" }, "Then it checks whether each port that may be a web app answers, with one request for its front page (a HEAD request: nothing is sent to log in or change anything)."),
      h("p", { class: "hint" }, "Nothing is changed on the server, the list isn't saved, and nothing is added until you tick it."),
      progress.el,
    ],
    actions: [
      { label: "Cancel" },
      {
        label: "Find services", kind: "primary", submit: true,
        onClick: async () => {
          progress.start();
          try {
            const r = await withHostKeys(() => api("POST", `/servers/${encodeURIComponent(server.id)}/discover`));
            if (!r) return false; // fingerprint not confirmed
            result = r;
          } finally {
            progress.stop();
          }
        },
      },
    ],
  });
  if (result) await resultsDialog(server, result);
}

/**
 * The progress line shown while a scan runs: a bar and the current step,
 * from the program's "discover" events (connecting, scanning, then each
 * port checked). Hidden until start().
 */
function scanProgress(server) {
  const bar = h("progress", { class: "update-progress", "aria-label": "Find services progress" }); // no value: busy
  const text = h("span", {}, "");
  const el = h("p", { class: "update-step scan-progress", role: "status", hidden: true }, bar, text);
  const show = (step, done = 0, total = 0) => {
    if (step === "checking" && total > 0) {
      bar.max = total;
      bar.value = done;
      text.textContent = `Checking whether each app answers (${done} of ${total})…`;
      return;
    }
    bar.removeAttribute("value");
    text.textContent = step === "scanning"
      ? "Listing open ports and Docker containers…"
      : `Connecting to ${server.name}…`;
  };
  const onEvent = (e) => {
    const p = e.detail;
    if (p.serverId === server.id) show(p.step, p.done, p.total);
  };
  return {
    el,
    start() {
      show("connecting");
      el.hidden = false;
      window.addEventListener("tunneltab-discover", onEvent);
    },
    stop() {
      window.removeEventListener("tunneltab-discover", onEvent);
      el.hidden = true;
    },
  };
}

/**
 * Whether a candidate answered like a web page when checked: true, false,
 * or null when it wasn't checked (known non-web ports).
 */
const answers = (c) => (c.check ? c.check.state === "responding" : null);

/** Web apps and unknown ports that answered go in the main list; the rest below. */
const isWeb = (c) => c.kind !== "other" && !(c.kind === "maybe" && answers(c) === false);

/** What the check found, in words. */
const CHECK_TEXT = { responding: "answers", not_web: "answers, but not as a web page", no_answer: "didn't answer" };

/** Where a candidate was found, in words. */
function foundAs(c) {
  const parts = [];
  if (c.container) parts.push(`Docker container ${c.container}${c.image ? ` (${c.image})` : ""}`);
  else if (c.process) parts.push(`program ${c.process}`);
  if (c.kind === "other") parts.push(c.app ? `${c.app}, not a web page` : "not a web page");
  // Listening only on the server itself is the usual, safe case: not mentioned.
  if (c.listen === "all") parts.push("open on all addresses");
  else if (c.listen === "other") parts.push(`only on ${c.host}`);
  const text = parts.join(" · ") || "listening port";
  if (!c.check) return text;
  const ok = c.check.state === "responding";
  return [h("span", { class: ["svc-check", ok ? "ok" : "bad"] }, `${ok ? "✓" : "✗"} ${CHECK_TEXT[c.check.state]}`), " · ", text];
}

function candidateRow(c) {
  const tick = h("input", {
    type: "checkbox", checked: !c.added && (answers(c) ?? c.kind === "web"), disabled: c.added,
    "aria-label": `Add port ${c.port}`,
  });
  const name = h("input", {
    type: "text", class: "discover-name", value: c.name, maxlength: 100, autocomplete: "off", spellcheck: "false",
    placeholder: `Port ${c.port}`, "aria-label": `Name for port ${c.port}`, disabled: c.added,
  });
  const protocol = h("select", { "aria-label": `Protocol for port ${c.port}`, disabled: c.added },
    ["http", "https"].map((p) => h("option", { value: p, selected: p === (answers(c) ? c.check.protocol : c.protocol) }, p)));
  // Typing a name ticks the row: you'd only name what you want to add.
  name.addEventListener("input", () => { if (name.value.trim()) tick.checked = true; });
  const row = h("tr", { class: c.added ? "added" : null },
    h("td", {}, tick),
    h("td", {}, name),
    h("td", { class: "mono" }, String(c.port), c.path ? h("span", { class: "muted" }, c.path) : null),
    h("td", {}, protocol),
    h("td", { class: "muted" }, c.added ? "Already added" : foundAs(c)));
  return {
    row, tick, name,
    service: () => ({
      label: name.value.trim(), remoteHost: c.host, remotePort: c.port, protocol: protocol.value, path: c.path || "",
    }),
  };
}

function table(rows) {
  return h("div", { class: "table-wrap" }, h("table", { class: "table discover" },
    h("thead", {}, h("tr", {},
      h("th", { scope: "col" }, h("span", { class: "sr-only" }, "Add")),
      h("th", { scope: "col" }, "Name"), h("th", { scope: "col" }, "Port"),
      h("th", { scope: "col" }, "Protocol"), h("th", { scope: "col" }, "Found as"))),
    h("tbody", {}, rows.map((r) => r.row))));
}

async function resultsDialog(server, result) {
  const rows = result.candidates.map(candidateRow);
  const web = rows.filter((_, i) => isWeb(result.candidates[i]));
  const other = rows.filter((_, i) => !isWeb(result.candidates[i]));
  const notes = [
    DOCKER_NOTE[result.docker] ? h("p", { class: "hint" }, DOCKER_NOTE[result.docker]) : null,
    result.truncated ? h("p", { class: "hint" }, `Only the first ${result.candidates.length} ports are shown.`) : null,
  ];
  const addable = rows.some((r) => !r.tick.disabled);

  const add = async () => {
    const picked = rows.filter((r) => r.tick.checked && !r.tick.disabled);
    if (!picked.length) throw new Error("Tick the services to add.");
    const unnamed = picked.find((r) => !r.name.value.trim());
    if (unnamed) {
      unnamed.name.focus();
      throw new Error("Give every ticked service a name.");
    }
    await api("POST", `/servers/${encodeURIComponent(server.id)}/services`, { services: picked.map((r) => r.service()) });
    toast(picked.length === 1 ? "Added 1 service." : `Added ${picked.length} services.`, "success");
  };

  await openDialog({
    title: `Services found on ${server.name}`,
    wide: "xl",
    body: rows.length
      ? [
        h("p", { class: "hint" }, "Tick the ones to add. TunnelTab checked each port: apps that answered like a web page are ticked already, set to http or https as they answered. Names can be changed here or later."),
        web.length ? table(web) : h("p", {}, "No web apps found."),
        other.length
          ? h("details", { class: "discover-other" },
            h("summary", {}, `Not web pages (${other.length})`),
            table(other))
          : null,
        notes,
      ]
      : [h("p", {}, "No open ports found."), notes],
    actions: addable
      ? [{ label: "Cancel" }, { label: "Add selected", kind: "primary", submit: true, onClick: add }]
      : [{ label: "Close", kind: "primary" }],
  });
}

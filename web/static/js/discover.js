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
  await openDialog({
    title: `Find services on ${server.name}`,
    body: [
      h("p", {}, "TunnelTab will connect to ", h("strong", {}, server.name),
        " and run one read-only command that lists its open ports and Docker containers (",
        h("code", {}, "ss"), " or ", h("code", {}, "netstat"), ", and ", h("code", {}, "docker ps"), ")."),
      h("p", { class: "hint" }, "Nothing is changed on the server, the list isn't saved, and nothing is added until you tick it."),
    ],
    actions: [
      { label: "Cancel" },
      {
        label: "Find services", kind: "primary", submit: true,
        onClick: async () => {
          const r = await withHostKeys(() => api("POST", `/servers/${encodeURIComponent(server.id)}/discover`));
          if (!r) return false; // fingerprint not confirmed
          result = r;
        },
      },
    ],
  });
  if (result) await resultsDialog(server, result);
}

/** Where a candidate was found, in words. */
function foundAs(c) {
  const parts = [];
  if (c.container) parts.push(`Docker container ${c.container}${c.image ? ` (${c.image})` : ""}`);
  else if (c.process) parts.push(`program ${c.process}`);
  if (c.kind === "other") parts.push(c.app ? `${c.app}, not a web page` : "not a web page");
  // Listening only on the server itself is the usual, safe case: not mentioned.
  if (c.listen === "all") parts.push("open on all addresses");
  else if (c.listen === "other") parts.push(`only on ${c.host}`);
  return parts.join(" · ") || "listening port";
}

function candidateRow(c) {
  const tick = h("input", {
    type: "checkbox", checked: c.kind === "web" && !c.added, disabled: c.added,
    "aria-label": `Add port ${c.port}`,
  });
  const name = h("input", {
    type: "text", class: "discover-name", value: c.name, maxlength: 100, autocomplete: "off", spellcheck: "false",
    placeholder: `Port ${c.port}`, "aria-label": `Name for port ${c.port}`, disabled: c.added,
  });
  const protocol = h("select", { "aria-label": `Protocol for port ${c.port}`, disabled: c.added },
    ["http", "https"].map((p) => h("option", { value: p, selected: p === c.protocol }, p)));
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
  const web = rows.filter((_, i) => result.candidates[i].kind !== "other");
  const other = rows.filter((_, i) => result.candidates[i].kind === "other");
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
        h("p", { class: "hint" }, "Tick the ones to add. Web apps TunnelTab recognises are ticked already; names can be changed here or later."),
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

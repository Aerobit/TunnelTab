// Generates the screenshots shown in README.md (saved to docs/images/).
//
//   cd tests/e2e && npm ci && npx playwright install chromium && node readme-shots.js
//
// Everything shown is made-up demo data: the only real connection is to the
// local fakessh demo server (-demo mode: realistic prompt and canned command
// output). The other servers use reserved example.com names and are added
// through the API without ever connecting. Re-run whenever the UI changes.

const { chromium } = require("playwright");
const { spawn, execFileSync } = require("child_process");
const readline = require("readline");
const path = require("path");
const os = require("os");
const fs = require("fs");

const repo = path.resolve(__dirname, "..", "..");
const OUT = path.join(repo, "docs", "images");
fs.mkdirSync(OUT, { recursive: true });

const children = [];
process.on("exit", () => children.forEach((c) => { try { c.kill("SIGINT"); } catch {} }));

function start(cmd, args) {
  const p = spawn(cmd, args, { stdio: ["ignore", "pipe", "pipe"] });
  children.push(p);
  const lines = [];
  const waiters = [];
  const onLine = (l) => { lines.push(l); waiters.forEach((w) => w()); };
  readline.createInterface({ input: p.stdout }).on("line", onLine);
  readline.createInterface({ input: p.stderr }).on("line", onLine);
  p.waitFor = (re, ms = 60000) => new Promise((resolve, reject) => {
    const t = setTimeout(() => reject(new Error(`timeout waiting for ${re}:\n${lines.join("\n")}`)), ms);
    const check = () => { const m = lines.join("\n").match(re); if (m) { clearTimeout(t); resolve(m); } };
    waiters.push(check); check();
  });
  return p;
}

(async () => {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "tunneltab-shots-"));
  const exe = process.platform === "win32" ? ".exe" : "";
  for (const [out, pkg] of [["tunneltab", "./cmd/tunneltab"], ["fakessh", "./internal/devtools/fakessh"]]) {
    execFileSync("go", ["build", "-ldflags", "-X main.version=0.1.0", "-o", path.join(tmp, out + exe), pkg], { cwd: repo, stdio: "inherit" });
  }
  const fake = start(path.join(tmp, "fakessh" + exe), ["-demo", "-port", "2222"]);
  const [, webPort] = await fake.waitFor(/remote port (\d+)/);
  const app = start(path.join(tmp, "tunneltab" + exe), ["--no-browser", "--port", "47901", "--data", path.join(tmp, "data")]);
  const [url] = await app.waitFor(/http:\/\/127\.0\.0\.1:47901\/\?launch=\S+/);

  const browser = await chromium.launch();
  const context = await browser.newContext({ viewport: { width: 1200, height: 740 }, deviceScaleFactor: 2 });
  const page = await context.newPage();
  const shot = (p, name, opts = {}) => p.screenshot({ path: path.join(OUT, name + ".png"), ...opts });
  const clearToasts = () => page.evaluate(() => document.querySelectorAll(".toast").forEach((t) => t.remove()));

  // Setup screen, with a passphrase typed in (shown as dots).
  await page.goto(url);
  await page.getByLabel("Master password").fill("correct horse battery staple");
  await page.getByLabel("Repeat it").fill("correct horse battery staple");
  await shot(page, "setup");
  await page.getByRole("button", { name: "Create vault" }).click();
  await page.getByText("Add your first project").waitFor();

  // Demo data through the API (no connections are made).
  const ids = await page.evaluate(async (webPort) => {
    const token = localStorage.getItem("tunneltab.session");
    const call = async (method, p, body) => {
      const r = await fetch("/api" + p, { method, headers: { Authorization: "Bearer " + token, "Content-Type": "application/json" }, body: JSON.stringify(body) });
      if (!r.ok) throw new Error(p + ": " + (await r.text()));
      return r.status === 204 ? null : r.json();
    };
    const home = await call("POST", "/projects", { name: "Home lab", description: "Self-hosted apps at home" });
    const clients = await call("POST", "/projects", { name: "Client sites", description: "Websites I host for clients" });
    const homelab = await call("POST", "/servers", { projectId: home.id, name: "homelab", host: "localhost", port: 2222, username: "demo", auth: { type: "password", password: "demo-password" } });
    const nas = await call("POST", "/servers", { projectId: home.id, name: "NAS", host: "nas.example.com", port: 22, username: "admin", auth: { type: "agent" } });
    const web = await call("POST", "/servers", { projectId: clients.id, name: "Web VPS", host: "vps1.example.com", port: 22, username: "deploy", auth: { type: "keyFile", keyPath: "keys/web-vps_ed25519" } });
    const staging = await call("POST", "/servers", { projectId: clients.id, name: "Staging", host: "staging.example.com", port: 2200, username: "deploy", auth: { type: "agent" } });
    const n8n = await call("POST", "/services", { serverId: homelab.id, label: "n8n", remotePort: Number(webPort), localPort: 5678 });
    const grafana = await call("POST", "/services", { serverId: homelab.id, label: "Grafana", remotePort: 3000, localPort: 3000 });
    await call("POST", "/services", { serverId: homelab.id, label: "Portainer", remotePort: 9443, protocol: "https" });
    await call("POST", "/services", { serverId: nas.id, label: "File browser", remotePort: 8080 });
    await call("POST", "/services", { serverId: web.id, label: "Admin panel", remotePort: 8080, path: "/admin" });
    await call("POST", "/services", { serverId: staging.id, label: "Analytics", remotePort: 8000 });
    return { homelab: homelab.id, n8n: n8n.id, grafana: grafana.id };
  }, webPort);
  await page.getByRole("heading", { name: "Client sites" }).waitFor();

  // Fingerprint confirmation (first connection to homelab), from its page.
  await page.locator(".side-server", { hasText: "homelab" }).getByRole("link").click();
  await page.getByRole("heading", { name: "homelab", level: 1 }).waitFor();
  await page.locator(".page-head").getByRole("button", { name: /More actions/ }).click();
  await page.getByRole("menuitem", { name: "Test connection" }).click();
  await page.getByRole("heading", { name: "Confirm new server" }).waitFor();
  await shot(page, "fingerprint");
  await page.getByRole("button", { name: "Trust and connect" }).click();
  await page.getByText("Connected to homelab.").waitFor();

  // Two running tunnels (Services tab), then the server page and the Overview.
  await page.locator(".page-tabs").getByRole("link", { name: /Services/ }).click();
  for (const name of ["n8n", "Grafana"]) {
    await page.locator(".service", { hasText: name }).getByRole("button", { name: "Start" }).click();
    await page.locator(".service", { hasText: name }).locator(".pill.active").waitFor();
  }
  await clearToasts();
  await page.mouse.move(0, 0);
  await shot(page, "services");
  await page.locator(".page-tabs").getByRole("link", { name: "Overview" }).click();
  await page.mouse.move(0, 0);
  await shot(page, "server");
  await page.getByRole("link", { name: "Overview" }).first().click();
  await page.getByRole("heading", { name: "Overview", level: 1 }).waitFor();
  await page.mouse.move(0, 0);
  await shot(page, "dashboard");

  // Adding a server (form only; cancelled).
  await page.locator(".side-project", { hasText: "Client sites" }).getByRole("button", { name: /More actions/ }).click();
  await page.getByRole("menuitem", { name: "Add server" }).click();
  await page.getByLabel("Name", { exact: true }).fill("New VPS");
  await page.getByLabel("Host", { exact: true }).fill("vps2.example.com");
  await page.getByLabel("Username").fill("deploy");
  await page.getByLabel("Log in with").selectOption("keyVault");
  await shot(page, "add-server");
  await page.getByRole("button", { name: "Cancel" }).click();

  // Terminal, inside the dashboard.
  await page.locator(".side-server", { hasText: "homelab" }).getByRole("link").click();
  await page.locator(".page-head").getByRole("button", { name: "+ New terminal" }).click();
  await page.locator(".term-pane .xterm-rows", { hasText: "demo@homelab" }).waitFor();
  for (const cmd of ["uptime", "df -h", "docker ps"]) {
    await page.keyboard.type(cmd, { delay: 15 });
    await page.keyboard.press("Enter");
    await page.waitForTimeout(250);
  }
  await clearToasts();
  await page.waitForTimeout(300);
  await shot(page, "terminal");

  // Settings.
  await page.getByRole("button", { name: /Settings/ }).click();
  await page.getByRole("tab", { name: "Servers" }).click();
  await page.locator(".host-list code").filter({ hasText: "SHA256:" }).waitFor();
  await shot(page, "settings");
  await page.getByRole("dialog").getByRole("button", { name: "Close", exact: true }).click();

  // Unlock screen.
  await page.getByRole("button", { name: "Lock" }).click();
  await page.getByRole("heading", { name: "Unlock TunnelTab" }).waitFor();
  await shot(page, "unlock");

  await browser.close();
  console.log("Screenshots written to", OUT);
  for (const f of fs.readdirSync(OUT)) console.log(" ", f, Math.round(fs.statSync(path.join(OUT, f)).size / 1024) + " KB");
  process.exit(0);
})().catch((e) => { console.error("FAILED:", e.message); process.exit(1); });

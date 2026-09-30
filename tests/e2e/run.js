// End-to-end walkthrough of the TunnelTab dashboard in a real (headless)
// browser, against the real program and the fakessh development server.
//
//   cd tests/e2e && npm ci && npx playwright install chromium && node run.js
//
// It builds both programs, then: creates a vault, a project, a server (with
// fingerprint confirmation) and a service; opens the web app through the
// tunnel; checks a tunneled page can't reach the dashboard's session or API;
// changes the master password; locks/unlocks; edits; stops; quits. It fails
// on any JavaScript error or Content Security Policy violation. Screenshots
// go to tests/e2e/screenshots/ (git-ignored).
const { chromium } = require("playwright");
const { spawn } = require("child_process");
const readline = require("readline");
const assert = require("assert");

const path = require("path");
const os = require("os");
const { execFileSync } = require("child_process");
const OUT = path.join(__dirname, "screenshots");
require("fs").mkdirSync(OUT, { recursive: true });

const children = [];
process.on("exit", () => children.forEach((c) => { try { c.kill("SIGINT"); } catch {} }));
function start(cmd, args, opts) {
  const p = spawn(cmd, args, { ...opts, stdio: ["ignore", "pipe", "pipe"] });
  children.push(p);
  const lines = [];
  const waiters = [];
  const onLine = (l) => { lines.push(l); waiters.forEach((w) => w()); };
  readline.createInterface({ input: p.stdout }).on("line", onLine);
  readline.createInterface({ input: p.stderr }).on("line", (l) => { onLine(l); });
  p.waitFor = (re, ms = 60000) => new Promise((resolve, reject) => {
    const t = setTimeout(() => reject(new Error(`timeout waiting for ${re}; got:\n${lines.join("\n")}`)), ms);
    const check = () => { const m = lines.join("\n").match(re); if (m) { clearTimeout(t); resolve(m); } };
    waiters.push(check); check();
  });
  return p;
}

(async () => {
  const repo = path.resolve(__dirname, "..", "..");
  const tmp = require("fs").mkdtempSync(path.join(os.tmpdir(), "tunneltab-e2e-"));
  const exe = process.platform === "win32" ? ".exe" : "";
  for (const [out, pkg] of [["tunneltab", "./cmd/tunneltab"], ["fakessh", "./internal/devtools/fakessh"]]) {
    execFileSync("go", ["build", "-ldflags", "-X main.version=0.1.0", "-o", path.join(tmp, out + exe), pkg], { cwd: repo, stdio: "inherit" });
  }
  const fake = start(path.join(tmp, "fakessh" + exe), [], {});
  const [, host, sshPort] = await fake.waitFor(/Host:\s+(\S+)\n\s+SSH port:\s+(\d+)/);
  const [, fpExpected] = await fake.waitFor(/Fingerprint to expect: (\S+)/);
  const [, webPort] = await fake.waitFor(/remote port (\d+)/);

  let updateRequests = 0;
  const fakeGitHub = require("http").createServer((req, res) => {
    updateRequests++;
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify({ tag_name: "v99.0.0", html_url: "https://github.com/Aerobit/TunnelTab/releases/tag/v99.0.0",
      published_at: "2026-10-15T10:00:00Z", draft: false, prerelease: false }));
  });
  await new Promise((r) => fakeGitHub.listen(0, "127.0.0.1", r));
  const updateURL = `http://127.0.0.1:${fakeGitHub.address().port}/latest`;

  const dataDir = path.join(tmp, "data");
  const app = start(path.join(tmp, "tunneltab" + exe), ["--no-browser", "--port", "47900", "--data", dataDir, "--update-url", updateURL]);
  const [url] = await app.waitFor(/http:\/\/127\.0\.0\.1:47900\/\?launch=\S+/);

  const browser = await chromium.launch();
  const context = await browser.newContext({ viewport: { width: 1200, height: 800 } });
  const page = await context.newPage();
  const problems = [];
  page.on("console", (m) => m.type() === "error" && !m.text().startsWith("Failed to load resource") && problems.push("console: " + m.text()));
  page.on("pageerror", (e) => problems.push("pageerror: " + e.message));
  await page.addInitScript(() => document.addEventListener("securitypolicyviolation",
    (e) => console.error("CSP violation: " + e.violatedDirective + " " + e.blockedURI)));
  const shot = (name) => page.screenshot({ path: `${OUT}/${name}.png`, fullPage: true });
  const step = (s) => console.log("✓", s);

  // 1. First run: create the vault.
  await page.goto(url);
  await page.getByRole("heading", { name: "Welcome" }).waitFor();
  assert.ok(!page.url().includes("launch="), "launch token left in the address bar");
  await shot("01-setup");
  await page.getByLabel("Master password").fill("short");
  await page.getByLabel("Repeat it").fill("short");
  await page.getByRole("button", { name: "Create vault" }).click();
  await page.getByText("Use at least 8 characters").waitFor();
  await page.getByLabel("Master password").fill("correct horse battery staple");
  await page.getByLabel("Repeat it").fill("correct horse battery staple");
  await page.getByRole("button", { name: "Create vault" }).click();
  await page.getByText("Add your first project").waitFor();
  await shot("02-empty");
  step("vault created");

  // 2. Project.
  await page.getByRole("button", { name: "+ New project" }).click();
  await page.getByLabel("Name", { exact: true }).fill("My VPSs");
  await page.getByLabel("Description").fill("Personal servers");
  await page.getByRole("button", { name: "Create project" }).click();
  await page.getByRole("heading", { name: "My VPSs" }).waitFor();
  step("project created");

  // 3. Server (password login), with fingerprint confirmation.
  await page.getByRole("button", { name: "+ Server" }).click();
  await page.getByLabel("Name", { exact: true }).fill("Demo VPS");
  await page.getByLabel("Host", { exact: true }).fill(host);
  await page.getByLabel("SSH port").fill(sshPort);
  await page.getByLabel("Username").fill("demo");
  await page.getByLabel("Log in with").selectOption("password");
  await page.getByLabel("Password", { exact: true }).fill("demo-password");
  await shot("03-server-form");
  await page.getByRole("button", { name: "Add server" }).click();
  await page.getByRole("heading", { name: "Confirm new server" }).waitFor();
  const fpShown = await page.locator(".fingerprint").textContent();
  assert.ok(fpShown.includes(fpExpected), `fingerprint shown ${fpShown}, expected ${fpExpected}`);
  await shot("04-fingerprint");
  await page.getByRole("button", { name: "Trust and connect" }).click();
  await page.getByText("Connected to Demo VPS.").waitFor();
  step("server added, fingerprint confirmed, login works");

  // 4. Service, then Open through the tunnel.
  await page.getByRole("button", { name: "+ Service" }).click();
  await page.getByLabel("Name", { exact: true }).fill("Demo app");
  await page.getByLabel("Remote port").fill(webPort);
  await page.getByLabel("Path").fill("/admin");
  await page.getByRole("button", { name: "Add service" }).click();
  await page.getByText("Demo app").waitFor();
  const [popup] = await Promise.all([
    context.waitForEvent("page"),
    page.getByRole("button", { name: "Open ↗" }).click(),
  ]);
  await popup.waitForLoadState();
  const popupText = await popup.textContent("body");
  assert.ok(popupText.includes("Hello through the tunnel!") && popupText.includes("/admin"), "tunneled page: " + popupText);
  assert.ok(popup.url().startsWith("http://127.0.0.1:"), "popup url " + popup.url());
  const opener = await popup.evaluate(() => window.opener === null);
  assert.ok(opener, "tunneled page can reach window.opener");
  await popup.close();
  await page.locator(".pill.active").waitFor();
  assert.strictEqual(await page.getByText("is ready").count(), 0, "redundant 'ready' toast after the tab opened");
  await shot("05-running");
  step("service opened through the tunnel (no opener access)");

  // 5. Tunneled page must not be able to use the dashboard session.
  const stolen = await context.newPage();
  await stolen.goto(popup.url());
  const attack = await stolen.evaluate(async () => {
    const token = localStorage.getItem("tunneltab.session");
    let status = "no-request";
    try {
      const r = await fetch("http://127.0.0.1:47900/api/data", { headers: { Authorization: "Bearer x" } });
      status = r.status;
    } catch (e) { status = "blocked: " + e.message; }
    return { token, status };
  });
  assert.strictEqual(attack.token, null, "tunneled origin can read the session token");
  assert.ok(String(attack.status).startsWith("blocked"), "cross-origin request not blocked: " + attack.status);
  await stolen.close();
  step("tunneled web app can't read the session or call the API");

  // 5b. Terminal in a new tab.
  const [termPage] = await Promise.all([
    context.waitForEvent("page"),
    page.getByRole("button", { name: "Terminal ↗" }).click(),
  ]);
  termPage.on("pageerror", (e) => problems.push("terminal pageerror: " + e.message));
  termPage.on("console", (m) => m.type() === "error" && !m.text().startsWith("Failed to load resource") && problems.push("terminal console: " + m.text()));
  await termPage.locator("#term-status", { hasText: "Connected" }).waitFor();
  const termText = () => termPage.locator(".xterm-rows").innerText();
  const waitTerm = async (want) => {
    for (let i = 0; i < 100; i++) {
      if ((await termText()).includes(want)) return;
      await termPage.waitForTimeout(50);
    }
    throw new Error("terminal never showed " + JSON.stringify(want) + ":\n" + (await termText()));
  };
  await waitTerm("Welcome to the fake shell");
  assert.strictEqual(await termPage.title(), "Demo VPS — TunnelTab");
  await termPage.keyboard.type("echo hello from the browser");
  await termPage.keyboard.press("Enter");
  await waitTerm("hello from the browser\n");
  await termPage.keyboard.type("size");
  await termPage.keyboard.press("Enter");
  await waitTerm("x");
  await termPage.screenshot({ path: `${OUT}/07-terminal.png` });
  step("terminal opens in a new tab and runs commands");

  // 5c. Copy and paste: Ctrl+C copies a selection (and doesn't interrupt),
  //     without one it interrupts; right-click copies; Ctrl+V pastes.
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  const clipboard = () => termPage.evaluate(() => navigator.clipboard.readText());
  const interrupts = async () => ((await termText()).match(/\^C/g) || []).length;
  // xterm.js covers the text with an overlay, so click by position.
  const wordAt = async (word) => {
    const box = await termPage.locator(".xterm-rows > div", { hasText: new RegExp("^" + word) }).first().boundingBox();
    return [box.x + 12, box.y + box.height / 2];
  };
  const selectWord = async (word) => termPage.mouse.dblclick(...(await wordAt(word)));
  await termPage.keyboard.type("echo copyme42");
  await termPage.keyboard.press("Enter");
  await waitTerm("copyme42\n");
  await selectWord("copyme42");
  await termPage.keyboard.press("Control+C");
  await termPage.locator("#term-toast", { hasText: "Copied" }).waitFor();
  assert.strictEqual(await clipboard(), "copyme42");
  assert.strictEqual(await interrupts(), 0, "Ctrl+C with a selection interrupted the shell");
  await termPage.keyboard.press("Control+C");
  await waitTerm("^C");
  step("Ctrl+C copies a selection, and interrupts without one");

  await termPage.keyboard.type("echo rightclick7");
  await termPage.keyboard.press("Enter");
  await waitTerm("rightclick7\n");
  await selectWord("rightclick7");
  await termPage.mouse.click(...(await wordAt("rightclick7")), { button: "right" });
  for (let i = 0; i < 40 && (await clipboard()) !== "rightclick7"; i++) await termPage.waitForTimeout(50);
  assert.strictEqual(await clipboard(), "rightclick7", "right-click didn't copy the selection");

  await termPage.evaluate(() => navigator.clipboard.writeText("echo pasted99"));
  await termPage.locator(".xterm-helper-textarea").focus();
  await termPage.keyboard.press("Control+V");
  await termPage.keyboard.press("Enter");
  await waitTerm("pasted99\n");
  step("right-click copies; Ctrl+V pastes");

  // 6. Settings shows the confirmed server.
  await page.getByRole("button", { name: "Settings" }).click();
  await page.getByRole("heading", { name: "Settings" }).waitFor();
  await page.locator(".host-list code").filter({ hasText: "SHA256:" }).waitFor();
  const listedFp = await page.locator(".host-list code").first().textContent();
  assert.ok(listedFp.includes(fpExpected), "settings fingerprint " + listedFp);
  await page.getByLabel("Current").fill("not my password");
  await page.getByLabel("New", { exact: true }).fill("a brand new passphrase");
  await page.getByLabel("Repeat new").fill("a brand new passphrase");
  await page.getByRole("button", { name: "Change master password" }).click();
  await page.locator(".inline-status.bad").filter({ hasText: "wrong" }).waitFor();
  await page.waitForTimeout(1200); // a wrong password starts a 1 s delay
  await page.getByLabel("Current").fill("correct horse battery staple");
  await page.getByLabel("New", { exact: true }).fill("a brand new passphrase");
  await page.getByLabel("Repeat new").fill("a brand new passphrase");
  await page.getByRole("button", { name: "Change master password" }).click();
  await page.locator(".inline-status.ok").waitFor();
  assert.strictEqual(updateRequests, 0, "contacted the release server before being asked");
  await page.getByRole("button", { name: "Check for updates" }).click();
  await page.getByText("TunnelTab 99.0.0 is available").waitFor();
  const notes = page.getByRole("link", { name: "Release notes and download" });
  assert.strictEqual(await notes.getAttribute("href"), "https://github.com/Aerobit/TunnelTab/releases/tag/v99.0.0");
  assert.strictEqual(updateRequests, 1);
  await shot("06-settings");
  await page.getByRole("button", { name: "Close" }).click();
  step("settings: fingerprint listed, master password changed, update check only on click");

  // 7. Lock / unlock while a long job runs in the terminal; the tunnel and the
  //    job keep running.
  await termPage.keyboard.type("count 10");
  await termPage.keyboard.press("Enter");
  await waitTerm("tick 1");
  await page.getByRole("button", { name: "Lock" }).click();
  await page.getByRole("heading", { name: "Unlock TunnelTab" }).waitFor();
  await termPage.locator("#term-message", { hasText: "Your session keeps running" }).waitFor();
  assert.ok(!(await termText()).includes("hello from the browser"), "terminal output still readable while locked");
  assert.ok(!(await termPage.locator("#terminal").isVisible()), "terminal view visible while locked");
  await termPage.screenshot({ path: `${OUT}/08-terminal-locked.png` });
  await termPage.keyboard.type("typed while locked");
  await termPage.waitForTimeout(2500); // the job finishes while locked
  await page.getByLabel("Master password").fill("wrong password");
  await page.getByRole("button", { name: "Unlock" }).click();
  await page.getByText("wrong master password").waitFor();
  await shot("09-unlock-wrong");
  await page.waitForFunction(() => document.querySelector("button[type=submit]")?.textContent === "Unlock", null, { timeout: 5000 });
  await page.getByLabel("Master password").fill("a brand new passphrase");
  await page.getByRole("button", { name: "Unlock" }).click();
  await page.locator(".pill.active").waitFor();
  step("lock and unlock (rate-limited retry), tunnel still running");

  // The terminal re-attaches by itself; the job's output (including what it
  // printed while locked) is replayed; nothing typed while locked got through.
  await termPage.locator("#term-status", { hasText: "Connected" }).waitFor();
  await waitTerm("tick 10");
  await waitTerm("hello from the browser");
  assert.ok(!(await termText()).includes("typed while locked"), "keystrokes while locked reached the shell");
  await termPage.keyboard.type("echo after unlock");
  await termPage.keyboard.press("Enter");
  await waitTerm("after unlock\n");
  step("locking kept the terminal's job running; it re-attached by itself after unlock");

  // Reloading the terminal tab re-attaches to the same session.
  assert.ok(/#[^/]+\/.+/.test(termPage.url()), "terminal id not kept in the address: " + termPage.url());
  await termPage.reload();
  await termPage.locator("#term-status", { hasText: "Connected" }).waitFor();
  await waitTerm("after unlock");
  await termPage.keyboard.type("exit 7");
  await termPage.keyboard.press("Enter");
  await termPage.locator("#term-message", { hasText: "exited with code 7" }).waitFor();
  await termPage.close();
  step("reload re-attaches to the same session; exit code shown");

  const [closing] = await Promise.all([
    context.waitForEvent("page"),
    page.getByRole("button", { name: "Terminal ↗" }).click(),
  ]);
  await closing.locator("#term-status", { hasText: "Connected" }).waitFor();
  const closingId = decodeURIComponent(closing.url().split("#")[1].split("/")[1]);
  await closing.close();
  const sessionGone = async () => page.evaluate(async (id) => {
    const r = await fetch(`/api/terminals/${encodeURIComponent(id)}/attach`, {
      method: "POST", headers: { Authorization: "Bearer " + localStorage.getItem("tunneltab.session") } });
    return r.status === 404;
  }, closingId);
  const closedAt = Date.now();
  while (!(await sessionGone())) {
    assert.ok(Date.now() - closedAt < 30000, "closing the terminal tab didn't end its session");
    await page.waitForTimeout(500);
  }
  step(`closing a terminal tab ends its session (after ${Math.round((Date.now() - closedAt) / 1000)} s)`);

  // 8. Reload keeps the session.
  await page.reload();
  await page.getByRole("heading", { name: "My VPSs" }).waitFor();
  step("reload keeps the session");

  // 9. Edit server keeping the saved password.
  await page.locator(".server-head").getByRole("button", { name: "Edit" }).click();
  const pwPlaceholder = await page.getByLabel("Password", { exact: true }).getAttribute("placeholder");
  assert.ok(pwPlaceholder.includes("Saved"), "password placeholder " + pwPlaceholder);
  await page.getByLabel("Name", { exact: true }).fill("Demo VPS (renamed)");
  await page.getByRole("button", { name: "Save" }).click();
  await page.getByText("Connected to Demo VPS (renamed).").waitFor();
  await page.locator(".pill.active").waitFor();
  step("edit server keeps the saved password, and a rename keeps the tunnel running");

  // Reordering. Add a second service, a second server and a second project.
  await page.evaluate(async () => {
    const token = localStorage.getItem("tunneltab.session");
    const call = async (method, p, body) => {
      const r = await fetch("/api" + p, { method, headers: { Authorization: "Bearer " + token, "Content-Type": "application/json" }, body: JSON.stringify(body) });
      if (!r.ok) throw new Error(p + ": " + (await r.text()));
      return r.json();
    };
    const data = (await (await fetch("/api/data", { headers: { Authorization: "Bearer " + token } })).json()).data;
    const server = data.servers[0];
    await call("POST", "/services", { serverId: server.id, label: "Second app", remotePort: 81 });
    await call("POST", "/servers", { projectId: server.projectId, name: "Backup box", host: "backup.example.com", port: 22, username: "u", auth: { type: "agent" } });
    await call("POST", "/projects", { name: "Archive" });
  });
  await page.getByRole("heading", { name: "Archive" }).waitFor();
  const labels = (sel) => page.locator(sel).allInnerTexts();
  const serviceNames = () => labels(".service .service-info strong");
  const serverNames = () => labels(".project >> nth=0 >> .server-info strong");
  assert.deepStrictEqual(await serviceNames(), ["Demo app", "Second app"]);

  // Keyboard: focus a grip and press ↑.
  await page.getByRole("button", { name: "Move service Second app" }).focus();
  await page.keyboard.press("ArrowUp");
  await page.waitForFunction(() => document.querySelector(".service strong")?.textContent === "Second app");
  assert.deepStrictEqual(await serviceNames(), ["Second app", "Demo app"]);
  assert.strictEqual(await page.evaluate(() => document.activeElement?.getAttribute("aria-label")), "Move service Second app",
    "focus not kept on the moved item");

  // Drag and drop: drag "Second app" below "Demo app".
  await page.getByRole("button", { name: "Move service Second app" })
    .dragTo(page.locator(".service", { hasText: "Demo app" }), { targetPosition: { x: 40, y: 30 } });
  await page.waitForFunction(() => document.querySelector(".service strong")?.textContent === "Demo app");
  assert.deepStrictEqual(await serviceNames(), ["Demo app", "Second app"]);

  // Servers: keyboard, then drag one into the other project.
  assert.deepStrictEqual(await serverNames(), ["Demo VPS (renamed)", "Backup box"]);
  await page.getByRole("button", { name: "Move server Backup box" }).focus();
  await page.keyboard.press("ArrowUp");
  await page.waitForFunction(() => document.querySelector(".server-info strong")?.textContent === "Backup box");
  await page.getByRole("button", { name: "Move server Backup box" })
    .dragTo(page.locator(".project", { hasText: "Archive" }).getByRole("heading", { name: "Archive" }));
  await page.locator(".project", { hasText: "Archive" }).getByText("Backup box").waitFor();
  assert.deepStrictEqual(await serverNames(), ["Demo VPS (renamed)"]);

  await page.reload();
  await page.getByRole("heading", { name: "Archive" }).waitFor();
  assert.deepStrictEqual(await serviceNames(), ["Demo app", "Second app"]);
  await page.locator(".project", { hasText: "Archive" }).getByText("Backup box").waitFor();
  step("reorder services and servers by keyboard and drag-and-drop; move a server to another project; order kept");

  // 10. Stop the tunnel, then quit.
  await page.locator(".service", { hasText: "Demo app" }).getByRole("button", { name: "Stop" }).click();
  await page.locator(".pill.active").waitFor({ state: "detached", timeout: 10000 });
  step("tunnel stopped");
  await page.getByRole("button", { name: "Quit" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Quit" }).click();
  await page.getByRole("heading", { name: "TunnelTab has stopped" }).waitFor();
  if (app.exitCode === null) await new Promise((r) => app.on("exit", r));
  step("quit stops the program");

  assert.deepStrictEqual(problems, [], "browser errors:\n" + problems.join("\n"));
  step("no JavaScript errors or CSP violations");
  await browser.close();
  fake.kill("SIGINT");
  console.log("E2E PASSED");
  process.exit(0);
})().catch((e) => { console.error("E2E FAILED:", e.message); process.exit(1); });

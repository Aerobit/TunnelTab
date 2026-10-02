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
  const p = spawn(cmd, args, { ...opts, stdio: ["pipe", "pipe", "pipe"] });
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
  // E2E_ZOOM=1.25 runs the walkthrough as if the browser were zoomed to 125%.
  const zoom = Number(process.env.E2E_ZOOM) || 1;
  const context = await browser.newContext({ viewport: { width: Math.round(1200 / zoom), height: Math.round(800 / zoom) }, deviceScaleFactor: zoom });
  const page = await context.newPage();
  const problems = [];
  let touches = 0; // "the user is active" reports (auto-lock)
  page.on("request", (r) => { if (r.url().endsWith("/api/touch")) touches++; });
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
  await page.getByRole("button", { name: "+ Add a server" }).click();
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

  // The server is in the sidebar; its page (in the address) has tabs.
  await page.locator(".side-server", { hasText: "Demo VPS" }).getByRole("link").click();
  await page.getByRole("heading", { name: "Demo VPS", level: 1 }).waitFor();
  assert.ok(page.url().includes("#/server/"), "server page not in the address: " + page.url());
  await page.locator(".page-tabs").getByRole("link", { name: /Services/ }).click();

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

  // 5b. Terminals inside the page: open one, type, split (a second one opens
  //     next to it), switch pages and come back (still there), pop one out.
  await page.getByRole("button", { name: "+ New terminal" }).click();
  await page.locator(".page-tabs a[aria-current=page]", { hasText: "Terminals" }).waitFor();
  const paneText = (i) => page.locator(".term-pane").nth(i).locator(".xterm-rows").innerText();
  const waitPane = async (i, want) => {
    for (let n = 0; n < 100; n++) {
      if ((await paneText(i).catch(() => "")).includes(want)) return;
      await page.waitForTimeout(50);
    }
    throw new Error(`pane ${i} never showed ${JSON.stringify(want)}:\n${await paneText(i).catch(() => "")}`);
  };
  await waitPane(0, "Welcome to the fake shell");
  await page.keyboard.type("echo hello from the browser"); // the new terminal has the keyboard
  await page.keyboard.press("Enter");
  await waitPane(0, "hello from the browser\n");
  await page.getByRole("button", { name: "Split" }).click();
  await page.locator(".term-pane").nth(1).waitFor();
  await waitPane(1, "Welcome to the fake shell");
  await page.locator(".term-pane").nth(1).locator(".xterm-helper-textarea").focus();
  await page.keyboard.type("echo second terminal");
  await page.keyboard.press("Enter");
  await waitPane(1, "second terminal\n");
  assert.strictEqual(await page.getByRole("tab", { name: /Terminal \d/ }).count(), 2);
  await shot("07b-terminals-split");
  // Every row fits inside the terminal's visible area (the bottom line isn't
  // cut off), with some room to spare.
  const rowsFit = (p, frame) => p.evaluate((sel) => [...document.querySelectorAll(sel)].every((f) => {
    const rows = f.querySelector(".xterm-screen")?.getBoundingClientRect();
    const visible = f.querySelector(".xterm-viewport")?.getBoundingClientRect();
    return rows && visible && rows.bottom <= visible.bottom - 3;
  }), frame);
  assert.ok(await rowsFit(page, ".term-pane"), "terminal rows run past the bottom of the pane");
  await page.locator(".page-tabs").getByRole("link", { name: "Overview" }).click();
  await page.locator(".kv").getByText("Terminals open").waitFor();
  await page.locator(".page-tabs").getByRole("link", { name: /Terminals/ }).click();
  await waitPane(0, "hello from the browser"); // kept while looking elsewhere
  step("terminals open inside the page: type, split, switch away and back");

  const [termPage] = await Promise.all([
    context.waitForEvent("page"),
    page.getByRole("button", { name: "Pop out ↗" }).click(),
  ]);
  await page.locator(".termview-msg", { hasText: "open in another tab" }).waitFor();
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
  await waitTerm("hello from the browser"); // the same session, replayed
  assert.strictEqual(await termPage.title(), "Demo VPS — TunnelTab");
  await termPage.keyboard.type("size");
  await termPage.keyboard.press("Enter");
  await waitTerm("x");
  await termPage.screenshot({ path: `${OUT}/07-terminal.png` });
  assert.ok(await rowsFit(termPage, ".term-view"), "terminal rows run past the bottom of the window");
  step("Pop out moves a terminal to its own tab (same session); the dashboard says where it went");

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
  await page.getByRole("button", { name: /Settings/ }).click();
  await page.getByRole("heading", { name: "Settings" }).waitFor();
  await page.getByRole("tab", { name: "Servers" }).click();
  await page.locator(".host-list code").filter({ hasText: "SHA256:" }).waitFor();
  const listedFp = await page.locator(".host-list code").first().textContent();
  assert.ok(listedFp.includes(fpExpected), "settings fingerprint " + listedFp);
  await page.getByRole("tab", { name: "Password" }).click();
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
  await page.getByRole("tab", { name: "Updates" }).click();
  await page.getByRole("button", { name: "Check for updates" }).click();
  await page.getByText("TunnelTab 99.0.0 is available").waitFor();
  const notes = page.getByRole("link", { name: "Release notes and download" });
  assert.strictEqual(await notes.getAttribute("href"), "https://github.com/Aerobit/TunnelTab/releases/tag/v99.0.0");
  assert.strictEqual(updateRequests, 1);
  await shot("06-settings");
  await page.getByRole("dialog").getByRole("button", { name: "Close", exact: true }).click();
  // The check found 99.0.0: Settings shows a dot and reopens on Updates.
  await page.locator("#sidebar .update-dot").waitFor();
  await page.locator(".side-foot").screenshot({ path: `${OUT}/12-update-dot.png` });
  await page.getByRole("button", { name: /Settings/ }).click();
  assert.strictEqual(await page.getByRole("tab", { name: "Updates" }).getAttribute("aria-selected"), "true");
  assert.strictEqual(updateRequests, 1, "reopening Settings checked again");
  await page.getByRole("dialog").getByRole("button", { name: "Close", exact: true }).click();
  step("settings: fingerprint listed, master password changed, update check only on click, update dot");

  // 7. Lock / unlock while a long job runs in the terminal; the tunnel and the
  //    job keep running.
  await termPage.keyboard.type("count 10");
  await termPage.keyboard.press("Enter");
  await waitTerm("tick 1");
  await page.getByRole("button", { name: "Lock" }).click();
  await page.getByRole("heading", { name: "Unlock TunnelTab" }).waitFor();
  await termPage.locator(".termview-msg", { hasText: "session keeps running" }).waitFor();
  assert.ok(!(await termText()).includes("hello from the browser"), "terminal output still readable while locked");
  assert.ok(!(await termPage.locator(".termview-screen").isVisible()), "terminal view visible while locked");
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
  // The dashboard's own terminal (the second one) re-attaches by itself.
  await page.locator(".page-tabs a[aria-current=page]", { hasText: "Terminals" }).waitFor();
  await waitPane(1, "second terminal");
  await page.locator(".page-tabs").getByRole("link", { name: /Services/ }).click();
  await page.locator(".pill.active").waitFor();
  step("lock and unlock (rate-limited retry), tunnel still running, in-page terminal back");

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
  await termPage.locator(".termview-msg", { hasText: "exited with code 7" }).waitFor();
  await termPage.close();
  step("reload re-attaches to the same session; exit code shown");

  // A terminal page of its own (not shown in the dashboard) ends when its tab closes.
  const serverPath = new URL(page.url()).hash.split("/")[2];
  const closing = await context.newPage();
  await closing.goto(new URL(page.url()).origin + "/terminal.html#" + serverPath);
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

  // 8. Reload keeps the session, and this tab's terminal comes back.
  await page.reload();
  await page.getByRole("heading", { name: "My VPSs" }).waitFor();
  await page.locator(".page-tabs").getByRole("link", { name: /Terminals/ }).click();
  await page.getByRole("tab", { name: /Terminal 1/ }).waitFor();
  assert.strictEqual(await page.getByRole("tab", { name: /Terminal \d/ }).count(), 1, "the exited terminal is still listed");
  await waitPane(0, "second terminal");
  step("reload keeps the session and brings this tab's terminal back");

  // 8b. The connection to the server drops and stays down for a while, with
  //     only a terminal using it (no tunnel): the terminal and the server say
  //     "Reconnecting" (never "Failed"), and once it's back a new shell takes
  //     over in the same terminal, below the old output, ready for typing.
  const services = page.locator(".page-tabs").getByRole("link", { name: /Services/ });
  const terminals = page.locator(".page-tabs").getByRole("link", { name: /Terminals/ });
  const demoApp = page.locator(".service", { hasText: "Demo app" });
  await services.click();
  await demoApp.getByRole("button", { name: "Stop" }).click();
  await demoApp.getByRole("button", { name: "Start" }).waitFor();
  await terminals.click();
  await waitPane(0, "second terminal");
  fake.stdin.write("down\n");
  await waitPane(0, "connection to the server lost");
  await page.locator(".termview-msg", { hasText: "Reconnecting" }).waitFor();
  for (let n = 0; n < 30; n++) { // 3 s of refused reconnects
    const pill = await page.locator(".page-head .pill").innerText();
    assert.ok(!/fail/i.test(pill), "the server shows " + pill + " while reconnecting");
    await page.waitForTimeout(100);
  }
  assert.match(await page.locator(".page-head .pill").innerText(), /Reconnecting/);
  await shot("07c-terminal-reconnecting");
  fake.stdin.write("up\n");
  for (let n = 0; ; n++) {
    const text = await paneText(0);
    if (text.lastIndexOf("Welcome to the fake shell") > text.indexOf("connection to the server lost")) break;
    if (n > 800) throw new Error("the terminal didn't reconnect:\n" + text);
    await page.waitForTimeout(50);
  }
  const after = await paneText(0);
  assert.ok(after.includes("second terminal"), "the old output was cleared:\n" + after);
  await page.locator(".termview-msg").first().waitFor({ state: "hidden" });
  assert.strictEqual(await page.getByRole("tab", { name: /Terminal \d/ }).count(), 1, "the terminal was replaced");
  await page.locator(".term-pane").nth(0).click();
  await page.keyboard.type("echo typed after the drop");
  await page.keyboard.press("Enter");
  await waitPane(0, "typed after the drop\n");
  await page.locator(".page-tabs").getByRole("link", { name: /Activity/ }).click();
  const activity = await page.locator("ul.activity").innerText();
  assert.ok(activity.includes("Reconnected") && !activity.includes("Couldn't connect"), "activity:\n" + activity);
  await services.click();
  await demoApp.getByRole("button", { name: "Start" }).click();
  await demoApp.getByRole("button", { name: "Stop" }).waitFor();
  step("a dropped connection: Reconnecting (not Failed), then the same terminal carries on, ready for typing");

  // 9. Edit server keeping the saved password.
  // Edit is in the server's "⋯" menu (also reachable by keyboard).
  const more = page.locator(".page-head").getByRole("button", { name: /More actions/ });
  await more.click();
  await page.getByRole("menuitem", { name: "Test connection" }).waitFor();
  await shot("11-server-menu");
  await page.keyboard.press("Escape");
  assert.strictEqual(await more.getAttribute("aria-expanded"), "false");
  await more.focus();
  await page.keyboard.press("Enter");
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Enter");
  const pwPlaceholder = await page.getByLabel("Password", { exact: true }).getAttribute("placeholder");
  assert.ok(pwPlaceholder.includes("Saved"), "password placeholder " + pwPlaceholder);
  await page.getByLabel("Name", { exact: true }).fill("Demo VPS (renamed)");
  await page.getByRole("button", { name: "Save" }).click();
  await page.getByText("Connected to Demo VPS (renamed).").waitFor();
  await page.locator(".pill.active").waitFor();
  step("edit server keeps the saved password, and a rename keeps the tunnel running");

  // A "Confirm new server" question answered after another key was
  // confirmed for that server (e.g. in another tab): the dialog closes and
  // it connects again by itself. A second fake SSH server, restarted on the
  // same port, presents a new key.
  const fakeFor = async (port) => {
    const p = start(path.join(tmp, "fakessh" + exe), ["-port", String(port)], {});
    const [, , newPort] = await p.waitFor(/Host:\s+(\S+)\n\s+SSH port:\s+(\d+)/);
    return [p, newPort];
  };
  const stopFake = (p) => new Promise((resolve) => { p.once("exit", resolve); p.kill(); });
  let [sideFake, sidePort] = await fakeFor(0);
  const asAnotherTab = (what) => page.evaluate(async (what) => {
    const token = localStorage.getItem("tunneltab.session");
    const call = async (method, p, body) => {
      const r = await fetch("/api" + p, { method, headers: { Authorization: "Bearer " + token, "Content-Type": "application/json" }, body: JSON.stringify(body) });
      return { status: r.status, body: r.status === 204 ? null : await r.json() };
    };
    const side = (await call("GET", "/data")).body.data.servers.find((s) => s.name === "Side door");
    if (what === "confirm") {
      const q = await call("POST", `/servers/${side.id}/test`);
      if ((await call("POST", "/hostkeys/confirm", { token: q.body.token })).status !== 200) throw new Error("confirm");
    } else if ((await call("DELETE", `/servers/${side.id}`)).status >= 300) {
      throw new Error("delete");
    }
  }, what);
  await page.getByRole("button", { name: /^More actions for project / }).first().click();
  await page.getByRole("menuitem", { name: "Add server" }).click();
  await page.getByLabel("Name", { exact: true }).fill("Side door");
  await page.getByLabel("Host", { exact: true }).fill(host);
  await page.getByLabel("SSH port").fill(sidePort);
  await page.getByLabel("Username").fill("demo");
  await page.getByLabel("Log in with").selectOption("password");
  await page.getByLabel("Password", { exact: true }).fill("demo-password");
  await page.getByRole("button", { name: "Add server" }).click();
  const trust = page.getByRole("button", { name: "Trust and connect" });
  await trust.waitFor();
  await stopFake(sideFake);
  [sideFake] = await fakeFor(sidePort);
  await asAnotherTab("confirm"); // the new key
  await trust.click(); // the old question, about the previous key
  await page.getByText("Connected to Side door.").waitFor();
  assert.strictEqual(await page.locator("dialog[open]").count(), 0, "the stale question stayed open");
  await asAnotherTab("delete");
  await page.locator(".side-server", { hasText: "Side door" }).waitFor({ state: "detached" });
  await stopFake(sideFake);
  step("a stale \"Confirm new server\" answer closes the question and connects again");

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
  const serverNames = () => labels(".side-project >> nth=0 >> .side-name");
  assert.deepStrictEqual(await serviceNames(), ["Demo app", "Second app"]);

  // A live event redraws the page; the keyboard focus stays where it was.
  const focusedName = () => page.evaluate(() => {
    const el = document.activeElement;
    return `${el?.closest(".service")?.querySelector("strong")?.textContent}/${el?.textContent}`;
  });
  const redrawn = async (change) => {
    await page.evaluate(() => { window.oldMain = document.querySelector("main"); });
    await change();
    await page.waitForFunction(() => document.querySelector("main") !== window.oldMain);
  };
  await page.locator(".service", { hasText: "Demo app" }).getByRole("button", { name: "Stop" }).focus();
  await redrawn(() => page.evaluate(async () => {
    const token = localStorage.getItem("tunneltab.session");
    const headers = { Authorization: "Bearer " + token, "Content-Type": "application/json" };
    const data = (await (await fetch("/api/data", { headers })).json()).data;
    for (const notes of ["focus test", ""]) { // leaves no notes behind
      const r = await fetch(`/api/servers/${data.servers[0].id}/notes`, { method: "PUT", headers, body: JSON.stringify({ notes }) });
      if (!r.ok) throw new Error("notes: " + r.status);
    }
  }));
  assert.strictEqual(await focusedName(), "Demo app/Stop", "focus lost after a live redraw");
  // A control that changes (Start → Stop) keeps the focus at its place.
  await page.locator(".service", { hasText: "Second app" }).getByRole("button", { name: "Start" }).focus();
  await redrawn(() => page.keyboard.press("Enter"));
  await page.locator(".service", { hasText: "Second app" }).getByRole("button", { name: "Stop" }).waitFor();
  assert.strictEqual(await focusedName(), "Second app/Stop", "focus lost after Start");
  await page.keyboard.press("Enter");
  await page.locator(".service", { hasText: "Second app" }).getByRole("button", { name: "Start" }).waitFor();
  assert.strictEqual(await focusedName(), "Second app/Start", "focus lost after Stop");
  step("keyboard focus stays on its control when live events redraw the page");

  // Keyboard: focus a grip and press ↑.
  await page.getByRole("button", { name: "Move service Second app" }).focus();
  await page.keyboard.press("ArrowUp");
  await page.waitForFunction(() => document.querySelector(".service strong")?.textContent === "Second app");
  assert.deepStrictEqual(await serviceNames(), ["Second app", "Demo app"]);
  assert.strictEqual(await page.evaluate(() => document.activeElement?.getAttribute("aria-label")), "Move service Second app",
    "focus not kept on the moved item");

  // Drag and drop: drag "Second app" below "Demo app".
  const demoRow = page.locator(".service", { hasText: "Demo app" });
  const demoBox = await demoRow.boundingBox(); // drop on its lower half, whatever its height
  await page.getByRole("button", { name: "Move service Second app" })
    .dragTo(demoRow, { targetPosition: { x: 40, y: demoBox.height - 8 } });
  await page.waitForFunction(() => document.querySelector(".service strong")?.textContent === "Demo app");
  assert.deepStrictEqual(await serviceNames(), ["Demo app", "Second app"]);

  // Servers: keyboard, then drag one into the other project.
  assert.deepStrictEqual(await serverNames(), ["Demo VPS (renamed)", "Backup box"]);
  await page.getByRole("button", { name: "Move server Backup box" }).focus();
  await page.keyboard.press("ArrowUp");
  await page.waitForFunction(() => document.querySelector(".side-name")?.textContent === "Backup box");
  await page.getByRole("button", { name: "Move server Backup box" })
    .dragTo(page.locator(".side-project", { hasText: "Archive" }).getByRole("heading", { name: "Archive" }));
  await page.locator(".side-project", { hasText: "Archive" }).getByText("Backup box").waitFor();
  assert.deepStrictEqual(await serverNames(), ["Demo VPS (renamed)"]);

  await page.reload();
  await page.getByRole("heading", { name: "Archive" }).waitFor();
  assert.deepStrictEqual(await serviceNames(), ["Demo app", "Second app"]);
  await page.locator(".side-project", { hasText: "Archive" }).getByText("Backup box").waitFor();
  step("reorder services and servers by keyboard and drag-and-drop; move a server to another project; order kept");

  // Overview: totals, running now, recent activity, all servers.
  await page.locator("#sidebar").getByRole("link", { name: "Overview" }).click();
  await page.getByRole("heading", { name: "Overview", level: 1 }).waitFor();
  assert.strictEqual(await page.locator(".tile", { hasText: "Tunnels running" }).locator(".tile-value").innerText(), "1");
  await page.locator(".box", { hasText: "Running now" }).getByText("Demo app").waitFor();
  await page.locator(".box", { hasText: "Recent activity" }).getByText("Demo app: tunnel started").first().waitFor();
  await page.locator("table").getByRole("link", { name: "Demo VPS (renamed)" }).waitFor();
  await shot("13-overview");

  // Narrow window: the sidebar opens from the Menu button and closes on navigation.
  await page.setViewportSize({ width: 600, height: 800 });
  await page.locator("#sidebar").waitFor({ state: "hidden", timeout: 3000 }); // slides out
  await page.getByRole("button", { name: "☰ Menu" }).click();
  await page.locator("#sidebar").getByRole("link", { name: /Demo VPS/ }).click();
  await page.getByRole("heading", { name: "Demo VPS (renamed)", level: 1 }).waitFor();
  await page.locator("#sidebar").waitFor({ state: "hidden", timeout: 3000 }); // closed after navigating
  await page.setViewportSize({ width: 1200, height: 800 });

  // Server page: Overview tab shows the connection, Activity tab its history; Back works.
  await page.locator(".kv").getByText("Password").waitFor();
  await page.locator(".page-tabs").getByRole("link", { name: "Activity" }).click();
  await page.locator(".activity").getByText("Demo app: tunnel started").first().waitFor();
  await page.goBack();
  await page.locator(".kv").waitFor();
  await page.locator(".page-tabs").getByRole("link", { name: /Services/ }).click();
  assert.ok(touches > 0, "clicking around the dashboard never reported activity (auto-lock would lock)");
  step("overview (totals, running now, activity, all servers), narrow-window menu, server tabs, Back button; clicks count as activity");

  // D3: ping, traffic and notes.
  await page.locator("#sidebar").getByRole("link", { name: "Overview" }).click();
  const trafficTile = await page.locator(".tile", { hasText: "Traffic today" }).locator(".tile-value").innerText();
  assert.ok(/\d/.test(trafficTile) && trafficTile !== "0 B", "traffic through the tunnel not counted: " + trafficTile);
  assert.match(await page.locator("table tbody tr").first().locator("td").nth(4).innerText(), /ms$/, "no ping in the servers table");
  await page.locator("table").getByRole("link", { name: "Demo VPS (renamed)" }).click();
  assert.match(await page.locator(".kv dd").nth(2).innerText(), /ms$/, "no ping on the server page");
  await page.locator("svg.chart").waitFor();
  await page.getByRole("link", { name: "Add notes" }).click();
  await page.getByRole("textbox", { name: /Notes about/ }).fill("Backups run nightly at 02:00.\nAdmin login is in the password manager.");
  await page.keyboard.press("Control+s");
  await page.locator(".inline-status.ok", { hasText: "Saved." }).waitFor();
  await shot("14-notes");
  await page.reload();
  await page.locator(".page-tabs").getByRole("link", { name: "Overview" }).click();
  await page.locator(".notes-preview", { hasText: "Backups run nightly at 02:00." }).waitFor();
  await shot("15-server-overview");
  // Nothing may stick out of the side-scrolling tab bar: on Windows that shows a scrollbar.
  const tabsBox = await page.locator(".page-tabs").evaluate((n) => [n.scrollHeight, n.clientHeight]);
  assert.ok(tabsBox[0] <= tabsBox[1], "the server page tabs overflow (scrollbar on Windows): " + tabsBox);
  await page.locator(".page-tabs").getByRole("link", { name: /Services/ }).click();
  step("ping (servers table and server page), traffic counted and charted, notes saved in the vault");

  // D4: server health is off by default. Connect (here "Show health": a service
  // keeps the server connected) shows it until Disconnect; ticking it in
  // Settings shows it while connected; unticking hides it again.
  await page.locator(".page-tabs").getByRole("link", { name: "Overview" }).click();
  const healthBox = page.locator(".box", { has: page.getByRole("heading", { name: "Health" }) });
  await healthBox.getByText("Show health to see").waitFor();
  await healthBox.getByRole("button", { name: "Show health" }).click();
  await healthBox.locator("meter").first().waitFor();
  await healthBox.getByText("Disk /srv/data").waitFor();
  await healthBox.getByText("of 3.8 GB").waitFor(); // memory: 2.3 GB of 3.8 GB
  await shot("16-health");
  await healthBox.getByRole("button", { name: "Disconnect" }).click();
  await healthBox.getByText("Show health to see").waitFor();
  await page.getByRole("button", { name: /Settings/ }).click();
  await page.getByRole("tab", { name: "Servers" }).click();
  const healthSwitch = page.getByRole("dialog").getByLabel("Health for Demo VPS (renamed)");
  assert.ok(!(await healthSwitch.isChecked()), "Settings shows health as on");
  await healthSwitch.check();
  await page.getByRole("dialog").locator(".inline-status.ok", { hasText: "health on" }).waitFor();
  await shot("16b-settings-servers");
  await page.getByRole("dialog").getByRole("button", { name: "Close", exact: true }).click();
  await healthBox.locator("meter").first().waitFor();
  assert.equal(await healthBox.getByRole("button", { name: "Disconnect" }).count(), 0, "Disconnect shown without Connect");
  await page.getByRole("button", { name: /Settings/ }).click();
  await page.getByRole("tab", { name: "Servers" }).click();
  await healthSwitch.uncheck();
  await page.getByRole("dialog").locator(".inline-status.ok", { hasText: "health off" }).waitFor();
  await page.getByRole("dialog").getByRole("button", { name: "Close", exact: true }).click();
  await healthBox.getByText("Show health to see").waitFor();
  await page.locator(".page-tabs").getByRole("link", { name: /Services/ }).click();
  step("server health: off by default, Show health/Disconnect on the server page, on and off from Settings");

  // §13: Find services asks first, then lists what the server runs; only ticked ones are added.
  await page.locator(".service", { hasText: "Demo app" }).waitFor();
  const servicesBefore = await page.locator(".service").count();
  await page.getByRole("button", { name: "Find services…" }).click();
  let dialog = page.getByRole("dialog");
  await dialog.getByText("run one read-only command").waitFor();
  await dialog.getByRole("button", { name: "Cancel" }).click();
  assert.strictEqual(await page.getByRole("dialog").count(), 0, "Cancel left a dialog open");
  await page.evaluate(() => {
    window.scanSteps = [];
    window.addEventListener("tunneltab-discover", (e) => window.scanSteps.push(`${e.detail.step} ${e.detail.done || 0}/${e.detail.total || 0}`));
  });
  await page.getByRole("button", { name: "Find services…" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Find services" }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByRole("heading", { name: "Services found on Demo VPS (renamed)" }).waitFor();
  // The dialog was told each step: connecting, scanning, then every check.
  const scanSteps = await page.evaluate(() => window.scanSteps);
  assert.deepStrictEqual(scanSteps.slice(0, 2), ["connecting 0/0", "scanning 0/0"], "scan steps: " + scanSteps);
  assert.match(scanSteps[scanSteps.length - 1], /^checking (\d+)\/\1$/, "the checks didn't finish: " + scanSteps);
  for (const port of [5678, 3000, 9443]) {
    assert.ok(await dialog.getByLabel(`Add port ${port}`, { exact: true }).isChecked(), `port ${port} not ticked`);
  }
  assert.strictEqual(await dialog.getByLabel(`Name for port 5678`).inputValue(), "n8n");
  // Each candidate was checked: the demo containers answer, Portainer on https.
  assert.strictEqual(await dialog.getByLabel("Protocol for port 9443").inputValue(), "https");
  await dialog.locator("tr", { hasText: "n8n" }).getByText("✓ answers").waitFor();
  assert.ok(await dialog.getByLabel(`Add port ${webPort}`, { exact: true }).isDisabled(), "the existing service can be ticked again");
  await dialog.locator("tr", { hasText: "Already added" }).waitFor();
  await dialog.getByText("Not web pages (3)").click(); // DNS, SSH, PostgreSQL
  assert.ok(!(await dialog.getByLabel("Add port 5432", { exact: true }).isChecked()), "a database was ticked");
  await dialog.getByLabel("Add port 9443", { exact: true }).uncheck();
  await dialog.getByLabel("Name for port 3000").fill("Graphs");
  await shot("17-find-services");
  await dialog.getByRole("button", { name: "Add selected" }).click();
  await page.getByText("Added 2 services.").waitFor();
  await page.locator(".service", { hasText: "Graphs" }).waitFor();
  await page.locator(".service", { hasText: "n8n" }).waitFor();
  assert.strictEqual(await page.locator(".service").count(), servicesBefore + 2, "wrong number of services added");
  assert.strictEqual(await page.locator(".service", { hasText: "Portainer" }).count(), 0, "an unticked service was added");
  await page.locator(".page-tabs").getByRole("link", { name: "Activity" }).click();
  await page.getByText("Searched for services").waitFor();
  await page.locator(".page-tabs").getByRole("link", { name: /Services/ }).click();
  step("find services: asks first, recognised apps ticked, existing marked, only ticked ones added");

  // Service checks: Check says whether the app behind a service answers.
  for (const [name, text] of [["Demo app", "✓ App answers"], ["Second app", "Nothing answers on port 81"]]) {
    const row = page.locator(".service", { hasText: name });
    await row.getByRole("button", { name: "Check" }).click();
    await row.getByText(text).waitFor();
  }
  step("service checks: an app that answers, and one that doesn't");

  // 10. Stop the tunnel, then quit.
  await page.locator(".service", { hasText: "Demo app" }).getByRole("button", { name: "Stop" }).click();
  await page.locator(".pill.active").waitFor({ state: "detached", timeout: 10000 });
  step("tunnel stopped");
  await page.getByRole("button", { name: "Quit" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Quit" }).click();
  await page.getByRole("heading", { name: "TunnelTab has stopped" }).waitFor();
  await shot("10-stopped");
  if (app.exitCode === null) await new Promise((r) => app.on("exit", r));
  step("quit stops the program");

  assert.deepStrictEqual(problems, [], "browser errors:\n" + problems.join("\n"));
  step("no JavaScript errors or CSP violations");
  await browser.close();
  fake.kill("SIGINT");
  console.log("E2E PASSED");
  process.exit(0);
})().catch((e) => { console.error("E2E FAILED:", e.message); process.exit(1); });

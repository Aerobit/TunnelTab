// End-to-end test of Settings → Update now, against real builds:
//
//   cd tests/e2e && node update.js
//
// It builds a TunnelTab test build made after 0.1.0 (version
// "0.1.0-3-gabc1234", as CI builds are named) and a signed 0.2.0 release (with a throwaway
// test key), serves the release from a fake GitHub, and clicks Update now.
// Checks: the new version replaces the program and starts by itself, the
// data (vault) survives, the leftovers are cleaned up. Then, on Linux, a
// release whose program can't start: the old version must come back.
const { chromium } = require("playwright");
const { spawn, execFileSync } = require("child_process");
const readline = require("readline");
const assert = require("assert");
const crypto = require("crypto");
const fs = require("fs");
const path = require("path");
const os = require("os");
const http = require("http");

const repo = path.resolve(__dirname, "..", "..");
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "tunneltab-update-"));
const win = process.platform === "win32";
const exeName = win ? "tunneltab.exe" : "tunneltab-linux-amd64";
const OUT = path.join(__dirname, "screenshots");
fs.mkdirSync(OUT, { recursive: true });
const step = (s) => console.log("✓", s);

const go = (args, env = {}) => execFileSync("go", args, { cwd: repo, env: { ...process.env, ...env }, encoding: "utf8" });

// Kill whatever TunnelTab is left running (restarted processes aren't our children).
const appDir = path.join(tmp, "app");
const dataDir = path.join(appDir, "data");
process.on("exit", () => {
  try { process.kill(JSON.parse(fs.readFileSync(path.join(dataDir, "instance.json"), "utf8")).pid); } catch {}
});

(async () => {
  // Throwaway release key.
  const keyFile = path.join(tmp, "key.txt");
  const pub = go(["run", "./scripts/signsums", "genkey", keyFile]).trim().split("\n").pop();
  const signingKey = fs.readFileSync(keyFile, "utf8").trim();
  const build = (version, out) => go(["build", "-ldflags",
    `-X main.version=${version} -X github.com/Aerobit/TunnelTab/internal/update.ReleasePublicKey=${pub}`,
    "-o", out, "./cmd/tunneltab"]);

  // A release: tunneltab-<v>.zip (folder "tunneltab/"), SHA256SUMS.txt, .sig.
  function makeRelease(version, programFile) {
    const dir = path.join(tmp, "rel-" + version);
    const pkg = path.join(dir, "tunneltab");
    fs.mkdirSync(pkg, { recursive: true });
    fs.copyFileSync(programFile, path.join(pkg, exeName));
    fs.writeFileSync(path.join(pkg, "README.txt"), `TunnelTab ${version}\r\n`);
    const zip = `tunneltab-${version}.zip`;
    go(["run", "./scripts/mkzip", pkg, path.join(dir, zip)]);
    const sum = crypto.createHash("sha256").update(fs.readFileSync(path.join(dir, zip))).digest("hex");
    fs.writeFileSync(path.join(dir, "SHA256SUMS.txt"), `${sum}  ${zip}\n`);
    go(["run", "./scripts/signsums", "sign", path.join(dir, "SHA256SUMS.txt")], { TUNNELTAB_SIGNING_KEY: signingKey });
    return { version, dir, files: [zip, "SHA256SUMS.txt", "SHA256SUMS.txt.sig"] };
  }

  let current = null; // the release the fake GitHub offers
  const gh = http.createServer((req, res) => {
    const base = `http://127.0.0.1:${gh.address().port}`;
    if (req.url === "/latest") {
      res.setHeader("Content-Type", "application/json");
      return res.end(JSON.stringify({
        tag_name: "v" + current.version, html_url: "https://github.com/Aerobit/TunnelTab/releases/tag/v" + current.version,
        published_at: "2026-10-15T10:00:00Z", draft: false, prerelease: false,
        assets: current.files.map((n) => ({ name: n, browser_download_url: `${base}/dl/${n}` })),
      }));
    }
    const name = decodeURIComponent(req.url.replace(/^\/dl\//, ""));
    if (!current.files.includes(name)) { res.statusCode = 404; return res.end(); }
    if (!name.endsWith(".zip")) return fs.createReadStream(path.join(current.dir, name)).pipe(res);
    // The zip goes out slowly, in 20 parts, so the progress bar can be seen moving.
    const zip = fs.readFileSync(path.join(current.dir, name));
    res.setHeader("Content-Length", zip.length);
    const part = Math.ceil(zip.length / 20);
    (async () => {
      for (let i = 0; i < zip.length; i += part) {
        res.write(zip.subarray(i, i + part));
        await new Promise((r) => setTimeout(r, 100));
      }
      res.end();
    })();
  });
  await new Promise((r) => gh.listen(0, "127.0.0.1", r));

  fs.mkdirSync(appDir, { recursive: true });
  const program = path.join(appDir, exeName);
  const START = "0.1.0-3-gabc1234"; // a test build: offered the next release
  build(START, program);
  fs.writeFileSync(path.join(appDir, "README.txt"), `TunnelTab ${START}\r\n`);
  const newBuild = path.join(tmp, "new-" + exeName);
  build("0.2.0", newBuild);
  const versionOf = () => execFileSync(program, ["--version"], { encoding: "utf8" }).trim();

  // Start the test build. Restarted versions inherit its output, so every launch
  // link (old and new) shows up here.
  const proc = spawn(program, ["--no-browser", "--port", "47902", "--data", dataDir,
    "--update-url", `http://127.0.0.1:${gh.address().port}/latest`], { stdio: ["ignore", "pipe", "pipe"] });
  const links = [];
  const output = [];
  for (const s of [proc.stdout, proc.stderr]) {
    readline.createInterface({ input: s }).on("line", (l) => {
      output.push(l);
      const m = l.match(/http:\/\/127\.0\.0\.1:\d+\/\?launch=\S+/);
      if (m) links.push(m[0]);
    });
  }
  const nthLink = async (n) => {
    for (let i = 0; i < 600 && links.length < n; i++) await new Promise((r) => setTimeout(r, 100));
    assert.ok(links.length >= n, `launch link #${n} never appeared:\n${output.join("\n")}`);
    return links[n - 1];
  };

  const browser = await chromium.launch();
  const context = await browser.newContext({ viewport: { width: 1200, height: 800 } });
  const problems = [];
  const newPage = async (url) => {
    const page = await context.newPage();
    page.on("pageerror", (e) => problems.push("pageerror: " + e.message));
    page.on("console", (m) => m.type() === "error" && !m.text().startsWith("Failed to load resource") && problems.push("console: " + m.text()));
    await page.goto(url);
    return page;
  };
  const unlock = async (page) => {
    await page.getByLabel("Master password").fill("correct horse battery staple");
    await page.getByRole("button", { name: "Unlock" }).click();
    await page.getByText("Add your first project").waitFor();
  };
  const clickUpdateNow = async (page) => {
    await page.getByRole("button", { name: /Settings/ }).click();
    await page.getByRole("tab", { name: "Updates" }).click();
    await page.getByRole("button", { name: "Check for updates" }).click();
    await page.getByRole("button", { name: "Update now" }).click();
    // Record every step and bar value the Settings dialog shows.
    await page.evaluate(() => {
      window.updateSteps = [];
      new MutationObserver(() => {
        const step = document.querySelector(".dialog .update-step");
        if (!step) return;
        const bar = step.querySelector("progress");
        const seen = { text: step.textContent, value: bar.hasAttribute("value") ? bar.value : null, max: bar.max };
        const last = window.updateSteps[window.updateSteps.length - 1];
        if (!last || last.text !== seen.text || last.value !== seen.value) window.updateSteps.push(seen);
      }).observe(document.body, { subtree: true, childList: true, characterData: true, attributes: true });
    });
    await page.getByRole("button", { name: "Update now" }).last().click(); // the confirmation
    await page.getByRole("heading", { name: "Updating TunnelTab" }).waitFor({ timeout: 60000 });
    return page.evaluate(() => window.updateSteps);
  };
  // The steps in order, each once (repeats of "Downloading" merged).
  const stepNames = (steps) => steps.map((s) => s.text.replace(/^Downloading.*/, "Downloading"))
    .filter((t, i, a) => i === 0 || a[i - 1] !== t);

  // 1. First run on the test build.
  let page = await newPage(await nthLink(1));
  await page.getByLabel("Master password").fill("correct horse battery staple");
  await page.getByLabel("Repeat it").fill("correct horse battery staple");
  await page.getByRole("button", { name: "Create vault" }).click();
  await page.getByText("Add your first project").waitFor();
  await page.getByText(`TunnelTab ${START}`).waitFor();
  step("test build running, vault created");

  // 2. Update to a good, signed 0.2.0.
  current = makeRelease("0.2.0", newBuild);
  await page.getByRole("button", { name: /Settings/ }).click();
  await page.getByRole("tab", { name: "Updates" }).click();
  await page.getByRole("button", { name: "Check for updates" }).click();
  await page.getByText("You're running a test build made after TunnelTab 0.1.0.").waitFor();
  await page.getByText("TunnelTab 0.2.0 is available").waitFor();
  await page.getByRole("button", { name: "Update now" }).scrollIntoViewIfNeeded();
  await page.screenshot({ path: `${OUT}/20-update-available.png` });
  await page.getByRole("dialog").getByRole("button", { name: "Close", exact: true }).click();
  const steps = await clickUpdateNow(page);
  await page.screenshot({ path: `${OUT}/21-updating.png` });
  // ("Restarting…" may arrive after the dialog has already made way for the Updating screen.)
  assert.deepStrictEqual(stepNames(steps).filter((t) => t !== "Restarting…"),
    ["Checking for the latest release…", "Checking the signature…", "Downloading", "Download checked; unpacking…", "Installing…"],
    "update steps: " + JSON.stringify(steps));
  const downloads = steps.filter((s) => s.text.startsWith("Downloading") && s.value !== null);
  assert.ok(downloads.length >= 5, "the bar barely moved: " + JSON.stringify(downloads));
  assert.ok(downloads.every((s, i) => i === 0 || s.value >= downloads[i - 1].value), "the bar went backwards");
  // (The last few may be replaced by "unpacking" before the page draws them.)
  const end = downloads[downloads.length - 1];
  assert.ok(end.value > end.max / 2, "the bar stopped early: " + JSON.stringify(end));
  assert.match(end.text, /^Downloading .+ of .+ \(\d+%\)$/);
  step(`Settings showed each step in order, and the bar moved (${downloads.length} updates)`);

  const oldPage = page;
  page = await newPage(await nthLink(2));
  await unlock(page);
  await page.getByText("TunnelTab 0.2.0").waitFor();
  assert.strictEqual(versionOf(), "TunnelTab 0.2.0");
  assert.strictEqual(fs.readFileSync(path.join(appDir, "README.txt"), "utf8"), "TunnelTab 0.2.0\r\n");
  await oldPage.getByText("TunnelTab 0.2.0 is running and opened in a new tab.").waitFor({ timeout: 30000 });
  await oldPage.screenshot({ path: `${OUT}/22-updated.png` });
  await oldPage.close();
  step("Update now installed 0.2.0, restarted, and the vault still unlocks; the old tab says the new version runs");

  const leftovers = () => fs.readdirSync(appDir).filter((n) => n.endsWith(".old") || n === ".update");
  for (let i = 0; i < 100 && leftovers().length; i++) await new Promise((r) => setTimeout(r, 200));
  assert.deepStrictEqual(leftovers(), [], "update leftovers not cleaned up");
  step("the previous version's files were removed");

  // 3. A signed release whose program can't start: the old one comes back.
  if (!win) {
    const broken = path.join(tmp, "broken");
    fs.writeFileSync(broken, "#!/bin/sh\nexit 3\n", { mode: 0o755 });
    current = makeRelease("0.3.0", broken);
    await clickUpdateNow(page);
    const oldPage = page;
    page = await newPage(await nthLink(3));
    await unlock(page);
    await page.getByText("TunnelTab 0.2.0").waitFor();
    assert.strictEqual(versionOf(), "TunnelTab 0.2.0");
    assert.deepStrictEqual(leftovers(), [], "rollback left files behind");
    await oldPage.getByText("TunnelTab 0.3.0 couldn't start, so the previous version (0.2.0) came back.").waitFor({ timeout: 30000 });
    await oldPage.close();
    step("a release that can't start is rolled back; 0.2.0 runs again, and the old tab says so");
  }

  // 4. Quit.
  page.once("dialog", (d) => d.accept());
  await page.getByRole("button", { name: "Quit" }).click();
  await page.getByRole("button", { name: "Quit" }).last().click();
  await page.getByRole("heading", { name: "TunnelTab has stopped" }).waitFor();

  assert.deepStrictEqual(problems, [], "browser problems");
  await browser.close();
  gh.close();
  console.log("UPDATE E2E PASSED");
  process.exit(0);
})().catch((err) => {
  console.error("UPDATE E2E FAILED:", err.message);
  process.exit(1);
});

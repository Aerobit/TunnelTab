// Temporary Phase 3 page: signs in with the one-time launch link and shows
// the vault state. Replaced by the real dashboard in Phase 4.

const SESSION_KEY = "tunneltab.session";
const status = document.getElementById("status");

async function signIn() {
  const params = new URLSearchParams(location.search);
  const launch = params.get("launch");
  if (launch) {
    // Remove the one-time token from the address bar and history.
    history.replaceState(null, "", "/");
    const res = await fetch("/api/session", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ launch }),
    });
    if (res.ok) {
      localStorage.setItem(SESSION_KEY, (await res.json()).session);
    }
  }
  return localStorage.getItem(SESSION_KEY);
}

function api(path, session, options = {}) {
  return fetch(path, {
    ...options,
    headers: { ...(options.headers || {}), Authorization: "Bearer " + session },
  });
}

async function main() {
  const session = await signIn();
  if (!session) {
    status.textContent = "Not signed in. Start TunnelTab again to open the dashboard.";
    return;
  }
  const res = await api("/api/state", session);
  if (!res.ok) {
    localStorage.removeItem(SESSION_KEY);
    status.textContent = "Session expired. Start TunnelTab again to open the dashboard.";
    return;
  }
  const state = await res.json();
  const vault = { none: "no vault yet", locked: "locked", unlocked: "unlocked" }[state.vault];
  status.textContent = `Signed in to TunnelTab ${state.version}. Vault: ${vault}.`;

  const quit = document.getElementById("quit");
  quit.hidden = false;
  quit.addEventListener("click", async () => {
    await api("/api/quit", session, { method: "POST" });
    status.textContent = "TunnelTab has stopped. You can close this tab.";
    quit.hidden = true;
  });
}

main().catch((err) => {
  status.textContent = "Can't reach TunnelTab: " + err.message;
});

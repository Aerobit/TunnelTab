// api.js — talks to the TunnelTab program.
//
// Signing in: the program opens /?launch=<one-time token>. We exchange it for
// a session token, kept in localStorage (which browsers keep separate per
// port, so tunneled web apps on other ports can't read it) and sent as a
// header on every request. See docs/ARCHITECTURE.md → "Signing in".

const SESSION_KEY = "tunneltab.session";

export class ApiError extends Error {
  constructor(status, body) {
    super(body?.message || `Request failed (${status})`);
    this.status = status;
    this.code = body?.error || "error";
    this.body = body || {};
  }
}

let onSignedOut = () => {};

/** Registers a callback for when the session is no longer valid. */
export function whenSignedOut(fn) {
  onSignedOut = fn;
}

function session() {
  try {
    return localStorage.getItem(SESSION_KEY);
  } catch {
    return null;
  }
}

/**
 * Redeems a launch token from the address bar, if present. Returns true when
 * a session is available.
 */
export async function signIn() {
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
  return Boolean(session());
}

/** The session token this tab uses now (it changes when another tab signs in). */
export function currentSession() {
  return session();
}

/**
 * Asks TunnelTab for its state with a given session token, without signing
 * out when it's refused (used after "Update now" to see the restart).
 * Returns "down" if nothing answers, "unknown" if TunnelTab answers but
 * doesn't know the token (it has restarted since), or the state
 * ({vault, version, …}).
 */
export async function probe(token) {
  try {
    const res = await fetch("/api/state", { headers: { Authorization: "Bearer " + token }, cache: "no-store" });
    if (res.status === 401) return "unknown";
    return res.ok ? await res.json() : "down";
  } catch {
    return "down";
  }
}

/** Sends an API request and returns the parsed JSON (or null). */
export async function api(method, path, body) {
  const headers = { Authorization: "Bearer " + session() };
  const init = { method, headers };
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body);
  }
  let res;
  try {
    res = await fetch("/api" + path, init);
  } catch {
    throw new ApiError(0, { error: "offline", message: "Can't reach TunnelTab. Is it still running?" });
  }
  const text = await res.text();
  let data = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = { message: text };
    }
  }
  if (!res.ok) {
    if (res.status === 401 && data?.error === "unauthorized") {
      localStorage.removeItem(SESSION_KEY);
      onSignedOut();
    }
    throw new ApiError(res.status, data);
  }
  return data;
}

/**
 * Streams live events (Server-Sent Events read with fetch, so the session
 * header can be sent). Reconnects with back-off. onStatus receives true when
 * connected and false while disconnected. query is appended to the URL
 * (the terminal page uses "?terminal=<id>" to keep its session alive).
 * Returns a function that stops it.
 */
export function streamEvents(onEvent, onStatus, query = "") {
  let stopped = false;
  let controller = null;
  let delay = 500;

  async function run() {
    while (!stopped) {
      controller = new AbortController();
      try {
        const res = await fetch("/api/events" + query, {
          headers: { Authorization: "Bearer " + session() },
          signal: controller.signal,
        });
        if (res.status === 401) {
          localStorage.removeItem(SESSION_KEY);
          onSignedOut();
          return;
        }
        if (!res.ok || !res.body) throw new Error("bad response");
        onStatus(true);
        delay = 500;
        onEvent({ type: "resync" }); // we may have missed events while away
        await readEvents(res.body, onEvent);
      } catch {
        // fall through to reconnect
      }
      if (stopped) return;
      onStatus(false);
      await new Promise((r) => setTimeout(r, delay));
      delay = Math.min(delay * 2, 5000);
    }
  }

  run();
  return () => {
    stopped = true;
    controller?.abort();
  };
}

async function readEvents(body, onEvent) {
  const reader = body.pipeThrough(new TextDecoderStream()).getReader();
  let buffer = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) return;
    buffer += value;
    let end;
    while ((end = buffer.indexOf("\n\n")) >= 0) {
      const block = buffer.slice(0, end);
      buffer = buffer.slice(end + 2);
      for (const line of block.split("\n")) {
        if (line.startsWith("data: ")) {
          try {
            onEvent(JSON.parse(line.slice(6)));
          } catch {
            // ignore malformed events
          }
        }
      }
    }
  }
}

import { ApiError, burn } from "./api";
import {
  JellyfinError,
  loginWithPassword,
  normalizeBase,
  quickConnectInitiate,
  quickConnectPoll,
  revokeSession,
} from "./jellyfin";
import type { Frame, Session, Setup } from "./types";
import {
  el,
  renderGrid,
  renderStatusBar,
  setLoadingDetail,
  setProgress,
  setStatus,
  showError,
  showView,
  startLoading,
  stopMessages,
} from "./ui";

const SETUP_KEY = "wb.setup";
const SESSION_KEY = "wb.session";
const QUICK_CONNECT_TIMEOUT_MS = 5 * 60 * 1000;
const STALE_STREAM_MS = 25_000;

let setup: Setup | null = readSetup();
let session: Session | null = readSession();

let quickGeneration = 0;
let burnController: AbortController | null = null;

function readSetup(): Setup | null {
  try {
    const raw = localStorage.getItem(SETUP_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as Partial<Setup>;
    if (!parsed.lbUser || !parsed.jellyfinUrl || !parsed.jellyfinUser) return null;
    return {
      lbUser: parsed.lbUser,
      jellyfinUrl: normalizeBase(parsed.jellyfinUrl),
      jellyfinUser: parsed.jellyfinUser,
    };
  } catch {
    return null;
  }
}

function readSession(): Session | null {
  try {
    const raw = sessionStorage.getItem(SESSION_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as Partial<Session>;
    if (!parsed.url || !parsed.token || !parsed.userId) return null;
    return {
      url: parsed.url,
      token: parsed.token,
      userId: parsed.userId,
      userName: parsed.userName ?? "",
      serverId: parsed.serverId ?? "",
    };
  } catch {
    return null;
  }
}

function saveSetup(next: Setup): void {
  setup = next;
  localStorage.setItem(SETUP_KEY, JSON.stringify(next));
}

function adoptSession(next: Session): void {
  session = next;
  sessionStorage.setItem(SESSION_KEY, JSON.stringify(next));
}

function clearSession(): void {
  session = null;
  sessionStorage.removeItem(SESSION_KEY);
  el("logoutBtn").hidden = true;
}

function message(err: unknown): string {
  if (err instanceof JellyfinError || err instanceof ApiError) return err.message;
  if (err instanceof Error) return err.message;
  return "Something went wrong.";
}

// ---------------------------------------------------------------- views

function showConnect(prefillError = ""): void {
  quickGeneration++;
  showView("connect");
  showError("connectError", prefillError);
  setMode("quick");
  void startQuickConnect();
}

function setMode(mode: "quick" | "password"): void {
  const quick = mode === "quick";
  el("quickConnectPanel").hidden = !quick;
  el("passwordForm").hidden = quick;
  el("connectSubtitle").textContent = quick
    ? "Open Jellyfin and approve this code. No password needed."
    : "Sign in with your Jellyfin password. It is sent directly to your server.";
  if (!quick) el<HTMLInputElement>("jellyfinPassword").focus();
}

async function startQuickConnect(): Promise<void> {
  if (!setup) {
    showView("setup");
    return;
  }
  const generation = ++quickGeneration;
  showError("connectError", "");
  el("quickCode").textContent = "……";

  let started: { secret: string; code: string };
  try {
    started = await quickConnectInitiate(setup.jellyfinUrl);
  } catch (err) {
    if (generation !== quickGeneration) return;
    setMode("password");
    showError("connectError", message(err));
    return;
  }
  if (generation !== quickGeneration) return;

  el("quickCode").textContent = formatCode(started.code);

  const deadline = Date.now() + QUICK_CONNECT_TIMEOUT_MS;
  let failures = 0;
  while (generation === quickGeneration && Date.now() < deadline) {
    await delay(2000);
    if (generation !== quickGeneration) return;

    try {
      const found = await quickConnectPoll(setup.jellyfinUrl, started.secret);
      failures = 0;
      if (found) {
        if (generation !== quickGeneration) return;
        adoptSession(found);
        void runBurn();
        return;
      }
    } catch (err) {
      if (generation !== quickGeneration) return;
      failures++;
      if (failures >= 3) {
        showError("connectError", message(err));
        return;
      }
    }
  }

  if (generation === quickGeneration) {
    showError("connectError", "The code expired. Start again to get a new one.");
    setMode("password");
  }
}

function formatCode(code: string): string {
  return code.length === 6 ? `${code.slice(0, 3)} ${code.slice(3)}` : code;
}

// ---------------------------------------------------------------- burn

async function runBurn(): Promise<void> {
  if (!setup) {
    showView("setup");
    return;
  }
  if (!session) {
    showConnect();
    return;
  }

  showError("setupError", "");
  showError("connectError", "");
  el("logoutBtn").hidden = false;
  startLoading();
  setProgress(4);
  setStatus("Loading…");

  // Supersede any in-flight burn: its watchdog and error handling must not
  // touch the UI once a newer run owns the screen.
  burnController?.abort();
  const controller = new AbortController();
  burnController = controller;

  let finished = false;
  let lastFrame = Date.now();

  const watchdog = window.setInterval(() => {
    if (Date.now() - lastFrame > STALE_STREAM_MS) controller.abort();
  }, 5000);

  try {
    await burn({
      session,
      lbUser: setup.lbUser,
      signal: controller.signal,
      onFrame: (frame) => {
        lastFrame = Date.now();
        if (handleFrame(frame)) finished = true;
      },
    });
    if (burnController === controller && !finished) {
      fail("The connection closed before the watchlist was ready.");
    }
  } catch (err) {
    if (burnController !== controller) return;
    if (isAbort(err)) {
      fail("The server took too long to answer. Try again.");
    } else {
      fail(message(err), err);
    }
  } finally {
    window.clearInterval(watchdog);
    if (burnController === controller) {
      stopMessages();
      burnController = null;
    }
  }
}

// handleFrame applies one stream frame and reports whether it was terminal.
function handleFrame(frame: Frame): boolean {
  switch (frame.type) {
    case "phase":
      setLoadingDetail(frame.phase);
      return false;
    case "film": {
      const pct = frame.total > 0 ? 5 + 90 * (frame.index / frame.total) : 10;
      setProgress(pct);
      const label = frame.year ? `${frame.title} (${frame.year})` : frame.title;
      setLoadingDetail(`${label} — ${frame.index}/${frame.total}`);
      return false;
    }
    case "result":
      setProgress(100);
      renderStatusBar(frame.meta);
      renderGrid(frame.films, session?.url ?? "");
      showView("grid");
      setStatus("");
      return true;
    case "error":
      if (frame.code === "jellyfin_unauthorized") {
        clearSession();
        showConnect("Your Jellyfin session expired. Please sign in again.");
        return true;
      }
      fail(frame.message);
      return true;
    case "ping":
      return false;
  }
}

function fail(text: string, err?: unknown): void {
  stopMessages();
  if (err instanceof ApiError && err.status === 401) {
    clearSession();
    showConnect("Your Jellyfin session expired. Please sign in again.");
    return;
  }
  showError("setupError", text);
  showView("setup");
  setStatus("");
}

function isAbort(err: unknown): boolean {
  return err instanceof DOMException && err.name === "AbortError";
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => window.setTimeout(resolve, ms));
}

// ---------------------------------------------------------------- events

el<HTMLFormElement>("setupForm").addEventListener("submit", (event) => {
  event.preventDefault();
  const form = new FormData(event.currentTarget as HTMLFormElement);
  const lbUser = String(form.get("letterboxdUser") ?? "").trim();
  const jellyfinUser = String(form.get("jellyfinUser") ?? "").trim();

  let url: string;
  try {
    url = normalizeBase(String(form.get("jellyfinUrl") ?? ""));
  } catch (err) {
    showError("setupError", message(err));
    return;
  }
  if (!/^[A-Za-z0-9_-]{1,64}$/.test(lbUser)) {
    showError("setupError", "That Letterboxd username is not valid.");
    return;
  }

  showError("setupError", "");
  const previous = setup;
  saveSetup({ lbUser, jellyfinUrl: url, jellyfinUser });

  if (previous && previous.jellyfinUrl !== url) {
    clearSession();
  }
  if (session && session.url === url) {
    void runBurn();
    return;
  }
  showConnect();
});

el<HTMLFormElement>("passwordForm").addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!setup) {
    showView("setup");
    return;
  }
  const form = new FormData(event.currentTarget as HTMLFormElement);
  const password = String(form.get("jellyfinPassword") ?? "");
  showError("connectError", "");

  try {
    const next = await loginWithPassword(setup.jellyfinUrl, setup.jellyfinUser, password);
    el<HTMLInputElement>("jellyfinPassword").value = "";
    adoptSession(next);
    void runBurn();
  } catch (err) {
    showError("connectError", message(err));
  }
});

el("usePasswordBtn").addEventListener("click", () => setMode("password"));

el("useQuickConnectBtn").addEventListener("click", () => {
  setMode("quick");
  void startQuickConnect();
});

el("logoutBtn").addEventListener("click", async () => {
  quickGeneration++;
  burnController?.abort();
  if (session) await revokeSession(session);
  clearSession();
  setStatus("Logged out");
  showConnect();
});

// ---------------------------------------------------------------- boot

function fillSetupForm(value: Setup): void {
  const form = el<HTMLFormElement>("setupForm");
  (form.elements.namedItem("letterboxdUser") as HTMLInputElement).value = value.lbUser;
  (form.elements.namedItem("jellyfinUrl") as HTMLInputElement).value = value.jellyfinUrl;
  (form.elements.namedItem("jellyfinUser") as HTMLInputElement).value = value.jellyfinUser;
}

function boot(): void {
  if (setup) fillSetupForm(setup);

  if (session && setup) {
    void runBurn();
    return;
  }
  if (setup) {
    showConnect();
    return;
  }
  showView("setup");
}

void boot();

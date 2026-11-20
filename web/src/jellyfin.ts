// All direct Jellyfin traffic lives here. The browser talks to the user's
// server over CORS; the backend never sees the password.

import type { Session } from "./types";

const CLIENT_NAME = "Watchlist Burner Web";
const CLIENT_VERSION = "1.0.0";
const DEVICE_KEY = "wb.device";

export class JellyfinError extends Error {
  constructor(
    message: string,
    readonly status = 0,
  ) {
    super(message);
    this.name = "JellyfinError";
  }
}

function deviceId(): string {
  let id = localStorage.getItem(DEVICE_KEY);
  if (!id) {
    id = crypto.randomUUID();
    localStorage.setItem(DEVICE_KEY, id);
  }
  return id;
}

function deviceName(): string {
  return /Mobi|Android|iPhone|iPad/i.test(navigator.userAgent) ? "Mobile" : "Browser";
}

function authorization(token?: string): string {
  let value = `MediaBrowser Client="${CLIENT_NAME}", Device="${deviceName()}", DeviceId="${deviceId()}", Version="${CLIENT_VERSION}"`;
  if (token) value += `, Token="${token}"`;
  return value;
}

function authHeaders(token?: string): Record<string, string> {
  const value = authorization(token);
  return {
    Authorization: value,
    "X-Emby-Authorization": value,
    Accept: "application/json",
  };
}

/** Normalizes a user-typed server URL, defaulting to https. */
export function normalizeBase(raw: string): string {
  let value = raw.trim().replace(/\/+$/, "");
  if (!value) throw new JellyfinError("Enter your Jellyfin server URL.");
  if (!/^https?:\/\//i.test(value)) value = `https://${value}`;

  let url: URL;
  try {
    url = new URL(value);
  } catch {
    throw new JellyfinError("That does not look like a valid server URL.");
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") {
    throw new JellyfinError("The server URL must use http or https.");
  }
  return (url.origin + url.pathname).replace(/\/+$/, "");
}

async function failure(res: Response): Promise<JellyfinError> {
  const body = await res.text().catch(() => "");
  const detail = body.trim().slice(0, 160);
  if (res.status === 401 || res.status === 403) {
    return new JellyfinError("Invalid username or password.", res.status);
  }
  if (res.status === 404) {
    return new JellyfinError("Jellyfin could not find that endpoint. Check the server URL.", res.status);
  }
  return new JellyfinError(detail || `Jellyfin answered ${res.status}.`, res.status);
}

function toSession(base: string, data: unknown): Session {
  const payload = data as {
    AccessToken?: string;
    ServerId?: string;
    User?: { Id?: string; Name?: string };
  };
  const token = payload.AccessToken;
  const user = payload.User;
  if (!token || !user?.Id) {
    throw new JellyfinError("Jellyfin did not return a session token.");
  }
  return {
    url: base,
    token,
    userId: user.Id,
    userName: user.Name ?? "",
    serverId: payload.ServerId ?? "",
  };
}

/** Starts a Quick Connect request and returns the code the user must approve. */
export async function quickConnectInitiate(base: string): Promise<{ secret: string; code: string }> {
  let res: Response;
  try {
    res = await fetch(`${base}/QuickConnect/Initiate`, {
      method: "POST",
      headers: authHeaders(),
      credentials: "omit",
      mode: "cors",
      signal: AbortSignal.timeout(10_000),
    });
  } catch {
    throw new JellyfinError("Could not reach that Jellyfin server. Check the URL and its CORS setting.");
  }
  if (!res.ok) throw await failure(res);

  const data = (await res.json()) as { Secret?: string; Code?: string };
  if (!data.Secret || !data.Code) {
    throw new JellyfinError("Quick Connect is disabled on this server.");
  }
  return { secret: data.Secret, code: data.Code };
}

/** Polls a Quick Connect request. Returns null while it is still pending. */
export async function quickConnectPoll(base: string, secret: string): Promise<Session | null> {
  const res = await fetch(`${base}/QuickConnect/Connect?secret=${encodeURIComponent(secret)}`, {
    headers: authHeaders(),
    credentials: "omit",
    mode: "cors",
    signal: AbortSignal.timeout(8_000),
  });
  if (res.status === 404) return null;
  if (!res.ok) throw await failure(res);
  return toSession(base, await res.json());
}

/** Password fallback: credentials go straight to Jellyfin, never to our API. */
export async function loginWithPassword(base: string, username: string, password: string): Promise<Session> {
  let res: Response;
  try {
    res = await fetch(`${base}/Users/AuthenticateByName`, {
      method: "POST",
      headers: { ...authHeaders(), "Content-Type": "application/json" },
      body: JSON.stringify({ Username: username, Pw: password }),
      credentials: "omit",
      mode: "cors",
      signal: AbortSignal.timeout(15_000),
    });
  } catch {
    throw new JellyfinError("Could not reach that Jellyfin server. Check the URL and its CORS setting.");
  }
  if (!res.ok) throw await failure(res);
  return toSession(base, await res.json());
}

/** Best-effort server-side token revocation on sign-out. */
export async function revokeSession(session: Session): Promise<void> {
  try {
    await fetch(`${session.url}/Sessions/Logout`, {
      method: "POST",
      headers: authHeaders(session.token),
      credentials: "omit",
      mode: "cors",
      signal: AbortSignal.timeout(3_000),
    });
  } catch {
    // Signing out locally is enough when the server is unreachable.
  }
}

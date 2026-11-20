// Client for our own backend. The Jellyfin session travels in request headers,
// never in a query string, so it cannot leak through logs or `Referer`.

import type { Film, Frame, Meta, Session } from "./types";

export class ApiError extends Error {
  constructor(
    readonly code: string,
    message: string,
    readonly status: number,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

function sessionHeaders(session: Session): Record<string, string> {
  return {
    "X-Jellyfin-URL": session.url,
    "X-Jellyfin-Token": session.token,
    "X-Jellyfin-User-Id": session.userId,
    Accept: "application/json",
  };
}

async function toApiError(res: Response): Promise<ApiError> {
  const body = (await res.json().catch(() => null)) as { code?: string; message?: string } | null;
  return new ApiError(
    body?.code ?? "http_error",
    body?.message ?? `The server answered ${res.status}.`,
    res.status,
  );
}

export interface BurnOptions {
  session: Session;
  lbUser: string;
  onFrame: (frame: Frame) => void;
  signal: AbortSignal;
}

/**
 * Streams the burn as NDJSON. Falls back to the single-response endpoint when
 * the browser cannot read a streamed body.
 */
export async function burn(opts: BurnOptions): Promise<void> {
  const url = `/api/watchlist/stream?lbuser=${encodeURIComponent(opts.lbUser)}`;
  const res = await fetch(url, {
    method: "POST",
    headers: sessionHeaders(opts.session),
    signal: opts.signal,
  });
  if (!res.ok) throw await toApiError(res);

  if (!res.body) {
    await burnJSON(opts);
    return;
  }

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";

  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });

    let newline = buffer.indexOf("\n");
    while (newline >= 0) {
      const line = buffer.slice(0, newline).trim();
      buffer = buffer.slice(newline + 1);
      if (line) opts.onFrame(JSON.parse(line) as Frame);
      newline = buffer.indexOf("\n");
    }
  }

  const tail = buffer.trim();
  if (tail) opts.onFrame(JSON.parse(tail) as Frame);
}

async function burnJSON(opts: BurnOptions): Promise<void> {
  const res = await fetch(`/api/watchlist?lbuser=${encodeURIComponent(opts.lbUser)}`, {
    method: "POST",
    headers: sessionHeaders(opts.session),
    signal: opts.signal,
  });
  if (!res.ok) throw await toApiError(res);

  const body = (await res.json()) as { films: Film[]; total: number; matched: number; watched: number };
  const meta: Meta = { total: body.total, matched: body.matched, watched: body.watched };
  opts.onFrame({ type: "result", films: body.films, meta });
}

/** Resolves a poster through the backend's Letterboxd cache. */
export async function resolvePoster(slug: string, signal?: AbortSignal): Promise<string> {
  try {
    const res = await fetch(`/api/poster?slug=${encodeURIComponent(slug)}`, { signal });
    if (!res.ok) return "";
    const data = (await res.json()) as { posterUrl?: string };
    return data.posterUrl ?? "";
  } catch {
    return "";
  }
}

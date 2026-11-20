import { resolvePoster } from "./api";
import type { Film, Meta } from "./types";

const FALLBACK_POSTER = "https://s.ltrbxd.com/static/img/empty-poster-125-AiuBHVCI.png";
const RING = 2 * Math.PI * 52;

export function el<T extends Element = HTMLElement>(id: string): T {
  const node = document.getElementById(id);
  if (!node) throw new Error(`missing element #${id}`);
  return node as unknown as T;
}

export type ViewName = "setup" | "connect" | "loading" | "grid";

const views: Record<ViewName, HTMLElement> = {
  setup: el("setupView"),
  connect: el("connectView"),
  loading: el("loadingView"),
  grid: el("gridView"),
};

export function showView(name: ViewName): void {
  for (const [key, node] of Object.entries(views)) {
    node.hidden = key !== name;
  }
  el("statusBar").hidden = name !== "grid";
}

export function setStatus(text: string): void {
  el("status").textContent = text;
}

export function showError(id: string, message: string): void {
  const node = el(id);
  node.textContent = message;
  node.hidden = !message;
}

export function setProgress(pct: number): void {
  const clamped = Math.max(0, Math.min(100, pct));
  el<SVGCircleElement>("progressFill").style.strokeDashoffset = String(RING * (1 - clamped / 100));
  el("progressPct").textContent = `${Math.round(clamped)}%`;
}

export function setLoadingDetail(text: string): void {
  el("loadingDetail").textContent = text;
}

export function renderStatusBar(meta: Meta): void {
  const total = meta.total || 0;
  const inLibrary = Math.max(0, meta.matched - meta.watched);
  const missing = Math.max(0, total - meta.matched);

  const set = (id: string, label: string, count: number): void => {
    const seg = el(id);
    seg.style.width = total ? `${(count / total) * 100}%` : "0%";
    seg.title = `${label}: ${count}`;
    seg.setAttribute("aria-label", `${label}: ${count}`);
  };
  set("barWatched", "Watched", meta.watched);
  set("barLibrary", "In library", inLibrary);
  set("barNolib", "Not in library", missing);
}

export function renderGrid(films: Film[], jellyfinUrl: string, seerrUrl: string): void {
  const grid = el("grid");
  grid.replaceChildren();

  const empty = el("empty");
  if (films.length === 0) {
    empty.textContent = "Nothing left to watch — your watchlist is either watched or missing from Jellyfin.";
    empty.hidden = false;
  } else {
    empty.hidden = true;
  }

  const fragment = document.createDocumentFragment();
  for (const film of films) {
    fragment.appendChild(renderCard(film, jellyfinUrl, seerrUrl));
  }
  grid.appendChild(fragment);
}

function renderCard(film: Film, jellyfinUrl: string, seerrUrl: string): HTMLElement {
  const card = document.createElement("div");
  card.className = `poster${film.inLibrary ? "" : " disabled"}`;

  const body = document.createElement("div");
  body.className = "poster-body";

  if (!film.inLibrary) {
    const badge = document.createElement("span");
    badge.className = "badge";
    badge.textContent = "Not in library";
    body.appendChild(badge);
  }

  const img = document.createElement("img");
  img.alt = `${film.title} (${film.year || "unknown year"})`;
  img.loading = "lazy";
  img.decoding = "async";
  img.width = 250;
  img.height = 375;
  attachPoster(img, film);
  body.appendChild(img);

  if (film.inLibrary && film.itemId) {
    const link = document.createElement("a");
    link.href = `${jellyfinUrl}/web/index.html#!/details?id=${encodeURIComponent(film.itemId)}`;
    link.target = "_blank";
    link.rel = "noopener noreferrer";
    link.setAttribute("aria-label", `Open ${film.title} (${film.year}) in Jellyfin`);
    link.appendChild(body);
    card.appendChild(link);
  } else {
    card.appendChild(body);
  }

  if (!film.inLibrary && seerrUrl) {
    card.appendChild(requestLink(film, seerrUrl));
  }

  return card;
}

function requestLink(film: Film, seerrUrl: string): HTMLAnchorElement {
  const link = document.createElement("a");
  link.className = "request-link";
  link.target = "_blank";
  link.rel = "noopener noreferrer";
  link.textContent = "Request";
  link.href = film.tmdbId
    ? `${seerrUrl}/movie/${encodeURIComponent(film.tmdbId)}`
    : `${seerrUrl}/search?query=${encodeURIComponent(`${film.title} ${film.year || ""}`.trim())}`;
  link.setAttribute("aria-label", `Request ${film.title} in Seerr`);
  return link;
}

function attachPoster(img: HTMLImageElement, film: Film): void {
  let triedResolve = false;
  let triedPlaceholder = false;

  const placeholder = (): void => {
    if (triedPlaceholder) {
      img.onerror = null;
      return;
    }
    triedPlaceholder = true;
    img.src = FALLBACK_POSTER;
  };

  const resolve = async (): Promise<void> => {
    if (triedResolve) {
      placeholder();
      return;
    }
    triedResolve = true;
    const url = await resolvePoster(film.slug);
    if (url) {
      img.src = url;
      return;
    }
    placeholder();
  };

  img.onerror = () => {
    void resolve();
  };

  if (film.posterUrl) {
    img.src = film.posterUrl;
  } else {
    void resolve();
  }
}

const MESSAGES = [
  "Just going through your watchlist…",
  "Whoa, there are a lot of them…",
  "Wait, you've watched how many?!",
  "What are you in the mood for tonight?",
  "Cleaning up the layout for your eyes…",
  "Almost there…",
];

let messageTimer: number | null = null;

export function startLoading(): void {
  showView("loading");
  setProgress(0);
  setLoadingDetail("");
  let i = 0;
  el("loadingText").textContent = MESSAGES[0] ?? "";
  stopMessages();
  messageTimer = window.setInterval(() => {
    i = (i + 1) % MESSAGES.length;
    el("loadingText").textContent = MESSAGES[i] ?? "";
  }, 4400);
}

export function stopMessages(): void {
  if (messageTimer !== null) {
    window.clearInterval(messageTimer);
    messageTimer = null;
  }
}

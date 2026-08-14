"use strict";

const $ = (sel) => document.querySelector(sel);

const KEYS = {
	JELLYFIN_URL: "wb.jellyfinUrl",
	JELLYFIN_USER: "wb.jellyfinUser",
	LETTERBOXD_USER: "wb.letterboxdUser"
};

const state = {
	jellyfinUrl: localStorage.getItem(KEYS.JELLYFIN_URL) || "",
	jellyfinUser: localStorage.getItem(KEYS.JELLYFIN_USER) || "",
	letterboxdUser: localStorage.getItem(KEYS.LETTERBOXD_USER) || ""
};

const views = {
	setup: $("#setupView"),
	login: $("#loginView"),
	loading: $("#loadingView"),
	grid: $("#gridView")
};

const LOADING_MESSAGES = [
	"Just going through your watchlist…",
	"Whoa, there are a lot of them…",
	"Wait, you've watched how many?!",
	"What are you in the mood for tonight?",
	"Cleaning up the layout for your eyes…",
	"Almost there…"
];

function showView(name) {
	for (const [key, el] of Object.entries(views)) {
		el.hidden = key !== name;
	}
	$("#statusBar").hidden = name !== "grid";
}

function setStatus(text) {
	$("#status").textContent = text || "";
}

function showError(target, message) {
	const el = $(target);
	el.textContent = message;
	el.hidden = !message;
}

async function api(path, options = {}) {
	const res = await fetch(path, {
		headers: { "Content-Type": "application/json" },
		...options
	});
	const data = await res.json().catch(() => ({}));
	return { res, data };
}

// renderStatusBar fills the fixed-size topbar bar by movie type. Hovering a
// colored segment reveals its count and meaning. Replaces the old text
// summary.
function renderStatusBar(meta) {
	const bar = $("#statusBar");
	if (!meta) {
		bar.hidden = true;
		return;
	}
	const total = meta.total || 0;
	const watched = meta.watched;
	const inLib = meta.matched - meta.watched;
	const noLib = total - meta.matched;

	const setSeg = (id, label, n) => {
		const seg = $(id);
		seg.style.width = total ? `${(n / total) * 100}%` : "0%";
		let tip = seg.querySelector(".statusbar-tip");
		if (!tip) {
			tip = document.createElement("span");
			tip.className = "statusbar-tip";
			seg.appendChild(tip);
		}
		tip.textContent = `${label}: ${n}`;
	};
	setSeg("#barWatched", "Watched", watched);
	setSeg("#barLibrary", "In library", inLib);
	setSeg("#barNolib", "Not in library", noLib);

	const segs = [$("#barWatched"), $("#barLibrary"), $("#barNolib")];
	const visible = segs.filter(s => s.style.width !== "0%");
	segs.forEach(s => { s.style.borderRadius = "0"; });
	if (visible.length === 1) {
		visible[0].style.borderRadius = "5px";
	} else if (visible.length > 1) {
		visible[0].style.borderRadius = "5px 0 0 5px";
		visible[visible.length - 1].style.borderRadius = "0 5px 5px 0";
	}

	bar.hidden = false;
	setStatus("");
}

function renderGrid(films, meta) {
	const grid = $("#grid");
	grid.innerHTML = "";

	renderStatusBar(meta);

	if (!films.length) {
		$("#empty").textContent = "Nothing to watch — your whole watchlist is either watched or missing from Jellyfin.";
		$("#empty").hidden = false;
	} else {
		$("#empty").hidden = true;
	}

	for (const f of films) {
		const card = document.createElement("div");
		card.className = "poster" + (f.inLibrary ? "" : " disabled");

		if (f.inLibrary) {
			const a = document.createElement("a");
			a.href = `${state.jellyfinUrl}/web/index.html#!/details?id=${f.itemId}`;
			a.target = "_blank";
			a.rel = "noopener";
			a.appendChild(buildInner(f));
			card.appendChild(a);
		} else {
			const div = document.createElement("div");
			div.appendChild(buildInner(f));
			card.appendChild(div);
		}

		grid.appendChild(card);
	}
}

function buildInner(f) {
	const wrap = document.createElement("div");
	wrap.style.position = "relative";

	if (!f.inLibrary) {
		const badge = document.createElement("span");
		badge.className = "badge";
		badge.textContent = "Not in library";
		wrap.appendChild(badge);
	}

	const img = document.createElement("img");
	img.src = f.posterUrl;
	img.alt = `${f.title} - ${f.year}`;
	img.loading = "lazy";
	img.onerror = () => { img.src = "https://s.ltrbxd.com/static/img/empty-poster-125-AiuBHVCI.png"; };
	wrap.appendChild(img);

	return wrap;
}

let loadingTimer = null;

const PROGRESS_CIRC = 2 * Math.PI * 52;
const SCRAPE_TARGET = 33;
let progressRaf = null;

function setProgress(pct) {
	$("#progressFill").classList.remove("indeterminate");
	pct = Math.max(0, Math.min(100, pct));
	$("#progressFill").style.strokeDashoffset = PROGRESS_CIRC * (1 - pct / 100);
	$("#progressPct").textContent = `${Math.round(pct)}%`;
}

function stopScrapeProgress() {
	if (progressRaf) {
		cancelAnimationFrame(progressRaf);
		progressRaf = null;
	}
}

// startScrapeProgress ramps the ring toward 1/3 while the Letterboxd
// watchlist is scraped (that phase has no known total), then holds there.
// The remaining 2/3 is driven by real Jellyfin counts once film frames flow.
function startScrapeProgress() {
	stopScrapeProgress();
	let pct = 0;
	setProgress(0);
	const step = () => {
		pct += (SCRAPE_TARGET - pct) * 0.02;
		if (pct >= SCRAPE_TARGET - 0.5) {
			setProgress(SCRAPE_TARGET);
			return;
		}
		setProgress(pct);
		progressRaf = requestAnimationFrame(step);
	};
	step();
}

function startLoading() {
	showView("loading");
	startScrapeProgress();
	const el = $("#loadingText");
	let i = 0;
	el.textContent = LOADING_MESSAGES[0];
	clearInterval(loadingTimer);
	loadingTimer = setInterval(() => {
		i = (i + 1) % LOADING_MESSAGES.length;
		el.textContent = LOADING_MESSAGES[i];
	}, 4400);
}

function stopLoading() {
	stopScrapeProgress();
	clearInterval(loadingTimer);
	loadingTimer = null;
}

function showLoadingDetail(text) {
	$("#loadingDetail").textContent = text || "";
}

function handleResult(data) {
	stopLoading();
	setProgress(100);
	if (data.error) {
		showError("#setupError", data.error);
		showView("setup");
		setStatus("");
		return;
	}
	if (!data.films) {
		showError("#setupError", "Something went wrong.");
		showView("setup");
		setStatus("");
		return;
	}
	renderGrid(data.films, data.meta);
	showView("grid");
	$("#logoutBtn").hidden = false;
}

async function loadWatchlist() {
	setStatus("Loading…");
	showError("#setupError", "");
	showError("#loginError", "");
	startLoading();

	let res, data;
	try {
		({ res, data } = await api(`/api/watchlist?lbuser=${encodeURIComponent(state.letterboxdUser)}`));
	} catch {
		stopLoading();
		showError("#setupError", "Could not reach the server.");
		showView("setup");
		setStatus("");
		return;
	}
	stopLoading();
	if (res.status === 401) {
		showView("login");
		setStatus("Session expired");
		return;
	}
	if (!res.ok) {
		showError("#setupError", data.error || "Something went wrong.");
		showView("setup");
		setStatus("");
		return;
	}
	handleResult(data);
}

// loadWatchlistWS streams real progress over a WebSocket and falls back to
// the plain HTTP endpoint if the socket can't be used or goes quiet. The
// idle timeout (reset on every frame) is what saves the page from hanging
// forever when the server accepts the handshake but never sends anything.
function loadWatchlistWS() {
	return new Promise((resolve) => {
		const ws = new WebSocket(`${location.origin.replace(/^http/, "ws")}/api/watchlist/ws?lbuser=${encodeURIComponent(state.letterboxdUser)}`);
		let lastTitle = "";
		let settled = false;
		let idleTimer = null;

		const finish = (result) => {
			if (settled) return;
			settled = true;
			clearTimeout(idleTimer);
			resolve(result);
		};

		const arm = () => {
			clearTimeout(idleTimer);
			idleTimer = setTimeout(() => {
				if (settled) return;
				stopLoading();
				ws.close();
				finish({ ok: false, wsFailed: true });
			}, 10000);
		};

		arm();

		ws.onopen = () => {
			startLoading();
			arm();
		};

		ws.onmessage = (ev) => {
			let msg;
			try { msg = JSON.parse(ev.data); } catch { return; }

			if (msg.type === "phase") {
				showLoadingDetail(msg.phase);
			} else if (msg.type === "film") {
				stopScrapeProgress();
				lastTitle = msg.title;
				if (msg.total > 0) {
					setProgress(SCRAPE_TARGET + (100 - SCRAPE_TARGET) * (msg.index / msg.total));
				}
				showLoadingDetail(lastTitle);
			} else if (msg.type === "result") {
				stopLoading();
				ws.close();
				finish({ ok: true, data: { films: msg.films, meta: msg.meta } });
				return;
			} else if (msg.type === "auth") {
				stopLoading();
				ws.close();
				showView("login");
				setStatus("Session expired");
				finish({ ok: false, auth: true });
				return;
			} else if (msg.type === "error") {
				stopLoading();
				ws.close();
				finish({ ok: false, error: msg.error });
				return;
			}
			arm();
		};

		ws.onerror = () => {
			ws.close();
			finish({ ok: false, wsFailed: true });
		};

		ws.onclose = () => {
			if (settled) return;
			stopLoading();
			finish({ ok: false, wsFailed: true });
		};
	});
}

async function loadWithWS() {
	let result;
	try {
		result = await loadWatchlistWS();
	} catch {
		result = { ok: false, wsFailed: true };
	}
	if (result.ok) {
		handleResult(result.data);
		return;
	}
	if (result.auth) {
		return;
	}
	if (result.wsFailed) {
		await loadWatchlist();
		return;
	}
	showError("#setupError", result.error || "Something went wrong.");
	showView("setup");
	setStatus("");
}

async function doLogin(password, onSuccess) {
	showError("#loginError", "");
	const { res, data } = await api("/api/login", {
		method: "POST",
		body: JSON.stringify({
			jellyfinUrl: state.jellyfinUrl,
			username: state.jellyfinUser,
			password
		})
	});
	if (!res.ok) {
		showError("#loginError", data.error || "Login failed.");
		return;
	}
	onSuccess();
}

$("#setupForm").addEventListener("submit", async (e) => {
	e.preventDefault();
	const fd = new FormData(e.target);
	state.jellyfinUrl = fd.get("jellyfinUrl").trim();
	state.jellyfinUser = fd.get("jellyfinUser").trim();
	state.letterboxdUser = fd.get("letterboxdUser").trim();
	localStorage.setItem(KEYS.JELLYFIN_URL, state.jellyfinUrl);
	localStorage.setItem(KEYS.JELLYFIN_USER, state.jellyfinUser);
	localStorage.setItem(KEYS.LETTERBOXD_USER, state.letterboxdUser);

	await doLogin(fd.get("jellyfinPassword"), () => loadWithWS());
});

$("#loginForm").addEventListener("submit", async (e) => {
	e.preventDefault();
	const fd = new FormData(e.target);
	await doLogin(fd.get("jellyfinPassword"), () => loadWithWS());
});


$("#logoutBtn").addEventListener("click", async () => {
	await api("/api/logout", { method: "POST" });
	$("#logoutBtn").hidden = true;
	showView("login");
	setStatus("Logged out");
});

(async function init() {
	try {
		if (!state.jellyfinUrl || !state.jellyfinUser || !state.letterboxdUser) {
			showView("setup");
			return;
		}
		$("#setupForm").elements.jellyfinUrl.value = state.jellyfinUrl;
		$("#setupForm").elements.jellyfinUser.value = state.jellyfinUser;
		$("#setupForm").elements.letterboxdUser.value = state.letterboxdUser;
		await loadWithWS();
	} catch (err) {
		console.error(err);
		showError("#setupError", "Something went wrong.");
		showView("setup");
		setStatus("");
	}
})();

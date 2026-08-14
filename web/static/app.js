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

function renderGrid(films, meta) {
	const grid = $("#grid");
	grid.innerHTML = "";

	if (meta) {
		const seen = films.length + meta.watched;
		setStatus(`${films.length} to watch · ${meta.watched} watched (hidden) · ${meta.total - meta.matched} not in library`);
	}

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

function startLoading() {
	showView("loading");
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
	clearInterval(loadingTimer);
	loadingTimer = null;
}

function showLoadingDetail(text) {
	$("#loadingDetail").textContent = text || "";
}

function handleResult(data) {
	stopLoading();
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
	$("#refreshBtn").hidden = false;
	$("#logoutBtn").hidden = false;
}

async function loadWatchlist() {
	setStatus("Loading…");
	showError("#setupError", "");
	showError("#loginError", "");
	startLoading();

	const { res, data } = await api(`/api/watchlist?lbuser=${encodeURIComponent(state.letterboxdUser)}`);
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
// the plain HTTP endpoint if the socket can't be used.
function loadWatchlistWS() {
	return new Promise((resolve) => {
		const ws = new WebSocket(`${location.origin.replace(/^http/, "ws")}/api/watchlist/ws?lbuser=${encodeURIComponent(state.letterboxdUser)}`);
		let filmCount = 0;
		let lastTitle = "";

		ws.onopen = () => {
			startLoading();
		};

		ws.onmessage = (ev) => {
			let msg;
			try { msg = JSON.parse(ev.data); } catch { return; }

			if (msg.type === "phase") {
				showLoadingDetail(msg.phase);
			} else if (msg.type === "film") {
				filmCount++;
				lastTitle = msg.title;
				showLoadingDetail(`${filmCount} movies in your library so far — e.g. "${lastTitle}"`);
			} else if (msg.type === "result") {
				stopLoading();
				ws.close();
				resolve({ ok: true, data: { films: msg.films, meta: msg.meta } });
			} else if (msg.type === "auth") {
				stopLoading();
				ws.close();
				showView("login");
				setStatus("Session expired");
				resolve({ ok: false, auth: true });
			} else if (msg.type === "error") {
				stopLoading();
				ws.close();
				resolve({ ok: false, error: msg.error });
			}
		};

		ws.onerror = () => {
			ws.close();
			resolve({ ok: false, wsFailed: true });
		};
	});
}

async function loadWithWS() {
	const result = await loadWatchlistWS();
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

$("#refreshBtn").addEventListener("click", () => loadWithWS());

$("#logoutBtn").addEventListener("click", async () => {
	await api("/api/logout", { method: "POST" });
	$("#refreshBtn").hidden = true;
	$("#logoutBtn").hidden = true;
	showView("login");
	setStatus("Logged out");
});

(async function init() {
	if (!state.jellyfinUrl || !state.jellyfinUser || !state.letterboxdUser) {
		showView("setup");
		return;
	}
	$("#setupForm").elements.jellyfinUrl.value = state.jellyfinUrl;
	$("#setupForm").elements.jellyfinUser.value = state.jellyfinUser;
	$("#setupForm").elements.letterboxdUser.value = state.letterboxdUser;
	await loadWithWS();
})();

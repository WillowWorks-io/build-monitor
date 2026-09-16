// Radiator client. Polls the local server, which is the only thing that
// talks to CircleCI -- the API token never reaches the browser.

const POLL_MS = 5000;
// Past this, the board on screen may not reflect reality, so say so
// rather than showing stale colours as if they were current.
const STALE_MS = 180000;

const board = document.getElementById("board");
const tally = document.getElementById("tally");
const conn = document.getElementById("conn");
const clock = document.getElementById("clock");
const empty = document.getElementById("empty");
const errors = document.getElementById("errors");

let lastBoard = null;
let consecutiveFailures = 0;

// A glyph per state, so the board never depends on hue alone. Roughly
// one man in twelve cannot separate the red and green reliably, and a
// radiator is read by whoever walks past it.
const GLYPH = {
  passed: "\u2713",
  failed: "\u2715",
  on_hold: "\u23f8",
  unknown: "?",
};

function dur(secs) {
  if (!secs || secs < 0) return "";
  if (secs < 60) return `${secs}s`;
  const m = Math.floor(secs / 60);
  const s = secs % 60;
  if (m < 60) return s ? `${m}m${String(s).padStart(2, "0")}s` : `${m}m`;
  return `${Math.floor(m / 60)}h${String(m % 60).padStart(2, "0")}m`;
}

function ago(iso) {
  if (!iso) return "";
  const then = new Date(iso).getTime();
  if (!Number.isFinite(then)) return "";
  const secs = Math.max(0, (Date.now() - then) / 1000);
  if (secs < 60) return `${Math.floor(secs)}s`;
  if (secs < 3600) return `${Math.floor(secs / 60)}m`;
  if (secs < 86400) return `${Math.floor(secs / 3600)}h`;
  return `${Math.floor(secs / 86400)}d`;
}

// Choose a column count that keeps tiles close to square, so the grid
// fills a display of any shape without leaving dead space.
function columnsFor(count) {
  if (count === 0) return 1;
  const ratio = board.clientWidth / Math.max(1, board.clientHeight);
  let best = 1;
  let bestScore = Infinity;
  for (let cols = 1; cols <= count; cols++) {
    const rows = Math.ceil(count / cols);
    const tileRatio = (board.clientWidth / cols) / (board.clientHeight / rows);
    // Favour tiles near 1.6:1, and penalise a ragged final row.
    const score = Math.abs(Math.log(tileRatio / 1.6)) + (cols * rows - count) * 0.06;
    if (score < bestScore) {
      bestScore = score;
      best = cols;
    }
  }
  return best;
}

function render(data) {
  const projects = data.projects || [];

  empty.hidden = projects.length > 0;
  board.hidden = projects.length === 0;

  const c = data.counts || {};
  tally.innerHTML = [
    c.failed ? `<span class="t-failed"><b>${c.failed}</b><span>failed</span></span>` : "",
    c.running ? `<span class="t-running"><b>${c.running}</b><span>running</span></span>` : "",
    c.on_hold ? `<span class="t-onhold"><b>${c.on_hold}</b><span>on hold</span></span>` : "",
    `<span class="t-passed"><b>${c.passed || 0}</b><span>passed</span></span>`,
  ].join("");

  paintFavicon(data.counts);
  paintTitle(data.counts);

  board.style.setProperty("--cols", columnsFor(projects.length));
  board.classList.toggle("has-failure", (c.failed || 0) > 0);

  // Long names shrink so they never clip, short ones stay poster-sized.
  const longest = projects.reduce((n, p) => Math.max(n, p.name.length), 0);
  const nameSize = longest > 22 ? "clamp(14px, 1.5vw, 28px)"
    : longest > 14 ? "clamp(16px, 1.9vw, 36px)"
    : "clamp(18px, 2.4vw, 46px)";

  board.replaceChildren(...projects.map((p) => {
    const a = document.createElement("a");
    a.className = `tile ${p.status}`;
    a.href = p.url;
    a.target = "_blank";
    a.rel = "noopener noreferrer";
    a.style.setProperty("--name-size", nameSize);

    const org = document.createElement("div");
    org.className = "tile-org";
    org.textContent = p.org;

    const name = document.createElement("div");
    name.className = "tile-name";
    name.textContent = p.name;

    const foot = document.createElement("div");
    foot.className = "tile-foot";

    const commit = document.createElement("div");
    commit.className = "tile-commit";
    const branch = document.createElement("span");
    branch.className = "tile-branch";
    branch.textContent = p.branch || "?";
    commit.append(branch, document.createTextNode(p.detail || p.commit_title || p.actor || ""));

    const age = document.createElement("div");
    age.className = "tile-age";
    if (p.status === "running") {
      age.textContent = p.eta_seconds ? `~${dur(p.eta_seconds)} left` : "running";
    } else {
      const d = dur(p.duration_seconds);
      age.textContent = d ? `${d} \u00b7 ${ago(p.finished_at)}` : ago(p.finished_at);
    }

    foot.append(commit, age);
    a.append(org, name);

    // How long it has been red matters more than that it is red: "just
    // broke" and "broken for a month" want different reactions.
    if (p.status === "failed" && p.broken_since) {
      const broken = document.createElement("div");
      broken.className = "broken";
      const span = ago(p.broken_since);
      broken.textContent = p.broken_builds > 1
        ? `broken ${span} \u00b7 ${p.broken_builds} builds`
        : `broken ${span}`;
      a.append(broken);
    }

    a.append(foot);

    if (p.status === "running") {
      const spin = document.createElement("div");
      spin.className = "spinner";
      a.append(spin);

      // Elapsed against this workflow's median duration. A spinner says
      // something is happening; a bar says how much longer.
      if (p.progress > 0) {
        const bar = document.createElement("div");
        bar.className = "progress";
        const fill = document.createElement("div");
        fill.className = "progress-fill";
        fill.style.width = `${Math.round(p.progress * 100)}%`;
        bar.append(fill);
        a.append(bar);
      }
    } else {
      const g = document.createElement("div");
      g.className = "glyph";
      g.textContent = GLYPH[p.status] || "";
      a.append(g);
    }
    return a;
  }));

  if (data.errors && data.errors.length) {
    errors.hidden = false;
    errors.replaceChildren(...data.errors.map((e) => {
      const p = document.createElement("p");
      p.textContent = e;
      return p;
    }));
  } else {
    errors.hidden = true;
  }
}

function paintFreshness() {
  if (!lastBoard) return;
  const updated = new Date(lastBoard.updated_at).getTime();
  const age = Date.now() - updated;

  if (consecutiveFailures > 2) {
    conn.className = "conn down";
    conn.textContent = "server unreachable";
  } else if (age > STALE_MS) {
    conn.className = "conn stale";
    conn.textContent = `stale ${ago(lastBoard.updated_at)}`;
  } else {
    conn.className = "conn";
    conn.textContent = `updated ${ago(lastBoard.updated_at)} ago`;
  }
  clock.textContent = new Date().toLocaleTimeString([], {
    hour: "2-digit", minute: "2-digit", second: "2-digit",
  });
}

async function poll() {
  try {
    const res = await fetch("/api/status", { cache: "no-store" });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    lastBoard = await res.json();
    consecutiveFailures = 0;
    render(lastBoard);
  } catch {
    // Keep the last good board on screen; the freshness line carries
    // the bad news rather than blanking the wall.
    consecutiveFailures++;
  }
  paintFreshness();
}

// Kiosk shortcuts: a radiator lives fullscreen on a spare display, where
// browser chrome is just wasted pixels.
addEventListener("keydown", (e) => {
  if (e.metaKey || e.ctrlKey || e.altKey) return;
  const k = e.key.toLowerCase();
  if (k === "f") {
    if (document.fullscreenElement) document.exitFullscreen();
    else document.documentElement.requestFullscreen().catch(() => {});
  } else if (k === "h") {
    document.body.classList.toggle("no-header");
    if (lastBoard) render(lastBoard);
  } else if (k === "?" || k === "/") {
    document.getElementById("help").hidden = !document.getElementById("help").hidden;
  } else if (k === "escape") {
    document.getElementById("help").hidden = true;
  }
});

let resizeTimer;
addEventListener("resize", () => {
  clearTimeout(resizeTimer);
  resizeTimer = setTimeout(() => lastBoard && render(lastBoard), 150);
});

poll();
setInterval(poll, POLL_MS);
setInterval(paintFreshness, 1000);

// --- live favicon ---------------------------------------------------
// The tab strip is the smallest possible radiator. Repainting the icon
// to match the board means a backgrounded tab still reports the build,
// which is most of the time a monitor spends.

const FAVICON_INK = {
  failed: "#e0483d",
  running: "#2f6fd0",
  on_hold: "#c78b16",
  unknown: "#5a6675",
  passed: "#22a55c",
};

function faviconFor(counts) {
  // The odd tile takes the worst live state; everything else stays
  // green, so the mark's identity holds and only its news changes.
  const worst = counts.failed ? "failed"
    : counts.running ? "running"
    : counts.on_hold ? "on_hold"
    : counts.unknown ? "unknown"
    : "passed";
  const odd = FAVICON_INK[worst];
  const rest = FAVICON_INK.passed;
  const tile = (x, y, fill) =>
    `<rect x="${x}" y="${y}" width="10" height="10" rx="2.4" fill="${fill}"/>`;
  return "data:image/svg+xml," + encodeURIComponent(
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">` +
    `<rect width="32" height="32" rx="7" fill="#0b0e13"/>` +
    tile(5, 5, odd) + tile(17, 5, rest) +
    tile(5, 17, rest) + tile(17, 17, rest) +
    `</svg>`);
}

let lastFavicon = "";

function paintFavicon(counts) {
  const href = faviconFor(counts || {});
  if (href === lastFavicon) return; // avoid pointless DOM churn every poll
  lastFavicon = href;
  let link = document.querySelector("link[rel='icon']");
  if (!link) {
    link = document.createElement("link");
    link.rel = "icon";
    document.head.append(link);
  }
  link.type = "image/svg+xml";
  link.href = href;
}

function paintTitle(counts) {
  const c = counts || {};
  // Failures belong in the tab title too: a truncated tab still shows
  // its first few characters.
  document.title = c.failed
    ? `(${c.failed}) FAILED — Build Monitor`
    : c.running
      ? `(${c.running}) running — Build Monitor`
      : "Build Monitor";
}

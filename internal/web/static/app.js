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
    age.textContent = p.status === "running" ? "running" : ago(p.finished_at);

    foot.append(commit, age);
    a.append(org, name, foot);

    if (p.status === "running") {
      const spin = document.createElement("div");
      spin.className = "spinner";
      a.append(spin);
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

let resizeTimer;
addEventListener("resize", () => {
  clearTimeout(resizeTimer);
  resizeTimer = setTimeout(() => lastBoard && render(lastBoard), 150);
});

poll();
setInterval(poll, POLL_MS);
setInterval(paintFreshness, 1000);

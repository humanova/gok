'use strict';

// ── Constants ────────────────────────────────────────────────────────────────
const PLAYBACK_DELAY_S  = 360;        // 6-min real-time lag
const LIVE_POLL_MS      = 30_000;     // poll server every 30 s in live mode
const CATCHUP_SPREAD_MS = 10_000;     // spread catch-up pings over 10 s on load
const SEEK_SPREAD_MS    = 3_000;      // spread catch-up pings over 3 s on seek
const GRID_SIZE         = 25;         // max topics shown
const RANGE_WINDOW_S    = 3_600;      // 1 h window fetched per seek
const CACHE_WINDOW_S    = 12 * 3600;  // client playback window: 12 h
const LIVE_THRESHOLD    = 0.998;      // scrubber frac ≥ this → live mode

// ── State ────────────────────────────────────────────────────────────────────
const state = {
  mode: 'live',          // 'live' | 'scrub'
  topics: [],            // current topic list (ranked)
  pendingTimers: [],     // setTimeout IDs (for bulk cancel)
  lastSeenTs: {},        // topicId → highest timestamp already scheduled
  pollTimer: null,
  scrubInterval: null,
  lastSnapshotAt: 0,
  speed: 1,              // playback speed multiplier (scrub mode only)
  replayOriginTs: 0,     // playback unix-s position when replay started
  replayWallMs: 0,       // Date.now() when replay started
  scrubWindowEnd: 0,     // end of last-fetched scrub window (unix s)
  fetchingNext: false,   // guard against concurrent window prefetches
};

let _rafId = null;

// DOM refs (populated in init())
let $grid, $scrubber, $topicCount, $lastUpdated, $playbackTime;

// ── Debug Panel ──────────────────────────────────────────────────────────────
const debug = {
  _items:   new Map(),
  _nextId:  0,
  _pending: 0,
  _$list:   null,
  _$header: null,

  init() {
    this._$list   = $id('debug-list');
    this._$header = $id('debug-header');
  },

  add(debugId, topicTitle, entryTs, fireAt) {
    const li = document.createElement('li');
    li.id        = `dbg-${debugId}`;
    li.className = 'dbg-item pending';
    li.dataset.fireAt = String(fireAt);

    const icon  = document.createElement('span');
    icon.className   = 'dbg-icon';
    icon.textContent = '·';

    const time  = document.createElement('span');
    time.className   = 'dbg-time';
    time.textContent = new Date(entryTs * 1000)
      .toLocaleTimeString('tr-TR', { hour: '2-digit', minute: '2-digit', second: '2-digit' });

    const title = document.createElement('span');
    title.className   = 'dbg-title';
    title.textContent = topicTitle;
    title.title       = topicTitle;

    li.append(icon, time, title);

    let inserted = false;
    for (const child of this._$list.children) {
      if (parseFloat(child.dataset.fireAt) > fireAt) {
        this._$list.insertBefore(li, child);
        inserted = true;
        break;
      }
    }
    if (!inserted) this._$list.appendChild(li);

    this._items.set(debugId, li);
    this._pending++;
    this._updateHeader();

    if (this._$list.children.length > 300) {
      for (const child of [...this._$list.children]) {
        if (this._$list.children.length <= 300) break;
        if (child.classList.contains('fired')) {
          this._items.delete(parseInt(child.id.replace('dbg-', ''), 10));
          child.remove();
        }
      }
    }
  },

  tick(debugId) {
    const li = this._items.get(debugId);
    if (!li) return;
    li.classList.remove('pending');
    li.classList.add('fired');
    li.querySelector('.dbg-icon').textContent = '✓';
    if (this._pending > 0) this._pending--;
    this._updateHeader();
  },

  clear() {
    this._$list.innerHTML = '';
    this._items.clear();
    this._pending = 0;
    this._updateHeader();
  },

  _updateHeader() {
    this._$header.textContent = `queue · ${this._pending} pending`;
  },
};

// ── Utilities ────────────────────────────────────────────────────────────────
const $id = id => document.getElementById(id);

/** Current playback position based on real wall clock minus the fixed lag. */
function playbackNow() {
  return Date.now() / 1000 - PLAYBACK_DELAY_S;
}

/** Current playback position accounting for mode and speed. */
function currentPlaybackTs() {
  if (state.mode === 'live') return playbackNow();
  return state.replayOriginTs + (Date.now() - state.replayWallMs) / 1000 * state.speed;
}

/** Format a unix timestamp as "DD.MM HH:MM" in Turkish locale. */
function fmtTs(ts) {
  return new Date(ts * 1000).toLocaleString('tr-TR', {
    day: '2-digit', month: '2-digit',
    hour: '2-digit', minute: '2-digit',
  });
}

// ── API calls ────────────────────────────────────────────────────────────────
async function fetchLive() {
  try {
    const r = await fetch('/api/pulse');
    if (!r.ok) throw new Error(r.status);
    return await r.json();
  } catch (e) {
    console.error('[pulse] fetchLive:', e);
    return null;
  }
}

async function fetchRange(since, until) {
  try {
    const url = `/api/pulse/range?since=${Math.floor(since)}&until=${Math.floor(until)}`;
    const r = await fetch(url);
    if (!r.ok) throw new Error(r.status);
    return await r.json();
  } catch (e) {
    console.error('[pulse] fetchRange:', e);
    return null;
  }
}

// ── Grid ─────────────────────────────────────────────────────────────────────
function createTile(t) {
  const tile = document.createElement('div');
  tile.className = 'tile';
  tile.id = `tile-${t.id}`;
  tile.style.order = t.rank - 1;
  applyHeat(tile, t);

  const label = document.createElement('div');
  label.className = 'tile-label';
  label.textContent = t.title;
  label.title = t.title;
  tile.appendChild(label);
  return tile;
}

function buildGrid(topics) {
  $grid.innerHTML = '';
  state.topics = topics;
  $topicCount.textContent = topics.length;
  topics.forEach(t => $grid.appendChild(createTile(t)));
}

function applyHeat(tile, t) {
  tile.style.setProperty('--heat', t.heat_score.toFixed(3));
  tile.style.setProperty('--rank-idx', (t.rank - 1).toString());
}

/**
 * Animate the grid to a new topic ranking using the FLIP technique.
 * Tiles that move animate via CSS transform. Entering tiles fade in,
 * leaving tiles fade out and are removed after the transition.
 */
function reorderGrid(newTopics) {
  const newIds = new Set(newTopics.map(t => t.id));
  const oldIds = new Set(state.topics.map(t => t.id));

  // Step 1 (FLIP: First) — record current rects for tiles that will survive.
  const beforeRects = new Map();
  state.topics.forEach(t => {
    const el = $id(`tile-${t.id}`);
    if (el && newIds.has(t.id)) {
      beforeRects.set(t.id, el.getBoundingClientRect());
    }
  });

  // Step 2 — fade out departing tiles.
  oldIds.forEach(id => {
    if (newIds.has(id)) return;
    const el = $id(`tile-${id}`);
    if (!el) return;
    el.style.transition = 'opacity 0.35s ease';
    el.style.opacity = '0';
    setTimeout(() => el.remove(), 400);
  });

  // Step 3 — add arriving tiles (hidden initially).
  newTopics.forEach(t => {
    if (oldIds.has(t.id)) return;
    const tile = createTile(t);
    tile.style.opacity = '0';
    $grid.appendChild(tile);
  });

  // Step 4 — update CSS order and heat for surviving tiles (suppress transitions).
  newTopics.forEach(t => {
    const el = $id(`tile-${t.id}`);
    if (!el || !oldIds.has(t.id)) return;
    el.style.transition = 'none';
    el.style.order = t.rank - 1;
    applyHeat(el, t);
  });

  // Step 5 (FLIP: Last + Invert + Play) — force layout, compute deltas, animate.
  void $grid.offsetHeight;

  newTopics.forEach(t => {
    const el = $id(`tile-${t.id}`);
    if (!el || !beforeRects.has(t.id)) return;

    const before = beforeRects.get(t.id);
    const after  = el.getBoundingClientRect();
    const dx = before.left - after.left;
    const dy = before.top  - after.top;

    if (Math.abs(dx) > 0.5 || Math.abs(dy) > 0.5) {
      el.style.transition = 'none';
      el.style.transform  = `translate(${dx}px, ${dy}px)`;
      void el.offsetHeight; // force paint
      el.style.transition = 'transform 0.55s cubic-bezier(0.4, 0, 0.2, 1), background 1.2s ease';
      el.style.transform  = '';
      el.addEventListener('transitionend', () => {
        el.style.transition = '';
        el.style.transform  = '';
      }, { once: true });
    } else {
      el.style.transition = '';
    }
  });

  // Step 6 — fade in arriving tiles.
  newTopics.forEach(t => {
    if (oldIds.has(t.id)) return;
    const el = $id(`tile-${t.id}`);
    if (!el) return;
    void el.offsetHeight;
    el.style.transition = 'opacity 0.4s ease';
    el.style.opacity    = '1';
    el.addEventListener('transitionend', () => { el.style.transition = ''; }, { once: true });
  });

  state.topics = newTopics;
  $topicCount.textContent = newTopics.length;
}

// ── Ping ─────────────────────────────────────────────────────────────────────
function firePing(topicId, rank) {
  const tile = $id(`tile-${topicId}`);
  if (!tile) return;

  const ping = document.createElement('div');
  ping.className = 'ping';

  // Colour: warm orange (rank 1) → cool blue (rank 25).
  const t = Math.max(0, Math.min(1, (rank - 1) / (GRID_SIZE - 1)));
  const hue = Math.round(28 + t * 172);
  const sat = Math.round(100 - t * 22);
  ping.style.setProperty('--ping-color', `hsl(${hue},${sat}%,65%)`);

  const ox = ((Math.random() - 0.5) * 34).toFixed(1);
  const oy = ((Math.random() - 0.5) * 34).toFixed(1);
  ping.style.setProperty('--ox', `${ox}%`);
  ping.style.setProperty('--oy', `${oy}%`);

  tile.appendChild(ping);
  ping.addEventListener('animationend', () => ping.remove(), { once: true });
}

// ── Scheduler ────────────────────────────────────────────────────────────────
/**
 * Schedule pings for all topics, globally sorted by entry timestamp.
 *
 * Past pings use proportional spread: oldest entry fires first at delay≈0,
 * newest entry fires last at delay≈catchupSpreadMs — preserving chronological order.
 *
 * Future pings fire at (ts - originTs) * 1000 / speed ms from now.
 * In live mode speed is always treated as 1 (real time).
 */
function schedulePings(topics, originTs, catchupSpreadMs, maxCatchup) {
  const spd = state.mode === 'scrub' ? state.speed : 1;

  const pastPings   = [];
  const futurePings = [];
  const newMaxTs    = {};

  topics.forEach(t => {
    const ts   = t.timestamps ?? [];
    const seen = state.lastSeenTs[t.id] ?? 0;

    for (const v of ts) {
      if (v <= seen) continue;
      const entry = { topicId: t.id, rank: t.rank, title: t.title, ts: v };
      if (v <= originTs) pastPings.push(entry);
      else               futurePings.push(entry);
      if ((newMaxTs[t.id] ?? 0) < v) newMaxTs[t.id] = v;
    }
  });

  // Sort both globally by timestamp (ascending = oldest fires first).
  pastPings.sort((a, b) => a.ts - b.ts);
  futurePings.sort((a, b) => a.ts - b.ts);

  // Catch-up: proportional spread so chronological order is preserved.
  const capped = isFinite(maxCatchup) ? pastPings.slice(-maxCatchup) : pastPings;
  if (capped.length > 0) {
    const minTs = capped[0].ts;
    const span  = Math.max(1, originTs - minTs);
    capped.forEach(p => {
      const frac    = (p.ts - minTs) / span;
      const delay   = catchupSpreadMs > 0 ? frac * catchupSpreadMs : 0;
      const fireAt  = Date.now() + delay;
      const debugId = debug._nextId++;
      debug.add(debugId, p.title, p.ts, fireAt);
      const timerId = setTimeout(() => { debug.tick(debugId); firePing(p.topicId, p.rank); }, delay);
      state.pendingTimers.push(timerId);
    });
  }

  // Future pings — apply speed divisor.
  futurePings.forEach(p => {
    const delay = (p.ts - originTs) * 1000 / spd;
    if (delay > 0 && delay < 86_400_000) {
      const fireAt  = Date.now() + delay;
      const debugId = debug._nextId++;
      debug.add(debugId, p.title, p.ts, fireAt);
      const timerId = setTimeout(() => { debug.tick(debugId); firePing(p.topicId, p.rank); }, delay);
      state.pendingTimers.push(timerId);
    }
  });

  // Advance per-topic high-water marks to prevent re-firing on re-runs.
  for (const tid in newMaxTs) {
    state.lastSeenTs[tid] = newMaxTs[tid];
  }
}

function cancelAllTimers() {
  state.pendingTimers.forEach(id => clearTimeout(id));
  state.pendingTimers = [];
  debug.clear();
}

// ── Replay animation (RAF scrubber advance) ───────────────────────────────────
function startReplayAnimation() {
  cancelReplayAnimation();
  function tick() {
    const playTs = currentPlaybackTs();
    const minTs  = Date.now() / 1000 - CACHE_WINDOW_S;
    const maxTs  = Date.now() / 1000 - PLAYBACK_DELAY_S;
    const frac   = Math.max(0, Math.min(1, (playTs - minTs) / (maxTs - minTs)));
    $scrubber.value = (frac * 1000).toFixed(0);
    setPlaybackTime(playTs);
    // Prefetch the next window when within 30 s of the current window end,
    // so scrub playback continues seamlessly at any speed.
    if (!state.fetchingNext && state.scrubWindowEnd > 0 && playTs > state.scrubWindowEnd - 30) {
      state.fetchingNext = true;
      const nextFrom = state.scrubWindowEnd;
      fetchRange(nextFrom, nextFrom + RANGE_WINDOW_S).then(snap => {
        state.fetchingNext = false;
        if (!snap || state.mode !== 'scrub') return;
        reorderGrid(snap.topics);
        schedulePings(snap.topics, nextFrom, 0, Infinity);
        state.scrubWindowEnd = nextFrom + RANGE_WINDOW_S;
      });
    }
    _rafId = requestAnimationFrame(tick);
  }
  _rafId = requestAnimationFrame(tick);
}

function cancelReplayAnimation() {
  if (_rafId !== null) {
    cancelAnimationFrame(_rafId);
    _rafId = null;
  }
}

// ── Mode management ───────────────────────────────────────────────────────────
function enterLiveMode() {
  cancelReplayAnimation();
  document.body.classList.add('is-live');
  state.mode = 'live';

  state.pollTimer = setInterval(async () => {
    const snap = await fetchLive();
    if (!snap) return;
    if (snap.snapshot_at !== state.lastSnapshotAt) {
      state.lastSnapshotAt = snap.snapshot_at;
      reorderGrid(snap.topics);
      // New entries from the latest scrape are near playbackNow; no spread needed.
      schedulePings(snap.topics, playbackNow(), 0, Infinity);
      setLastUpdated(snap.snapshot_at);
    }
  }, LIVE_POLL_MS);

  state.scrubInterval = setInterval(syncScrubberToLive, 15_000);
  syncScrubberToLive();
}

function exitLiveMode() {
  document.body.classList.remove('is-live');
  state.mode = 'scrub';
  clearInterval(state.pollTimer);     state.pollTimer = null;
  clearInterval(state.scrubInterval); state.scrubInterval = null;
}

/** Seek to a historical point: cancel everything, fetch a 1 h window, replay. */
async function seekTo(targetTs) {
  cancelAllTimers();
  cancelReplayAnimation();
  state.lastSeenTs  = {};
  state.fetchingNext = false;
  state.mode = 'scrub';

  state.replayOriginTs = targetTs;
  state.replayWallMs   = Date.now();

  const snap = await fetchRange(targetTs, targetTs + RANGE_WINDOW_S);
  if (!snap) return;

  state.scrubWindowEnd = targetTs + RANGE_WINDOW_S;
  reorderGrid(snap.topics);
  schedulePings(snap.topics, targetTs, SEEK_SPREAD_MS, Infinity);
  setPlaybackTime(targetTs);
  startReplayAnimation();
}

/** Re-enter live mode: fresh fetch, clear dedup state, re-schedule. */
async function goLive() {
  // exitLiveMode first so we never leak a second pair of intervals if goLive
  // is triggered while already live (e.g. user clicks the rightmost scrubber
  // position without dragging, so no 'input' event fires beforehand).
  exitLiveMode();
  cancelAllTimers();
  cancelReplayAnimation();
  state.lastSeenTs = {};
  const snap = await fetchLive();
  if (!snap) {
    // Restore live appearance so the indicator stays green and the user
    // can retry by interacting with the scrubber.
    document.body.classList.add('is-live');
    state.mode = 'live';
    $playbackTime.textContent = 'bağlantı hatası';
    return;
  }
  reorderGrid(snap.topics);
  state.lastSnapshotAt = snap.snapshot_at;
  schedulePings(snap.topics, playbackNow(), CATCHUP_SPREAD_MS, Infinity);
  setLastUpdated(snap.snapshot_at);
  enterLiveMode();
}

// ── Speed control ─────────────────────────────────────────────────────────────
function setSpeed(s) {
  state.speed = s;
  document.querySelectorAll('.speed-btn').forEach(b => {
    b.classList.toggle('active', parseFloat(b.dataset.speed) === s);
  });
  // In scrub mode: restart replay from current position with new speed.
  if (state.mode === 'scrub') {
    seekTo(currentPlaybackTs());
  }
}

// ── Scrubber ─────────────────────────────────────────────────────────────────
/** Map scrubber value [0, 1000] to a unix timestamp. */
function scrubToTs(val) {
  const frac = val / 1000;
  const now  = Date.now() / 1000;
  return now - CACHE_WINDOW_S + frac * (CACHE_WINDOW_S - PLAYBACK_DELAY_S);
}

function initScrubber() {
  // While dragging: exit live mode and preview the time (no fetch yet).
  $scrubber.addEventListener('input', () => {
    if (state.mode === 'live') exitLiveMode();
    cancelReplayAnimation();
    setPlaybackTime(scrubToTs($scrubber.value));
  });

  // On release: fetch data for the chosen position.
  $scrubber.addEventListener('change', async () => {
    const frac = $scrubber.value / 1000;
    if (frac >= LIVE_THRESHOLD) {
      await goLive();
    } else {
      await seekTo(scrubToTs($scrubber.value));
    }
  });
}

/** Pin the scrubber thumb to the live (rightmost) position. */
function syncScrubberToLive() {
  $scrubber.value = 1000;
  setPlaybackTime(currentPlaybackTs());
}

// ── Status bar helpers ────────────────────────────────────────────────────────
function setPlaybackTime(ts) {
  $playbackTime.textContent = fmtTs(ts);
}

function setLastUpdated(snapshotAt) {
  $lastUpdated.textContent = '· ' + new Date(snapshotAt * 1000)
    .toLocaleTimeString('tr-TR', { hour: '2-digit', minute: '2-digit' });
}

// ── Init ─────────────────────────────────────────────────────────────────────
async function init() {
  $grid         = $id('grid');
  $scrubber     = $id('scrubber');
  $topicCount   = $id('topic-count');
  $lastUpdated  = $id('last-updated');
  $playbackTime = $id('playback-time');
  debug.init();

  initScrubber();

  // Wire up speed buttons.
  document.querySelectorAll('.speed-btn').forEach(btn => {
    btn.addEventListener('click', () => setSpeed(parseFloat(btn.dataset.speed)));
  });

  const snap = await fetchLive();
  if (!snap) {
    $playbackTime.textContent = 'veri alınamadı';
    return;
  }

  buildGrid(snap.topics);
  state.lastSnapshotAt = snap.snapshot_at;
  setLastUpdated(snap.snapshot_at);

  schedulePings(snap.topics, playbackNow(), CATCHUP_SPREAD_MS, Infinity);
  enterLiveMode();
}

document.addEventListener('DOMContentLoaded', init);

'use strict';

// ── Constants ────────────────────────────────────────────────────────────────
const PLAYBACK_DELAY_S  = 300;        // 5-min real-time lag
const LIVE_POLL_MS      = 30_000;     // poll server every 30 s in live mode
const CATCHUP_SPREAD_MS = 10_000;     // spread catch-up pings over 10 s on load
const SEEK_SPREAD_MS    = 3_000;      // spread catch-up pings over 3 s on seek
const RANGE_WINDOW_S    = 3_600;      // 1 h window fetched per seek
const CACHE_WINDOW_S    = 24 * 3600;  // client playback window: 24 h
const LIVE_THRESHOLD    = 0.998;      // scrubber frac ≥ this → live mode
const MAX_VISIBLE_PINGS = 3;
const METER_THRESHOLDS  = [1, 2, 4, 7, 11];
const BRIEF_CACHE_TTL_MS = 60_000;

// Constrain animation work in social-app WebViews
const IS_SOCIAL_IN_APP_BROWSER = /\b(?:Twitter|TwitterAndroid|X\/|LinkedInApp|LinkedIn|Instagram)/i.test(navigator.userAgent);
const REPLAY_RENDER_INTERVAL_MS = IS_SOCIAL_IN_APP_BROWSER ? 75 : 0;
const PING_MIN_GAP_MS = IS_SOCIAL_IN_APP_BROWSER ? 125 : 0;

// ── State ────────────────────────────────────────────────────────────────────
const state = {
  mode: 'live',          // 'live' | 'scrub'
  topics: [],            // current topic list (ranked)
  pendingTimers: [],     // setTimeout IDs (for bulk cancel)
  seenTimestampCounts: {}, // topicId → timestamp → event count already scheduled
  pollTimer: null,
  scrubInterval: null,
  lastSnapshotAt: 0,
  speed: 1,              // playback speed multiplier (scrub mode only)
  replayOriginTs: 0,     // playback unix-s position when replay started
  replayWallMs: 0,       // Date.now() when replay started
  scrubWindowEnd: 0,     // end of last-fetched scrub window (unix s)
  fetchingNext: false,   // guard against concurrent window prefetches
  activePings: {},       // topicId → currently visible pings
  burstCounts: {},       // topicId → pings compressed into the visible burst marker
  burstTimers: {},       // topicId → burst marker timeout
  afterglowTimers: {},   // topicId → transient activity glow timeout
  selectedTopic: null,
  view: 'grid',          // 'grid' | 'list'
  briefCache: new Map(), // topicId → { brief, expiresAt }
  briefRequestID: 0,
  previewPlaybackTs: null,
  lastActivitySecond: -1,
  transitioningLive: false,
  nextPingAt: 0,
};

let _rafId = null;
let _lastReplayRenderAt = 0;

// DOM refs (populated in init())
let $grid, $topicList, $scrubber, $lastUpdated, $playbackTime;

// ── Debug Panel ──────────────────────────────────────────────────────────────
const debug = {
  enabled:  true,
  _items:   new Map(),
  _nextId:  0,
  _pending: 0,
  _autoFollow: true,
  _$list:   null,

  init() {
    this._$list   = $id('debug-list');
    this._$toggle = $id('event-toggle');
    this._$count  = $id('event-toggle-count');
    this._$follow = $id('event-follow');
    this._$toggle.addEventListener('click', () => {
      const isOpen = document.body.classList.toggle('events-open');
      this._$toggle.setAttribute('aria-expanded', String(isOpen));
      if (isOpen && this._autoFollow) this.scrollToLatest();
    });
    this._$follow.addEventListener('click', () => {
      this._autoFollow = true;
      this._updateHeader();
      this.scrollToLatest();
    });
    this._$list.addEventListener('wheel', () => this.pauseFollowing(), { passive: true });
    this._$list.addEventListener('touchstart', () => this.pauseFollowing(), { passive: true });
    this._$list.addEventListener('pointerdown', () => this.pauseFollowing());
    this._$list.addEventListener('keydown', () => this.pauseFollowing());
  },

  close() {
    document.body.classList.remove('events-open');
    this._$toggle.setAttribute('aria-expanded', 'false');
  },

  add(debugId, topicTitle, entryTs, fireAt, minuteCount) {
    if (!this.enabled) return;
    const li = document.createElement('li');
    li.id        = `dbg-${debugId}`;
    li.className = 'dbg-item';
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
    if (minuteCount > 1) {
      const burst = document.createElement('span');
      burst.className = 'dbg-burst';
      burst.textContent = `×${minuteCount}`;
      burst.title = `Bu dakika ${minuteCount} girdi`;
      li.appendChild(burst);
    }

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
    if (!this.enabled) return;
    const li = this._items.get(debugId);
    if (!li) return;
    li.classList.remove('pending');
    li.classList.add('fired');
    li.querySelector('.dbg-icon').textContent = '✓';
    if (this._pending > 0) this._pending--;
    this._updateHeader();
    if (this._autoFollow) this.scrollToItem(li);
  },

  clear() {
    if (!this.enabled) return;
    this._$list.innerHTML = '';
    this._items.clear();
    this._pending = 0;
    this._updateHeader();
  },

  _updateHeader() {
    if (!this.enabled) return;
    $id('debug-title').textContent = `olaylar · ${this._pending} sırada`;
    this._$count.textContent = String(this._pending);
    this._$follow.textContent = this._autoFollow ? 'takipte' : 'takibi aç';
    this._$follow.classList.toggle('is-paused', !this._autoFollow);
  },

  pauseFollowing() {
    if (!this._autoFollow) return;
    this._autoFollow = false;
    this._updateHeader();
  },

  scrollToLatest() {
    const latest = this._$list.querySelector('.dbg-item.fired:last-of-type')
      ?? this._$list.lastElementChild;
    if (latest) this.scrollToItem(latest);
  },

  scrollToItem(item) {
    item.scrollIntoView({ block: 'center', behavior: 'smooth' });
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

// The source exposes minute-level timestamps only. Keep those raw timestamps
// intact, but deterministically place same-minute events across that minute for
// playback. This makes volume visible without inventing random source times.
function virtualizeTimestampEvents(timestamps) {
  const events = [];
  for (let start = 0; start < timestamps.length;) {
    const rawTs = timestamps[start];
    let end = start + 1;
    while (end < timestamps.length && timestamps[end] === rawTs) end++;

    const minuteCount = end - start;
    const minuteStart = Math.floor(rawTs / 60) * 60;
    for (let index = 0; index < minuteCount; index++) {
      events.push({
        ts: rawTs,
        playTs: minuteStart + 60 * (index + 0.5) / minuteCount,
        minuteCount,
      });
    }
    start = end;
  }
  return events;
}

function recentVisualEventCount(timestamps, playbackTs) {
  return virtualizeTimestampEvents(timestamps)
    .filter(event => event.playTs > playbackTs - 300 && event.playTs <= playbackTs).length;
}

// ── API calls ────────────────────────────────────────────────────────────────
async function fetchLive() {
  try {
    const r = await fetch('api/pulse');
    if (!r.ok) throw new Error(r.status);
    return await r.json();
  } catch (e) {
    console.error('[pulse] fetchLive:', e);
    return null;
  }
}

async function fetchRange(since, until) {
  try {
    const url = `api/pulse/range?since=${Math.floor(since)}&until=${Math.floor(until)}`;
    const r = await fetch(url);
    if (!r.ok) throw new Error(r.status);
    return await r.json();
  } catch (e) {
    console.error('[pulse] fetchRange:', e);
    return null;
  }
}

async function fetchTopicBrief(topicID) {
  try {
    const response = await fetch(`api/topics/${topicID}/brief`);
    if (!response.ok) throw new Error(response.status);
    return await response.json();
  } catch (error) {
    console.error('[pulse] fetchTopicBrief:', error);
    return null;
  }
}

// ── Grid ─────────────────────────────────────────────────────────────────────
function createTile(t) {
  const tile = document.createElement('div');
  tile.className = 'tile';
  tile.id = `tile-${t.id}`;
  tile.style.order = t.rank - 1;
  tile.classList.toggle('mobile-secondary', t.rank > 20);
  tile.tabIndex = 0;
  tile.setAttribute('role', 'button');
  applyHeat(tile, t);

  const rank = document.createElement('span');
  rank.className = 'tile-rank';
  rank.textContent = String(t.rank).padStart(2, '0');
  tile.appendChild(rank);

  const meter = document.createElement('span');
  meter.className = 'tile-meter';
  for (let i = 0; i < 5; i++) meter.appendChild(document.createElement('i'));
  tile.appendChild(meter);

  const burst = document.createElement('span');
  burst.className = 'burst-count';
  tile.appendChild(burst);

  const label = document.createElement('div');
  label.className = 'tile-label';
  label.textContent = t.title;
  label.title = t.title;
  tile.appendChild(label);
  updateTileDetails(tile, t);
  tile.addEventListener('click', () => openTopicBrief(t));
  tile.addEventListener('keydown', event => {
    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault();
      openTopicBrief(t);
    }
  });
  return tile;
}

function buildGrid(topics) {
  $grid.innerHTML = '';
  state.topics = topics;
  state.topics.forEach(t => $grid.appendChild(createTile(t)));
}

function formatRankMovement(delta) {
  if (!Number.isFinite(delta) || delta === 0) return { label: '→', direction: 'steady' };
  return delta > 0
    ? { label: `↑ ${delta}`, direction: 'up' }
    : { label: `↓ ${Math.abs(delta)}`, direction: 'down' };
}

function countRecentEntries(timestamps, playbackTs, windowSecs = 900) {
  return (timestamps ?? []).filter(ts => ts > playbackTs - windowSecs && ts <= playbackTs).length;
}

function firstPopularLabel(firstPopularAt, playbackTs) {
  if (!firstPopularAt) return '';
  const ageSecs = playbackTs - firstPopularAt;
  return ageSecs >= 0 && ageSecs < 24 * 3600 ? 'yeni' : '';
}

function createTopicRow(topic, playbackTs = currentPlaybackTs()) {
  const row = document.createElement('div');
  row.className = 'topic-row';
  row.id = `topic-row-${topic.id}`;
  row.tabIndex = 0;
  row.setAttribute('role', 'button');

  const title = document.createElement('span');
  title.className = 'topic-row-title';
  title.textContent = topic.title;
  title.title = topic.title;

  const trend = document.createElement('span');
  trend.className = 'topic-row-trend';

  const activity = document.createElement('span');
  activity.className = 'topic-row-activity';

  const status = document.createElement('span');
  status.className = 'topic-row-status';

  row.append(status, trend, title, activity);
  updateTopicRowDetails(row, topic, playbackTs);
  row.addEventListener('click', () => openTopicBrief(topic));
  row.addEventListener('keydown', event => {
    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault();
      openTopicBrief(topic);
    }
  });
  return row;
}

function updateTopicRowDetails(row, topic, playbackTs = currentPlaybackTs()) {
  const trend = row.querySelector('.topic-row-trend');
  const activity = row.querySelector('.topic-row-activity');
  if (state.mode === 'live') {
    const movement = formatRankMovement(topic.rank_delta);
    trend.textContent = movement.label;
    trend.className = `topic-row-trend ${movement.direction}`;
    const entryCount = countRecentEntries(topic.timestamps, playbackTs);
    activity.textContent = `${entryCount} / 15 dk`;
  } else {
    trend.textContent = '';
    trend.className = 'topic-row-trend';
    activity.textContent = '';
  }

  const status = row.querySelector('.topic-row-status');
  const label = firstPopularLabel(topic.first_popular_at, playbackTs);
  status.textContent = label;
  status.classList.toggle('is-new', label === 'yeni');
}

function buildTopicList(topics) {
  $topicList.innerHTML = '';
  topics.forEach(topic => $topicList.appendChild(createTopicRow(topic)));
}

function applyHeat(tile, t) {
  const heat = Math.max(0.05, Math.min(1, t.heat_score));
  tile.style.setProperty('--tile-hue', String(Math.round(214 - heat * 185)));
  tile.style.setProperty('--tile-saturation', `${Math.round(22 + heat * 52)}%`);
  tile.style.setProperty('--tile-lightness', `${(94 - heat * 35).toFixed(1)}%`);
  tile.classList.toggle('tile-top', t.rank <= 3);
}

/** The background color applyHeat() produces for a heat score, without reading the DOM. */
function heatColor(score) {
  const heat = Math.max(0.05, Math.min(1, score ?? 0));
  return `hsl(${Math.round(214 - heat * 185)}, ${Math.round(22 + heat * 52)}%, ${(94 - heat * 35).toFixed(1)}%)`;
}

function updateTileDetails(tile, t, playbackTs = currentPlaybackTs()) {
  const rank = tile.querySelector('.tile-rank');
  if (rank) rank.textContent = String(t.rank).padStart(2, '0');

  const timestamps = t.timestamps ?? [];
  const recentCount = recentVisualEventCount(timestamps, playbackTs);
  tile.querySelectorAll('.tile-meter i').forEach((bar, index) => {
    bar.classList.toggle('active', recentCount >= METER_THRESHOLDS[index]);
  });
  tile.classList.toggle('meter-overflow', recentCount > METER_THRESHOLDS.at(-1));
}

function refreshActivityIndicators(playbackTs, force = false) {
  const second = Math.floor(playbackTs);
  if (!force && second === state.lastActivitySecond) return;
  state.lastActivitySecond = second;
  state.topics.forEach(topic => {
    const tile = $id(`tile-${topic.id}`);
    if (tile) updateTileDetails(tile, topic, playbackTs);
    const row = $id(`topic-row-${topic.id}`);
    if (row) updateTopicRowDetails(row, topic, playbackTs);
  });
}

function openTopicBrief(topic) {
  if (document.body.classList.contains('brief-open') && state.selectedTopic?.id === topic.id) {
    closeTopicBrief();
    return;
  }

  state.selectedTopic = topic;
  const requestID = ++state.briefRequestID;
  $id('brief-topic').textContent = topic.title;
  $id('brief-search').href = `https://www.google.com/search?q=${encodeURIComponent(topic.title)}`;
  $id('brief-topic-link').href = topicPopularURL(topic);
  document.body.classList.add('brief-open');

  const cached = state.briefCache.get(topic.id);
  if (cached?.expiresAt > Date.now()) {
    renderTopicBrief(cached.brief);
    return;
  }
  state.briefCache.delete(topic.id);
  renderBriefLoading();
  fetchTopicBrief(topic.id).then(brief => {
    if (brief?.available) {
      state.briefCache.set(topic.id, {
        brief,
        expiresAt: Date.now() + BRIEF_CACHE_TTL_MS,
      });
    }
    if (requestID !== state.briefRequestID || state.selectedTopic?.id !== topic.id) return;
    const resolved = brief ?? { available: false };
    renderTopicBrief(resolved);
  });
}

function topicPopularURL(topic) {
  const fallback = `https://eksisozluk.com/?q=${encodeURIComponent(topic.title)}`;
  const url = new URL(topic.url || fallback);
  url.searchParams.set('a', 'popular');
  return url.href;
}

function closeTopicBrief() {
  document.body.classList.remove('brief-open');
  state.selectedTopic = null;
  state.briefRequestID++;
}

function initBriefGestures() {
  const panel = $id('brief-panel');
  let touchStart = null;

  panel.addEventListener('touchstart', event => {
    const touch = event.touches[0];
    touchStart = { x: touch.clientX, y: touch.clientY };
  }, { passive: true });

  panel.addEventListener('touchend', event => {
    if (!touchStart || !document.body.classList.contains('brief-open')) return;
    const touch = event.changedTouches[0];
    const deltaX = touch.clientX - touchStart.x;
    const deltaY = touch.clientY - touchStart.y;
    touchStart = null;

    if (deltaX <= -64 && Math.abs(deltaX) > Math.abs(deltaY)) closeTopicBrief();
  }, { passive: true });

  panel.addEventListener('touchcancel', () => { touchStart = null; }, { passive: true });
}

function initEventGestures() {
  const panel = $id('debug-panel');
  let touchStart = null;

  panel.addEventListener('touchstart', event => {
    const touch = event.touches[0];
    touchStart = { x: touch.clientX, y: touch.clientY };
  }, { passive: true });

  panel.addEventListener('touchend', event => {
    if (!touchStart || !document.body.classList.contains('events-open')) return;
    const touch = event.changedTouches[0];
    const deltaX = touch.clientX - touchStart.x;
    const deltaY = touch.clientY - touchStart.y;
    touchStart = null;

    if (deltaX >= 64 && Math.abs(deltaX) > Math.abs(deltaY)) debug.close();
  }, { passive: true });

  panel.addEventListener('touchcancel', () => { touchStart = null; }, { passive: true });
}

function renderBriefLoading() {
  const content = $id('brief-content');
  content.innerHTML = '';
  const loading = document.createElement('p');
  loading.className = 'brief-loading';
  loading.textContent = 'Özet yükleniyor…';
  content.appendChild(loading);
}

function renderTopicBrief(brief) {
  const content = $id('brief-content');
  content.innerHTML = '';
  if (!brief?.available || !brief.payload?.summary) {
    const unavailable = document.createElement('p');
    unavailable.className = 'brief-unavailable';
    unavailable.textContent = 'bu konunun özeti henüz hazır değil! dilersen başlığa gidip bakabilirsin.';
    content.appendChild(unavailable);
    return;
  }

  if (brief.generated_at) {
    const meta = document.createElement('p');
    meta.className = 'brief-meta';
    meta.textContent = `${new Date(brief.generated_at).toLocaleString('tr-TR', {
      day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit',
    })} · ${brief.entry_count} girdi`;
    content.appendChild(meta);
  }

  const summaryHeading = document.createElement('h3');
  summaryHeading.textContent = 'Ne konuşuluyor?';
  const summary = document.createElement('p');
  summary.className = 'brief-summary';
  summary.textContent = brief.payload.summary;
  content.append(summaryHeading, summary);

  const sides = brief.payload.debate?.sides ?? [];
  if (sides.length === 0) return;

  const debateHeading = document.createElement('h3');
  debateHeading.textContent = 'Tartışma';
  const debate = document.createElement('div');
  debate.className = 'brief-debate';
  sides.forEach(side => {
    const card = document.createElement('article');
    card.className = 'brief-side';
    const stance = document.createElement('h4');
    stance.textContent = side.stance;
    const argument = document.createElement('p');
    argument.textContent = side.argument;
    const support = document.createElement('span');
    support.className = `brief-support ${side.support ?? 'balanced'}`;
    support.textContent = supportLabel(side.support);
    card.append(stance, support, argument);
    (side.quotes ?? []).slice(0, 2).forEach(quote => {
      const blockquote = document.createElement('blockquote');
      blockquote.textContent = `“${quote}”`;
      card.appendChild(blockquote);
    });
    debate.appendChild(card);
  });
  content.append(debateHeading, debate);
}

function supportLabel(support) {
  if (support === 'majority') return 'baskın görüş';
  if (support === 'minority') return 'azınlık görüşü';
  return 'denge';
}

function hasLayoutBox(rect) {
  return rect.width > 0 && rect.height > 0;
}

/**
 * Animate the grid to a new topic ranking using the FLIP technique.
 * Tiles that move animate via CSS transform. Entering tiles fade in,
 * leaving tiles fade out and are removed after the transition.
 */
function reorderGrid(newTopics) {
  // A seek or mode change can start another reconciliation before a prior
  // leave animation ends. Those tiles are no longer in state.topics.
  $grid.querySelectorAll('.tile-leaving').forEach(tile => tile.remove());

  const newIds = new Set(newTopics.map(t => t.id));
  const oldIds = new Set(state.topics.map(t => t.id));

  // Step 1 (FLIP: First) — record current rects for tiles that will survive.
  const beforeRects = new Map();
  state.topics.forEach(t => {
    const el = $id(`tile-${t.id}`);
    if (el && newIds.has(t.id)) {
      const rect = el.getBoundingClientRect();
      if (hasLayoutBox(rect)) beforeRects.set(t.id, rect);
    }
  });

  // Step 2 — fade out departing tiles.
  const gridRect = $grid.getBoundingClientRect();
  oldIds.forEach(id => {
    if (newIds.has(id)) return;
    const el = $id(`tile-${id}`);
    if (!el) return;
    const rect = el.getBoundingClientRect();
    if (!hasLayoutBox(rect)) {
      el.remove();
      return;
    }
    el.style.left = `${rect.left - gridRect.left}px`;
    el.style.top = `${rect.top - gridRect.top}px`;
    el.style.width = `${rect.width}px`;
    el.style.height = `${rect.height}px`;
    el.classList.add('tile-leaving');
    el.addEventListener('animationend', () => el.remove(), { once: true });
    setTimeout(() => el.remove(), 400);
  });

  // Step 3 — add arriving tiles.
  newTopics.forEach(t => {
    if (oldIds.has(t.id)) return;
    const tile = createTile(t);
    tile.classList.add('tile-entering');
    $grid.appendChild(tile);
  });

  // Step 4 — update CSS order and animate heat for surviving tiles.
  // Colors are derived from the heat scores instead of read back from the
  // DOM, so reordering doesn't force a style recalc per tile.
  const prevHeatById = new Map(state.topics.map(t => [t.id, t.heat_score]));
  newTopics.forEach(t => {
    const el = $id(`tile-${t.id}`);
    if (!el || !oldIds.has(t.id)) return;
    const previousColor = heatColor(prevHeatById.get(t.id));
    el.style.transition = 'none';
    el.style.order = t.rank - 1;
    el.classList.toggle('mobile-secondary', t.rank > 20);
    applyHeat(el, t);
    updateTileDetails(el, t);
    const nextColor = heatColor(t.heat_score);
    if (previousColor !== nextColor) {
      el.animate(
        [{ backgroundColor: previousColor }, { backgroundColor: nextColor }],
        { duration: 700, easing: 'cubic-bezier(0.2, 0.7, 0.2, 1)' },
      );
    }
  });

  // Step 5 (FLIP: Last + Invert + Play).
  // Measure all survivors first (one layout flush), then invert every mover,
  // flush once and release them together — instead of forcing a layout per
  // moving tile.
  const movers = [];
  const resets = [];
  newTopics.forEach(t => {
    const el = $id(`tile-${t.id}`);
    if (!el) return;

    const before = beforeRects.get(t.id);
    const after  = el.getBoundingClientRect();
    if (!before || !hasLayoutBox(after)) {
      resets.push({ el, clearTransform: true });
      return;
    }
    const dx = before.left - after.left;
    const dy = before.top  - after.top;

    if (Math.abs(dx) > 0.5 || Math.abs(dy) > 0.5) {
      movers.push({ el, dx, dy });
    } else {
      resets.push({ el, clearTransform: false });
    }
  });

  resets.forEach(({ el, clearTransform }) => {
    el.style.transition = '';
    if (clearTransform) el.style.transform = '';
  });
  movers.forEach(({ el, dx, dy }) => {
    el.style.transition = 'none';
    el.style.transform  = `translate(${dx}px, ${dy}px)`;
  });
  void $grid.offsetHeight; // single flush before releasing every mover

  movers.forEach(({ el }) => {
    el.style.transition = 'transform 0.55s cubic-bezier(0.4, 0, 0.2, 1), background 1.2s ease';
    el.style.transform  = '';
    el.addEventListener('transitionend', () => {
      el.style.transition = '';
      el.style.transform  = '';
    }, { once: true });
  });

  state.topics = newTopics;
  buildTopicList(newTopics);
  refreshActivityIndicators(currentPlaybackTs(), true);
}

// ── Ping ─────────────────────────────────────────────────────────────────────
function firePing(topicId, heat, minuteCount) {
  const tile = $id(`tile-${topicId}`);
  if (!tile) return;
  const intensity = Math.min(1, Math.log1p(minuteCount) / Math.log(11));
  const active = state.activePings[topicId] ?? 0;
  const maxVisiblePings = IS_SOCIAL_IN_APP_BROWSER ? 1 : MAX_VISIBLE_PINGS;
  if (active >= maxVisiblePings) {
    showBurst(tile, topicId, minuteCount);
    return;
  }
  state.activePings[topicId] = active + 1;
  showAfterglow(tile, topicId, intensity);

  const ping = document.createElement('div');
  ping.className = 'ping';

  // Heat, not grid position, determines the event colour.
  const normalizedHeat = Math.max(0, Math.min(1, heat ?? 0));
  const hue = Math.round(214 - normalizedHeat * 185);
  ping.style.setProperty('--ping-color', `hsl(${hue}, 92%, 67%)`);
  ping.style.setProperty('--ping-ink', `hsl(${hue}, 76%, 35%)`);
  ping.style.setProperty('--ping-size', `${Math.round(14 + intensity * 12)}px`);
  ping.style.setProperty('--ping-spread', `${Math.round(4 + intensity * 10)}px`);
  ping.style.setProperty('--ping-glow', `${Math.round(13 + intensity * 18)}px`);
  ping.style.setProperty('--ping-final-scale', (3.1 + intensity * 2.2).toFixed(2));

  const ox = ((Math.random() - 0.5) * 34).toFixed(1);
  const oy = ((Math.random() - 0.5) * 34).toFixed(1);
  ping.style.setProperty('--ox', `${ox}%`);
  ping.style.setProperty('--oy', `${oy}%`);

  tile.appendChild(ping);
  // animationend never fires if the tab is hidden mid-animation (CSS
  // animations pause), which used to leak the tile's ping slot permanently.
  let settled = false;
  const settle = () => {
    if (settled) return;
    settled = true;
    state.activePings[topicId] = Math.max(0, (state.activePings[topicId] ?? 1) - 1);
    ping.remove();
  };
  ping.addEventListener('animationend', settle, { once: true });
  setTimeout(settle, 1_700);
}

function showAfterglow(tile, topicId, intensity) {
  tile.style.setProperty('--activity-alpha', (0.18 + intensity * 0.42).toFixed(2));
  tile.classList.add('tile-active');
  clearTimeout(state.afterglowTimers[topicId]);
  state.afterglowTimers[topicId] = setTimeout(() => {
    tile.classList.remove('tile-active');
    delete state.afterglowTimers[topicId];
  }, 700);
}

function showBurst(tile, topicId, minuteCount) {
  state.burstCounts[topicId] = (state.burstCounts[topicId] ?? 0) + 1;
  tile.classList.add('tile-burst');
  const label = tile.querySelector('.burst-count');
  if (label) label.textContent = `${minuteCount}/dk`;
  clearTimeout(state.burstTimers[topicId]);
  state.burstTimers[topicId] = setTimeout(() => {
    tile.classList.remove('tile-burst');
    if (label) label.textContent = '';
    delete state.burstCounts[topicId];
    delete state.burstTimers[topicId];
  }, 900);
}

// ── Scheduler ────────────────────────────────────────────────────────────────
/**
 * Schedule pings for all topics, globally sorted by virtual playback time.
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
  topics.forEach(t => {
    const events = virtualizeTimestampEvents(t.timestamps ?? []);
    const seen = state.seenTimestampCounts[t.id] ?? {};
    const observed = {};

    for (const event of events) {
      const key = String(event.ts);
      observed[key] = (observed[key] ?? 0) + 1;
      if (observed[key] <= (seen[key] ?? 0)) continue;
      const entry = { topicId: t.id, heat: t.heat_score, title: t.title, ...event };
      if (entry.playTs <= originTs) pastPings.push(entry);
      else                          futurePings.push(entry);
    }
    for (const [timestamp, count] of Object.entries(observed)) {
      seen[timestamp] = Math.max(seen[timestamp] ?? 0, count);
    }
    state.seenTimestampCounts[t.id] = seen;
  });

  // Sort both globally by virtual time (ascending = oldest fires first).
  pastPings.sort((a, b) => a.playTs - b.playTs);
  futurePings.sort((a, b) => a.playTs - b.playTs);

  // Catch-up: proportional spread so chronological order is preserved.
  const capped = isFinite(maxCatchup) ? pastPings.slice(-maxCatchup) : pastPings;
  if (capped.length > 0) {
    const minTs = capped[0].playTs;
    const span  = Math.max(1, originTs - minTs);
    capped.forEach(p => {
      const frac    = (p.playTs - minTs) / span;
      const delay   = catchupSpreadMs > 0 ? frac * catchupSpreadMs : 0;
      const fireAt  = nextPingFireAt(delay);
      const debugId = debug._nextId++;
      debug.add(debugId, p.title, p.ts, fireAt, p.minuteCount);
      const timerId = setTimeout(() => { debug.tick(debugId); firePing(p.topicId, p.heat, p.minuteCount); }, fireAt - Date.now());
      state.pendingTimers.push(timerId);
    });
  }

  // Future pings — apply speed divisor.
  futurePings.forEach(p => {
    const delay = (p.playTs - originTs) * 1000 / spd;
    if (delay > 0 && delay < 86_400_000) {
      const fireAt  = nextPingFireAt(delay);
      const debugId = debug._nextId++;
      debug.add(debugId, p.title, p.ts, fireAt, p.minuteCount);
      const timerId = setTimeout(() => { debug.tick(debugId); firePing(p.topicId, p.heat, p.minuteCount); }, fireAt - Date.now());
      state.pendingTimers.push(timerId);
    }
  });
}

function nextPingFireAt(delay) {
  const requestedAt = Date.now() + delay;
  const fireAt = Math.max(requestedAt, state.nextPingAt);
  state.nextPingAt = fireAt + PING_MIN_GAP_MS;
  return fireAt;
}

function cancelAllTimers() {
  state.pendingTimers.forEach(id => clearTimeout(id));
  state.pendingTimers = [];
  Object.values(state.burstTimers).forEach(id => clearTimeout(id));
  Object.values(state.afterglowTimers).forEach(id => clearTimeout(id));
  state.activePings = {};
  state.burstCounts = {};
  state.burstTimers = {};
  state.afterglowTimers = {};
  state.nextPingAt = 0;
  document.querySelectorAll('.tile-burst').forEach(tile => {
    tile.classList.remove('tile-burst');
    const label = tile.querySelector('.burst-count');
    if (label) label.textContent = '';
  });
  debug.clear();
}

// ── Replay animation (RAF scrubber advance) ───────────────────────────────────
function startReplayAnimation() {
  cancelReplayAnimation();
  _lastReplayRenderAt = 0;
  function tick() {
    const frameNow = performance.now();
    if (frameNow - _lastReplayRenderAt < REPLAY_RENDER_INTERVAL_MS) {
      _rafId = requestAnimationFrame(tick);
      return;
    }
    _lastReplayRenderAt = frameNow;
    const playTs = currentPlaybackTs();
    const minTs  = Date.now() / 1000 - CACHE_WINDOW_S;
    const maxTs  = Date.now() / 1000 - PLAYBACK_DELAY_S;
    const frac   = Math.max(0, Math.min(1, (playTs - minTs) / (maxTs - minTs)));
    $scrubber.value = (frac * 1000).toFixed(0);
    setPlaybackTime(playTs);
    refreshActivityIndicators(playTs);
    if (playTs >= maxTs && !state.transitioningLive) {
      state.transitioningLive = true;
      goLive().finally(() => { state.transitioningLive = false; });
      return;
    }
    // Prefetch the next window when within 30 s of the current window end,
    // so scrub playback continues seamlessly at any speed.
    if (!state.fetchingNext && state.scrubWindowEnd > 0 && playTs > state.scrubWindowEnd - 30) {
      state.fetchingNext = true;
      const nextFrom = state.scrubWindowEnd;
      const nextUntil = Math.min(nextFrom + RANGE_WINDOW_S, maxTs);
      if (nextUntil <= nextFrom) {
        state.fetchingNext = false;
        return;
      }
      fetchRange(nextFrom, nextUntil).then(snap => {
        state.fetchingNext = false;
        if (!snap || state.mode !== 'scrub') return;
        reorderGrid(snap.topics);
        schedulePings(snap.topics, nextFrom, 0, Infinity);
        state.scrubWindowEnd = snap.window_to;
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
  state.seenTimestampCounts = {};
  state.previewPlaybackTs = null;
  state.lastActivitySecond = -1;
  state.fetchingNext = false;
  state.mode = 'scrub';

  state.replayOriginTs = targetTs;
  state.replayWallMs   = Date.now();

  const snap = await fetchRange(targetTs, targetTs + RANGE_WINDOW_S);
  if (!snap) return;

  state.scrubWindowEnd = snap.window_to;
  reorderGrid(snap.topics);
  schedulePings(snap.topics, targetTs, SEEK_SPREAD_MS, Infinity);
  refreshActivityIndicators(targetTs, true);
  setPlaybackTime(targetTs);
  showReplayNotice(`${fmtTs(targetTs)}'den itibaren oynatılıyor`);
  startReplayAnimation();
}

/** Re-enter live mode: fresh fetch, clear dedup state, re-schedule. */
async function goLive(showNotice = false) {
  // exitLiveMode first so we never leak a second pair of intervals if goLive
  // is triggered while already live (e.g. user clicks the rightmost scrubber
  // position without dragging, so no 'input' event fires beforehand).
  exitLiveMode();
  cancelAllTimers();
  cancelReplayAnimation();
  state.seenTimestampCounts = {};
  state.previewPlaybackTs = null;
  state.lastActivitySecond = -1;
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
  if (showNotice) showReplayNotice();
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

function showReplayNotice(message = 'son 1 saat oynatılıyor') {
  const notice = $id('replay-notice');
  $id('replay-notice-text').textContent = message;
  notice.classList.remove('is-visible');
  void notice.offsetWidth;
  notice.classList.add('is-visible');
}

// ── Scrubber ─────────────────────────────────────────────────────────────────
/** Map scrubber value [0, 1000] to a unix timestamp. */
function scrubToTs(val) {
  const frac = val / 1000;
  const now  = Date.now() / 1000;
  return now - CACHE_WINDOW_S + frac * (CACHE_WINDOW_S - PLAYBACK_DELAY_S);
}

function initScrubber() {
  const dismissScrubberHint = () => document.body.classList.add('scrubber-hint-dismissed');

  $scrubber.addEventListener('pointerdown', dismissScrubberHint, { once: true });

  // While dragging: exit live mode and preview the time (no fetch yet).
  $scrubber.addEventListener('input', () => {
    if (state.mode === 'live') exitLiveMode();
    cancelReplayAnimation();
    state.previewPlaybackTs = scrubToTs($scrubber.value);
    setPlaybackTime(state.previewPlaybackTs);
    refreshActivityIndicators(state.previewPlaybackTs, true);
  });

  // On release: fetch data for the chosen position.
  $scrubber.addEventListener('change', async () => {
    const frac = $scrubber.value / 1000;
    state.previewPlaybackTs = null;
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
  const playbackTs = currentPlaybackTs();
  setPlaybackTime(playbackTs);
  refreshActivityIndicators(playbackTs);
}

// ── Status bar helpers ────────────────────────────────────────────────────────
function setPlaybackTime(ts) {
  $playbackTime.textContent = fmtTs(ts);
}

function setLastUpdated(snapshotAt) {
  $lastUpdated.textContent = 'son güncelleme ' + new Date(snapshotAt * 1000)
    .toLocaleTimeString('tr-TR', { hour: '2-digit', minute: '2-digit' });
}

function setView(view) {
  state.view = view;
  document.body.classList.toggle('list-view', view === 'list');
  document.querySelectorAll('.view-btn').forEach(button => {
    const selected = button.dataset.view === view;
    button.classList.toggle('active', selected);
    button.setAttribute('aria-pressed', String(selected));
  });
  try {
    localStorage.setItem('gok-pulse-view', view);
  } catch (error) {
    console.warn('[pulse] could not save view preference:', error);
  }
}

function initViewToggle() {
  let initialView = 'grid';
  try {
    initialView = localStorage.getItem('gok-pulse-view') === 'list' ? 'list' : 'grid';
  } catch (error) {
    console.warn('[pulse] could not read view preference:', error);
  }
  document.querySelectorAll('.view-btn').forEach(button => {
    button.addEventListener('click', () => setView(button.dataset.view));
  });
  setView(initialView);
}

// ── Init ─────────────────────────────────────────────────────────────────────
async function init() {
  $grid         = $id('grid');
  $topicList    = $id('topic-list');
  $scrubber     = $id('scrubber');
  $lastUpdated  = $id('last-updated');
  $playbackTime = $id('playback-time');
  debug.init();

  $id('brief-close').addEventListener('click', closeTopicBrief);
  initBriefGestures();
  initEventGestures();
  $id('go-live').addEventListener('click', () => goLive(true));

  initScrubber();
  initViewToggle();

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
  buildTopicList(snap.topics);
  state.lastSnapshotAt = snap.snapshot_at;
  setLastUpdated(snap.snapshot_at);

  schedulePings(snap.topics, playbackNow(), CATCHUP_SPREAD_MS, Infinity);
  enterLiveMode();
  showReplayNotice();
}

document.addEventListener('DOMContentLoaded', init);

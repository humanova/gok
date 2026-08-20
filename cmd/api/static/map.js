'use strict';

const canvas = document.getElementById('map-canvas');
const context = canvas.getContext('2d');
const searchInput = document.getElementById('topic-search');
const searchResults = document.getElementById('search-results');
const regionSelect = document.getElementById('region-select');
const labelToggle = document.getElementById('label-toggle');
const resetButton = document.getElementById('reset-view');
const infoToggle = document.getElementById('info-toggle');
const status = document.getElementById('map-status');
const topicPanel = document.getElementById('topic-panel');
const panelTitle = document.getElementById('panel-title');
const panelRegion = document.getElementById('panel-region');
const panelConnections = document.getElementById('panel-connections');
const panelLink = document.getElementById('panel-link');
const closePanel = document.getElementById('close-panel');
const hoverTooltip = document.getElementById('hover-tooltip');
const infoPanel = document.getElementById('map-info-panel');
const closeInfoPanel = document.getElementById('close-info-panel');
const mapInfoTitle = document.getElementById('map-info-title');
const mapInfoWindow = document.getElementById('map-info-window');
const mapFacts = document.getElementById('map-facts');
const mapGeneratedAt = document.getElementById('map-generated-at');
const languageButtons = document.querySelectorAll('[data-language]');
const mapInfoText = {
  intro: document.getElementById('map-info-intro'),
  heading: document.getElementById('map-info-heading'),
  eligibility: document.getElementById('map-info-eligibility'),
  connection: document.getElementById('map-info-connection'),
  note: document.getElementById('map-info-note'),
  contact: document.getElementById('map-contact-text'),
  github: document.getElementById('map-github-text'),
  githubSuffix: document.getElementById('map-github-suffix'),
};

const MIN_SCALE = 0.25;
const MAX_SCALE = 24;
const INFO_PANEL_VISIBILITY_KEY = 'gok-atlas-info-panel-visible';
const INFO_PANEL_LANGUAGE_KEY = 'gok-atlas-info-panel-language';

const mapVariant = window.location.pathname === '/long-term-map' ? 'long-term' : 'current';
const mapDetails = {
  current: { endpoint: '/api/map' },
  'long-term': { endpoint: '/api/long-term-map' },
};

const infoTranslations = {
  tr: {
    titles: { current: 'Güncel Harita', 'long-term': 'Uzun Dönem Haritası' },
    mapLabels: { current: 'Güncel', 'long-term': 'Uzun dönem' }, facts: ['başlık', 'bağlantı', 'bölge'],
    period: 'Seçilen dönem', ago: 'Son', days: 'gün', months: 'ay', years: 'yıl',
    intro: "Bu harita, Ekşi Sözlük'te canlılığını koruyan başlıkları ve birbirleriyle olan bağlantılarını gösterir.",
    heading: 'Harita nasıl oluşturuldu?',
    eligibility: "Bir başlığın yer alabilmesi için en az 30 farklı yazarın yazmış olması, bu yazarlardan en az 3'ünün farklı aylarda tekrar yazması ve başlığın en az 6 ay boyunca aktif kalması gerekir. Böylece günlük haberler ve diğer anlık/kısa süreli başlıklar dışarıda bırakılır.",
    connection: 'Bir bağlantı, en az 3 yazarın iki başlıkta da yazdığını gösterir. Anlamlı ilişkilerin tespit edilebilmesi için çok sayıda başlığa yazan yazarlar hesaba katılmamıştır.',
    note: 'Bağlantılar başlıkların benzer olduğunu değil yalnızca yazarlarının kesiştiğini gösterir.',
    contact: 'Bana ulaşmak için: ', github: 'İlginizi çektiyse yıldızlayabilirsiniz: ', githubSuffix: '', updated: 'Son güncelleme:', locale: 'tr-TR',
  },
  en: {
    titles: { current: 'Current Map', 'long-term': 'Long-Term Map' },
    mapLabels: { current: 'Current', 'long-term': 'Long term' }, facts: ['topics', 'links', 'regions'],
    period: 'Selected period', ago: 'Last', days: 'days', months: 'months', years: 'years',
    intro: 'This map shows the enduring Ekşi Sözlük topics and the links between them.',
    heading: 'How is the map made?',
    eligibility: 'A topic needs at least 30 distinct writers, 3 writers who return in different months, and activity across at least 6 months. Short-lived spikes and event-driven topics are left out.',
    connection: 'A link means that at least 3 writers contributed to both topics. Writers active across many topics are excluded from this calculation.',
    note: 'Links do not mean the topics are similar or share a point of view. They only show an overlap in writers.',
    contact: 'Feel free to reach me on ', github: 'If you find it useful, star it on ', githubSuffix: '.', updated: 'Last updated:', locale: 'en-GB',
  },
};

function getInitialInfoLanguage() {
  try {
    const language = localStorage.getItem(INFO_PANEL_LANGUAGE_KEY);
    return language === 'en' ? 'en' : 'tr';
  } catch {
    return 'tr';
  }
}

const state = {
  data: null,
  nodesByID: new Map(),
  nodesByDegree: [],
  regionHulls: new Map(),
  clustersByID: new Map(),
  transform: { scale: 1, x: 0, y: 0 },
  fitScale: 1,
  drag: null,
  pointers: new Map(),
  pinch: null,
  hoverID: null,
  selectedID: null,
  rendered: new Map(),
  regionLabels: new Map(),
  activeRegion: 'all',
  degreeRingThreshold: Infinity,
  infoLanguage: getInitialInfoLanguage(),
};

const regionLabels = {
  football: 'FUTBOL', other_sports: 'DİĞER SPORLAR', turkish_politics: 'TÜRKİYE SİYASETİ',
  world_politics: 'DÜNYA SİYASETİ', relationships: 'İLİŞKİLER', daily_life: 'GÜNDELİK HAYAT',
  music: 'MÜZİK', film_tv: 'FİLM VE TV', games_tech: 'OYUN VE TEKNOLOJİ', economy: 'EKONOMİ',
  culture_art: 'KÜLTÜR VE SANAT', society_identity: 'TOPLUM VE KİMLİK', science_health: 'BİLİM VE SAĞLIK',
  local_life: 'YEREL YAŞAM', media: 'MEDYA', news_events: 'GÜNCEL OLAYLAR', other: 'DİĞER',
};

const regionPalette = {
  football: [193, 56, 39], other_sports: [276, 42, 43], turkish_politics: [39, 63, 42],
  world_politics: [224, 48, 42], relationships: [87, 42, 44], daily_life: [27, 61, 43],
  music: [244, 49, 45], film_tv: [2, 58, 46], games_tech: [151, 48, 38], economy: [320, 51, 45],
  culture_art: [303, 47, 43], society_identity: [344, 53, 45], science_health: [178, 43, 37],
  local_life: [49, 50, 39], media: [326, 39, 38], news_events: [14, 56, 42], other: [116, 34, 38],
};

function colorForRegion(region, alpha = 1) {
  const [hue, saturation, lightness] = regionPalette[region] || [152, 35, 40];
  return `hsla(${hue}, ${saturation}%, ${lightness}%, ${alpha})`;
}

function formatNumber(value) {
  return new Intl.NumberFormat(infoTranslations[state.infoLanguage].locale).format(value);
}

function formatWindow(windowStart, generatedAt) {
  const translation = infoTranslations[state.infoLanguage];
  const start = new Date(windowStart);
  const end = new Date(generatedAt);
  if (Number.isNaN(start.getTime()) || Number.isNaN(end.getTime()) || end <= start) return translation.period;
  let months = (end.getUTCFullYear() - start.getUTCFullYear()) * 12 + end.getUTCMonth() - start.getUTCMonth();
  if (end.getUTCDate() < start.getUTCDate()) months--;
  if (months < 1) return `${translation.ago} ${formatNumber(Math.round((end - start) / 86_400_000))} ${translation.days}`;
  const years = Math.floor(months / 12);
  months %= 12;
  const parts = [];
  if (years) parts.push(`${formatNumber(years)} ${translation.years}`);
  if (months) parts.push(`${formatNumber(months)} ${translation.months}`);
  return `${translation.ago} ${parts.join(' ')}`;
}

function getInitialInfoPanelVisibility() {
  try {
    const visibility = localStorage.getItem(INFO_PANEL_VISIBILITY_KEY);
    if (visibility === 'true') return true;
    if (visibility === 'false') return false;
  } catch {
  }
  return !window.matchMedia('(max-width: 700px)').matches;
}

function setInfoPanelVisible(visible, persist = true) {
  infoPanel.hidden = !visible;
  infoToggle.setAttribute('aria-expanded', String(visible));
  if (persist) {
    try {
      localStorage.setItem(INFO_PANEL_VISIBILITY_KEY, String(visible));
    } catch {}
  }
}

function renderMapInfo(data) {
  const translation = infoTranslations[state.infoLanguage];
  mapInfoTitle.textContent = translation.titles[mapVariant];
  mapInfoWindow.textContent = formatWindow(data.window_start, data.generated_at);
  mapInfoText.intro.textContent = translation.intro;
  mapInfoText.heading.textContent = translation.heading;
  mapInfoText.eligibility.textContent = translation.eligibility;
  mapInfoText.connection.textContent = translation.connection;
  mapInfoText.note.textContent = translation.note;
  mapInfoText.contact.textContent = translation.contact;
  mapInfoText.github.textContent = translation.github;
  mapInfoText.githubSuffix.textContent = translation.githubSuffix;
  mapFacts.replaceChildren();
  const facts = [
    [translation.facts[0], data.nodes.length],
    [translation.facts[1], data.edges.length],
    [translation.facts[2], new Set(data.nodes.map(node => node.region)).size],
  ];
  for (const [label, value] of facts) {
    const term = document.createElement('dt');
    term.textContent = label;
    const detail = document.createElement('dd');
    detail.textContent = formatNumber(value);
    mapFacts.append(term, detail);
  }
  for (const link of document.querySelectorAll('.map-switcher a')) {
    const active = link.dataset.map === mapVariant;
    link.textContent = translation.mapLabels[link.dataset.map];
    link.setAttribute('aria-current', active ? 'page' : 'false');
  }
  for (const button of languageButtons) {
    button.setAttribute('aria-pressed', String(button.dataset.language === state.infoLanguage));
  }
  if (data.generated_at) {
    const generatedAt = new Date(data.generated_at).toLocaleString(translation.locale, {
      day: '2-digit', month: 'long', year: 'numeric', hour: '2-digit', minute: '2-digit', timeZone: 'UTC',
    });
    mapGeneratedAt.textContent = `${translation.updated} ${generatedAt} UTC`;
  }
  document.title = `gök atlas · ${translation.titles[mapVariant]}`;
}

function resizeCanvas() {
  const ratio = Math.min(window.devicePixelRatio || 1, 2);
  canvas.width = Math.floor(window.innerWidth * ratio);
  canvas.height = Math.floor(window.innerHeight * ratio);
  canvas.style.width = `${window.innerWidth}px`;
  canvas.style.height = `${window.innerHeight}px`;
  context.setTransform(ratio, 0, 0, ratio, 0, 0);
  draw();
}

function fitView() {
  const nodes = visibleNodes();
  if (!nodes.length) return;
  const minX = Math.min(...nodes.map(node => node.x));
  const maxX = Math.max(...nodes.map(node => node.x));
  const minY = Math.min(...nodes.map(node => node.y));
  const maxY = Math.max(...nodes.map(node => node.y));
  const padding = Math.min(window.innerWidth, window.innerHeight) < 700 ? 44 : 88;
  state.fitScale = Math.min((window.innerWidth - padding * 2) / (maxX - minX), (window.innerHeight - padding * 2) / (maxY - minY));
  state.transform.scale = state.fitScale;
  state.transform.x = window.innerWidth / 2 - (minX + maxX) / 2 * state.transform.scale;
  state.transform.y = window.innerHeight / 2 - (minY + maxY) / 2 * state.transform.scale;
  draw();
}

function screenPoint(node) {
  return {
    x: node.x * state.transform.scale + state.transform.x,
    y: node.y * state.transform.scale + state.transform.y,
  };
}

function visibleNodes() {
  if (!state.data) return [];
  return state.activeRegion === 'all'
    ? state.data.nodes
    : state.data.nodes.filter(node => node.region === state.activeRegion);
}

function convexHull(points) {
  if (points.length < 4) return points;
  const sorted = [...points].sort((left, right) => left.x - right.x || left.y - right.y);
  const cross = (origin, left, right) => (left.x - origin.x) * (right.y - origin.y) - (left.y - origin.y) * (right.x - origin.x);
  const lower = [];
  for (const point of sorted) {
    while (lower.length >= 2 && cross(lower[lower.length - 2], lower[lower.length - 1], point) <= 0) lower.pop();
    lower.push(point);
  }
  const upper = [];
  for (const point of sorted.reverse()) {
    while (upper.length >= 2 && cross(upper[upper.length - 2], upper[upper.length - 1], point) <= 0) upper.pop();
    upper.push(point);
  }
  return lower.slice(0, -1).concat(upper.slice(0, -1));
}

function drawRegionHalos(nodes) {
  const selected = state.nodesByID.get(state.selectedID);
  const regions = new Set();
  for (const node of nodes) {
    regions.add(node.region);
  }
  for (const region of regions) {
    if (selected && region !== selected.region) continue;
    const hull = state.regionHulls.get(region);
    if (hull.length < 3) continue;
    const selectedRegion = selected?.region === region;
    context.save();
    context.beginPath();
    const first = screenPoint(hull[0]);
    context.moveTo(first.x, first.y);
    for (const point of hull.slice(1)) {
      const screen = screenPoint(point);
      context.lineTo(screen.x, screen.y);
    }
    context.closePath();
    context.fillStyle = colorForRegion(region, selectedRegion ? 0.08 : 0.045);
    context.strokeStyle = colorForRegion(region, selectedRegion ? 0.36 : 0.17);
    context.lineWidth = selectedRegion ? 1.35 : 1;
    context.lineJoin = 'round';
    context.shadowColor = colorForRegion(region, selectedRegion ? 0.18 : 0.12);
    context.shadowBlur = selectedRegion ? 20 : 18;
    context.fill();
    context.shadowBlur = 0;
    context.stroke();
    context.restore();
  }
}

function cacheRegionHulls(nodes) {
  const regions = new Map();
  for (const node of nodes) {
    const points = regions.get(node.region) || [];
    points.push({ x: node.x, y: node.y });
    regions.set(node.region, points);
  }
  state.regionHulls = new Map([...regions].map(([region, points]) => [region, convexHull(points)]));
}

function radiusForNode(node) {
  const degreeRadius = Math.max(2.2, Math.min(9, 2 + Math.log2(node.degree + 1) * 1.35));
  const relativeZoom = state.transform.scale / state.fitScale;
  const zoomProgress = Math.max(0, Math.min(1, Math.log2(relativeZoom) / 2));
  const zoomFactor = 0.62 + 0.38 * zoomProgress;
  return Math.max(1.3, degreeRadius * zoomFactor);
}

function clampScale(scale) {
  return Math.max(MIN_SCALE, Math.min(MAX_SCALE, scale));
}

function isOnScreen(point, margin = 16) {
  return point.x >= -margin && point.x <= window.innerWidth + margin && point.y >= -margin && point.y <= window.innerHeight + margin;
}

function updatePinchTransform() {
  if (!state.pinch || state.pointers.size !== 2) return;
  const [first, second] = [...state.pointers.values()];
  const midpointX = (first.x + second.x) / 2;
  const midpointY = (first.y + second.y) / 2;
  const distance = Math.hypot(first.x - second.x, first.y - second.y);
  if (!distance) return;
  const nextScale = clampScale(state.pinch.scale * distance / state.pinch.distance);
  state.transform.scale = nextScale;
  state.transform.x = midpointX - state.pinch.worldX * nextScale;
  state.transform.y = midpointY - state.pinch.worldY * nextScale;
  draw();
}

function beginPinch() {
  if (state.pointers.size !== 2) return;
  const [first, second] = [...state.pointers.values()];
  const midpointX = (first.x + second.x) / 2;
  const midpointY = (first.y + second.y) / 2;
  state.pinch = {
    distance: Math.hypot(first.x - second.x, first.y - second.y),
    scale: state.transform.scale,
    worldX: (midpointX - state.transform.x) / state.transform.scale,
    worldY: (midpointY - state.transform.y) / state.transform.scale,
  };
  state.drag = null;
  hoverTooltip.hidden = true;
}

let drawFrame = null;

function draw() {
  if (drawFrame !== null) return;
  drawFrame = requestAnimationFrame(() => {
    drawFrame = null;
    render();
  });
}

function render() {
  context.clearRect(0, 0, window.innerWidth, window.innerHeight);
  if (!state.data) return;
  state.rendered.clear();
  const nodes = visibleNodes();
  const atMaximumZoom = state.transform.scale === MAX_SCALE;
  const visibleIDs = new Set(nodes.map(node => node.id));
  const nodesOnScreen = nodes.filter(node => isOnScreen(screenPoint(node), radiusForNode(node) + 5));
  const renderedIDs = new Set(nodesOnScreen.map(node => node.id));
  const selected = state.selectedID;
  const selectedNode = state.nodesByID.get(selected);
  const connectedIDs = new Set(selected ? [selected] : []);
  if (selected) {
    for (const edge of state.data.edges) {
      if (edge.source === selected) connectedIDs.add(edge.target);
      if (edge.target === selected) connectedIDs.add(edge.source);
    }
  }

  drawRegionHalos(nodes);

  context.lineWidth = 1;
  for (const edge of state.data.edges) {
    const source = state.nodesByID.get(edge.source);
    const target = state.nodesByID.get(edge.target);
    if (!source || !target || !visibleIDs.has(edge.source) || !visibleIDs.has(edge.target)) continue;
    if (!renderedIDs.has(edge.source) && !renderedIDs.has(edge.target)) continue;
    const a = screenPoint(source);
    const b = screenPoint(target);
    const selectedEdge = selected && (edge.source === selected || edge.target === selected);
    context.strokeStyle = selected
      ? (selectedEdge ? colorForRegion(selectedNode.region, 0.72) : 'rgba(33, 55, 43, 0.018)')
      : `rgba(33, 55, 43, ${Math.min(0.18, 0.025 + edge.weight * 0.9)})`;
    context.lineWidth = selectedEdge ? 1.7 : 0.7;
    context.beginPath();
    context.moveTo(a.x, a.y);
    context.lineTo(b.x, b.y);
    context.stroke();
  }

  for (const node of nodesOnScreen) {
    const point = screenPoint(node);
    const selectedNode = node.id === selected;
    const hovered = node.id === state.hoverID;
    const connected = connectedIDs.has(node.id);
    const radius = radiusForNode(node);
    state.rendered.set(node.id, { x: point.x, y: point.y, radius });
    context.fillStyle = colorForRegion(node.region, selected ? (connected ? 1 : 0.13) : (selectedNode || hovered ? 1 : 0.82));
    context.beginPath();
    context.arc(point.x, point.y, selectedNode || hovered ? radius + 2 : radius, 0, Math.PI * 2);
    context.fill();
    if (state.transform.scale >= state.fitScale * 1.75 && node.degree >= state.degreeRingThreshold && (!selected || connected)) {
      context.strokeStyle = colorForRegion(node.region, selectedNode || hovered ? 0.72 : 0.25);
      context.lineWidth = 0.7;
      context.beginPath();
      context.arc(point.x, point.y, radius + 2.8, 0, Math.PI * 2);
      context.stroke();
    }
    if (selectedNode || hovered) {
      context.strokeStyle = '#ffffff';
      context.lineWidth = 1.5;
      context.stroke();
    }
  }

  if (atMaximumZoom) {
    drawTopicLabels();
    hideRegionLabels();
  } else {
    updateRegionLabels(nodes);
  }

  const hover = state.nodesByID.get(state.hoverID);
  if (hover && !state.drag) updateHoverTooltip(hover);
  else hoverTooltip.hidden = true;
}

function drawTopicLabels() {
  if (!labelToggle.checked) return;
  const placed = [];
  const margin = 8;
  context.save();
  context.font = '650 11px "Avenir Next", "Trebuchet MS", sans-serif';
  context.textBaseline = 'middle';
  context.fillStyle = 'rgba(23, 37, 30, 0.9)';
  for (const node of state.nodesByDegree) {
    const point = state.rendered.get(node.id);
    if (!point) continue;
    const radius = radiusForNode(node);
    const width = context.measureText(node.title).width;
    const height = 14;
    const candidates = [
      { x: point.x + radius + 5, y: point.y, align: 'left' },
      { x: point.x - radius - 5, y: point.y, align: 'right' },
    ];
    const position = candidates.find(candidate => {
      const left = candidate.align === 'left' ? candidate.x : candidate.x - width;
      const bounds = { left, top: candidate.y - height / 2, right: left + width, bottom: candidate.y + height / 2 };
      const inView = bounds.left >= margin && bounds.right <= window.innerWidth - margin && bounds.top >= margin && bounds.bottom <= window.innerHeight - margin;
      const overlaps = placed.some(other => bounds.left < other.right + 4 && bounds.right > other.left - 4 && bounds.top < other.bottom + 3 && bounds.bottom > other.top - 3);
      if (inView && !overlaps) candidate.bounds = bounds;
      return inView && !overlaps;
    });
    if (!position) continue;
    context.textAlign = position.align;
    context.fillText(node.title, position.x, position.y);
    placed.push(position.bounds);
  }
  context.restore();
}

function hideRegionLabels() {
  for (const element of state.regionLabels.values()) element.hidden = true;
}

function updateRegionLabels(nodes) {
  const centers = new Map();
  for (const node of nodes) {
    const entry = centers.get(node.region) || { x: 0, y: 0, count: 0 };
    entry.x += node.x;
    entry.y += node.y;
    entry.count++;
    centers.set(node.region, entry);
  }
  const labelCandidates = [
    [0, 0], [0, -28], [0, 28], [44, 0], [-44, 0],
    [44, -28], [-44, -28], [44, 28], [-44, 28],
  ];
  const placed = [];
  const entries = [...state.regionLabels].sort(([left], [right]) => {
    return (centers.get(right)?.count || 0) - (centers.get(left)?.count || 0);
  });
  context.font = '800 10px "Avenir Next", "Trebuchet MS", sans-serif';
  for (const [region, element] of entries) {
    if (!labelToggle.checked || !centers.has(region) || centers.get(region).count < 3) {
      element.hidden = true;
      continue;
    }
    const center = centers.get(region);
    const point = screenPoint({ x: center.x / center.count, y: center.y / center.count });
    const width = Math.ceil(context.measureText(element.textContent).width) + 14;
    const height = 24;
    const margin = 12;
    let position = null;
    for (const [offsetX, offsetY] of labelCandidates) {
      const x = Math.max(margin + width / 2, Math.min(window.innerWidth - margin - width / 2, point.x + offsetX));
      const y = Math.max(margin + height / 2, Math.min(window.innerHeight - margin - height / 2, point.y + offsetY));
      const bounds = { left: x - width / 2, top: y - height / 2, right: x + width / 2, bottom: y + height / 2 };
      const overlaps = placed.some(other => bounds.left < other.right + 6 && bounds.right > other.left - 6 && bounds.top < other.bottom + 6 && bounds.bottom > other.top - 6);
      if (!overlaps) {
        position = { x, y, bounds };
        break;
      }
    }
    if (!position) {
      element.hidden = true;
      continue;
    }
    element.hidden = false;
    element.style.left = `${position.x}px`;
    element.style.top = `${position.y}px`;
    placed.push(position.bounds);
  }
}

function updateHoverTooltip(node) {
  const point = state.rendered.get(node.id);
  if (!point) return;
  hoverTooltip.textContent = node.title;
  hoverTooltip.hidden = false;
  const width = hoverTooltip.offsetWidth;
  const height = hoverTooltip.offsetHeight;
  const margin = 12;
  const preferRight = point.x + 14 + width <= window.innerWidth - margin;
  const x = preferRight ? point.x + 14 : point.x - width - 14;
  const y = Math.max(margin, Math.min(window.innerHeight - height - margin, point.y - height - 10));
  hoverTooltip.style.left = `${Math.max(margin, x)}px`;
  hoverTooltip.style.top = `${y}px`;
}

function findNodeAt(x, y) {
  let found = null;
  let distance = Infinity;
  for (const [id, point] of state.rendered) {
    const candidate = Math.hypot(point.x - x, point.y - y);
    if (candidate <= Math.max(11, point.radius + 5) && candidate < distance) {
      found = id;
      distance = candidate;
    }
  }
  return found;
}

function selectNode(id) {
  state.selectedID = id;
  const node = state.nodesByID.get(id);
  if (!node) return;
  topicPanel.hidden = false;
  panelTitle.textContent = node.title;
  panelRegion.textContent = regionLabels[node.region] || node.region;
  renderConnections(node);
  panelLink.href = node.url;
  draw();
}

function connectedNodes(node) {
  return state.data.edges
    .filter(edge => edge.source === node.id || edge.target === node.id)
    .map(edge => ({ id: edge.source === node.id ? edge.target : edge.source, weight: edge.weight }))
    .map(connection => ({ node: state.nodesByID.get(connection.id), weight: connection.weight }))
    .filter(connection => connection.node)
    .sort((left, right) => right.weight - left.weight);
}

function renderConnections(node) {
  panelConnections.replaceChildren();
  const connections = connectedNodes(node);
  if (!connections.length) {
    const empty = document.createElement('span');
    empty.className = 'connection-empty';
    empty.textContent = 'bağlantı yok';
    panelConnections.appendChild(empty);
    return;
  }
  for (const connection of connections) {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'connection-chip';
    button.textContent = connection.node.title;
    button.title = connection.node.title;
    button.addEventListener('click', () => focusNode(connection.node));
    panelConnections.appendChild(button);
  }
}

function clearSelection() {
  state.selectedID = null;
  topicPanel.hidden = true;
  draw();
}

function focusNode(node) {
  if (state.activeRegion !== 'all' && node.region !== state.activeRegion) {
    state.activeRegion = 'all';
    regionSelect.value = 'all';
  }
  state.transform.x = window.innerWidth / 2 - node.x * state.transform.scale;
  state.transform.y = window.innerHeight / 2 - node.y * state.transform.scale;
  selectNode(node.id);
}

function updateSearch() {
  const query = searchInput.value.trim().toLocaleLowerCase('tr-TR');
  searchResults.replaceChildren();
  if (!query || !state.data) return;
  const matches = state.data.nodes
    .filter(node => (state.activeRegion === 'all' || node.region === state.activeRegion) && node.title.toLocaleLowerCase('tr-TR').includes(query))
    .slice(0, 10);
  for (const node of matches) {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'search-result';
    button.textContent = node.title;
    button.addEventListener('click', () => {
      searchInput.value = '';
      searchResults.replaceChildren();
      focusNode(node);
    });
    searchResults.appendChild(button);
  }
}

async function loadMap() {
  try {
    const response = await fetch(mapDetails[mapVariant].endpoint);
    if (!response.ok) throw new Error(String(response.status));
    const data = await response.json();
    if (!data.available) throw new Error('harita verisi bulunamadı');
    state.data = data;
    for (const node of data.nodes) state.nodesByID.set(node.id, node);
    state.nodesByDegree = [...data.nodes].sort((left, right) => right.degree - left.degree);
    cacheRegionHulls(data.nodes);
    for (const cluster of data.clusters) state.clustersByID.set(cluster.id, cluster);
    initializeRegions(data.nodes);
    renderMapInfo(data);
    status.textContent = '';
    resizeCanvas();
    fitView();
  } catch (error) {
    status.textContent = error.message || 'harita yüklenemedi';
  }
}

function initializeRegions(nodes) {
  const degrees = nodes.map(node => node.degree).sort((left, right) => left - right);
  state.degreeRingThreshold = Math.max(3, degrees[Math.floor((degrees.length - 1) * 0.92)] || Infinity);
  const regions = [...new Set(nodes.map(node => node.region))].sort((left, right) => {
    return (regionLabels[left] || left).localeCompare(regionLabels[right] || right, 'tr-TR');
  });
  for (const region of regions) {
    const option = document.createElement('option');
    option.value = region;
    option.textContent = (regionLabels[region] || region).toLocaleLowerCase('tr-TR');
    regionSelect.appendChild(option);
    const label = document.createElement('span');
    label.className = 'region-label';
    label.textContent = regionLabels[region] || region;
    label.hidden = true;
    document.getElementById('map-app').appendChild(label);
    state.regionLabels.set(region, label);
  }
}

canvas.addEventListener('pointerdown', event => {
  state.pointers.set(event.pointerId, { x: event.clientX, y: event.clientY });
  state.drag = { x: event.clientX, y: event.clientY, moved: false };
  canvas.setPointerCapture(event.pointerId);
  if (state.pointers.size === 2) beginPinch();
});

canvas.addEventListener('pointermove', event => {
  if (state.pointers.has(event.pointerId)) {
    state.pointers.set(event.pointerId, { x: event.clientX, y: event.clientY });
  }
  if (state.pointers.size === 2) {
    updatePinchTransform();
    return;
  }
  if (state.drag) {
    const dx = event.clientX - state.drag.x;
    const dy = event.clientY - state.drag.y;
    if (Math.abs(dx) + Math.abs(dy) > 2) state.drag.moved = true;
    state.transform.x += dx;
    state.transform.y += dy;
    state.drag.x = event.clientX;
    state.drag.y = event.clientY;
    draw();
    return;
  }
  if (event.pointerType === 'touch') return;
  const nextHover = findNodeAt(event.clientX, event.clientY);
  if (nextHover !== state.hoverID) {
    state.hoverID = nextHover;
    canvas.style.cursor = nextHover ? 'pointer' : 'grab';
    draw();
  }
});

canvas.addEventListener('pointerup', event => {
  const drag = state.drag;
  const wasPinching = state.pinch !== null;
  state.pointers.delete(event.pointerId);
  state.pinch = null;
  state.drag = null;
  if (!wasPinching && !drag?.moved) {
    const nodeID = findNodeAt(event.clientX, event.clientY);
    if (nodeID) {
      selectNode(nodeID);
    } else {
      clearSelection();
    }
  }
  draw();
});

canvas.addEventListener('pointercancel', event => {
  state.pointers.delete(event.pointerId);
  state.drag = null;
  state.pinch = null;
  hoverTooltip.hidden = true;
  draw();
});

canvas.addEventListener('wheel', event => {
  event.preventDefault();
  const factor = event.deltaY < 0 ? 1.12 : 0.89;
  const nextScale = clampScale(state.transform.scale * factor);
  const worldX = (event.clientX - state.transform.x) / state.transform.scale;
  const worldY = (event.clientY - state.transform.y) / state.transform.scale;
  state.transform.scale = nextScale;
  state.transform.x = event.clientX - worldX * nextScale;
  state.transform.y = event.clientY - worldY * nextScale;
  draw();
}, { passive: false });

searchInput.addEventListener('input', updateSearch);
searchInput.addEventListener('keydown', event => {
  if (event.key === 'Escape') {
    searchInput.value = '';
    searchResults.replaceChildren();
    searchInput.blur();
  }
});
regionSelect.addEventListener('change', () => {
  state.activeRegion = regionSelect.value;
  const selected = state.nodesByID.get(state.selectedID);
  if (selected && state.activeRegion !== 'all' && selected.region !== state.activeRegion) clearSelection();
  searchInput.value = '';
  searchResults.replaceChildren();
  fitView();
});
labelToggle.addEventListener('change', draw);
resetButton.addEventListener('click', () => {
  clearSelection();
  fitView();
});
infoToggle.addEventListener('click', () => setInfoPanelVisible(infoPanel.hidden));
closeInfoPanel.addEventListener('click', () => setInfoPanelVisible(false));
for (const button of languageButtons) {
  button.addEventListener('click', () => {
    state.infoLanguage = button.dataset.language;
    try {
      localStorage.setItem(INFO_PANEL_LANGUAGE_KEY, state.infoLanguage);
    } catch {}
    if (state.data) renderMapInfo(state.data);
  });
}
closePanel.addEventListener('click', clearSelection);
window.addEventListener('resize', resizeCanvas);

setInfoPanelVisible(getInitialInfoPanelVisibility(), false);
loadMap();

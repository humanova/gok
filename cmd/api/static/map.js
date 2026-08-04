'use strict';

const canvas = document.getElementById('map-canvas');
const context = canvas.getContext('2d');
const searchInput = document.getElementById('topic-search');
const searchResults = document.getElementById('search-results');
const regionSelect = document.getElementById('region-select');
const labelToggle = document.getElementById('label-toggle');
const resetButton = document.getElementById('reset-view');
const status = document.getElementById('map-status');
const topicPanel = document.getElementById('topic-panel');
const panelTitle = document.getElementById('panel-title');
const panelCommunity = document.getElementById('panel-community');
const panelConnections = document.getElementById('panel-connections');
const panelLink = document.getElementById('panel-link');
const closePanel = document.getElementById('close-panel');
const hoverTooltip = document.getElementById('hover-tooltip');

const MIN_SCALE = 0.25;
const MAX_SCALE = 24;

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
};

const regionLabels = {
  football: 'FUTBOL', other_sports: 'DİĞER SPORLAR', turkish_politics: 'TÜRKİYE SİYASETİ',
  world_politics: 'DÜNYA SİYASETİ', relationships: 'İLİŞKİLER', daily_life: 'GÜNDELİK HAYAT',
  music: 'MÜZİK', film_tv: 'FİLM VE TV', games_tech: 'OYUN VE TEKNOLOJİ', economy: 'EKONOMİ',
  culture_art: 'KÜLTÜR VE SANAT', society_identity: 'TOPLUM VE KİMLİK', science_health: 'BİLİM VE SAĞLIK',
  local_life: 'YEREL YAŞAM', media: 'MEDYA', news_events: 'GÜNCEL OLAYLAR', other: 'DİĞER',
};

const regionPalette = {
  football: [191, 56, 39], other_sports: [8, 49, 43], turkish_politics: [38, 47, 43],
  world_politics: [186, 46, 40], relationships: [77, 40, 46], daily_life: [35, 47, 43],
  music: [237, 42, 42], film_tv: [6, 46, 42], games_tech: [145, 48, 39], economy: [336, 43, 46],
  culture_art: [313, 43, 43], society_identity: [347, 38, 47], science_health: [66, 37, 41],
  local_life: [33, 42, 44], media: [334, 48, 44], news_events: [3, 42, 44], other: [97, 31, 41],
};

function colorForRegion(region, alpha = 1) {
  const [hue, saturation, lightness] = regionPalette[region] || [152, 35, 40];
  return `hsla(${hue}, ${saturation}%, ${lightness}%, ${alpha})`;
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
  const regions = new Set();
  for (const node of nodes) {
    regions.add(node.region);
  }
  for (const region of regions) {
    const hull = state.regionHulls.get(region);
    if (hull.length < 3) continue;
    context.save();
    context.beginPath();
    const first = screenPoint(hull[0]);
    context.moveTo(first.x, first.y);
    for (const point of hull.slice(1)) {
      const screen = screenPoint(point);
      context.lineTo(screen.x, screen.y);
    }
    context.closePath();
    context.fillStyle = colorForRegion(region, 0.045);
    context.strokeStyle = colorForRegion(region, 0.17);
    context.lineWidth = 1;
    context.lineJoin = 'round';
    context.shadowColor = colorForRegion(region, 0.12);
    context.shadowBlur = 18;
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
  return Math.max(2.2, Math.min(9, 2 + Math.log2(node.degree + 1) * 1.35));
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
    if (node.degree >= state.degreeRingThreshold && (!selected || connected)) {
      context.strokeStyle = colorForRegion(node.region, selectedNode || hovered ? 0.9 : 0.38);
      context.lineWidth = 1;
      context.beginPath();
      context.arc(point.x, point.y, radius + 3.2, 0, Math.PI * 2);
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
  panelCommunity.textContent = regionLabels[node.region] || node.region.toUpperCase();
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
    .sort((left, right) => right.weight - left.weight)
    .slice(0, 5);
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
  if (!query) return;
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
    const response = await fetch('/api/map');
    if (!response.ok) throw new Error(String(response.status));
    const data = await response.json();
    if (!data.available) throw new Error('harita verisi bulunamadı');
    state.data = data;
    for (const node of data.nodes) state.nodesByID.set(node.id, node);
    state.nodesByDegree = [...data.nodes].sort((left, right) => right.degree - left.degree);
    cacheRegionHulls(data.nodes);
    for (const cluster of data.clusters) state.clustersByID.set(cluster.id, cluster);
    initializeRegions(data.nodes);
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
resetButton.addEventListener('click', fitView);
closePanel.addEventListener('click', clearSelection);
window.addEventListener('resize', resizeCanvas);

loadMap();

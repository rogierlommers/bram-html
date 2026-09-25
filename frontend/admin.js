const state = document.querySelector('#admin-state');
const dashboard = document.querySelector('#admin-dashboard');
const onlinePill = document.querySelector('#online-pill');
const numberFormatter = new Intl.NumberFormat();
const dateFormatter = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' });

async function api(path, options = {}) {
  const headers = new Headers(options.headers);
  headers.set('Accept', 'application/json');
  const response = await fetch(path, { ...options, headers });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    const error = new Error(data.error || 'Statistics could not be loaded.');
    error.status = response.status;
    throw error;
  }
  return data;
}

function setText(selector, value) {
  document.querySelector(selector).textContent = numberFormatter.format(value);
}

function svgElement(name, attributes = {}) {
  const element = document.createElementNS('http://www.w3.org/2000/svg', name);
  Object.entries(attributes).forEach(([key, value]) => element.setAttribute(key, value));
  return element;
}

function renderChart(activity) {
  const width = 960;
  const height = 310;
  const margin = { top: 16, right: 20, bottom: 42, left: 38 };
  const plotWidth = width - margin.left - margin.right;
  const plotHeight = height - margin.top - margin.bottom;
  const maximum = Math.max(1, ...activity.flatMap((day) => [day.activeUsers, day.newUsers, day.pagesCreated]));
  const roundedMaximum = Math.max(4, Math.ceil(maximum / 4) * 4);
  const svg = svgElement('svg', { viewBox: `0 0 ${width} ${height}`, role: 'img', 'aria-labelledby': 'chart-title chart-description' });
  const title = svgElement('title', { id: 'chart-title' });
  title.textContent = 'Activity during the last 30 days';
  const description = svgElement('desc', { id: 'chart-description' });
  description.textContent = 'Line chart of unique active users, new users, and pages created per day.';
  svg.append(title, description);

  for (let step = 0; step <= 4; step++) {
    const y = margin.top + (plotHeight * step) / 4;
    svg.append(svgElement('line', { class: 'chart-grid', x1: margin.left, x2: width - margin.right, y1: y, y2: y }));
    const label = svgElement('text', { class: 'chart-label', x: margin.left - 8, y: y + 4, 'text-anchor': 'end' });
    label.textContent = String(roundedMaximum - (roundedMaximum * step) / 4);
    svg.append(label);
  }

  const xFor = (index) => margin.left + (plotWidth * index) / Math.max(1, activity.length - 1);
  const yFor = (value) => margin.top + plotHeight - (plotHeight * value) / roundedMaximum;
  const series = [
    ['activeUsers', 'active'],
    ['newUsers', 'users'],
    ['pagesCreated', 'pages']
  ];
  series.forEach(([property, className]) => {
    const points = activity.map((day, index) => `${xFor(index)},${yFor(day[property])}`).join(' ');
    svg.append(svgElement('polyline', { class: `chart-line ${className}`, points }));
  });

  activity.forEach((day, index) => {
    if (index % 5 !== 0 && index !== activity.length - 1) return;
    const label = svgElement('text', { class: 'chart-label', x: xFor(index), y: height - 13, 'text-anchor': 'middle' });
    label.textContent = new Date(`${day.date}T00:00:00Z`).toLocaleDateString(undefined, { month: 'short', day: 'numeric', timeZone: 'UTC' });
    svg.append(label);
  });

  document.querySelector('#activity-chart').replaceChildren(svg);
}

function formatDate(value) {
  return dateFormatter.format(new Date(value));
}

function renderPages(pages) {
  const body = document.querySelector('#admin-pages');
  document.querySelector('#pages-count').textContent = `${numberFormatter.format(pages.length)} ${pages.length === 1 ? 'page' : 'pages'}`;
  body.replaceChildren();

  if (pages.length === 0) {
    const row = document.createElement('tr');
    const cell = document.createElement('td');
    cell.colSpan = 4;
    cell.className = 'admin-pages-empty';
    cell.textContent = 'No pages have been saved yet.';
    row.append(cell);
    body.append(row);
    return;
  }

  pages.forEach((page) => {
    const row = document.createElement('tr');
    const title = document.createElement('th');
    title.scope = 'row';
    title.textContent = page.title;
    const owner = document.createElement('td');
    owner.textContent = page.ownerEmail;
    const created = document.createElement('td');
    created.textContent = formatDate(page.createdAt);
    const updated = document.createElement('td');
    updated.textContent = formatDate(page.updatedAt);
    row.append(title, owner, created, updated);
    body.append(row);
  });
}

function renderStatistics(stats) {
  setText('#total-users', stats.summary.totalUsers);
  setText('#total-pages', stats.summary.totalPages);
  setText('#online-users', stats.summary.onlineUsers);
  setText('#online-count', stats.summary.onlineUsers);
  renderChart(stats.activity);
  renderPages(stats.pages);
  state.hidden = true;
  dashboard.hidden = false;
  onlinePill.hidden = false;
}

async function loadStatistics() {
  try {
    renderStatistics(await api('/api/admin/stats'));
  } catch (error) {
    state.hidden = false;
    dashboard.hidden = true;
    onlinePill.hidden = true;
    if (error.status === 401) {
      state.replaceChildren('Sign in with an administrator account from the ', Object.assign(document.createElement('a'), { href: '/', textContent: 'playground' }), '.');
    } else if (error.status === 403) {
      state.textContent = 'This page is only available to administrators.';
    } else {
      state.textContent = error.message;
    }
  }
}

async function reportActivity() {
  try {
    await api('/api/activity', { method: 'POST' });
  } catch {
    // The statistics request displays authentication and server errors.
  }
}

let refreshTimeout;
let refreshInProgress = false;
let refreshWhenReady = false;

async function refresh() {
  refreshTimeout = undefined;
  if (document.hidden) return;
  if (refreshInProgress) {
    refreshWhenReady = true;
    return;
  }

  refreshInProgress = true;
  try {
    await reportActivity();
    await loadStatistics();
  } finally {
    refreshInProgress = false;
    if (document.hidden) return;
    if (refreshWhenReady) {
      refreshWhenReady = false;
      refresh();
    } else {
      refreshTimeout = window.setTimeout(refresh, 30_000);
    }
  }
}

document.addEventListener('visibilitychange', () => {
  window.clearTimeout(refreshTimeout);
  refreshTimeout = undefined;
  if (document.hidden) {
    refreshWhenReady = false;
  } else if (refreshInProgress) {
    refreshWhenReady = true;
  } else {
    refresh();
  }
});

refresh();

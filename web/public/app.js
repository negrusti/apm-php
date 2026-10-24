'use strict';
const $ = id => document.getElementById(id);
const palette = ['#309c7b', '#80b19d', '#8e9dc4', '#b7a0c5', '#d7b78b', '#d9dfd3'];
const state = { kind: 'plugin', app: 'all', range: 3600, sort: 'self_ms', desc: true, page: 0, data: null, status: null, seq: 0, detailSeq: 0, detail: null };
const number = new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 });
const compact = new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 });
const esc = value => String(value).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const fmt = n => number.format(n || 0);
const duration = ms => ms >= 3600000 ? `${fmt(ms / 3600000)} h` : ms >= 60000 ? `${fmt(ms / 60000)} min` : ms >= 1000 ? `${fmt(ms / 1000)} s` : `${fmt(ms)} ms`;
const set = (id, value) => { $(id).textContent = value; };
let toastTimer, redrawTimer;

async function api(path, options = {}) {
 const response = await fetch(path, { credentials: 'same-origin', ...options });
 let body; try { body = await response.json(); } catch { throw new Error('The server returned an unreadable response.'); }
 if (!response.ok) {
  if (response.status === 401 && !path.includes('/login')) showLogin();
  throw new Error(body.error || `Request failed (${response.status}).`);
 }
 return body;
}
function showLogin() { $('shell').hidden = true; $('login').hidden = false; state.data = null; }
function toast(message) { set('toast', message); $('toast').hidden = false; clearTimeout(toastTimer); toastTimer = setTimeout(() => { $('toast').hidden = true; }, 3200); }
function showError(message) { set('errorMessage', message); $('errorBanner').hidden = false; }
function query(name = '') {
 const to = name && state.data ? state.data.to : Math.floor(Date.now() / 1000);
 const from = name && state.data ? state.data.from : to - state.range;
 const q = new URLSearchParams({ app: state.app, kind: state.kind, from: String(from), to: String(to) });
 if (name) q.set('name', name);
 return `/api/dashboard?${q}`;
}

async function start() {
 try {
  state.status = await api('/api/status');
  $('login').hidden = true; $('shell').hidden = false;
  $('sampleBanner').hidden = !state.status.sample;
  $('sourceBadge').classList.toggle('sample', state.status.sample);
  set('sourceBadge', state.status.sample ? 'Sample data' : 'ClickHouse connected');
  $('logoutButton').hidden = state.status.sample;
  await load();
 } catch (error) { if (!$('login').hidden) return; if ($('shell').hidden) { showLogin(); set('loginError', error.message); } else showError(error.message); }
}
async function load() {
 if ($('shell').hidden) return;
 const seq = ++state.seq;
 $('dataArea').classList.add('busy');
 $('loading').hidden = false; $('refreshButton').classList.add('spinning'); $('errorBanner').hidden = true;
 try {
  const apps = await api('/api/applications'); if (seq !== state.seq) return;
  const previous = state.app;
  $('application').replaceChildren(new Option('All applications', 'all'));
  for (const app of apps) $('application').add(new Option(app.name, app.id));
  if (previous !== 'all' && !apps.some(a => a.id === previous)) state.app = 'all';
  $('application').value = state.app;
  const data = await api(query()); if (seq !== state.seq) return;
  state.data = data; $('dataArea').hidden = false; render();
  set('updatedAt', `Updated ${new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })}`);
 } catch (error) { if (seq === state.seq && !$('shell').hidden) { $('dataArea').hidden = true; showError(error.message); set('updatedAt', 'Update failed'); } }
 finally { if (seq === state.seq) { $('dataArea').classList.remove('busy'); $('loading').hidden = true; $('refreshButton').classList.remove('spinning'); } }
}
function value(id, text, unit) { $(id).replaceChildren(document.createTextNode(text)); if (unit) { const span = document.createElement('span'); span.className = 'value-unit'; span.textContent = unit; $(id).append(span); } }
function render() {
 const d = state.data; if (!d) return;
 const hook = state.kind === 'hook';
 $('pluginTab').classList.toggle('active', !hook); $('hookTab').classList.toggle('active', hook);
 $('pluginTab').setAttribute('aria-selected', String(!hook)); $('hookTab').setAttribute('aria-selected', String(hook));
 set(hook ? 'hookCount' : 'pluginCount', d.rows.length);
 set('statSelf', duration(d.self_ms)); value('statRate', compact.format(d.calls_per_min), '/ min');
 set('statCalls', `${fmt(d.calls)} calls in this window`); value('statAvg', fmt(d.avg_ms), 'ms');
 set('statTop', d.rows[0]?.name || 'No data'); set('statShare', d.rows.length ? `${fmt(d.rows[0].share)}% of ${hook ? 'hook' : 'plugin/theme'} self time` : 'Waiting for observed callbacks');
 set('chartSubtitle', hook ? 'Self-time contribution by WordPress hook' : 'Self-time contribution by plugin & theme');
 $('tableTitle').replaceChildren(document.createTextNode(hook ? 'Hook breakdown ' : 'Plugin & theme breakdown '), Object.assign(document.createElement('span'), { className: 'count-pill', id: 'rowCount', textContent: d.rows.length }));
 set('nameHeading', hook ? 'WordPress hook' : 'Plugin / theme');
 $('search').placeholder = hook ? 'Find a hook…' : 'Find a plugin or theme…';
 $('emptyState').hidden = d.rows.length > 0;
 $('exportButton').disabled = d.rows.length === 0;
 set('windowLabel', `${new Date(d.from * 1000).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })} — ${new Date(d.to * 1000).toLocaleString([], { hour: '2-digit', minute: '2-digit' })}`);
 set('activityPeak', `Peak ${compact.format(Math.max(0, ...d.call_rates))} / min`);
 renderTable(); renderCharts();
 $('chartLegend').replaceChildren();
 d.series.forEach((series, i) => {
  const item = document.createElement(series.name === 'Other' ? 'span' : 'button'); item.className = 'legend-item';
  const dot = document.createElement('span'); dot.className = 'legend-dot'; dot.style.background = palette[i % palette.length];
  item.append(dot, document.createTextNode(series.name));
  if (series.name !== 'Other') { item.type = 'button'; item.addEventListener('click', () => openDetail(series.name)); }
  $('chartLegend').append(item);
 });
}
function filteredRows() {
 const needle = $('search').value.toLowerCase().trim();
 return (state.data?.rows || []).filter(r => r.name.toLowerCase().includes(needle)).sort((a, b) => {
  const delta = a[state.sort] - b[state.sort]; return (state.desc ? -delta : delta) || a.name.localeCompare(b.name);
 });
}
function renderTable() {
 if (!state.data) return;
 const rows = filteredRows(), pageSize = 12;
 state.page = Math.max(0, Math.min(state.page, Math.ceil(rows.length / pageSize) - 1));
 const page = rows.slice(state.page * pageSize, (state.page + 1) * pageSize);
 $('breakdownRows').replaceChildren();
 page.forEach(row => {
  const index = state.data.rows.findIndex(r => r.name === row.name), color = palette[Math.min(index, 5)];
  const tr = document.createElement('tr');
  tr.innerHTML = `<td><button type="button" class="component-button" aria-label="View ${esc(row.name)} details"><span class="component-icon" aria-hidden="true">${state.kind === 'hook' ? '⌁' : esc(row.name.slice(0, 1).toUpperCase())}</span><span class="component-name">${esc(row.name)}</span></button></td><td>${fmt(row.calls)}</td><td>${fmt(row.avg_ms)} ms</td><td>${duration(row.total_ms)}</td><td class="self-time">${duration(row.self_ms)}</td><td><div class="share-wrap"><div class="share-track"><div class="share-fill"></div></div><span class="share-value">${fmt(row.share)}%</span></div></td><td class="arrow-column row-arrow" aria-hidden="true">↗</td>`;
  tr.querySelector('.share-fill').style.width = `${Math.max(0, Math.min(100, row.share))}%`;
  tr.querySelector('.share-fill').style.background = color;
  tr.addEventListener('click', () => openDetail(row.name));
  $('breakdownRows').append(tr);
 });
 $('noResults').hidden = rows.length > 0 || !state.data.rows.length;
 set('tableSummary', rows.length ? `Showing ${state.page * pageSize + 1}–${Math.min((state.page + 1) * pageSize, rows.length)} of ${rows.length} ${state.kind === 'hook' ? 'hooks' : 'plugins & themes'}` : 'No observed components');
 let pager = $('pager'); if (!pager) { pager = document.createElement('span'); pager.id = 'pager'; pager.className = 'pager'; document.querySelector('.table-footer').append(pager); }
 pager.hidden = rows.length <= pageSize; pager.replaceChildren();
 [['←', -1], ['→', 1]].forEach(([label, step]) => { const b = document.createElement('button'); b.className = 'icon-button'; b.textContent = label; b.setAttribute('aria-label', step < 0 ? 'Previous page' : 'Next page'); b.disabled = step < 0 ? state.page === 0 : (state.page + 1) * pageSize >= rows.length; b.addEventListener('click', () => { state.page += step; renderTable(); }); pager.append(b); });
 document.querySelectorAll('[data-sort]').forEach(b => { const active = b.dataset.sort === state.sort; b.classList.toggle('sorted', active); b.querySelector('span').textContent = active ? (state.desc ? '↓' : '↑') : '↕'; b.closest('th').setAttribute('aria-sort', active ? (state.desc ? 'descending' : 'ascending') : 'none'); });
}
function renderCharts() {
 const d = state.data; if (!d) return;
 drawChart($('mainChart'), d.times, d.series, 'ms / min', true);
 drawChart($('activityChart'), d.times, [{ name: 'Calls', values: d.call_rates }], 'calls / min', false);
 if ($('detailDialog').open && state.detail) drawChart($('detailChart'), state.detail.times, state.detail.series, 'ms / min', false);
}
function drawChart(container, times, series, unit, stacked) {
 container.replaceChildren();
 if (!times.length || !series.length || !series.some(s => s.values.some(v => v > 0))) { container.innerHTML = '<div class="chart-empty">No observations in this window</div>'; return; }
 const w = Math.max(260, container.clientWidth), h = Math.max(160, container.clientHeight), left = 43, right = 12, top = 13, bottom = 29, plotW = w - left - right, plotH = h - top - bottom;
 const totals = times.map((_, i) => stacked ? series.reduce((sum, s) => sum + (s.values[i] || 0), 0) : Math.max(...series.map(s => s.values[i] || 0)));
 const max = Math.max(1, ...totals) * 1.12;
 const x = i => left + i / Math.max(1, times.length - 1) * plotW;
 const y = v => top + plotH - v / max * plotH;
 let svg = `<svg class="chart-svg" viewBox="0 0 ${w} ${h}" role="img" aria-label="${esc(unit)} over the selected time range"><title>${esc(unit)} over time. The component table provides numeric totals.</title>`;
 for (let i = 0; i <= 4; i++) { const value = max * i / 4, yy = y(value); svg += `<line class="chart-grid" x1="${left}" x2="${w - right}" y1="${yy}" y2="${yy}"/><text x="${left - 8}" y="${yy + 3}" text-anchor="end">${esc(compact.format(value))}</text>`; }
 const base = times.map(() => 0);
 series.forEach((s, si) => {
  const lower = base.slice(), upper = s.values.map((v, i) => v + (stacked ? base[i] : 0));
  const points = upper.map((v, i) => `${x(i).toFixed(2)},${y(v).toFixed(2)}`);
  if (times.length === 1) points.push(`${w - right},${y(upper[0])}`);
  const lowerPoints = lower.map((v, i) => `${x(i).toFixed(2)},${y(stacked ? v : 0).toFixed(2)}`).reverse();
  svg += `<path d="M${points.join(' L')} L${lowerPoints.join(' L')} Z" fill="${palette[si % palette.length]}" opacity="${stacked ? 0.8 : 0.13}"/>`;
  svg += `<path d="M${points.join(' L')}" fill="none" stroke="${palette[si % palette.length]}" stroke-width="${stacked ? 1 : 2}" stroke-linejoin="round"/>`;
  if (stacked) upper.forEach((v, i) => { base[i] = v; });
 });
 const tickCount = w < 400 ? 3 : 5;
 for (let i = 0; i < tickCount; i++) {
  const index = Math.round((times.length - 1) * i / (tickCount - 1));
  const date = new Date(times[index] * 1000), label = state.range > 86400 ? date.toLocaleDateString([], { month: 'short', day: 'numeric' }) : date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  svg += `<text x="${x(index)}" y="${h - 8}" text-anchor="${i === 0 ? 'start' : i === tickCount - 1 ? 'end' : 'middle'}">${esc(label)}</text>`;
 }
 svg += '</svg>'; container.innerHTML = svg;
 const tip = document.createElement('div'); tip.className = 'chart-tip'; tip.hidden = true; container.append(tip);
 container.onpointermove = e => {
  const bounds = container.getBoundingClientRect(), px = e.clientX - bounds.left;
  if (px < left || px > w - right) { tip.hidden = true; return; }
  const index = Math.max(0, Math.min(times.length - 1, Math.round((px - left) / plotW * (times.length - 1))));
  tip.innerHTML = `<strong>${esc(new Date(times[index] * 1000).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }))}</strong>` + series.map(s => `<span>${esc(s.name)} · ${fmt(s.values[index])} ${esc(unit)}</span>`).join('');
  tip.hidden = false; tip.style.left = `${Math.max(5, Math.min(px + 12, w - tip.offsetWidth - 5))}px`;
 };
 container.onpointerleave = () => { tip.hidden = true; };
}
async function openDetail(name) {
 const seq = ++state.detailSeq;
 state.detail = null; set('detailName', name); set('detailCategory', state.kind === 'hook' ? 'WORDPRESS HOOK' : 'PLUGIN / THEME');
 $('detailStats').replaceChildren(); $('detailChart').innerHTML = '<div class="dialog-loading">Loading component details…</div>';
 if (!$('detailDialog').open) $('detailDialog').showModal();
 try {
  const d = await api(query(name)); if (seq !== state.detailSeq) return; state.detail = d;
  const row = d.rows[0]; if (!row) { $('detailChart').innerHTML = '<div class="chart-empty">No observations in this window</div>'; return; }
  const stats = [['Calls', fmt(row.calls)], ['Self time', duration(row.self_ms)], ['Average duration', `${fmt(row.avg_ms)} ms`], ['Total time', duration(row.total_ms)], ['Longest callback', `${fmt(row.max_ms)} ms`], ['Throughput', `${fmt(d.calls_per_min)} / min`]];
  $('detailStats').innerHTML = stats.map(([label, v]) => `<div class="detail-stat"><span>${esc(label)}</span><strong>${esc(v)}</strong></div>`).join('');
  drawChart($('detailChart'), d.times, d.series, 'ms / min', false);
 } catch (error) { if (seq === state.detailSeq) { $('detailChart').textContent = error.message; } }
}
function setup() {
 const host = state.status?.sample ? 'apm.example.com' : state.status?.collector_host || 'apm.example.com';
 set('setupDaemonCode', `backend = "${state.status?.sample ? 'https:' : location.protocol}//${host}"`);
 set('setupCode', `newrelic.appname = "Your WordPress site"\nnewrelic.license = "YOUR_40_CHARACTER_INGEST_KEY"\nnewrelic.daemon.address = "/run/relay-php-daemon/agent.sock"\nnewrelic.daemon.dont_launch = 3\nnewrelic.framework.wordpress.hooks = true\nnewrelic.framework.wordpress.hooks.options = "plugin_callbacks"\nnewrelic.browser_monitoring.auto_instrument = false\nnewrelic.application_logging.forwarding.enabled = false`);
 set('setupSource', state.status?.sample ? 'You are viewing illustrative sample data in a local, temporary store. A native Linux ClickHouse setup is included in this project. This sample workspace does not prove PHP-agent interoperability.' : 'Metrics are stored in ClickHouse with approximately seven-day retention. The server administrator provisions per-client ingestion keys; the workspace access token is separate.');
 $('setupDialog').showModal();
}
function exportCSV() {
 if (!state.data?.rows.length) return;
 const safeName = name => /^[=+\-@\t\r]/.test(name) ? `'${name}` : name;
 const quote = value => `"${String(value).replace(/"/g, '""')}"`;
 const header = ['component', 'calls', 'average_ms', 'total_ms', 'self_ms', 'max_ms', 'self_time_share_percent', 'window_start_utc', 'window_end_utc'];
 const lines = [header, ...filteredRows().map(r => [safeName(r.name), r.calls, r.avg_ms, r.total_ms, r.self_ms, r.max_ms, r.share, new Date(state.data.from * 1000).toISOString(), new Date(state.data.to * 1000).toISOString()])];
 const url = URL.createObjectURL(new Blob([lines.map(line => line.map(quote).join(',')).join('\r\n')], { type: 'text/csv;charset=utf-8' }));
 const a = document.createElement('a'); a.href = url; a.download = `relay-wordpress-${state.kind}-${new Date().toISOString().slice(0, 10)}.csv`; a.click(); setTimeout(() => URL.revokeObjectURL(url), 1000); toast('Breakdown exported.');
}
$('loginForm').addEventListener('submit', async e => { e.preventDefault(); set('loginError', ''); const button = e.currentTarget.querySelector('button'); button.disabled = true; try { await api('/api/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token: $('accessToken').value }) }); $('accessToken').value = ''; await start(); } catch (error) { set('loginError', error.message); } finally { button.disabled = false; } });
document.querySelectorAll('[data-kind]').forEach(button => button.addEventListener('click', () => { if (state.kind === button.dataset.kind) return; state.kind = button.dataset.kind; state.page = 0; $('search').value = ''; state.data = null; $('breakdownRows').replaceChildren(); load(); }));
$('application').addEventListener('change', e => { state.app = e.target.value; state.page = 0; load(); });
$('range').addEventListener('change', e => { state.range = Number(e.target.value); state.page = 0; load(); });
$('search').addEventListener('input', () => { state.page = 0; renderTable(); });
document.querySelectorAll('[data-sort]').forEach(button => button.addEventListener('click', () => { if (state.sort === button.dataset.sort) state.desc = !state.desc; else { state.sort = button.dataset.sort; state.desc = true; } state.page = 0; renderTable(); }));
$('refreshButton').addEventListener('click', load); $('retryButton').addEventListener('click', load); $('exportButton').addEventListener('click', exportCSV);
['navSetup', 'helpButton', 'connectionButton', 'sampleConnect', 'emptyConnect'].forEach(id => $(id).addEventListener('click', setup));
$('navPerformance').addEventListener('click', () => { $('search').value = ''; state.page = 0; load(); });
$('detailClose').addEventListener('click', () => { state.detailSeq++; $('detailDialog').close(); });
$('setupClose').addEventListener('click', () => $('setupDialog').close());
[$('detailDialog'), $('setupDialog')].forEach(dialog => dialog.addEventListener('click', e => { if (e.target === dialog) { const b = dialog.getBoundingClientRect(); if (e.clientX < b.left || e.clientX > b.right || e.clientY < b.top || e.clientY > b.bottom) dialog.close(); } }));
$('copySetup').addEventListener('click', async () => { try { await navigator.clipboard.writeText($('setupCode').textContent); toast('Configuration copied.'); } catch { toast('Select and copy the configuration above.'); } });
$('logoutButton').addEventListener('click', async () => { try { await api('/api/logout', { method: 'POST' }); showLogin(); } catch (error) { toast(error.message); } });
document.addEventListener('keydown', e => { if (e.key === '/' && !['INPUT', 'TEXTAREA', 'SELECT'].includes(document.activeElement.tagName) && !$('shell').hidden && !$('detailDialog').open && !$('setupDialog').open) { e.preventDefault(); $('search').focus(); } });
new ResizeObserver(() => { clearTimeout(redrawTimer); redrawTimer = setTimeout(renderCharts, 120); }).observe($('mainChart'));
setInterval(() => { if (!$('shell').hidden && !document.hidden && $('autoRefresh').checked && !$('detailDialog').open && !$('setupDialog').open) load(); }, 30000);
start();

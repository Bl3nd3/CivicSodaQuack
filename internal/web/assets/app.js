// Copyright (c) 2026 Neomantra Corp
//
// The browser half of csq. Deliberately dependency-free: it ships inside the
// binary, so every kilobyte is one a user downloads with the tool, and a build
// step would put a node toolchain between a contributor and a one-line fix.
'use strict';

const $ = (sel, el = document) => el.querySelector(sel);
const state = { portals: [], modes: [], view: 'analyses', syncEnabled: false, job: null };

// ---------------------------------------------------------------- utilities

function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === 'class') node.className = v;
    else if (k === 'html') node.innerHTML = v;
    else if (k.startsWith('on')) node.addEventListener(k.slice(2), v);
    else if (v === true) node.setAttribute(k, '');
    else if (v !== false && v != null) node.setAttribute(k, v);
  }
  for (const c of children.flat()) {
    if (c == null) continue;
    node.append(c.nodeType ? c : document.createTextNode(String(c)));
  }
  return node;
}

// mount replaces a container's children, dropping the nulls that conditional
// sections produce. replaceChildren, unlike el() below, renders a null child as
// the literal text "null" — a bug that shows up as debris on the page rather
// than as an error anywhere.
function mount(root, ...nodes) {
  root.replaceChildren(...nodes.flat().filter((n) => n != null && n !== false));
}

async function api(path, opts) {
  const res = await fetch(path, opts);
  const body = await res.json().catch(() => ({ error: `${res.status} ${res.statusText}` }));
  if (!res.ok) throw body;
  return body;
}

// Numbers are formatted the way a reader expects rather than the way a database
// returns them; a raw 8614139 in a table is a number nobody reads correctly.
const fmt = new Intl.NumberFormat();

// Mirrors formatNumber in chart.go. The two must agree: the same figure appears
// in the live page and in the exported report, and a dollar total that reads
// 12,486,464,619.2 in one and 12,486,464,619 in the other looks like two
// different numbers rather than two renderings of one.
function formatCell(v) {
  if (v === null || v === undefined) return '—';
  if (typeof v !== 'number') return String(v);
  if (Number.isInteger(v)) return fmt.format(v);
  // Past a hundred, a fractional part is noise next to the magnitude.
  if (Math.abs(v) >= 100) return fmt.format(Math.round(v));
  return v.toFixed(2);
}
const isNum = (v) => typeof v === 'number';

// ---------------------------------------------------------------- chrome

function renderPortals() {
  const box = $('#portals');
  mount(box, ...state.portals.map((p) =>
    el('span', { class: 'chip', title: p.path }, el('b', {}, p.city || p.portal))));
}

const VIEWS = ['analyses', 'explore', 'health'];

// The view lives in the query string so a tab can be bookmarked, pasted to a
// colleague, or survive a reload. A query parameter rather than a fragment,
// because plenty of chat and mail clients quietly drop everything after a #.
function viewFromURL() {
  const v = new URLSearchParams(location.search).get('view');
  return VIEWS.includes(v) ? v : 'analyses';
}

// setURL writes the current place in the app to the address bar. Sharing a
// finding should be a matter of copying the URL, not describing which tab and
// which question to click.
function setURL(params) {
  const url = new URL(location.href);
  url.search = '';
  for (const [k, v] of Object.entries(params)) {
    if (v) url.searchParams.set(k, v);
  }
  history.replaceState(null, '', url);
}

function switchView(name, updateURL = true) {
  state.view = name;
  if (updateURL) setURL({ view: name });
  for (const tab of document.querySelectorAll('.tab')) {
    tab.setAttribute('aria-selected', String(tab.dataset.view === name));
  }
  for (const v of document.querySelectorAll('.view')) {
    v.hidden = v.id !== `view-${name}`;
  }
  if (name === 'analyses') renderAnalyses();
  if (name === 'explore') renderExplore();
  if (name === 'health') renderHealth();
}

// ---------------------------------------------------------------- analyses

function renderAnalyses() {
  setURL({ view: 'analyses' });
  const root = $('#view-analyses');
  mount(root,
    el('h1', {}, 'Analyses'),
    el('p', { class: 'lede' },
      'Each analysis is a set of prepared questions about one subject, with the ' +
      'caveats that keep the answers honest. Pick one to see its questions.'),
    el('div', { class: 'cards' }, state.modes.map(modeCard)),
  );
}

function modeCard(m) {
  let dot = 'blocked', label = m.reason || 'Not available';
  if (m.ready) { dot = 'ready'; label = `Ready · ${m.query_count} questions`; }
  else if (!m.applicable) { dot = ''; }

  return el('button', {
    class: 'card',
    disabled: !m.applicable,
    onclick: () => m.applicable && showMode(m),
  },
    el('h3', {}, m.title.split('—')[0].trim()),
    el('p', {}, m.summary),
    el('div', { class: 'status' }, el('i', { class: `dot ${dot}` }), label),
  );
}

function showMode(m, autoQuery = null) {
  setURL({ view: 'analyses', mode: m.name, query: autoQuery });
  const root = $('#view-analyses');
  mount(root,
    el('button', { class: 'back', onclick: renderAnalyses }, '← All analyses'),
    el('h1', {}, m.title),
    el('p', { class: 'lede' }, m.about),

    !m.ready && m.applicable ? notReadyPanel(m) : null,
    !m.applicable && m.reason
      ? el('div', { class: 'callout' },
          el('strong', {}, 'Not available for this portal'), m.reason)
      : null,

    m.datasets.length
      ? el('div', { class: 'caveats' },
          el('h3', {}, 'Datasets this needs'),
          el('ul', {}, m.datasets.map((d) =>
            el('li', {},
              el('b', {}, d.name || d.table), ' — ',
              d.present ? `${fmt.format(d.rows)} rows held locally` : 'not synced yet'))))
      : null,

    el('div', { class: 'qlist' }, m.queries.map((q) =>
      el('button', {
        class: 'qrow', id: `q-${q.name}`,
        onclick: (e) => runQuery(m, q, e.currentTarget),
      }, el('b', {}, q.name), el('span', {}, q.desc)))),

    el('div', { class: 'toolbar' },
      el('a', { class: 'btn secondary', href: `/report/${m.name}.html`, target: '_blank' },
        'Open full report'),
      el('a', { class: 'btn secondary', href: `/report/${m.name}.html?download=1` },
        'Download report to share')),

    m.caveats.length
      ? el('div', { class: 'caveats' },
          el('h3', {}, 'Read before quoting any of this'),
          el('ul', {}, m.caveats.map((c) => el('li', {}, c))))
      : null,
  );
}

// notReadyPanel is the dead end turned into a next step. With --config the
// page can fetch the data itself; without it, the panel prints the command that
// does, because telling someone what is missing without telling them how to get
// it is where a friendly tool stops being one.
function notReadyPanel(m) {
  const needed = m.datasets.filter((d) => !d.present);
  return el('div', { class: 'callout' },
    el('strong', {}, 'No data for this analysis yet'),
    `${needed.length} of ${m.datasets.length} datasets still need downloading.`,
    state.syncEnabled
      ? el('div', { style: 'margin-top:10px' },
          el('button', {
            class: 'btn',
            id: 'getdata',
            onclick: () => startSync(m),
          }, 'Download this data'),
          el('span', { class: 'muted', style: 'margin-left:10px' },
            'This can take a while for large datasets.'))
      : el('code', {}, m.fix_command || ''),
    el('div', { id: 'jobpanel' }));
}

async function startSync(m) {
  const btn = $('#getdata');
  if (btn) { btn.disabled = true; btn.textContent = 'Starting…'; }
  try {
    await api('/api/sync', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ mode: m.name, alias: state.portals[0]?.alias }),
    });
  } catch (err) {
    mount($('#jobpanel'), errorNode(err));
    if (btn) { btn.disabled = false; btn.textContent = 'Download this data'; }
  }
}

// renderJob paints whatever the current download is doing. It is driven by the
// server-sent event stream, so it keeps moving during a sync that runs for
// minutes and survives a page reload mid-download.
function renderJob(job) {
  state.job = job;
  const panel = $('#jobpanel');
  if (!panel || !job) return;

  const done = job.state !== 'running';
  const btn = $('#getdata');
  if (btn) {
    btn.disabled = !done;
    btn.textContent = done ? 'Download this data' : 'Downloading…';
  }

  const lines = (job.datasets || []).map((d) => {
    const mark = { ok: '✓', failed: '✕', running: '⋯', waiting: '·' }[d.state] || '·';
    const rows = d.rows ? ` — ${fmt.format(d.rows)} rows` : '';
    return el('li', {}, `${mark} ${d.table || d.id}${rows}`,
      d.error ? el('span', { class: 'muted' }, ` ${d.error}`) : null);
  });

  mount(panel,
    el('div', { style: 'margin-top:12px' },
      el('b', {}, done ? `Download ${job.state}` : 'Downloading…'),
      el('ul', { style: 'margin:6px 0 0; padding-left:18px' }, lines),
      job.error ? el('div', { class: 'muted', style: 'margin-top:6px' }, job.error) : null,
      job.state === 'running'
        ? el('button', { class: 'btn secondary', style: 'margin-top:10px',
            onclick: () => api('/api/sync/stop', { method: 'POST' }) }, 'Stop')
        : null,
      job.state === 'done'
        ? el('button', { class: 'btn', style: 'margin-top:10px', onclick: reloadModes },
            'Show the analysis')
        : null));
}

// reloadModes re-reads readiness after a download so the page reflects the data
// that just landed rather than the state it opened with.
async function reloadModes() {
  const { modes } = await api('/api/modes');
  state.modes = modes;
  const current = new URLSearchParams(location.search).get('mode');
  const m = modes.find((x) => x.name === current);
  if (m) showMode(m); else renderAnalyses();
}

// listenForSyncEvents keeps the page in step with a download started here, in
// another tab, or before this page was opened.
function listenForSyncEvents() {
  const src = new EventSource('/api/sync/events');
  src.onmessage = (e) => {
    try { renderJob(JSON.parse(e.data)); } catch { /* ignore a partial frame */ }
  };
}

async function runQuery(m, q, anchor) {
  setURL({ view: 'analyses', mode: m.name, query: q.name });
  let out = $('#result');
  if (!out) {
    out = el('div', { class: 'result', id: 'result' });
    anchor.closest('.qlist').after(out);
  }
  mount(out, el('p', { class: 'spinner' }, 'Running…'));

  try {
    const res = await api('/api/run', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ mode: m.name, query: q.name }),
    });
    mount(out, ...resultNodes(res, q));
  } catch (err) {
    mount(out, errorNode(err));
  }
  out.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
}

function errorNode(err) {
  return el('div', { class: 'callout' },
    el('strong', {}, err.error || 'Something went wrong'),
    err.fix || '',
    err.fix_command ? el('code', {}, err.fix_command) : null);
}

function resultNodes(res, q) {
  const nodes = [el('h2', {}, q.name), el('p', { class: 'lede' }, q.desc)];

  // Exclusions come before the numbers, never after. A reader who sees the
  // table first has already drawn a conclusion by the time they reach a
  // footnote saying a city was left out.
  if (res.excluded && res.excluded.length) {
    nodes.push(el('div', { class: 'callout' },
      el('strong', {}, 'Excluded from this comparison'),
      el('ul', {}, res.excluded.map((x) => el('li', {}, `${x.city} — ${x.reason}`))),
      'A city is missing because it does not publish the data, never because it scored zero.'));
  }
  if (res.not_a_comparison) {
    nodes.push(el('div', { class: 'callout' },
      el('strong', {}, 'Not a comparison'),
      'Only one city qualified, so this is a single-city figure.'));
  }

  if (!res.rows.length) {
    nodes.push(el('p', { class: 'spinner' }, 'This question returned no rows.'));
    return nodes;
  }

  if (res.chart) nodes.push(...chartNodes(res));
  nodes.push(tableNode(res));
  nodes.push(el('p', { class: 'muted' },
    `${fmt.format(res.rows.length)} rows · ${res.elapsed}` +
    (res.truncated ? ' · showing the first rows only' : '')));
  return nodes;
}

function tableNode(res) {
  const numeric = res.columns.map((_, i) => res.rows.some((r) => isNum(r[i])));
  return el('div', { class: 'tablewrap' },
    el('table', {},
      el('thead', {}, el('tr', {}, res.columns.map((c, i) =>
        el('th', { class: numeric[i] ? 'num' : '' }, c)))),
      el('tbody', {}, res.rows.map((r) =>
        el('tr', {}, r.map((v, i) =>
          el('td', { class: numeric[i] ? 'num' : '' }, formatCell(v))))))));
}

// Horizontal bars, drawn from the column choice the server made. The rules
// mirror the exported report exactly: one hue for a magnitude comparison, the
// two categorical slots when cities are the subject, direct value labels so
// identity never rests on colour alone.
function chartNodes(res) {
  const SVG = 'http://www.w3.org/2000/svg';
  const { label_col: L, value_col: V, city_col: C, series } = res.chart;
  const rows = res.rows.slice(0, 20);
  const rowH = 30, gap = 2, labelW = 240, barMaxW = 360, valueW = 110, pad = 8;
  const max = Math.max(...rows.map((r) => Math.abs(r[V] ?? 0)), 1);

  const svg = document.createElementNS(SVG, 'svg');
  svg.setAttribute('viewBox', `0 0 ${labelW + barMaxW + valueW} ${pad + rows.length * rowH}`);
  svg.setAttribute('height', pad + rows.length * rowH);
  svg.setAttribute('width', '100%');
  svg.setAttribute('class', 'chart');
  svg.setAttribute('role', 'img');

  rows.forEach((r, i) => {
    const y = pad + i * rowH;
    const w = Math.max(2, (Math.abs(r[V] ?? 0) / max) * barMaxW);
    const seriesIdx = C >= 0 ? Math.max(0, series.indexOf(r[C])) : 0;
    let label = String(r[L] ?? '');
    if (C >= 0) label += ' · ' + r[C];

    const t = document.createElementNS(SVG, 'text');
    t.setAttribute('x', labelW - 10); t.setAttribute('y', y + rowH / 2 + 4);
    t.setAttribute('text-anchor', 'end'); t.setAttribute('class', 'bar-label');
    t.textContent = label.length > 34 ? label.slice(0, 33) + '…' : label;
    svg.append(t);

    const rect = document.createElementNS(SVG, 'rect');
    rect.setAttribute('x', labelW); rect.setAttribute('y', y + gap);
    rect.setAttribute('width', w); rect.setAttribute('height', rowH - gap * 2);
    rect.setAttribute('rx', 4);
    rect.setAttribute('fill', `var(--series-${seriesIdx + 1})`);
    const title = document.createElementNS(SVG, 'title');
    title.textContent = `${label}: ${formatCell(r[V])}`;
    rect.append(title);
    svg.append(rect);

    const val = document.createElementNS(SVG, 'text');
    val.setAttribute('x', labelW + w + 8); val.setAttribute('y', y + rowH / 2 + 4);
    val.setAttribute('class', 'bar-value');
    val.textContent = formatCell(r[V]);
    svg.append(val);
  });

  const nodes = [svg];
  if (series && series.length >= 2) {
    nodes.push(el('div', { class: 'legend' }, series.map((s, i) =>
      el('span', {}, el('i', { class: 'swatch', style: `background: var(--series-${i + 1})` }), s))));
  }
  return nodes;
}

// ---------------------------------------------------------------- explore

let exploreState = { q: '', category: '', offset: 0 };

async function renderExplore() {
  const root = $('#view-explore');
  mount(root,
    el('h1', {}, 'Explore data'),
    el('p', { class: 'lede' },
      'Everything the portal publishes, and whether you hold a copy of it. ' +
      'A dataset you have not synced cannot be queried — an empty result from ' +
      'one would look exactly like a real finding of nothing.'),
    el('div', { class: 'toolbar' },
      el('input', {
        type: 'search', placeholder: 'Search datasets…', value: exploreState.q,
        oninput: debounce((e) => { exploreState.q = e.target.value; exploreState.offset = 0; loadCatalog(); }, 250),
      }),
      el('select', { id: 'catfilter', onchange: (e) => { exploreState.category = e.target.value; exploreState.offset = 0; loadCatalog(); } },
        el('option', { value: '' }, 'All categories'))),
    el('div', { id: 'catalog' }, el('p', { class: 'spinner' }, 'Loading…')),
  );

  api('/api/categories').then(({ categories }) => {
    const sel = $('#catfilter');
    if (!sel) return;
    for (const c of categories) {
      sel.append(el('option', { value: c.name }, `${c.name} (${fmt.format(c.count)})`));
    }
    sel.value = exploreState.category;
  }).catch(() => {});

  loadCatalog();
}

async function loadCatalog() {
  const box = $('#catalog');
  if (!box) return;
  mount(box, el('p', { class: 'spinner' }, 'Loading…'));
  try {
    const p = new URLSearchParams({
      q: exploreState.q, category: exploreState.category,
      limit: '50', offset: String(exploreState.offset),
    });
    const page = await api('/api/catalog?' + p);
    mount(box,
      el('p', { class: 'muted' },
        `${fmt.format(page.total)} datasets · showing ${page.entries.length}`),
      el('div', { class: 'qlist' }, page.entries.map(datasetRow)),
      pager(page));
  } catch (err) {
    mount(box, errorNode(err));
  }
}

function datasetRow(d) {
  return el('div', { class: 'qrow' },
    el('b', {}, d.name),
    el('span', {}, (d.description || '').slice(0, 220) || 'No description published.'),
    el('div', { class: 'status', style: 'margin-top:8px; display:flex; gap:8px; flex-wrap:wrap' },
      el('span', { class: `badge ${d.synced ? 'have' : ''}` },
        d.synced ? `You have ${fmt.format(d.local_rows)} rows` : 'Not synced'),
      d.category ? el('span', { class: 'badge' }, d.category) : null,
      d.row_count != null ? el('span', { class: 'badge' }, `${fmt.format(d.row_count)} rows upstream`) : null,
      el('span', { class: 'badge' }, d.id)));
}

function pager(page) {
  const back = page.offset > 0;
  const fwd = page.offset + page.limit < page.total;
  if (!back && !fwd) return null;
  return el('div', { class: 'toolbar', style: 'margin-top:16px' },
    back ? el('button', { class: 'btn secondary', onclick: () => { exploreState.offset -= page.limit; loadCatalog(); } }, '← Previous') : null,
    fwd ? el('button', { class: 'btn secondary', onclick: () => { exploreState.offset += page.limit; loadCatalog(); } }, 'Next →') : null);
}

function debounce(fn, ms) {
  let t;
  return (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms); };
}

// ---------------------------------------------------------------- health

// Data health is the research mode wearing plain language. It runs before an
// analysis, not after: the questions it answers — what failed, what is missing,
// which columns carry impossible dates — are the ones that decide whether any
// later number means anything.
const HEALTH_QUERIES = [
  ['failed-runs', 'Syncs that did not finish', 'A silent gap in your data is the fastest route to a wrong conclusion.'],
  ['coverage-gaps', 'What you are missing', 'Datasets the portal publishes that you have never synced.'],
  ['provenance', 'Where your data came from', 'The citation record: what was synced, when, and how long it took.'],
  ['date-columns', 'Dates worth distrusting', 'Civic data routinely carries impossible dates. Range-check these.'],
];

async function renderHealth() {
  const root = $('#view-health');
  mount(root,
    el('h1', {}, 'Data health'),
    el('p', { class: 'lede' },
      'The due-diligence pass that belongs before an analysis, not after it. ' +
      'These read your own sync records, so they work whatever you have loaded.'),
    el('div', { id: 'health' }, el('p', { class: 'spinner' }, 'Checking…')),
  );

  const box = $('#health');
  const nodes = [];
  for (const [name, title, desc] of HEALTH_QUERIES) {
    try {
      const res = await api('/api/run', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ mode: 'research', query: name, limit: 25 }),
      });
      nodes.push(el('h2', { style: 'margin-top:32px' }, title));
      nodes.push(el('p', { class: 'lede' }, desc));
      nodes.push(res.rows.length
        ? tableNode(res)
        : el('p', { class: 'muted' }, 'Nothing to report here — which is the good outcome.'));
    } catch (err) {
      nodes.push(el('h2', { style: 'margin-top:32px' }, title), errorNode(err));
    }
  }
  mount(box, ...nodes);
}

// ---------------------------------------------------------------- boot

async function boot() {
  for (const tab of document.querySelectorAll('.tab')) {
    tab.addEventListener('click', () => switchView(tab.dataset.view));
  }
  try {
    const [{ portals }, { modes }, syncStatus] = await Promise.all([
      api('/api/portals'), api('/api/modes'), api('/api/sync/status'),
    ]);
    state.portals = portals;
    state.modes = modes;
    state.syncEnabled = syncStatus.enabled;
    if (state.syncEnabled) listenForSyncEvents();
    renderPortals();
    // Read the deep link before rendering: renderAnalyses rewrites the URL to
    // the bare tab, which would erase the very parameters being honoured.
    const p = new URLSearchParams(location.search);
    switchView(viewFromURL(), false);

    // A link may point at one analysis, or at one question inside it.
    const wanted = state.modes.find((m) => m.name === p.get('mode'));
    if (wanted && viewFromURL() === 'analyses') {
      showMode(wanted, p.get('query'));
      const q = wanted.queries.find((x) => x.name === p.get('query'));
      if (q) runQuery(wanted, q, $(`#q-${q.name}`));
    }
    window.addEventListener('popstate', () => switchView(viewFromURL(), false));
  } catch (err) {
    mount($('#main'), errorNode(err));
  }
}

boot();

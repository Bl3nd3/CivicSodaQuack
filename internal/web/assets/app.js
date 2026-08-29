// Copyright (c) 2026 Neomantra Corp
//
// The browser half of csq. Deliberately dependency-free: it ships inside the
// binary, so every kilobyte is one a user downloads with the tool, and a build
// step would put a node toolchain between a contributor and a one-line fix.
'use strict';

const $ = (sel, el = document) => el.querySelector(sel);
const state = {
  portals: [], modes: [], view: 'analyses',
  syncEnabled: false, canSetup: false, cities: [], job: null,
};

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
  mount(box,
    ...state.portals.map((p) =>
      el('span', { class: 'chip', title: p.path }, el('b', {}, p.city || p.portal))),
    state.canSetup
      ? el('button', { class: 'chip chip-btn', onclick: () => renderSetup() }, '+ Add a city')
      : null);
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

// showViewChrome moves the tab highlight and visibility without rendering
// anything into the panel. Kept separate from switchView so a renderer can put
// itself on screen without re-entering the dispatch that called it — the setup
// flow lives inside the analyses tab, and having it call switchView made the
// two call each other forever.
function showViewChrome(name) {
  state.view = name;
  for (const tab of document.querySelectorAll('.tab')) {
    tab.setAttribute('aria-selected', String(tab.dataset.view === name));
  }
  for (const v of document.querySelectorAll('.view')) {
    v.hidden = v.id !== `view-${name}`;
  }
}

function switchView(name, updateURL = true) {
  showViewChrome(name);
  if (updateURL) setURL({ view: name });
  if (name === 'analyses') renderAnalyses();
  if (name === 'explore') renderExplore();
  if (name === 'health') renderHealth();
}

// ---------------------------------------------------------------- analyses

function renderAnalyses() {
  // With nothing loaded there are no analyses to list, and a grid of greyed
  // cards explaining why is a worse first impression than simply asking the
  // question that gets someone started.
  if (!state.portals.length && state.canSetup) return renderSetup();

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

// ---------------------------------------------------------------- setup

// renderSetup is the first-run flow: pick a city, pick an analysis, see what it
// will cost, confirm.
//
// It exists because everything else in csq assumes you already have a database,
// and getting the first one required knowing that `csq modes init` writes a
// YAML that `csq sync` then consumes. That is three pieces of vocabulary before
// a single number appears on screen.
function renderSetup(city = null) {
  showViewChrome('analyses');
  setURL({ view: 'analyses', setup: '1' });
  const root = $('#view-analyses');

  if (!city) {
    mount(root,
      state.portals.length
        ? el('button', { class: 'back', onclick: renderAnalyses }, '← Back to analyses')
        : null,
      el('h1', {}, 'Pick a city'),
      el('p', { class: 'lede' },
        'csq knows how to map these cities\u2019 published data onto its analyses. ' +
        'Pick one and it will download what that analysis needs \u2014 no configuration files, ' +
        'no dataset ids.'),
      el('div', { class: 'cards' }, state.cities.map((c) =>
        el('button', { class: 'card', onclick: () => renderSetup(c) },
          el('h3', {}, c.city),
          el('p', {}, `${c.analyses.length} ${c.analyses.length === 1 ? 'analysis' : 'analyses'} available`),
          el('div', { class: 'status' },
            el('i', { class: `dot ${c.attached ? 'ready' : ''}` }),
            c.attached ? 'already loaded' : c.portal)))));
    return;
  }

  mount(root,
    el('button', { class: 'back', onclick: () => renderSetup() }, '← All cities'),
    el('h1', {}, city.city),
    el('p', { class: 'lede' },
      'Pick what you want to look at. csq will download only the datasets that ' +
      'analysis needs, and tell you what it is doing while it does it.'),
    el('div', { class: 'qlist' }, city.analyses.map((a) =>
      el('button', { class: 'qrow', onclick: () => confirmSetup(city, a) },
        el('b', {}, a.title.split('\u2014')[0].trim()),
        el('span', {}, a.summary),
        el('div', { class: 'status', style: 'margin-top:8px; gap:8px; display:flex' },
          el('span', { class: 'badge' }, `${a.datasets} datasets`),
          el('span', { class: 'badge' }, `~${fmt.format(a.approx_rows)} rows`),
          el('span', { class: 'badge' }, a.approx_time))))),
    el('div', { id: 'jobpanel' }));
}

// confirmSetup states the cost before committing to it. A download measured in
// tens of minutes should not start because someone clicked a card.
function confirmSetup(city, a) {
  const panel = $('#jobpanel');
  mount(panel, el('div', { class: 'callout' },
    el('strong', {}, `Download ${a.datasets} datasets for ${city.city}?`),
    `About ${fmt.format(a.approx_rows)} rows, roughly ${a.approx_time}. ` +
    'You can stop it at any point, and anything already downloaded is kept.',
    el('div', { style: 'margin-top:10px; display:flex; gap:8px' },
      el('button', {
        class: 'btn', id: 'getdata',
        onclick: async () => {
          const btn = $('#getdata');
          if (btn) { btn.disabled = true; btn.textContent = 'Starting…'; }
          try {
            await api('/api/setup', {
              method: 'POST',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ portal: city.portal, mode: a.mode }),
            });
            state.syncEnabled = true;
            ensureSyncStream();
          } catch (err) {
            mount(panel, errorNode(err));
          }
        },
      }, 'Start the download'),
      el('button', { class: 'btn secondary', onclick: () => renderSetup(city) }, 'Cancel'))));
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
    ensureSyncStream();
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
  if (done) closeSyncStream();
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
  const [{ portals }, { modes }, avail] = await Promise.all([
    api('/api/portals'), api('/api/modes'), api('/api/available'),
  ]);
  state.portals = portals;
  state.modes = modes;
  state.cities = avail.cities;
  renderPortals();
  const current = new URLSearchParams(location.search).get('mode');
  const m = modes.find((x) => x.name === current);
  if (m) showMode(m); else renderAnalyses();
}

// The event stream is opened only while a download is actually running, and
// closed when it reaches a terminal state.
//
// An always-open EventSource is a connection held for something that is not
// happening: it keeps the page permanently "loading" to anything measuring
// network idle, and it costs a server goroutine and a subscriber slot per open
// tab for no benefit.
let syncStream = null;

function ensureSyncStream() {
  if (syncStream) return;
  syncStream = new EventSource('/api/sync/events');
  syncStream.onmessage = (e) => {
    try { renderJob(JSON.parse(e.data)); } catch { /* ignore a partial frame */ }
  };
  syncStream.onerror = () => { /* EventSource reconnects on its own */ };
}

function closeSyncStream() {
  if (!syncStream) return;
  syncStream.close();
  syncStream = null;
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

// Markers mirror the terminal renderer. Unknown gets one of its own rather
// than being hidden: a reader who cannot see that a check was skipped assumes
// it passed, which turns a gap in the evidence into a claim about the data.
const CONF_MARK = { pass: '\u2713', warn: '\u26a0', fail: '\u2717', unknown: '\u00b7' };

// confidenceNodes renders the data-fitness block for one result.
//
// The score is never rendered on its own. Every branch here that shows a
// number also shows the signals behind it and the limits on reading it —
// a bare percentage is precisely the artefact this is meant to prevent.
function confidenceNodes(c) {
  if (!c) return null;
  if (!c.assessed) {
    if (!c.datasets || !c.datasets.length) return null;
    return el('div', { class: 'confidence' },
      el('div', { class: 'conf-head' },
        el('span', { class: 'conf-score unknown' }, 'Not assessed'),
        el('span', { class: 'muted' },
          'None of the datasets behind this answer could be profiled.')));
  }

  const band = c.band || 'insufficient';
  const groups = [
    c.signals.filter((s) => s.level === 'pass'),
    c.signals.filter((s) => s.level === 'unknown'),
    c.signals.filter((s) => s.level === 'warn' || s.level === 'fail'),
  ];
  const multi = (c.datasets || []).length > 1;

  const line = (s) => el('li', { class: `conf-sig ${s.level}` },
    el('span', { class: 'conf-mark', 'aria-hidden': 'true' }, CONF_MARK[s.level] || ''),
    el('span', {},
      el('span', {}, (multi && s.dataset ? `${s.dataset}: ` : '') + s.label),
      s.detail ? el('span', { class: 'conf-detail' }, s.detail) : null));

  return el('div', { class: 'confidence' },
    el('div', { class: 'conf-head' },
      el('span', { class: `conf-score ${band}` }, `${c.score}%`),
      el('span', { class: 'conf-what' },
        el('b', {}, `Confidence: ${band}`),
        el('span', { class: 'muted' },
          c.coverage < 100
            ? `${c.coverage}% of checks could be run \u2014 the score covers only those.`
            : 'The share of records this query reads that are present and usable.')),
      c.freshness_days != null
        ? el('span', { class: 'conf-fresh' },
            el('b', {}, c.freshness_days === 1 ? '1 day' : `${c.freshness_days} days`),
            el('span', { class: 'muted' }, 'since the portal changed this data'))
        : null),
    el('div', { class: 'conf-bar' },
      el('span', { class: `conf-fill ${band}`, style: `width:${Math.max(c.score, 2)}%` })),
    el('ul', { class: 'conf-signals' }, groups.flat().map(line)),
    c.limits && c.limits.length
      ? el('details', { class: 'conf-limits' },
          el('summary', {}, 'What this score does not mean'),
          el('ul', {}, c.limits.map((l) => el('li', {}, l))))
      : null);
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

  // Confidence sits above the numbers for the same reason exclusions do. A
  // score printed under a table is a footnote; printed over one it is a
  // qualifier, and the whole point is that it be read before the figures are.
  const conf = confidenceNodes(res.confidence);
  if (conf) nodes.push(conf);

  if (!res.rows.length) {
    nodes.push(el('p', { class: 'spinner' }, 'This question returned no rows.'));
    return nodes;
  }

  if (res.chart) nodes.push(...chartNodes(res));
  nodes.push(tableNode(res));

  // Export runs the query again server-side with a far higher row cap, so what
  // downloads is the whole result rather than the page's 1000-row view.
  nodes.push(el('div', { class: 'toolbar' },
    el('a', { class: 'btn secondary', href: `/export/${res.mode}/${res.query}.csv` },
      'Download CSV'),
    el('a', { class: 'btn secondary', href: `/export/${res.mode}/${res.query}.json` },
      'Download JSON'),
    el('span', { class: 'muted' },
      `ran in ${res.elapsed}` +
      (res.truncated ? ' · the page shows the first 1,000 rows; the download has the rest' : ''))));
  return nodes;
}

// tableNode renders a result as a sortable, filterable table.
//
// Sorting and filtering happen on the rows already in the browser and never
// re-run the query. That keeps them instant, and it keeps the guarantee that
// the page cannot author SQL: rearranging rows you were already given is not
// the same as asking a new question.
//
// The caption below the table always states how many rows the view is showing
// out of how many were returned, because a filtered table that does not say it
// is filtered is a straightforwardly misleading object.
function tableNode(res) {
  const numeric = res.columns.map((_, i) => res.rows.some((r) => isNum(r[i])));
  const view = { sort: null, dir: 1, filter: '' };

  const wrap = el('div', { class: 'tablewrap' });
  const caption = el('p', { class: 'muted' });
  const filterBox = el('input', {
    type: 'search',
    placeholder: 'Filter these rows…',
    oninput: debounce((e) => { view.filter = e.target.value.toLowerCase(); draw(); }, 150),
  });

  function visibleRows() {
    let rows = res.rows;
    if (view.filter) {
      rows = rows.filter((r) => r.some((v) =>
        v != null && String(v).toLowerCase().includes(view.filter)));
    }
    if (view.sort != null) {
      const i = view.sort;
      // Copy before sorting: res.rows is also what the chart reads, and
      // reordering it underneath would make the two disagree.
      rows = rows.slice().sort((a, b) => {
        const x = a[i], y = b[i];
        if (x == null && y == null) return 0;
        if (x == null) return 1;   // nulls last, whichever way it is sorted
        if (y == null) return -1;
        if (isNum(x) && isNum(y)) return (x - y) * view.dir;
        return String(x).localeCompare(String(y)) * view.dir;
      });
    }
    return rows;
  }

  function draw() {
    const rows = visibleRows();
    mount(wrap, el('table', {},
      el('thead', {}, el('tr', {}, res.columns.map((c, i) =>
        el('th', {
          class: (numeric[i] ? 'num ' : '') + 'sortable',
          title: `Sort by ${c}`,
          onclick: () => {
            view.dir = view.sort === i ? -view.dir : (numeric[i] ? -1 : 1);
            view.sort = i;
            draw();
          },
        }, c, view.sort === i ? el('span', { class: 'arrow' }, view.dir > 0 ? '▲' : '▼') : null)))),
      el('tbody', {}, rows.map((r) =>
        el('tr', {}, r.map((v, i) =>
          el('td', { class: numeric[i] ? 'num' : '' }, formatCell(v))))))));

    caption.textContent = rows.length === res.rows.length
      ? `${fmt.format(rows.length)} rows`
      : `${fmt.format(rows.length)} of ${fmt.format(res.rows.length)} rows shown`;
  }

  draw();
  return el('div', {},
    el('div', { class: 'toolbar' }, filterBox),
    wrap, caption);
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
    const [{ portals }, { modes }, syncStatus, avail] = await Promise.all([
      api('/api/portals'), api('/api/modes'), api('/api/sync/status'),
      api('/api/available'),
    ]);
    state.portals = portals;
    state.modes = modes;
    state.syncEnabled = syncStatus.enabled;
    state.canSetup = avail.can_setup;
    state.cities = avail.cities;
    // Only attach to the stream if something is actually running — a download
    // started before this page was opened, or in another tab.
    if (syncStatus.job && syncStatus.job.state === 'running') ensureSyncStream();
    renderPortals();
    // Read the deep link before rendering: renderAnalyses rewrites the URL to
    // the bare tab, which would erase the very parameters being honoured.
    const p = new URLSearchParams(location.search);
    if (p.get('setup') && state.canSetup) {
      renderPortals();
      renderSetup();
      return;
    }
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

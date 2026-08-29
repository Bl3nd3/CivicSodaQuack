// Copyright (c) 2026 Neomantra Corp

package web

// reportHTML is the standalone report template.
//
// Every colour is defined as a token on bare :root, with dark values repeated
// under both the OS media query and an explicit data-theme stamp, so a reader's
// theme choice wins in both directions and no colour has its only definition
// inside a conditional block.
const reportHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
:root {
  color-scheme: light;
  --surface:        #fcfcfb;
  --surface-2:      #f4f3f0;
  --border:         #e0dfda;
  --text-primary:   #0b0b0b;
  --text-secondary: #52514e;
  --text-muted:     #77756e;
  --accent:         #2a78d6;
  --series-1:       #2a78d6;
  --series-2:       #eb6834;
  --warn-bg:        #fdf6e6;
  --warn-border:    #eda100;
  --warn-text:      #5c4300;
}
@media (prefers-color-scheme: dark) {
  :root:not([data-theme="light"]) {
    color-scheme: dark;
    --surface:        #1a1a19;
    --surface-2:      #232322;
    --border:         #383835;
    --text-primary:   #ffffff;
    --text-secondary: #c3c2b7;
    --text-muted:     #96958c;
    --accent:         #3987e5;
    --series-1:       #3987e5;
    --series-2:       #d95926;
    --warn-bg:        #2b2412;
    --warn-border:    #c98500;
    --warn-text:      #f0d79a;
  }
}
:root[data-theme="dark"] {
  color-scheme: dark;
  --surface:        #1a1a19;
  --surface-2:      #232322;
  --border:         #383835;
  --text-primary:   #ffffff;
  --text-secondary: #c3c2b7;
  --text-muted:     #96958c;
  --accent:         #3987e5;
  --series-1:       #3987e5;
  --series-2:       #d95926;
  --warn-bg:        #2b2412;
  --warn-border:    #c98500;
  --warn-text:      #f0d79a;
}
* { box-sizing: border-box; }
body {
  margin: 0; padding: 0;
  background: var(--surface);
  color: var(--text-primary);
  font: 16px/1.6 ui-sans-serif, -apple-system, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
}
.wrap { max-width: 900px; margin: 0 auto; padding: 48px 24px 96px; }
header { border-bottom: 2px solid var(--border); padding-bottom: 24px; margin-bottom: 32px; }
h1 { font-size: 30px; line-height: 1.25; margin: 0 0 8px; letter-spacing: -0.01em; }
.meta { color: var(--text-muted); font-size: 14px; }
.about { color: var(--text-secondary); margin: 20px 0 0; }
h2 { font-size: 20px; margin: 48px 0 4px; letter-spacing: -0.01em; }
h2 .slug { color: var(--text-muted); font-weight: 400; font-size: 14px; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
.desc { color: var(--text-secondary); margin: 0 0 16px; font-size: 15px; }
.callout {
  background: var(--warn-bg); border-left: 3px solid var(--warn-border);
  color: var(--warn-text); padding: 12px 16px; margin: 16px 0; font-size: 14px;
  border-radius: 0 6px 6px 0;
}
.callout strong { display: block; margin-bottom: 4px; }
.callout ul { margin: 6px 0 0; padding-left: 20px; }
/* Confidence sits above the numbers so it reads as a qualifier, not a
   footnote. No bar here: a printed report is the one place a decorative
   element cannot be relied on, so every value is text. */
.confidence { border: 1px solid var(--border); border-radius: 8px; background: var(--surface-2); padding: 16px 20px; margin: 0 0 20px; }
.conf-head { display: flex; align-items: baseline; gap: 16px; flex-wrap: wrap; margin-bottom: 12px; }
.conf-score { font-size: 24px; font-weight: 700; line-height: 1; }
.conf-score.high { color: #008300; }
.conf-score.moderate { color: #a56800; }
.conf-score.low, .conf-score.insufficient { color: #c2410c; }
.conf-what { display: flex; flex-direction: column; gap: 2px; }
.conf-what b { text-transform: capitalize; }
.conf-fresh { margin-left: auto; color: var(--text-muted); font-size: 13px; }
.conf-signals { list-style: none; margin: 0; padding: 0; }
.conf-sig { display: flex; gap: 8px; font-size: 14px; color: var(--text-secondary); margin-bottom: 6px; }
.conf-mark { flex: none; width: 1em; font-weight: 700; }
.conf-sig.pass .conf-mark { color: #008300; }
.conf-sig.warn .conf-mark { color: #a56800; }
.conf-sig.fail .conf-mark { color: #c2410c; }
.conf-sig.fail { color: var(--text-primary); }
.conf-detail { display: block; color: var(--text-muted); font-size: 13px; margin-top: 2px; }
.conf-limits { margin: 12px 0 0; padding-left: 20px; color: var(--text-muted); font-size: 13px; }
.conf-limits li { margin-bottom: 5px; }
.caveats { background: var(--surface-2); border: 1px solid var(--border); border-radius: 8px; padding: 20px 24px; margin: 24px 0 0; }
.caveats h3 { margin: 0 0 12px; font-size: 15px; text-transform: uppercase; letter-spacing: 0.06em; color: var(--text-muted); }
.caveats li { margin-bottom: 10px; color: var(--text-secondary); font-size: 14px; }
.caveats li:last-child { margin-bottom: 0; }
figure { margin: 0 0 20px; }
.chart { display: block; max-width: 100%; height: auto; overflow: visible; }
.bar-label { fill: var(--text-secondary); font-size: 12px; font-family: inherit; }
.bar-value { fill: var(--text-primary); font-size: 12px; font-weight: 600; font-family: inherit; }
.legend { display: flex; gap: 16px; margin: 8px 0 0 240px; font-size: 13px; color: var(--text-secondary); }
.legend span { display: inline-flex; align-items: center; gap: 6px; }
.swatch { width: 10px; height: 10px; border-radius: 2px; display: inline-block; }
.tablewrap { overflow-x: auto; border: 1px solid var(--border); border-radius: 8px; }
table { border-collapse: collapse; width: 100%; font-size: 14px; }
th, td { text-align: left; padding: 8px 12px; border-bottom: 1px solid var(--border); white-space: nowrap; }
th { background: var(--surface-2); font-weight: 600; color: var(--text-secondary); position: sticky; top: 0; }
tr:last-child td { border-bottom: none; }
td.num, th.num { text-align: right; font-variant-numeric: tabular-nums; }
.skipped { color: var(--text-muted); font-style: italic; padding: 16px 0; }
footer { margin-top: 64px; padding-top: 24px; border-top: 1px solid var(--border); color: var(--text-muted); font-size: 13px; }
footer code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
@media print {
  body { background: #fff; }
  .wrap { max-width: none; padding: 0; }
  h2 { break-after: avoid; }
  figure, .tablewrap { break-inside: avoid; }
}
</style>
</head>
<body>
<div class="wrap">
<header>
  <h1>{{.Title}}</h1>
  <div class="meta">
    Generated {{.GeneratedAt}} by CivicSodaQuack ·
    {{range $i, $p := .Portals}}{{if $i}}, {{end}}{{if $p.City}}{{$p.City}}{{else}}{{$p.Portal}}{{end}}{{end}}
  </div>
  <p class="about">{{.About}}</p>
  {{if .Caveats}}
  <div class="caveats">
    <h3>Read before quoting any of this</h3>
    <ul>{{range .Caveats}}<li>{{.}}</li>{{end}}</ul>
  </div>
  {{end}}
</header>

{{range .Sections}}
<section>
  <h2>{{.Name}}</h2>
  <p class="desc">{{.Desc}}</p>

  {{if .Excluded}}
  <div class="callout">
    <strong>Excluded from this comparison</strong>
    <ul>{{range .Excluded}}<li>{{.City}} — {{.Reason}}</li>{{end}}</ul>
    A city is missing because it does not publish the data, never because it scored zero.
  </div>
  {{end}}

  {{if .NotAComparison}}
  <div class="callout"><strong>Not a comparison</strong>
    Only one city qualified, so this is a single-city figure rather than a comparison.</div>
  {{end}}

  {{with .Confidence}}{{if .Assessed}}
  <div class="confidence">
    <div class="conf-head">
      <span class="conf-score {{.Band}}">{{.Score}}%</span>
      <span class="conf-what"><b>Confidence: {{.Band}}</b>
        <span class="muted">Measures whether the data is fit to be queried — not whether the finding is true.</span></span>
      {{with .FreshnessLine}}<span class="conf-fresh">{{.}}</span>{{end}}
    </div>
    <ul class="conf-signals">
      {{range .Confirmations}}<li class="conf-sig pass"><span class="conf-mark">&#10003;</span> {{.Label}}</li>{{end}}
      {{range .Unmeasured}}<li class="conf-sig unknown"><span class="conf-mark">&middot;</span> {{.Label}}</li>{{end}}
      {{range .Problems}}<li class="conf-sig {{.Level}}"><span class="conf-mark">{{if eq .Level "fail"}}&#10007;{{else}}&#9888;{{end}}</span> {{.Label}}{{with .Detail}}<span class="conf-detail">{{.}}</span>{{end}}</li>{{end}}
    </ul>
    <ul class="conf-limits">{{range .Limits}}<li>{{.}}</li>{{end}}</ul>
  </div>
  {{end}}{{end}}

  {{if .Skipped}}
    <p class="skipped">{{.Skipped}}</p>
  {{else}}
    {{with .Chart}}
    <figure>
      {{.SVG}}
      {{if .HasLegend}}
      <div class="legend">
        {{range $i, $s := .Series}}<span><i class="swatch" style="background:{{if eq $i 0}}var(--series-1){{else}}var(--series-2){{end}}"></i>{{$s}}</span>{{end}}
      </div>
      {{end}}
    </figure>
    {{end}}
    <div class="tablewrap">
      <table>
        <thead><tr>{{range .Columns}}<th>{{.}}</th>{{end}}</tr></thead>
        <tbody>
        {{range .Rows}}<tr>{{range .}}<td>{{.}}</td>{{end}}</tr>{{end}}
        </tbody>
      </table>
    </div>
    {{if .Truncated}}<p class="skipped">Showing the first rows only; the full result is longer.</p>{{end}}
  {{end}}
</section>
{{end}}

<footer>
  Built from data synced by CivicSodaQuack. Sources:
  {{range $i, $p := .Portals}}{{if $i}}; {{end}}<code>{{$p.Portal}}</code>{{end}}.
  Reproduce with <code>csq modes run {{.Mode}}</code>.
  A timestamp records when csq last pulled the data, which is not when the city last updated it.
</footer>
</div>
</body>
</html>
`

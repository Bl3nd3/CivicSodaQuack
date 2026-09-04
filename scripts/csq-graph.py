#!/usr/bin/env python3
# Copyright (c) 2026 Neomantra Corp
"""Turn csq JSON into charts.

csq already hands you its numbers as JSON — a query result from
`csq query --format json`, or a full mode result from the web UI's
`/export/<mode>/<query>.json`. This turns either into a self-contained HTML
page of charts that opens in any browser: no server, no toolchain, no
third-party JavaScript, and no network access from the page.

    csq query --db chicago.duckdb --format json "SELECT ..." | csq-graph.py -
    csq-graph.py ranking-per_capita.json corruption-vendors.json -o charts.html

The rules deciding *whether* a result may be drawn are the same narrow ones the
web UI applies in internal/web/chart.go: one text column to name the marks, one
numeric column to size them. A result that does not meet them renders as a
table with the reason printed, never as a chart implying a comparison the data
cannot support.

Caveats, exclusions and the confidence report travel with every figure, for the
same reason they travel with the CSV and JSON exports: a chart is the easiest
place for a hedged number to lose its hedge.

Python 3.8+. Standard library only.
"""

import argparse
import html
import json
import math
import os
import re
import sys
import webbrowser

MAX_SERIES = 8        # the palette's ceiling; past it the result stays a table
MAX_BARS = 20         # default rows drawn; the rest stay in the table view
TABLE_ROW_CAP = 1000  # rows rendered into the table view

# The validated default palette from the data-viz reference, unchanged and in
# its documented order — that ordering is the colourblind-safety mechanism, not
# decoration, so slots are assigned in order and never cycled or generated.
# Slots 1 and 2 are the two the web UI's SVG charts already use, so a chart
# made here and a chart made in the page are the same chart.
#
# Checked with the reference validator in both modes: every gate passes. Light
# mode warns that aqua, yellow and magenta sit below 3:1 on the light surface,
# which obliges the relief rule — hence the always-present legend, the direct
# labels, and the table view under every figure.
SERIES_LIGHT = ["#2a78d6", "#eb6834", "#1baf7a", "#eda100",
                "#e87ba4", "#008300", "#4a3aa7", "#e34948"]
SERIES_DARK = ["#3987e5", "#d95926", "#199e70", "#c98500",
               "#d55181", "#008300", "#9085e9", "#e66767"]


# ---------------------------------------------------------------- loading

def load_documents(paths):
    """Read each input path ('-' for stdin) as JSON.

    A parse failure names the file it came from: a run over several exports
    that ends in "Expecting value: line 2 column 1" leaves the reader to guess
    which one.
    """
    docs = []
    for p in paths:
        try:
            if p == "-":
                docs.append(("stdin", json.load(sys.stdin)))
            else:
                with open(p, "r", encoding="utf-8") as fh:
                    docs.append((os.path.basename(p), json.load(fh)))
        except json.JSONDecodeError as e:
            raise ValueError("%s is not valid JSON: %s" % (p, e))
    return docs


def normalise(doc, source):
    """Flatten one JSON document into zero or more result dicts.

    Four shapes are accepted, because csq emits two of them and anyone
    bundling several results into one file produces the other two:

      * a mode result   {"columns": [...], "rows": [[...]], "caveats": [...]}
      * a record array  [{"col": val, ...}, ...]   (csq query --format json)
      * an array of mode results
      * {"results": [ <mode result>, ... ]}
    """
    if isinstance(doc, dict) and isinstance(doc.get("results"), list):
        out = []
        for item in doc["results"]:
            out.extend(normalise(item, source))
        return out

    if isinstance(doc, dict) and "columns" in doc and "rows" in doc:
        return [from_result(doc, source)]

    if isinstance(doc, list):
        if doc and all(isinstance(d, dict) and "columns" in d and "rows" in d
                       for d in doc):
            out = []
            for item in doc:
                out.extend(normalise(item, source))
            return out
        return [from_records(doc, source)]

    raise ValueError("%s: unrecognised JSON — expected a csq result object "
                     "(columns/rows) or an array of row objects" % source)


def from_result(doc, source):
    """A full csq result: the rows, and everything qualifying them."""
    return {
        "source": source,
        "title": doc.get("title") or doc.get("query") or source,
        "desc": doc.get("desc") or "",
        "mode": doc.get("mode") or "",
        "query": doc.get("query") or "",
        "columns": list(doc.get("columns") or []),
        "rows": [list(r) for r in (doc.get("rows") or [])],
        "caveats": list(doc.get("caveats") or []),
        "excluded": list(doc.get("excluded") or []),
        "not_a_comparison": bool(doc.get("not_a_comparison")),
        "truncated": bool(doc.get("truncated")),
        "confidence": doc.get("confidence"),
    }


def from_records(records, source):
    """`csq query --format json` — an array of {column: value} objects.

    Columns come from the order keys first appear, which is the SELECT order
    for every record DuckDB produces. A key missing from a later record is a
    null in that row, never a shorter row.
    """
    cols = []
    for rec in records:
        if not isinstance(rec, dict):
            raise ValueError("%s: expected an array of objects" % source)
        for k in rec:
            if k not in cols:
                cols.append(k)
    rows = [[rec.get(c) for c in cols] for rec in records]
    # A bare query result carries no title of its own, so the file it arrived
    # in is the most honest name available for it.
    title = os.path.splitext(source)[0]
    return {
        "source": source, "title": title, "desc": "", "mode": "", "query": "",
        "columns": cols, "rows": rows, "caveats": [], "excluded": [],
        "not_a_comparison": False, "truncated": False, "confidence": None,
    }


# ------------------------------------------------------------ column types

def is_number(v):
    return isinstance(v, (int, float)) and not isinstance(v, bool)


def column_is_numeric(rows, i):
    seen = False
    for r in rows:
        if i >= len(r) or r[i] is None:
            continue
        if not is_number(r[i]):
            return False
        seen = True
    return seen


def column_is_text(rows, i):
    """Every non-null value is a string.

    A string that looks like a number ("2024") still counts as text: it is the
    label the reader reads along the axis — a year, a ward, a beat — and
    excluding it would leave a per-year comparison with no label column.
    """
    seen = False
    for r in rows:
        if i >= len(r) or r[i] is None:
            continue
        if not isinstance(r[i], str):
            return False
        seen = True
    return seen


def column_all_null(rows, i):
    return all(i >= len(r) or r[i] is None for r in rows)


def is_monotonic(rows, i):
    """Whether a column never changes direction — what ORDER BY leaves behind.

    A mode query orders by the measure it is about, so the sorted column is the
    measure. Finding it this way needs no SQL parsing. Nulls are skipped rather
    than treated as breaks: a trailing null in an aggregate should not
    disqualify the measure.
    """
    prev, seen, up, down = 0.0, False, False, False
    for r in rows:
        if i >= len(r) or r[i] is None:
            continue
        if not is_number(r[i]):
            return False
        v = float(r[i])
        if seen:
            if v > prev:
                up = True
            elif v < prev:
                down = True
            if up and down:
                return False
        prev, seen = v, True
    # One value, or one repeated value, is not evidence of a sort.
    return seen and (up or down)


def measure_column(rows, numeric):
    """Which numeric column the marks should show.

    Taking the first is wrong often enough to matter: "top vendors" returns
    (vendor, contracts, total_awarded, avg_award) ordered by total value, so
    charting the first numeric column would draw contract *counts* against rows
    sorted by dollars — visibly jumbled bars next to a correctly sorted table,
    which reads as a broken chart at best and a different claim at worst.
    """
    for i in numeric:
        if is_monotonic(rows, i):
            return i
    return numeric[0]


YEAR_RE = re.compile(r"^(19|20)\d{2}$")
PERIOD_RE = re.compile(r"^(19|20)\d{2}-\d{2}(-\d{2})?([T ].*)?$")


def is_temporal(labels):
    """Whether the labels are periods, so the marks belong on a time axis."""
    if len(set(labels)) < 4:
        return False
    return all(YEAR_RE.match(str(l)) or PERIOD_RE.match(str(l)) for l in labels)


def find_column(cols, name):
    if not name:
        return -1
    for i, c in enumerate(cols):
        if c.lower() == name.lower():
            return i
    raise ValueError("no column named %r (have: %s)" % (name, ", ".join(cols)))


# ---------------------------------------------------------- form selection

def pick_spec(res, opts):
    """Decide whether a result can honestly be drawn, and from which columns.

    Returns (spec, reason). Exactly one of the two is set: a reason is what
    gets printed above the table when the answer is no, so a reader is never
    left wondering whether the chart is missing or the data was unchartable.
    """
    cols, rows = res["columns"], res["rows"]
    if not rows:
        return None, "the result has no rows"
    if len(cols) < 2:
        return None, "a chart needs a column to name the marks and one to size them"

    series_col = find_column(cols, opts.series)
    if series_col < 0:
        for i, c in enumerate(cols):
            if c.lower() == "city":
                series_col = i
                break

    numeric, text = [], []
    for i in range(len(cols)):
        if i == series_col:
            continue
        if column_is_numeric(rows, i):
            numeric.append(i)
        elif column_is_text(rows, i):
            text.append(i)

    label_col = find_column(cols, opts.label)
    if label_col < 0:
        if len(text) == 0:
            return None, ("no text column to name the marks — pick one with "
                          "--label if a numeric column holds the labels")
        if len(text) > 1:
            names = ", ".join(cols[i] for i in text)
            return None, ("%d text columns (%s), so there is no single thing "
                          "the marks would name — pick one with --label"
                          % (len(text), names))
        label_col = text[0]

    value_col = find_column(cols, opts.value)
    if value_col < 0:
        if not numeric:
            # An all-null column is neither numeric nor text, and saying "no
            # numeric column" about a column that is plainly a count sends the
            # reader looking for the wrong problem.
            empty = [cols[i] for i in range(len(cols))
                     if i != series_col and column_all_null(rows, i)]
            if empty:
                return None, ("no numeric column to size the marks — every "
                              "value in %s is null" % ", ".join(empty))
            return None, ("no numeric column to size the marks — pick one "
                          "with --value if a text column holds the numbers")
        # Several numeric columns of different units is the case a dual-axis
        # chart is invented for, and a dual-axis chart is the single most
        # misleading form in common use. One measure gets drawn; the rest stay
        # in the table, where their units are their own.
        value_col = measure_column(rows, numeric)
    elif not column_is_numeric(rows, value_col):
        return None, "--value %s is not numeric" % cols[value_col]

    series = []
    if series_col >= 0:
        for r in rows:
            v = r[series_col] if series_col < len(r) else None
            key = "—" if v is None else str(v)
            if key not in series:
                series.append(key)
        if len(series) > MAX_SERIES:
            return None, ("%d series is past what %d colours can keep apart; "
                          "the numbers are in the table below"
                          % (len(series), MAX_SERIES))

    return {"label": label_col, "value": value_col, "series": series_col,
            "series_names": series}, None


def build_marks(res, spec, top):
    """Collect (label, series index, value) triples, capped at `top` groups."""
    rows, sc = res["rows"], spec["series"]
    li, vi = spec["label"], spec["value"]
    groups, order = {}, []
    for r in rows:
        if li >= len(r) or vi >= len(r) or r[vi] is None:
            continue
        label = "—" if r[li] is None else str(r[li])
        si = 0
        if sc >= 0:
            key = "—" if (sc >= len(r) or r[sc] is None) else str(r[sc])
            si = spec["series_names"].index(key)
        if label not in groups:
            groups[label] = []
            order.append(label)
        groups[label].append((si, float(r[vi])))
    shown = order[:top]
    return [(l, groups[l]) for l in shown], len(order)


# ----------------------------------------------------------- number format

def fmt_full(v):
    """Thousands-separated, for tables and axis ticks."""
    if v == int(v) and abs(v) < 1e15:
        return "{:,}".format(int(v))
    return "{:,.2f}".format(v)


def fmt_compact(v):
    a = abs(v)
    for cut, suf in ((1e12, "T"), (1e9, "B"), (1e6, "M"), (1e3, "K")):
        if a >= cut:
            return "{:.1f}{}".format(v / cut, suf).replace(".0" + suf, suf)
    return fmt_full(v)


def value_formatter(values, compact=True):
    """One number format for a whole set of values.

    Formatting each value on its own merits produces "812.40" beside "388" and
    "184.3M" beside "121M" in the same chart, which reads as two different
    measures. The set picks the format once: the same number of decimals for
    all of them, and — where space is tight enough to earn it — a compact
    suffix. `compact=False` is for the places that must carry the whole
    number: the tooltip a value is pushed into, and the table view.
    """
    top = max((abs(v) for v in values), default=0.0)
    if compact and top >= 100000:
        return lambda v: "{:.1f}{}".format(*_scale(v))
    dec = 0
    for v in values:
        if v != int(v):
            dec = max(dec, 1 if abs(round(v, 1) - v) < 1e-9 else 2)
    return lambda v: "{:,.{d}f}".format(v, d=dec)


def _scale(v):
    a = abs(v)
    for cut, suf in ((1e12, "T"), (1e9, "B"), (1e6, "M"), (1e3, "K")):
        if a >= cut:
            return v / cut, suf
    return v, ""


def nice_ticks(lo, hi, count=4):
    """Round tick values, so the axis reads 0 / 1,000 / 2,000."""
    if hi <= lo:
        hi = lo + 1
    raw = (hi - lo) / float(count)
    mag = 10 ** math.floor(math.log10(raw)) if raw > 0 else 1
    step = mag * 10
    for m in (1, 2, 2.5, 5, 10):
        if raw <= mag * m:
            step = mag * m
            break
    start = math.floor(lo / step) * step
    ticks, v = [], start
    while v <= hi + step * 0.5:
        ticks.append(round(v, 10))
        v += step
    return ticks


# ------------------------------------------------------------------ marks

def esc(s):
    return html.escape(str(s), quote=True)


def truncate(s, n):
    return s if len(s) <= n else s[:max(1, n - 1)] + "…"


def bar_path(x_base, x_end, y, h, r=4):
    """A bar with a 4px rounded data-end, square at the baseline.

    The rounding follows the data end rather than the right-hand side, so a
    negative bar growing leftwards is rounded on the left and still square
    where it meets the zero line.
    """
    r = max(0.0, min(float(r), abs(x_end - x_base) / 2.0, h / 2.0))
    s = 1.0 if x_end >= x_base else -1.0
    xr = x_end - s * r
    sweep = 1 if s > 0 else 0
    return ("M{xb:.1f},{y:.1f} L{xr:.1f},{y:.1f} "
            "A{r:.1f},{r:.1f} 0 0 {sw} {xe:.1f},{yr:.1f} "
            "L{xe:.1f},{yb:.1f} A{r:.1f},{r:.1f} 0 0 {sw} {xr:.1f},{y2:.1f} "
            "L{xb:.1f},{y2:.1f} Z").format(
        xb=x_base, xe=x_end, xr=xr, y=y, yr=y + r, yb=y + h - r, y2=y + h,
        r=r, sw=sweep)


def bar_chart(res, spec, marks):
    """A horizontal bar chart as inline SVG.

    Horizontal because the labels are names — vendors, complaint categories,
    request types — and a column chart would either truncate them or rotate
    them to 45 degrees, a legibility tax paid on every read.
    """
    cols = res["columns"]
    names = spec["series_names"] or [cols[spec["value"]]]
    ns = max(1, len(spec["series_names"]))

    width = 900.0
    char_w = 6.6                    # 12px system sans, averaged
    longest = max(len(l) for l, _ in marks)
    pad_l = min(260.0, max(96.0, longest * char_w + 16))
    label_chars = int((pad_l - 16) / char_w)
    pad_r, pad_t, pad_b = 104.0, 34.0, 14.0
    thick = 22.0 if ns == 1 else min(18.0, max(8.0, 44.0 / ns))
    gap = 2.0                       # the surface gap; never a stroke
    group_h = ns * thick + (ns - 1) * gap
    row_h = group_h + 16.0
    height = pad_t + len(marks) * row_h + pad_b

    values = [v for _, vs in marks for _, v in vs]
    fmt = value_formatter(values)
    fmt_exact = value_formatter(values, compact=False)
    hi, lo = max(values), min(0.0, min(values))
    ticks = nice_ticks(lo, hi)
    span = ticks[-1] - ticks[0] or 1.0
    plot_w = width - pad_l - pad_r

    def x_of(v):
        return pad_l + (v - ticks[0]) / span * plot_w

    # Value at the tip while the chart stays readable; past that the axis, the
    # tooltip and the table carry the numbers instead of flooding the marks.
    label_tips = sum(len(vs) for _, vs in marks) <= 24

    out = ['<svg class="chart" viewBox="0 0 {:.0f} {:.0f}" role="img" '
           'preserveAspectRatio="xMinYMin meet" '
           'aria-label="{}">'.format(width, height, esc(res["title"]))]

    for t in ticks:
        x = x_of(t)
        out.append('<line class="grid" x1="{x:.1f}" y1="{y0:.1f}" x2="{x:.1f}" '
                   'y2="{y1:.1f}"/>'.format(x=x, y0=pad_t - 10, y1=height - pad_b))
        out.append('<text class="tick" x="{:.1f}" y="{:.1f}" text-anchor="middle">'
                   '{}</text>'.format(x, pad_t - 16, esc(fmt_compact(t))))

    # Bars grow from zero, always: the length is the magnitude, so a baseline
    # anywhere else overstates every difference on the chart.
    zero_x = x_of(min(max(0.0, ticks[0]), ticks[-1]))
    for gi, (label, values) in enumerate(marks):
        top = pad_t + gi * row_h
        out.append('<text class="cat" x="{:.1f}" y="{:.1f}" text-anchor="end">{}'
                   '<title>{}</title></text>'.format(
                       pad_l - 12, top + group_h / 2 + 4,
                       esc(truncate(label, label_chars)), esc(label)))
        for bi, (si, v) in enumerate(values):
            y = top + bi * (thick + gap)
            x1 = x_of(v)
            out.append('<path class="bar" d="{}" fill="var(--series-{})"/>'.format(
                bar_path(zero_x, x1, y, thick), si + 1))
            if label_tips:
                anchor = "start" if x1 >= zero_x else "end"
                dx = 8 if x1 >= zero_x else -8
                out.append('<text class="val" x="{:.1f}" y="{:.1f}" '
                           'text-anchor="{}">{}</text>'.format(
                               x1 + dx, y + thick / 2 + 4, anchor, esc(fmt(v))))
            # The hit target is the whole band, not the painted pixels.
            out.append('<rect class="hit" x="{:.1f}" y="{:.1f}" width="{:.1f}" '
                       'height="{:.1f}" tabindex="0" data-label="{}" '
                       'data-series="{}" data-slot="{}" data-value="{}"/>'.format(
                           pad_l, y - 1, plot_w, thick + 2, esc(label),
                           esc(names[si] if si < len(names) else ""),
                           si + 1, esc(fmt_exact(v))))
    out.append('<line class="axis" x1="{x:.1f}" y1="{y0:.1f}" x2="{x:.1f}" '
               'y2="{y1:.1f}"/>'.format(x=zero_x, y0=pad_t - 10, y1=height - pad_b))
    out.append("</svg>")
    return "".join(out)


def line_chart(res, spec, marks, cid):
    """A line chart, for labels that are periods.

    Sorted by period rather than by the result's order: a time axis that runs
    out of order is not a time axis. The table below keeps the original order.
    """
    names = spec["series_names"] or [res["columns"][spec["value"]]]
    ns = max(1, len(spec["series_names"]))
    marks = sorted(marks, key=lambda m: m[0])
    labels = [l for l, _ in marks]

    width, height = 900.0, 340.0
    pad_l, pad_r, pad_t, pad_b = 76.0, 120.0, 24.0, 44.0
    plot_w, plot_h = width - pad_l - pad_r, height - pad_t - pad_b

    vals = [v for _, vs in marks for _, v in vs]
    fmt = value_formatter(vals)
    fmt_exact = value_formatter(vals, compact=False)
    hi, lo = max(vals), min(vals)
    # Include zero when it is close enough that the shape survives it; a series
    # sitting far above zero gets a fitted range, with the ticks saying so.
    if 0 <= lo < 0.35 * hi:
        lo = 0.0
    ticks = nice_ticks(lo, hi)
    span = (ticks[-1] - ticks[0]) or 1.0

    def x_of(i):
        return pad_l + (plot_w / 2 if len(marks) == 1
                        else i * plot_w / (len(marks) - 1))

    def y_of(v):
        return pad_t + plot_h - (v - ticks[0]) / span * plot_h

    out = ['<svg class="chart" viewBox="0 0 {:.0f} {:.0f}" role="img" '
           'preserveAspectRatio="xMinYMin meet" '
           'aria-label="{}">'.format(width, height, esc(res["title"]))]
    for t in ticks:
        y = y_of(t)
        out.append('<line class="grid" x1="{:.1f}" y1="{y:.1f}" x2="{:.1f}" '
                   'y2="{y:.1f}"/>'.format(pad_l, pad_l + plot_w, y=y))
        out.append('<text class="tick" x="{:.1f}" y="{:.1f}" text-anchor="end">'
                   '{}</text>'.format(pad_l - 10, y + 4, esc(fmt_compact(t))))

    every = max(1, int(math.ceil(len(marks) / 10.0)))
    for i, l in enumerate(labels):
        if i % every and i != len(labels) - 1:
            continue
        out.append('<text class="tick" x="{:.1f}" y="{:.1f}" text-anchor="middle">'
                   '{}</text>'.format(x_of(i), height - pad_b + 20, esc(l)))

    out.append('<line class="axis" x1="{:.1f}" y1="{y:.1f}" x2="{:.1f}" '
               'y2="{y:.1f}"/>'.format(pad_l, pad_l + plot_w, y=pad_t + plot_h))
    # Hidden with a class, not the `hidden` attribute: SVG elements ignore it,
    # which leaves the crosshair parked on the axis at x=0.
    out.append('<line class="cross" id="cross-{}" x1="0" y1="{:.1f}" x2="0" '
               'y2="{:.1f}"/>'.format(cid, pad_t - 4, pad_t + plot_h))

    points = {}
    for i, (label, values) in enumerate(marks):
        for si, v in values:
            points.setdefault(si, []).append((i, v, label))
    for si in sorted(points):
        pts = points[si]
        d = " ".join("{:.1f},{:.1f}".format(x_of(i), y_of(v)) for i, v, _ in pts)
        out.append('<polyline class="line" points="{}" stroke="var(--series-{})"/>'
                   .format(d, si + 1))
        li, lv, _ = pts[-1]
        out.append('<circle class="dot" cx="{:.1f}" cy="{:.1f}" r="4.5" '
                   'fill="var(--series-{})"/>'.format(x_of(li), y_of(lv), si + 1))
        if ns <= 4:
            out.append('<text class="val" x="{:.1f}" y="{:.1f}">{}</text>'.format(
                x_of(li) + 12, y_of(lv) + 4, esc(fmt(lv))))

    payload = {"x": [x_of(i) for i in range(len(marks))],
               "labels": labels,
               "series": [{"name": names[si] if si < len(names) else "",
                           "slot": si + 1,
                           "values": {str(i): fmt_exact(v) for i, v, _ in points[si]}}
                          for si in sorted(points)]}
    out.append('<rect class="overlay" x="{:.1f}" y="{:.1f}" width="{:.1f}" '
               'height="{:.1f}" tabindex="0" data-cid="{}" data-points="{}"/>'.format(
                   pad_l, pad_t, plot_w, plot_h, cid,
                   esc(json.dumps(payload, separators=(",", ":")))))
    out.append("</svg>")

    note = "Ordered by period; the table keeps the result's own order."
    if ticks[0] > 0:
        # A line whose axis starts above zero exaggerates every movement on it.
        # That is sometimes the readable choice, but it is never the reader's
        # default assumption, so the chart says so rather than letting the tick
        # labels carry it alone.
        note += (" The value axis starts at %s, not zero, so the movement is "
                 "magnified." % fmt_full(ticks[0]))
    return "".join(out), note


def stat_tile(res, spec, marks):
    """One row, one number. A one-bar bar chart is not a chart."""
    label, values = marks[0]
    v = values[0][1]
    return ('<div class="tile"><div class="tile-label">{}</div>'
            '<div class="tile-value">{}</div>'
            '<div class="tile-sub">{}</div></div>').format(
        esc(res["columns"][spec["value"]]), esc(fmt_compact(v)), esc(label))


def legend(names):
    """Two or more series always get a legend; one never does.

    A box with a single swatch restates the title and costs space — but with
    two, identity must never rest on colour alone.
    """
    if len(names) < 2:
        return ""
    items = "".join(
        '<span class="key"><span class="swatch" style="background:var(--series-{})">'
        '</span>{}</span>'.format(i + 1, esc(n)) for i, n in enumerate(names))
    return '<div class="legend">{}</div>'.format(items)


# ---------------------------------------------------------------- figures

def notes_block(res):
    """Caveats, exclusions and confidence, above the numbers they qualify.

    There is deliberately no code path here that renders a figure without
    them — the same property the web UI and the CSV/JSON exports hold.
    """
    out = []
    for c in res["caveats"]:
        out.append('<li class="warn">{}</li>'.format(esc(c)))
    for x in res["excluded"]:
        out.append('<li class="warn">Excluded: <b>{}</b> — {}</li>'.format(
            esc(x.get("city", "")), esc(x.get("reason", ""))))
    if res["not_a_comparison"]:
        out.append('<li class="warn">Only one city qualified, so this is not '
                   'a comparison.</li>')
    if res["truncated"]:
        out.append('<li class="warn">The row cap was hit, so these rows are a '
                   'prefix of the result.</li>')

    c = res.get("confidence")
    if isinstance(c, dict) and c.get("assessed"):
        band = str(c.get("band", ""))
        out.append('<li class="conf conf-{}">Confidence <b>{}%</b> ({}) — the '
                   'share of records this query reads that are present and '
                   'usable.</li>'.format(esc(band), esc(c.get("score", 0)), esc(band)))
        if isinstance(c.get("coverage"), int) and c["coverage"] < 100:
            out.append('<li class="conf">{}% of checks could be run; the score '
                       'covers only those.</li>'.format(esc(c["coverage"])))
        for s in c.get("signals") or []:
            if s.get("level") in ("warn", "fail"):
                out.append('<li class="warn">{}</li>'.format(esc(s.get("label", ""))))
            elif s.get("level") == "unknown":
                out.append('<li class="unknown">Not checked: {}</li>'.format(
                    esc(s.get("label", ""))))
        for lim in c.get("limits") or []:
            out.append('<li class="unknown">{}</li>'.format(esc(lim)))

    if not out:
        return ""
    return '<ul class="notes">{}</ul>'.format("".join(out))


def table_view(res):
    """The table view: every figure's relief, and its accessibility fallback."""
    cols, rows = res["columns"], res["rows"]
    head = "".join("<th>{}</th>".format(esc(c)) for c in cols)
    # One format per column, at full width: a column of right-aligned numbers
    # only lines up if they all carry the same decimals, and a table is where
    # a reader comes for the number the chart abbreviated.
    fmts = {}
    for i in range(len(cols)):
        vals = [float(r[i]) for r in rows
                if i < len(r) and is_number(r[i])]
        if vals:
            fmts[i] = value_formatter(vals, compact=False)
    body = []
    for r in rows[:TABLE_ROW_CAP]:
        cells = []
        for i in range(len(cols)):
            v = r[i] if i < len(r) else None
            if v is None:
                cells.append('<td class="null">—</td>')
            elif is_number(v):
                cells.append('<td class="num">{}</td>'.format(
                    esc(fmts.get(i, fmt_full)(float(v)))))
            else:
                cells.append("<td>{}</td>".format(esc(v)))
        body.append("<tr>{}</tr>".format("".join(cells)))
    more = ""
    if len(rows) > TABLE_ROW_CAP:
        more = '<p class="more">Showing {:,} of {:,} rows.</p>'.format(
            TABLE_ROW_CAP, len(rows))
    return ('<details class="table-view"><summary>Table view ({:,} row{})</summary>'
            '<div class="scroll"><table><thead><tr>{}</tr></thead><tbody>{}</tbody>'
            '</table></div>{}</details>').format(
        len(rows), "" if len(rows) == 1 else "s", head, "".join(body), more)


def figure(res, opts, cid):
    """One result: what qualifies it, the chart if it earns one, the table."""
    head = ['<section class="fig">']
    head.append("<h2>{}</h2>".format(esc(res["title"])))
    sub = res["desc"] or ""
    origin = " · ".join(p for p in (res["mode"], res["query"], res["source"]) if p)
    if sub:
        head.append('<p class="desc">{}</p>'.format(esc(sub)))
    head.append('<p class="origin">{}</p>'.format(esc(origin)))
    head.append(notes_block(res))

    try:
        spec, reason = pick_spec(res, opts)
    except ValueError as e:
        spec, reason = None, str(e)

    if spec is None:
        head.append('<p class="nochart">No chart: {}.</p>'.format(esc(reason)))
        head.append(table_view(res))
        head.append("</section>")
        return "".join(head)

    marks, total = build_marks(res, spec, opts.top)
    if not marks:
        head.append('<p class="nochart">No chart: every value in {} is '
                    'null.</p>'.format(esc(res["columns"][spec["value"]])))
        head.append(table_view(res))
        head.append("</section>")
        return "".join(head)

    names = spec["series_names"] or [res["columns"][spec["value"]]]
    labels = [l for l, _ in marks]
    ns = max(1, len(spec["series_names"]))

    if len(marks) == 1 and ns == 1 and len(marks[0][1]) == 1:
        body, note = stat_tile(res, spec, marks), ""
    elif is_temporal(labels):
        body, note = line_chart(res, spec, marks, cid)
    else:
        body = bar_chart(res, spec, marks)
        note = ""
        if total > len(marks):
            note = ("First {:,} of {:,} rows, in the result's own order."
                    .format(len(marks), total))

    head.append('<p class="measure">Measure: <b>{}</b>{}</p>'.format(
        esc(res["columns"][spec["value"]]),
        "" if spec["series"] < 0 else
        " · series: <b>%s</b>" % esc(res["columns"][spec["series"]])))
    head.append(legend(names))
    head.append('<div class="plot">{}</div>'.format(body))
    if note:
        head.append('<p class="note">{}</p>'.format(esc(note)))
    head.append(table_view(res))
    head.append("</section>")
    return "".join(head)


# ------------------------------------------------------------------- page

CSS = """
:root {
  color-scheme: light;
  --plane:#f9f9f7; --surface-1:#fcfcfb; --text-primary:#0b0b0b;
  --text-secondary:#52514e; --muted:#898781; --grid:#e1e0d9; --axis:#c3c2b7;
  --border:rgba(11,11,11,0.10); --warn:#d03b3b; --good:#0ca30c;
  /*SERIES-LIGHT*/
}
@media (prefers-color-scheme: dark) {
  :root:where(:not([data-theme="light"])) {
    color-scheme: dark;
    --plane:#0d0d0d; --surface-1:#1a1a19; --text-primary:#ffffff;
    --text-secondary:#c3c2b7; --muted:#898781; --grid:#2c2c2a; --axis:#383835;
    --border:rgba(255,255,255,0.10); --warn:#e66767; --good:#0ca30c;
    /*SERIES-DARK*/
  }
}
:root[data-theme="dark"] {
  color-scheme: dark;
  --plane:#0d0d0d; --surface-1:#1a1a19; --text-primary:#ffffff;
  --text-secondary:#c3c2b7; --muted:#898781; --grid:#2c2c2a; --axis:#383835;
  --border:rgba(255,255,255,0.10); --warn:#e66767; --good:#0ca30c;
  --series-1:#3987e5; --series-2:#d95926; --series-3:#199e70; --series-4:#c98500;
  --series-5:#d55181; --series-6:#008300; --series-7:#9085e9; --series-8:#e66767;
}
* { box-sizing: border-box; }
body { margin:0; background:var(--plane); color:var(--text-primary);
  font-family: system-ui, -apple-system, "Segoe UI", sans-serif; font-size:15px;
  line-height:1.5; }
.page { max-width: 1040px; margin:0 auto; padding: 40px 24px 80px; }
header h1 { font-size: 24px; margin:0 0 4px; }
header p { color: var(--text-secondary); margin:0 0 28px; }
.fig { background:var(--surface-1); border:1px solid var(--border);
  border-radius:10px; padding:22px 24px 18px; margin-bottom:22px; }
.fig h2 { font-size:17px; margin:0 0 4px; }
.desc { color:var(--text-secondary); margin:0 0 6px; }
.origin, .measure, .note, .more { color:var(--muted); font-size:12.5px; margin:0 0 10px; }
.measure b { color:var(--text-secondary); font-weight:600; }
.note { margin:8px 0 0; }
.notes { list-style:none; padding:0; margin:0 0 14px; font-size:13px; }
.notes li { padding:5px 10px; border-left:2px solid var(--axis);
  background:var(--plane); margin-bottom:4px; color:var(--text-secondary); }
.notes li.warn { border-left-color:var(--warn); }
.notes li.conf { border-left-color:var(--good); }
.notes li.conf-low, .notes li.conf-insufficient { border-left-color:var(--warn); }
.nochart { color:var(--text-secondary); font-size:13.5px; padding:10px 12px;
  background:var(--plane); border-radius:6px; margin:0 0 12px; }
.legend { display:flex; flex-wrap:wrap; gap:14px; margin:0 0 10px; font-size:12.5px;
  color:var(--text-secondary); }
.key { display:inline-flex; align-items:center; gap:6px; }
.swatch { width:10px; height:10px; border-radius:2px; display:inline-block; }
.plot { overflow-x:auto; }
svg.chart { width:100%; height:auto; min-width:520px; display:block; }
.grid { stroke:var(--grid); stroke-width:1; }
.axis { stroke:var(--axis); stroke-width:1; }
.cross { stroke:var(--axis); stroke-width:1; visibility:hidden; }
.cross.on { visibility:visible; }
.bar { stroke:none; }
.line { fill:none; stroke-width:2; stroke-linejoin:round; stroke-linecap:round; }
.dot { stroke:var(--surface-1); stroke-width:2; }
text { font-family: inherit; }
.tick { fill:var(--muted); font-size:11px; font-variant-numeric: tabular-nums; }
.cat { fill:var(--text-secondary); font-size:12px; }
.val { fill:var(--text-secondary); font-size:11.5px; font-variant-numeric: tabular-nums; }
.hit, .overlay { fill:transparent; outline:none; }
.hit:hover, .hit:focus { fill:var(--text-primary); fill-opacity:0.05; }
.overlay:focus { stroke:var(--axis); }
.table-view { margin-top:12px; border-top:1px solid var(--border); padding-top:10px; }
.table-view summary { cursor:pointer; color:var(--text-secondary); font-size:13px; }
.scroll { overflow-x:auto; margin-top:10px; }
table { border-collapse:collapse; font-size:13px; width:100%; }
th, td { text-align:left; padding:5px 12px 5px 0; border-bottom:1px solid var(--grid);
  white-space:nowrap; }
th { color:var(--muted); font-weight:600; }
td.num { text-align:right; font-variant-numeric: tabular-nums; }
td.null { color:var(--muted); }
.tile { padding:8px 0 14px; }
.tile-label { color:var(--muted); font-size:13px; }
.tile-value { font-size:48px; font-weight:600; line-height:1.1; }
.tile-sub { color:var(--text-secondary); font-size:14px; }
#tip { position:fixed; z-index:9; pointer-events:none; background:var(--surface-1);
  color:var(--text-primary); border:1px solid var(--border); border-radius:8px;
  padding:8px 10px; font-size:12.5px; box-shadow:0 6px 20px rgba(0,0,0,0.14);
  max-width:280px; }
#tip[hidden] { display:none; }
#tip .row { display:flex; align-items:baseline; gap:8px; }
#tip .k { width:14px; height:2px; border-radius:1px; flex:none; }
#tip .v { font-weight:600; font-variant-numeric: tabular-nums; }
#tip .n { color:var(--text-secondary); }
#tip .head { color:var(--text-secondary); margin-bottom:4px; }
@media print { .table-view { display:block; } .table-view summary { display:none; } }
"""

JS = r"""
(function () {
  var tip = document.getElementById('tip');

  // Labels come from query output, so they are untrusted text: every insertion
  // below goes through textContent, never innerHTML.
  function row(slot, name, value) {
    var r = document.createElement('div'); r.className = 'row';
    if (slot) {
      var k = document.createElement('span'); k.className = 'k';
      k.style.background = 'var(--series-' + slot + ')'; r.appendChild(k);
    }
    var v = document.createElement('span'); v.className = 'v';
    v.textContent = value; r.appendChild(v);
    if (name) {
      var n = document.createElement('span'); n.className = 'n';
      n.textContent = name; r.appendChild(n);
    }
    return r;
  }
  function head(text) {
    var h = document.createElement('div'); h.className = 'head';
    h.textContent = text; return h;
  }
  function show(node, x, y) {
    tip.textContent = ''; tip.appendChild(node); tip.hidden = false;
    var b = tip.getBoundingClientRect();
    var left = Math.min(x + 14, window.innerWidth - b.width - 8);
    var top = Math.max(8, Math.min(y - b.height - 12, window.innerHeight - b.height - 8));
    tip.style.left = left + 'px'; tip.style.top = top + 'px';
  }
  function hide() { tip.hidden = true; }

  function barTip(el, x, y) {
    var frag = document.createDocumentFragment();
    frag.appendChild(head(el.getAttribute('data-label')));
    frag.appendChild(row(el.getAttribute('data-slot'),
                         el.getAttribute('data-series'),
                         el.getAttribute('data-value')));
    show(frag, x, y);
  }

  function lineTip(el, clientX, clientY) {
    var data = JSON.parse(el.getAttribute('data-points'));
    var svg = el.ownerSVGElement, box = svg.getBoundingClientRect();
    var scale = box.width / svg.viewBox.baseVal.width;
    var local = (clientX - box.left) / scale;
    var best = 0, bestD = Infinity;
    for (var i = 0; i < data.x.length; i++) {
      var d = Math.abs(data.x[i] - local);
      if (d < bestD) { bestD = d; best = i; }
    }
    var cross = document.getElementById('cross-' + el.getAttribute('data-cid'));
    if (cross) {
      cross.setAttribute('x1', data.x[best]); cross.setAttribute('x2', data.x[best]);
      cross.classList.add('on');
    }
    var frag = document.createDocumentFragment();
    frag.appendChild(head(data.labels[best]));
    // One tooltip lists every series at that x, so the pointer never has to
    // land on a line to get a value.
    for (var s = 0; s < data.series.length; s++) {
      var ser = data.series[s], v = ser.values[String(best)];
      if (v !== undefined) frag.appendChild(row(ser.slot, ser.name, v));
    }
    show(frag, clientX, clientY);
  }

  function hideCross(el) {
    var cross = document.getElementById('cross-' + el.getAttribute('data-cid'));
    if (cross) cross.classList.remove('on');
  }

  document.addEventListener('pointermove', function (e) {
    var hit = e.target.closest ? e.target.closest('.hit, .overlay') : null;
    if (!hit) { hide(); return; }
    if (hit.classList.contains('overlay')) lineTip(hit, e.clientX, e.clientY);
    else barTip(hit, e.clientX, e.clientY);
  });
  document.addEventListener('pointerleave', hide, true);
  document.addEventListener('focusin', function (e) {
    var el = e.target;
    if (!el.classList) return;
    var b = el.getBoundingClientRect();
    if (el.classList.contains('hit')) barTip(el, b.right, b.top + b.height);
    else if (el.classList.contains('overlay')) lineTip(el, b.right - 1, b.top + b.height);
  });
  document.addEventListener('focusout', function (e) {
    if (e.target.classList && e.target.classList.contains('overlay')) hideCross(e.target);
    hide();
  });
  document.addEventListener('pointerout', function (e) {
    if (e.target.classList && e.target.classList.contains('overlay')) hideCross(e.target);
  });
})();
"""


def _series_vars(colors):
    return " ".join("--series-%d:%s;" % (i + 1, c) for i, c in enumerate(colors))


# The slot lists above are the only place the palette is written down; the
# stylesheet is filled in from them, so a swapped palette cannot half-apply.
CSS = (CSS.replace("/*SERIES-LIGHT*/", _series_vars(SERIES_LIGHT))
          .replace("/*SERIES-DARK*/", _series_vars(SERIES_DARK)))


def page(figures, title, subtitle):
    return ("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n"
            "<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">\n"
            "<title>{title}</title>\n<style>{css}</style>\n</head>\n<body>\n"
            "<div class=\"page\">\n<header><h1>{title}</h1><p>{sub}</p></header>\n"
            "{figs}\n</div>\n<div id=\"tip\" hidden></div>\n<script>{js}</script>\n"
            "</body>\n</html>\n").format(
        title=esc(title), sub=esc(subtitle), css=CSS, js=JS,
        figs="\n".join(figures))


# ------------------------------------------------------------------- main

def main(argv=None):
    ap = argparse.ArgumentParser(
        prog="csq-graph.py",
        description="Turn csq JSON results into a self-contained HTML page of charts.",
        epilog="Accepts a csq mode result (columns/rows), an array of row objects "
               "from `csq query --format json`, an array of results, or "
               "{\"results\": [...]}.")
    ap.add_argument("inputs", nargs="+", metavar="FILE",
                    help="JSON files, or - for stdin")
    ap.add_argument("-o", "--output", default="csq-charts.html",
                    help="output HTML file (default: csq-charts.html)")
    ap.add_argument("--stdout", action="store_true",
                    help="write the HTML to stdout instead of a file")
    ap.add_argument("--open", dest="open_", action="store_true",
                    help="open the page in a browser when it is written")
    ap.add_argument("--title", default="csq charts", help="page title")
    ap.add_argument("--top", type=int, default=MAX_BARS, metavar="N",
                    help="rows to draw per chart (default: %d)" % MAX_BARS)
    ap.add_argument("--label", metavar="COL", help="column naming the marks")
    ap.add_argument("--value", metavar="COL", help="column sizing the marks")
    ap.add_argument("--series", metavar="COL",
                    help="column splitting the marks into series (default: city)")
    opts = ap.parse_args(argv)

    if opts.top < 1:
        ap.error("--top must be at least 1")

    try:
        documents = load_documents(opts.inputs)
    except (ValueError, OSError) as e:
        print("csq-graph: %s" % e, file=sys.stderr)
        return 2

    results = []
    for source, doc in documents:
        try:
            results.extend(normalise(doc, source))
        except ValueError as e:
            print("csq-graph: %s" % e, file=sys.stderr)
            return 2
    if not results:
        print("csq-graph: no results in the input", file=sys.stderr)
        return 2

    figs = [figure(r, opts, i) for i, r in enumerate(results)]
    sub = "{} result{} from {}".format(
        len(results), "" if len(results) == 1 else "s",
        ", ".join(sorted({r["source"] for r in results})))
    doc = page(figs, opts.title, sub)

    if opts.stdout:
        sys.stdout.write(doc)
        return 0

    with open(opts.output, "w", encoding="utf-8") as fh:
        fh.write(doc)
    print("csq-graph: wrote %s (%d figure%s)" % (
        opts.output, len(figs), "" if len(figs) == 1 else "s"), file=sys.stderr)
    if opts.open_:
        webbrowser.open("file://" + os.path.abspath(opts.output))
    return 0


if __name__ == "__main__":
    sys.exit(main())

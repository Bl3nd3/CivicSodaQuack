#!/usr/bin/env python3
# Copyright (c) 2026 Neomantra Corp
"""Fit a model to several variables at once, and say what it cannot conclude.

`csq-graph.py` draws one measure. This reads a result with several numeric
columns, fits an ordinary least squares model of one against the rest, and
renders the result as a self-contained HTML page: a correlation matrix, the
coefficients with their confidence intervals, observed against predicted, and
the residuals.

    csq query --db chicago.duckdb --format json "SELECT ..." \
      | csq-predict.py - --target crimes

The prediction machinery is the easy half. The hard half, and most of this
file, is refusing to let it overclaim:

  * A coefficient whose interval spans zero is reported as indistinguishable
    from zero, never as a small effect.
  * Collinear predictors are detected and named, because a multivariable model
    built on them produces individually meaningless coefficients while still
    reporting a confident R².
  * Counts over a shared geography are flagged for the size confound they
    almost always carry: ward crime, ward 311 calls and ward permits correlate
    because wards differ in size, and a model that reports that as a finding
    has discovered the population of Chicago.
  * The word "cause" appears nowhere in the output, and the limits render
    above the figures rather than below them.

Python 3.8+. Standard library only — the t and F distributions are computed
here rather than imported.
"""

import argparse
import html
import json
import math
import os
import sys
import webbrowser

MAX_PREDICTORS = 12
TABLE_ROW_CAP = 500

# The same validated palette csq-graph.py uses, in the same documented order.
SERIES_LIGHT = ["#2a78d6", "#eb6834", "#1baf7a", "#eda100",
                "#e87ba4", "#008300", "#4a3aa7", "#e34948"]
SERIES_DARK = ["#3987e5", "#d95926", "#199e70", "#c98500",
               "#d55181", "#008300", "#9085e9", "#e66767"]

# Correlation is a polarity, so the heatmap is diverging: two hues that read as
# opposite, with a neutral midpoint that reads as "nothing". Blue<->red, never
# a rainbow and never a hue in the middle.
DIVERGE = {
    "light": {"neg": "#2a78d6", "mid": "#f0efec", "pos": "#e34948"},
    "dark": {"neg": "#3987e5", "mid": "#383835", "pos": "#e66767"},
}


# ---------------------------------------------------------------- loading

def load_documents(paths):
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
    """Accept the same JSON shapes csq emits, as csq-graph.py does."""
    if isinstance(doc, dict) and isinstance(doc.get("results"), list):
        out = []
        for item in doc["results"]:
            out.extend(normalise(item, source))
        return out
    if isinstance(doc, dict) and "columns" in doc and "rows" in doc:
        return [{
            "source": source,
            "title": doc.get("title") or doc.get("query") or source,
            "desc": doc.get("desc") or "",
            "mode": doc.get("mode") or "", "query": doc.get("query") or "",
            "columns": list(doc.get("columns") or []),
            "rows": [list(r) for r in (doc.get("rows") or [])],
            "caveats": list(doc.get("caveats") or []),
            "excluded": list(doc.get("excluded") or []),
            "truncated": bool(doc.get("truncated")),
            "confidence": doc.get("confidence"),
        }]
    if isinstance(doc, list):
        if doc and all(isinstance(d, dict) and "columns" in d and "rows" in d
                       for d in doc):
            out = []
            for item in doc:
                out.extend(normalise(item, source))
            return out
        cols = []
        for rec in doc:
            if not isinstance(rec, dict):
                raise ValueError("%s: expected an array of objects" % source)
            for k in rec:
                if k not in cols:
                    cols.append(k)
        return [{
            "source": source, "title": os.path.splitext(source)[0], "desc": "",
            "mode": "", "query": "", "columns": cols,
            "rows": [[rec.get(c) for c in cols] for rec in doc],
            "caveats": [], "excluded": [], "truncated": False,
            "confidence": None,
        }]
    raise ValueError("%s: unrecognised JSON — expected a csq result object "
                     "(columns/rows) or an array of row objects" % source)


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
    seen = False
    for r in rows:
        if i >= len(r) or r[i] is None:
            continue
        if not isinstance(r[i], str):
            return False
        seen = True
    return seen


# ------------------------------------------------------- linear algebra

def mat_t(a):
    return [[a[i][j] for i in range(len(a))] for j in range(len(a[0]))]


def mat_mul(a, b):
    n, m, p = len(a), len(b), len(b[0])
    out = [[0.0] * p for _ in range(n)]
    for i in range(n):
        ai = a[i]
        for k in range(m):
            v = ai[k]
            if v == 0.0:
                continue
            bk = b[k]
            oi = out[i]
            for j in range(p):
                oi[j] += v * bk[j]
    return out


def mat_vec(a, v):
    return [sum(a[i][j] * v[j] for j in range(len(v))) for i in range(len(a))]


def inverse(a):
    """Gauss-Jordan with partial pivoting. None when the matrix is singular."""
    n = len(a)
    m = [list(a[i]) + [1.0 if i == j else 0.0 for j in range(n)]
         for i in range(n)]
    for col in range(n):
        piv = max(range(col, n), key=lambda r: abs(m[r][col]))
        if abs(m[piv][col]) < 1e-12:
            return None
        m[col], m[piv] = m[piv], m[col]
        pv = m[col][col]
        m[col] = [x / pv for x in m[col]]
        for r in range(n):
            if r == col:
                continue
            f = m[r][col]
            if f == 0.0:
                continue
            m[r] = [x - f * y for x, y in zip(m[r], m[col])]
    return [row[n:] for row in m]


# ------------------------------------------------------------ statistics

def mean(xs):
    return sum(xs) / float(len(xs))


def pearson(xs, ys):
    n = len(xs)
    if n < 2:
        return float("nan")
    mx, my = mean(xs), mean(ys)
    sxy = sum((x - mx) * (y - my) for x, y in zip(xs, ys))
    sxx = sum((x - mx) ** 2 for x in xs)
    syy = sum((y - my) ** 2 for y in ys)
    if sxx <= 0 or syy <= 0:
        return float("nan")
    return sxy / math.sqrt(sxx * syy)


def _betacf(a, b, x):
    maxit, eps, fpmin = 300, 3e-16, 1e-300
    qab, qap, qam = a + b, a + 1.0, a - 1.0
    c = 1.0
    d = 1.0 - qab * x / qap
    if abs(d) < fpmin:
        d = fpmin
    d = 1.0 / d
    h = d
    for mm in range(1, maxit + 1):
        m2 = 2 * mm
        aa = mm * (b - mm) * x / ((qam + m2) * (a + m2))
        d = 1.0 + aa * d
        if abs(d) < fpmin:
            d = fpmin
        c = 1.0 + aa / c
        if abs(c) < fpmin:
            c = fpmin
        d = 1.0 / d
        h *= d * c
        aa = -(a + mm) * (qab + mm) * x / ((a + m2) * (qap + m2))
        d = 1.0 + aa * d
        if abs(d) < fpmin:
            d = fpmin
        c = 1.0 + aa / c
        if abs(c) < fpmin:
            c = fpmin
        d = 1.0 / d
        de = d * c
        h *= de
        if abs(de - 1.0) < eps:
            break
    return h


def betai(a, b, x):
    """Regularised incomplete beta — the one special function this needs."""
    if x <= 0.0:
        return 0.0
    if x >= 1.0:
        return 1.0
    lb = math.lgamma(a + b) - math.lgamma(a) - math.lgamma(b)
    bt = math.exp(lb + a * math.log(x) + b * math.log(1.0 - x))
    if x < (a + 1.0) / (a + b + 2.0):
        return bt * _betacf(a, b, x) / a
    return 1.0 - bt * _betacf(b, a, 1.0 - x) / b


def t_two_sided_p(t, df):
    if df <= 0 or not math.isfinite(t):
        return float("nan")
    return betai(df / 2.0, 0.5, df / (df + t * t))


def f_p(f, d1, d2):
    if d1 <= 0 or d2 <= 0 or not math.isfinite(f) or f <= 0:
        return float("nan")
    return betai(d2 / 2.0, d1 / 2.0, d2 / (d2 + d1 * f))


def t_critical(df, alpha=0.05):
    """Two-sided critical value, by bisection on the p-value."""
    if df <= 0:
        return float("nan")
    lo, hi = 0.0, 1000.0
    for _ in range(200):
        mid = (lo + hi) / 2.0
        if t_two_sided_p(mid, df) > alpha:
            lo = mid
        else:
            hi = mid
    return (lo + hi) / 2.0


def ols(x_rows, y):
    """Least squares with an intercept, plus everything needed to doubt it."""
    n = len(y)
    k = len(x_rows[0])
    design = [[1.0] + list(r) for r in x_rows]
    xt = mat_t(design)
    xtx = mat_mul(xt, design)
    inv = inverse(xtx)
    if inv is None:
        return None
    beta = mat_vec(inv, mat_vec(xt, y))

    fitted = [sum(b * v for b, v in zip(beta, row)) for row in design]
    resid = [a - b for a, b in zip(y, fitted)]
    ybar = mean(y)
    rss = sum(r * r for r in resid)
    tss = sum((v - ybar) ** 2 for v in y)
    df = n - k - 1
    if df <= 0 or tss <= 0:
        return None

    r2 = 1.0 - rss / tss
    adj = 1.0 - (1.0 - r2) * (n - 1) / float(df)
    sigma2 = rss / df
    se = [math.sqrt(max(0.0, sigma2 * inv[i][i])) for i in range(k + 1)]
    tcrit = t_critical(df)

    coefs = []
    for i in range(k + 1):
        t = beta[i] / se[i] if se[i] > 0 else float("nan")
        coefs.append({
            "beta": beta[i], "se": se[i], "t": t,
            "p": t_two_sided_p(t, df),
            "lo": beta[i] - tcrit * se[i],
            "hi": beta[i] + tcrit * se[i],
        })

    # An exact fit leaves no residual variance to divide by. It is reachable
    # with integer data — a rate against its own numerator, a total against
    # its parts — which is precisely the case this tool is meant to catch, so
    # crashing on it would be the worst possible response. F is infinite, its
    # p-value undefined, and the R² > 0.999 notice does the real talking.
    if sigma2 == 0:
        fstat = float("inf")
    else:
        fstat = ((tss - rss) / k) / sigma2 if k > 0 else float("nan")
    return {
        "n": n, "k": k, "df": df, "r2": r2, "adj_r2": adj,
        "sigma": math.sqrt(sigma2), "coefs": coefs,
        "fitted": fitted, "resid": resid,
        "f": fstat, "f_p": f_p(fstat, k, df),
    }


def vif(x_rows, names):
    """Variance inflation: how far each predictor is explained by the others.

    This is the check that decides whether individual coefficients mean
    anything at all, so it is computed rather than assumed.
    """
    out = []
    k = len(names)
    for j in range(k):
        if k < 2:
            out.append(1.0)
            continue
        y = [r[j] for r in x_rows]
        others = [[r[i] for i in range(k) if i != j] for r in x_rows]
        m = ols(others, y)
        if m is None or m["r2"] >= 1.0:
            out.append(float("inf"))
        else:
            out.append(1.0 / max(1e-12, 1.0 - m["r2"]))
    return out


def looks_like_counts(values):
    """Non-negative whole numbers — the shape a GROUP BY count arrives in."""
    seen = False
    for v in values:
        if v is None:
            continue
        if v < 0 or abs(v - round(v)) > 1e-9:
            return False
        seen = True
    return seen


# --------------------------------------------------------------- drawing

def esc(s):
    return html.escape(str(s), quote=True)


def fmt(v, dec=None):
    if v is None or (isinstance(v, float) and not math.isfinite(v)):
        return "—"
    if dec is not None:
        return "{:,.{d}f}".format(v, d=dec)
    a = abs(v)
    if a >= 1e6:
        return "{:,.0f}".format(v)
    if a >= 100:
        return "{:,.1f}".format(v)
    if a >= 1:
        return "{:,.2f}".format(v)
    if a == 0:
        return "0"
    # Coefficients on a large-count predictor are genuinely tiny. Rounding
    # them to "0.000" hides both the size and the sign, and prints "-0.000",
    # which reads as a bug rather than a number.
    if a < 0.001:
        return "{:.1e}".format(v)
    return "{:,.3f}".format(v)


def fmt_p(p):
    if p is None or not math.isfinite(p):
        return "—"
    if p < 0.001:
        return "< 0.001"
    return "{:.3f}".format(p)


def nice_ticks(lo, hi, count=4):
    """Ticks that always span the data, first at or below, last at or above."""
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
    while True:
        ticks.append(round(v, 10))
        if v >= hi - 1e-9:
            return ticks
        v += step


def tick_text(v):
    a = abs(v)
    for cut, suf in ((1e9, "B"), (1e6, "M"), (1e3, "K")):
        if a >= cut:
            return ("{:.1f}{}".format(v / cut, suf)).replace(".0" + suf, suf)
    if a < 1 and a > 0:
        return "{:.2f}".format(v)
    return "{:,.0f}".format(v)


def mix(c1, c2, t):
    """Blend two hex colours. Used only for the diverging correlation ramp."""
    t = max(0.0, min(1.0, t))
    a = [int(c1[i:i + 2], 16) for i in (1, 3, 5)]
    b = [int(c2[i:i + 2], 16) for i in (1, 3, 5)]
    return "#%02x%02x%02x" % tuple(
        int(round(a[i] + (b[i] - a[i]) * t)) for i in range(3))


def ink_on(hexcol):
    r, g, b = [int(hexcol[i:i + 2], 16) / 255.0 for i in (1, 3, 5)]
    lum = 0.2126 * r + 0.7152 * g + 0.0722 * b
    return "#0b0b0b" if lum > 0.55 else "#ffffff"


def heatmap(names, corr):
    """Correlation matrix.

    Diverging, because correlation has a sign and a meaningful zero: two hues
    that read as opposite with a neutral midpoint, never a rainbow and never a
    hue in the middle. Every cell carries its number as well as its colour, so
    the value never rests on hue alone.
    """
    n = len(names)
    cell = 44.0
    longest = max(len(x) for x in names)
    gut = max(86.0, longest * 6.4 + 10)
    # A -45 degree label runs up and to the right of its column, so the box
    # needs room above it and beyond the last one, or the final name is cut
    # off by the edge of the SVG.
    overhang = longest * 6.4 * 0.71 + 12
    top = overhang + 14
    w = gut + n * cell + overhang
    h = top + n * cell + 12

    out = ['<svg class="chart" viewBox="0 0 {:.0f} {:.0f}" role="img" '
           'style="max-width:{:.0f}px" preserveAspectRatio="xMinYMin meet" '
           'aria-label="Correlation matrix">'.format(w, h, w)]
    for j, nm in enumerate(names):
        x = gut + j * cell + cell / 2
        out.append('<text class="tick rot" x="{:.1f}" y="{:.1f}" '
                   'transform="rotate(-45 {:.1f} {:.1f})" '
                   'text-anchor="start">{}</text>'.format(
                       x, top - 10, x, top - 10, esc(nm)))
    for i, nm in enumerate(names):
        y = top + i * cell + cell / 2 + 4
        out.append('<text class="cat" x="{:.1f}" y="{:.1f}" '
                   'text-anchor="end">{}</text>'.format(gut - 10, y, esc(nm)))
        for j in range(n):
            r = corr[i][j]
            x = gut + j * cell
            yy = top + i * cell
            if r is None or not math.isfinite(r):
                light = dark = "#00000000"
                txt = "—"
                ink_l = ink_d = "var(--muted)"
            else:
                t = abs(r)
                pole = "pos" if r >= 0 else "neg"
                light = mix(DIVERGE["light"]["mid"], DIVERGE["light"][pole], t)
                dark = mix(DIVERGE["dark"]["mid"], DIVERGE["dark"][pole], t)
                txt = "{:.2f}".format(r)
                ink_l, ink_d = ink_on(light), ink_on(dark)
            # 2px surface gap between cells: white does the separating, never
            # a stroke drawn round each one.
            out.append('<rect class="cell" x="{:.1f}" y="{:.1f}" width="{:.1f}" '
                       'height="{:.1f}" rx="3" style="--l:{};--d:{}"/>'.format(
                           x + 1, yy + 1, cell - 2, cell - 2, light, dark))
            out.append('<text class="cellv" x="{:.1f}" y="{:.1f}" '
                       'text-anchor="middle" style="--l:{};--d:{}">{}</text>'
                       .format(x + cell / 2, yy + cell / 2 + 4,
                               ink_l, ink_d, esc(txt)))
            out.append('<rect class="hit" x="{:.1f}" y="{:.1f}" width="{:.1f}" '
                       'height="{:.1f}" tabindex="0" data-label="{}" '
                       'data-series="" data-slot="0" data-value="r = {}"/>'
                       .format(x, yy, cell, cell,
                               esc("%s vs %s" % (names[i], names[j])), esc(txt)))
    out.append("</svg>")
    return "".join(out)


def coef_plot(names, std_betas, los, his, crosses):
    """Standardised coefficients with their 95% intervals.

    Standardised so predictors in different units are comparable at a glance;
    the raw coefficients are in the table, where their units can be stated. An
    interval crossing zero is drawn muted and labelled, because the honest
    reading of it is "cannot be told apart from no effect", not "small".
    """
    n = len(names)
    w = 820.0
    gut = max(96.0, max(len(x) for x in names) * 6.6 + 14)
    pad_r, pad_t, row = 120.0, 34.0, 34.0
    h = pad_t + n * row + 18

    lo = min(list(los) + [0.0])
    hi = max(list(his) + [0.0])
    pad = (hi - lo) * 0.08 or 1.0
    ticks = nice_ticks(lo - pad, hi + pad)
    span = ticks[-1] - ticks[0] or 1.0
    plot_w = w - gut - pad_r

    def x_of(v):
        return gut + (v - ticks[0]) / span * plot_w

    out = ['<svg class="chart" viewBox="0 0 {:.0f} {:.0f}" role="img" '
           'style="max-width:{:.0f}px" preserveAspectRatio="xMinYMin meet" '
           'aria-label="Coefficients with 95% intervals">'.format(w, h, w)]
    for t in ticks:
        out.append('<line class="grid" x1="{x:.1f}" y1="{:.1f}" x2="{x:.1f}" '
                   'y2="{:.1f}"/>'.format(pad_t - 10, h - 14, x=x_of(t)))
        out.append('<text class="tick" x="{:.1f}" y="{:.1f}" '
                   'text-anchor="middle">{}</text>'.format(
                       x_of(t), pad_t - 16, esc(fmt(t, 2))))
    out.append('<line class="zero" x1="{x:.1f}" y1="{:.1f}" x2="{x:.1f}" '
               'y2="{:.1f}"/>'.format(pad_t - 10, h - 14, x=x_of(0.0)))

    for i, nm in enumerate(names):
        y = pad_t + i * row + row / 2
        cls = "null" if crosses[i] else "live"
        out.append('<text class="cat" x="{:.1f}" y="{:.1f}" text-anchor="end">'
                   '{}</text>'.format(gut - 12, y + 4, esc(nm)))
        out.append('<line class="ci {}" x1="{:.1f}" y1="{y:.1f}" x2="{:.1f}" '
                   'y2="{y:.1f}"/>'.format(cls, x_of(los[i]), x_of(his[i]), y=y))
        out.append('<circle class="pt {}" cx="{:.1f}" cy="{:.1f}" r="5"/>'
                   .format(cls, x_of(std_betas[i]), y))
        note = " (spans 0)" if crosses[i] else ""
        out.append('<text class="val" x="{:.1f}" y="{:.1f}">{}{}</text>'.format(
            w - pad_r + 10, y + 4, esc(fmt(std_betas[i], 2)), esc(note)))
        out.append('<rect class="hit" x="{:.1f}" y="{:.1f}" width="{:.1f}" '
                   'height="{:.1f}" tabindex="0" data-label="{}" '
                   'data-series="standardised, 95% CI" data-slot="1" '
                   'data-value="{} [{}, {}]"/>'.format(
                       gut, y - row / 2, plot_w, row, esc(nm),
                       esc(fmt(std_betas[i], 3)), esc(fmt(los[i], 3)),
                       esc(fmt(his[i], 3))))
    out.append("</svg>")
    return "".join(out)


def scatter(xs, ys, labels, x_title, y_title, diagonal=False, zero=False):
    """A scatter of two measures, with an optional reference line."""
    w, h = 820.0, 340.0
    pad_l, pad_r, pad_t, pad_b = 78.0, 24.0, 22.0, 46.0
    plot_w, plot_h = w - pad_l - pad_r, h - pad_t - pad_b

    xt = nice_ticks(min(xs), max(xs))
    if diagonal:
        both = list(xs) + list(ys)
        xt = nice_ticks(min(both), max(both))
        yt = xt
    else:
        yt = nice_ticks(min(list(ys) + ([0.0] if zero else [])),
                        max(list(ys) + ([0.0] if zero else [])))
    xs_span = (xt[-1] - xt[0]) or 1.0
    ys_span = (yt[-1] - yt[0]) or 1.0

    def px(v):
        return pad_l + (v - xt[0]) / xs_span * plot_w

    def py(v):
        return pad_t + plot_h - (v - yt[0]) / ys_span * plot_h

    out = ['<svg class="chart" viewBox="0 0 {:.0f} {:.0f}" role="img" '
           'style="max-width:{:.0f}px" preserveAspectRatio="xMinYMin meet" '
           'aria-label="{} against {}">'
           .format(w, h, w, esc(y_title), esc(x_title))]
    for t in yt:
        out.append('<line class="grid" x1="{:.1f}" y1="{y:.1f}" x2="{:.1f}" '
                   'y2="{y:.1f}"/>'.format(pad_l, pad_l + plot_w, y=py(t)))
        out.append('<text class="tick" x="{:.1f}" y="{:.1f}" text-anchor="end">'
                   '{}</text>'.format(pad_l - 10, py(t) + 4, esc(tick_text(t))))
    for t in xt:
        out.append('<text class="tick" x="{:.1f}" y="{:.1f}" '
                   'text-anchor="middle">{}</text>'.format(
                       px(t), h - pad_b + 20, esc(tick_text(t))))
    if diagonal:
        out.append('<line class="ref" x1="{:.1f}" y1="{:.1f}" x2="{:.1f}" '
                   'y2="{:.1f}"/>'.format(px(xt[0]), py(xt[0]),
                                          px(xt[-1]), py(xt[-1])))
    if zero:
        out.append('<line class="zero" x1="{:.1f}" y1="{y:.1f}" x2="{:.1f}" '
                   'y2="{y:.1f}"/>'.format(pad_l, pad_l + plot_w, y=py(0.0)))

    for i in range(len(xs)):
        out.append('<circle class="pt live" cx="{:.1f}" cy="{:.1f}" r="4.5"/>'
                   .format(px(xs[i]), py(ys[i])))
        # The hit area is far bigger than the 9px mark: a pinpoint nobody can
        # land on is a tooltip nobody sees.
        out.append('<rect class="hit" x="{:.1f}" y="{:.1f}" width="24" '
                   'height="24" tabindex="0" data-label="{}" data-series="{}" '
                   'data-slot="1" data-value="{} / {}"/>'.format(
                       px(xs[i]) - 12, py(ys[i]) - 12,
                       esc(labels[i] if i < len(labels) else ""),
                       esc("%s, %s" % (x_title, y_title)),
                       esc(fmt(xs[i])), esc(fmt(ys[i]))))

    out.append('<text class="axis-title" x="{:.1f}" y="{:.1f}" '
               'text-anchor="middle">{}</text>'.format(
                   pad_l + plot_w / 2, h - 6, esc(x_title)))
    out.append('<text class="axis-title" x="14" y="{:.1f}" '
               'transform="rotate(-90 14 {:.1f})" text-anchor="middle">{}'
               '</text>'.format(pad_t + plot_h / 2, pad_t + plot_h / 2,
                                esc(y_title)))
    out.append("</svg>")
    return "".join(out)


# ----------------------------------------------------------- conclusions

def read_the_model(target, names, model, vifs, counts_everywhere, unit):
    """Turn the fit into sentences, including the ones that withdraw it.

    Every claim here is gated on something measured. A coefficient is only
    reported as an association when its interval excludes zero; a model is
    only reported as explaining anything when the F test says the whole thing
    beats the mean. Nothing is scored down by a fudge factor — a finding
    either survives its own interval or it is stated as undetermined.
    """
    says, limits = [], []
    n, k, df = model["n"], model["k"], model["df"]

    pct = model["r2"] * 100.0
    says.append(
        "Across %d %s, the model accounts for %.1f%% of the variation in %s "
        "(adjusted for the %d predictors: %.1f%%)."
        % (n, unit, pct, target, k, model["adj_r2"] * 100.0))

    if math.isfinite(model["f_p"]) and model["f_p"] > 0.05:
        says.append(
            "Taken as a whole the model cannot be told apart from one that "
            "predicts the average of %s every time (F p = %s). Read nothing "
            "below as a finding." % (target, fmt_p(model["f_p"])))

    for i, nm in enumerate(names):
        c = model["coefs"][i + 1]
        crosses = c["lo"] <= 0.0 <= c["hi"]
        if crosses:
            says.append(
                "%s: no association distinguishable from zero — a one-unit "
                "increase goes with a change in %s somewhere between %s and "
                "%s, an interval that includes no change at all."
                % (nm, target, fmt(c["lo"]), fmt(c["hi"])))
        else:
            direction = "more" if c["beta"] > 0 else "fewer"
            says.append(
                "%s: a one-unit increase goes with %s %s %s (95%% CI %s to %s, "
                "p %s), holding the other predictors fixed."
                % (nm, fmt(abs(c["beta"])), direction, target,
                   fmt(c["lo"]), fmt(c["hi"]), fmt_p(c["p"])))

    # ---- what the model is not allowed to say ----

    limits.append(
        "These are associations, not causes. Nothing here establishes that "
        "changing a predictor would change %s; an unmeasured variable that "
        "moves both would produce exactly this output." % target)

    if model["r2"] > 0.999:
        # A perfect fit on civic data is not a triumph, it is a tautology:
        # a rate regressed on its own numerator, or a total on its parts.
        limits.append(
            "The fit is essentially exact (R² %.4f). On observational data "
            "that nearly always means %s is arithmetically derived from a "
            "predictor — a rate against its own numerator, a total against "
            "its parts — in which case the model has recovered a definition "
            "rather than found anything." % (model["r2"], target))

    # Concentration of the thing being explained. Greenwich 2016 was the case
    # that forced this: five of 991 sales carried 89% of the variance, so the
    # coefficients were a statement about five houses while the page reported
    # p < 0.001 and said nothing. Measured against the target rather than via
    # hat values, because "which rows is this fit actually about" is the
    # question, and a share of variance answers it in one sentence.
    y = [f + r for f, r in zip(model["fitted"], model["resid"])]
    ybar = mean(y)
    total = sum((v - ybar) ** 2 for v in y)
    if total > 0:
        top_n = max(3, int(math.ceil(0.01 * n)))
        contrib = sorted(((v - ybar) ** 2 for v in y), reverse=True)[:top_n]
        share = sum(contrib) / total
        # Only worth saying when a small minority of rows dominates: a tenth of
        # the rows holding a quarter of the variance is ordinary.
        if top_n <= 0.10 * n and share >= 0.25:
            limits.append(
                "%d of the %d %s — %.0f%% of them — carry %.0f%% of the "
                "variation in %s being explained. Every coefficient above is "
                "mostly a statement about those %d rows; check them before "
                "reading anything else, since one of them being wrong moves "
                "the whole model."
                % (top_n, n, unit, 100.0 * top_n / n, share * 100.0, target,
                   top_n))

    worst = max(vifs) if vifs else 1.0
    if worst >= 10:
        bad = [names[i] for i, v in enumerate(vifs) if v >= 10]
        limits.append(
            "Severe collinearity (%s). These predictors largely explain each "
            "other, so the model can fit well while the individual "
            "coefficients are arbitrary — a different sample can flip their "
            "signs. Do not read them one at a time."
            % ", ".join("%s VIF %.1f" % (names[i], vifs[i])
                        for i in range(len(names)) if names[i] in bad))
    elif worst >= 5:
        limits.append(
            "Moderate collinearity (worst VIF %.1f). The coefficients are "
            "less stable than their intervals suggest." % worst)

    if counts_everywhere:
        # What was measured is only that every value is a non-negative whole
        # number. Dollar amounts pass that test too, so asserting these are
        # counts over areas states a fact this code cannot know — and a
        # warning that is sometimes plainly wrong teaches readers to skip the
        # whole block. The detection stays; the claim becomes conditional.
        limits.append(
            "Every variable here is a non-negative whole number. If these are "
            "counts over the same areas — crimes, calls and permits per ward — "
            "then all of them grow with the size of the area, the model mostly "
            "rediscovers which areas are big, and you want rates: normalise per "
            "resident or per square mile first. If they are amounts rather than "
            "tallies, this does not apply.")

    if n < 10 * k:
        limits.append(
            "%d observations for %d predictors is thin (under ten per "
            "predictor). The fit will look better on this data than on any "
            "other." % (n, k))

    limits.append(
        "The model was fitted over the observed range of each predictor and "
        "says nothing outside it. A prediction for values beyond that range "
        "is extrapolation, which this script does not do.")

    limits.append(
        "R² measures how much variation the model reproduces, not whether "
        "the data are right. The caveats and confidence csq attaches to the "
        "underlying query still apply, and are shown above.")

    return says, limits


# ------------------------------------------------------------------ page

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
  /*SERIES-DARK*/
}
* { box-sizing: border-box; }
body { margin:0; background:var(--plane); color:var(--text-primary);
  font-family: system-ui, -apple-system, "Segoe UI", sans-serif; font-size:15px;
  line-height:1.55; }
.page { max-width: 1040px; margin:0 auto; padding: 40px 24px 80px; }
header h1 { font-size:24px; margin:0 0 4px; }
header p { color:var(--text-secondary); margin:0 0 26px; }
.card { background:var(--surface-1); border:1px solid var(--border);
  border-radius:10px; padding:22px 24px 18px; margin-bottom:22px; }
.card h2 { font-size:17px; margin:0 0 10px; }
.card h3 { font-size:14px; margin:18px 0 8px; color:var(--text-secondary);
  font-weight:600; }
.sub { color:var(--muted); font-size:12.5px; margin:0 0 12px; }
ul.lines { list-style:none; padding:0; margin:0 0 6px; font-size:13.5px; }
ul.lines li { padding:7px 11px; border-left:2px solid var(--axis);
  background:var(--plane); margin-bottom:5px; color:var(--text-secondary); }
ul.lines li.says { border-left-color:var(--series-1); color:var(--text-primary); }
ul.lines li.null { border-left-color:var(--muted); }
ul.lines li.limit { border-left-color:var(--warn); }
ul.lines li.conf { border-left-color:var(--good); }
.hero { font-size:40px; font-weight:600; line-height:1.1; margin:2px 0 0; }
.hero-label { color:var(--muted); font-size:13px; }
.stats { display:flex; flex-wrap:wrap; gap:28px; margin:0 0 14px; }
svg.chart { width:100%; height:auto; min-width:460px; display:block; }
.plot { overflow-x:auto; margin:6px 0 2px; }
.grid { stroke:var(--grid); stroke-width:1; }
.zero { stroke:var(--axis); stroke-width:1; }
.ref { stroke:var(--axis); stroke-width:1; stroke-dasharray:none; }
.ci { stroke-width:2; stroke-linecap:round; }
.ci.live { stroke:var(--series-1); }
.ci.null { stroke:var(--muted); }
.pt { stroke:var(--surface-1); stroke-width:2; }
.pt.live { fill:var(--series-1); }
.pt.null { fill:var(--muted); }
.cell { fill:var(--l); }
.cellv { fill:var(--l); font-size:11px; font-variant-numeric:tabular-nums; }
@media (prefers-color-scheme: dark) {
  :root:where(:not([data-theme="light"])) .cell { fill:var(--d); }
  :root:where(:not([data-theme="light"])) .cellv { fill:var(--d); }
}
:root[data-theme="dark"] .cell { fill:var(--d); }
:root[data-theme="dark"] .cellv { fill:var(--d); }
text { font-family:inherit; }
.tick { fill:var(--muted); font-size:11px; font-variant-numeric:tabular-nums; }
.rot { font-size:11px; }
.cat { fill:var(--text-secondary); font-size:12px; }
.val { fill:var(--text-secondary); font-size:11.5px;
  font-variant-numeric:tabular-nums; }
.axis-title { fill:var(--muted); font-size:11.5px; }
.hit { fill:transparent; outline:none; }
.hit:hover, .hit:focus { fill:var(--text-primary); fill-opacity:0.06; }
table { border-collapse:collapse; font-size:13px; width:100%; }
th, td { text-align:left; padding:6px 12px 6px 0; border-bottom:1px solid var(--grid);
  white-space:nowrap; }
th { color:var(--muted); font-weight:600; }
td.num { text-align:right; font-variant-numeric:tabular-nums; }
td.flag { color:var(--muted); }
details { margin-top:12px; border-top:1px solid var(--border); padding-top:10px; }
summary { cursor:pointer; color:var(--text-secondary); font-size:13px; }
.scroll { overflow-x:auto; margin-top:10px; }
#tip { position:fixed; z-index:9; pointer-events:none; background:var(--surface-1);
  color:var(--text-primary); border:1px solid var(--border); border-radius:8px;
  padding:8px 10px; font-size:12.5px; box-shadow:0 6px 20px rgba(0,0,0,0.14);
  max-width:300px; }
#tip[hidden] { display:none; }
#tip .head { color:var(--text-secondary); margin-bottom:3px; }
#tip .v { font-weight:600; font-variant-numeric:tabular-nums; }
#tip .n { color:var(--text-secondary); }
"""

JS = r"""
(function () {
  var tip = document.getElementById('tip');
  // Variable names come from query output: untrusted text, inserted only
  // through textContent.
  function show(el, x, y) {
    tip.textContent = '';
    var h = document.createElement('div'); h.className = 'head';
    h.textContent = el.getAttribute('data-label'); tip.appendChild(h);
    var v = document.createElement('div'); v.className = 'v';
    v.textContent = el.getAttribute('data-value'); tip.appendChild(v);
    var s = el.getAttribute('data-series');
    if (s) { var n = document.createElement('div'); n.className = 'n';
             n.textContent = s; tip.appendChild(n); }
    tip.hidden = false;
    var b = tip.getBoundingClientRect();
    tip.style.left = Math.min(x + 14, window.innerWidth - b.width - 8) + 'px';
    tip.style.top = Math.max(8, Math.min(y - b.height - 12,
                    window.innerHeight - b.height - 8)) + 'px';
  }
  document.addEventListener('pointermove', function (e) {
    var hit = e.target.closest ? e.target.closest('.hit') : null;
    if (hit) show(hit, e.clientX, e.clientY); else tip.hidden = true;
  });
  document.addEventListener('focusin', function (e) {
    if (e.target.classList && e.target.classList.contains('hit')) {
      var b = e.target.getBoundingClientRect();
      show(e.target, b.right, b.top + b.height);
    }
  });
  document.addEventListener('focusout', function () { tip.hidden = true; });
})();
"""


def _vars(colors):
    return " ".join("--series-%d:%s;" % (i + 1, c) for i, c in enumerate(colors))


CSS = (CSS.replace("/*SERIES-LIGHT*/", _vars(SERIES_LIGHT))
          .replace("/*SERIES-DARK*/", _vars(SERIES_DARK)))


def page(title, subtitle, body):
    return ("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n"
            "<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">\n"
            "<title>{t}</title>\n<style>{css}</style>\n</head>\n<body>\n"
            "<div class=\"page\">\n<header><h1>{t}</h1><p>{s}</p></header>\n{b}\n"
            "</div>\n<div id=\"tip\" hidden></div>\n<script>{js}</script>\n"
            "</body>\n</html>\n").format(t=esc(title), s=esc(subtitle),
                                         css=CSS, js=JS, b=body)


# ------------------------------------------------------------------ prep

def prepare(res, opts):
    """Choose the columns, drop what cannot be used, and say what was dropped."""
    cols, rows = res["columns"], res["rows"]
    notes = []

    numeric = [i for i in range(len(cols)) if column_is_numeric(rows, i)]
    text = [i for i in range(len(cols)) if column_is_text(rows, i)]
    if not numeric:
        raise ValueError("no numeric columns to model")

    def find(name):
        for i, c in enumerate(cols):
            if c.lower() == name.lower():
                return i
        raise ValueError("no column named %r (have: %s)" % (name, ", ".join(cols)))

    ti = find(opts.target)
    if ti not in numeric:
        raise ValueError("--target %s is not numeric" % cols[ti])

    label_i = find(opts.label) if opts.label else (text[0] if len(text) == 1 else -1)

    if opts.predictors:
        pis = [find(n) for n in opts.predictors.split(",") if n.strip()]
    else:
        pis = [i for i in numeric if i != ti and i != label_i]
    pis = [i for i in pis if i != ti]
    if not pis:
        raise ValueError("no predictor columns left besides the target")

    # Rows missing any value in play cannot contribute to a joint fit, and
    # quietly imputing them would invent data.
    used = [ti] + pis
    keep = [r for r in rows
            if all(i < len(r) and is_number(r[i]) for i in used)]
    dropped = len(rows) - len(keep)
    if dropped:
        notes.append("%d of %d rows dropped for missing values in the columns "
                     "used." % (dropped, len(rows)))

    # A predictor that never varies explains nothing and makes the normal
    # equations singular.
    live = []
    for i in pis:
        vals = [r[i] for r in keep]
        if len(set(vals)) < 2:
            notes.append("%s dropped: it has the same value in every row."
                         % cols[i])
        else:
            live.append(i)
    if not live:
        raise ValueError("every predictor is constant once rows with missing "
                         "values are dropped")
    if len(live) > MAX_PREDICTORS:
        raise ValueError("%d predictors is more than this will fit blind; name "
                         "the ones you mean with --predictors" % len(live))

    y = [float(r[ti]) for r in keep]
    x_rows = [[float(r[i]) for i in live] for r in keep]

    # These two fail inside the normal equations in ways that look identical to
    # collinearity, so they are caught here where the real reason is still known.
    if len(set(y)) < 2:
        raise ValueError("%s is the same in every row, so there is no variation "
                         "to model" % cols[ti])
    if len(keep) - len(live) - 1 <= 0:
        raise ValueError("%d rows cannot support %d predictors — a fit needs at "
                         "least %d rows, and far more to mean anything"
                         % (len(keep), len(live), len(live) + 2))
    labels = [str(r[label_i]) if label_i >= 0 and label_i < len(r) and
              r[label_i] is not None else "row %d" % (n + 1)
              for n, r in enumerate(keep)]

    counts = looks_like_counts(y) and all(
        looks_like_counts([r[j] for r in x_rows]) for j in range(len(live)))

    return {
        "target": cols[ti], "names": [cols[i] for i in live],
        "y": y, "x": x_rows, "labels": labels, "rows": keep,
        "counts_everywhere": counts, "notes": notes,
    }


def sd(xs):
    n = len(xs)
    if n < 2:
        return 0.0
    m = mean(xs)
    return math.sqrt(sum((v - m) ** 2 for v in xs) / float(n - 1))


def source_notes(res, prep):
    """Caveats from csq, then anything preparation had to drop."""
    out = []
    for c in res["caveats"]:
        out.append('<li class="limit">%s</li>' % esc(c))
    for x in res["excluded"]:
        out.append('<li class="limit">Excluded: <b>%s</b> — %s</li>'
                   % (esc(x.get("city", "")), esc(x.get("reason", ""))))
    if res["truncated"]:
        out.append('<li class="limit">The row cap was hit upstream, so this '
                   'models a prefix of the result.</li>')
    c = res.get("confidence")
    if isinstance(c, dict) and c.get("assessed"):
        out.append('<li class="conf">Confidence <b>%s%%</b> (%s) — the share '
                   'of records the underlying query reads that are present '
                   'and usable.</li>' % (esc(c.get("score", 0)),
                                         esc(c.get("band", ""))))
    for n in prep["notes"]:
        out.append('<li>%s</li>' % esc(n))
    return out


def data_table(cols, rows):
    head = "".join("<th>%s</th>" % esc(c) for c in cols)
    body = []
    for r in rows[:TABLE_ROW_CAP]:
        cells = []
        for i in range(len(cols)):
            v = r[i] if i < len(r) else None
            if v is None:
                cells.append('<td class="flag">—</td>')
            elif is_number(v):
                cells.append('<td class="num">%s</td>' % esc(fmt(float(v))))
            else:
                cells.append("<td>%s</td>" % esc(v))
        body.append("<tr>%s</tr>" % "".join(cells))
    more = ("<p class=\"sub\">Showing %d of %d rows.</p>"
            % (TABLE_ROW_CAP, len(rows))) if len(rows) > TABLE_ROW_CAP else ""
    return ('<details><summary>The data (%d rows)</summary><div class="scroll">'
            '<table><thead><tr>%s</tr></thead><tbody>%s</tbody></table></div>%s'
            '</details>' % (len(rows), head, "".join(body), more))


def build(res, prep, model, vifs, opts):
    target, names = prep["target"], prep["names"]
    y, x_rows = prep["y"], prep["x"]
    sy = sd(y)
    std, los, his, crosses = [], [], [], []
    for i, nm in enumerate(names):
        sx = sd([r[i] for r in x_rows])
        scale = (sx / sy) if sy > 0 else 0.0
        c = model["coefs"][i + 1]
        std.append(c["beta"] * scale)
        los.append(c["lo"] * scale)
        his.append(c["hi"] * scale)
        crosses.append(c["lo"] <= 0.0 <= c["hi"])

    says, limits = read_the_model(target, names, model, vifs,
                                  prep["counts_everywhere"], opts.unit)

    out = []

    notes = source_notes(res, prep)
    out.append('<section class="card"><h2>What was fitted</h2>'
               '<p class="sub">%s &middot; %s modelled against %s, over %d %s.</p>'
               '%s</section>' % (
                   esc(res["title"]), esc(target), esc(", ".join(names)),
                   model["n"], esc(opts.unit),
                   ('<ul class="lines">%s</ul>' % "".join(notes)) if notes else ""))

    stats = (
        '<div class="stats">'
        '<div><div class="hero-label">R²</div><div class="hero">%.0f%%</div></div>'
        '<div><div class="hero-label">adjusted R²</div><div class="hero">%.0f%%</div></div>'
        '<div><div class="hero-label">observations</div><div class="hero">%d</div></div>'
        '<div><div class="hero-label">predictors</div><div class="hero">%d</div></div>'
        '</div>' % (model["r2"] * 100, model["adj_r2"] * 100,
                    model["n"], model["k"]))
    out.append('<section class="card"><h2>What the model says</h2>%s'
               '<ul class="lines">%s</ul>'
               '<h3>What it cannot say</h3><ul class="lines">%s</ul>'
               '</section>' % (
                   stats,
                   "".join('<li class="%s">%s</li>'
                           % ("null" if "distinguishable from zero" in s
                              or "cannot be told apart" in s else "says", esc(s))
                           for s in says),
                   "".join('<li class="limit">%s</li>' % esc(l)
                           for l in limits)))

    rowsq = []
    for i, nm in enumerate(names):
        c = model["coefs"][i + 1]
        flag = "spans zero" if crosses[i] else ""
        if vifs[i] >= 10:
            flag = (flag + "; collinear").strip("; ")
        rowsq.append(
            "<tr><td>%s</td><td class=\"num\">%s</td><td class=\"num\">%s</td>"
            "<td class=\"num\">%s to %s</td><td class=\"num\">%s</td>"
            "<td class=\"num\">%s</td><td class=\"flag\">%s</td></tr>" % (
                esc(nm), esc(fmt(c["beta"])), esc(fmt(c["se"])),
                esc(fmt(c["lo"])), esc(fmt(c["hi"])), esc(fmt_p(c["p"])),
                esc(fmt(vifs[i], 1)), esc(flag)))
    icept = model["coefs"][0]
    out.append(
        '<section class="card"><h2>Coefficients</h2>'
        '<p class="sub">Standardised, so predictors in different units are '
        'comparable; a bar crossing the zero line is undetermined, not small.</p>'
        '<div class="plot">%s</div>'
        '<div class="scroll"><table><thead><tr><th>predictor</th>'
        '<th>coefficient</th><th>std. error</th><th>95%% interval</th><th>p</th>'
        '<th>VIF</th><th></th></tr></thead><tbody>%s'
        '<tr><td>(intercept)</td><td class="num">%s</td><td class="num">%s</td>'
        '<td class="num">%s to %s</td><td class="num">%s</td><td class="num">—</td>'
        '<td></td></tr></tbody></table></div></section>' % (
            coef_plot(names, std, los, his, crosses), "".join(rowsq),
            esc(fmt(icept["beta"])), esc(fmt(icept["se"])),
            esc(fmt(icept["lo"])), esc(fmt(icept["hi"])),
            esc(fmt_p(icept["p"]))))

    allnames = [target] + names
    series = [y] + [[r[i] for r in x_rows] for i in range(len(names))]
    corr = [[pearson(series[i], series[j]) for j in range(len(allnames))]
            for i in range(len(allnames))]
    out.append('<section class="card"><h2>How the variables move together</h2>'
               '<p class="sub">Pearson correlation, −1 to +1. Pairwise and '
               'marginal: it ignores every other variable, which is why it '
               'disagrees with the coefficients above.</p>'
               '<div class="plot">%s</div></section>'
               % heatmap(allnames, corr))

    out.append('<section class="card"><h2>Where the model fits, and where it '
               'misses</h2><p class="sub">Left: each %s against what the model '
               'predicts for it — the diagonal is a perfect fit. Right: what is '
               'left over. Structure in the residuals means the straight-line '
               'form is wrong, however high R² is.</p>'
               '<div class="plot">%s</div><div class="plot">%s</div></section>'
               % (esc(opts.unit.rstrip("s")),
                  scatter(model["fitted"], y, prep["labels"],
                          "predicted " + target, "observed " + target,
                          diagonal=True),
                  scatter(model["fitted"], model["resid"], prep["labels"],
                          "predicted " + target, "residual", zero=True)))

    order = sorted(range(len(y)), key=lambda i: -abs(model["resid"][i]))[:10]
    miss = "".join(
        "<tr><td>%s</td><td class=\"num\">%s</td><td class=\"num\">%s</td>"
        "<td class=\"num\">%s</td></tr>" % (
            esc(prep["labels"][i]), esc(fmt(y[i])), esc(fmt(model["fitted"][i])),
            esc(fmt(model["resid"][i]))) for i in order)
    out.append('<section class="card"><h2>The ten the model misses most</h2>'
               '<p class="sub">Large residuals are where the model is wrong, '
               'which is usually more interesting than where it is right — and '
               'is a list of places to look, not a list of findings.</p>'
               '<div class="scroll"><table><thead><tr><th>%s</th>'
               '<th>observed %s</th><th>predicted</th><th>residual</th></tr>'
               '</thead><tbody>%s</tbody></table></div>%s</section>' % (
                   esc(opts.unit.rstrip("s")), esc(target), miss,
                   data_table(res["columns"], prep["rows"])))

    return "".join(out)


def main(argv=None):
    ap = argparse.ArgumentParser(
        prog="csq-predict.py",
        description="Fit a linear model over several numeric columns of a csq "
                    "result and render it, with its limits, as one HTML page.")
    ap.add_argument("inputs", nargs="+", metavar="FILE",
                    help="JSON files, or - for stdin")
    ap.add_argument("--target", required=True, metavar="COL",
                    help="the column to model (required: picking it for you "
                         "would be choosing your question)")
    ap.add_argument("--predictors", metavar="A,B,C",
                    help="columns to model it from (default: every other "
                         "numeric column)")
    ap.add_argument("--label", metavar="COL",
                    help="column naming each observation (default: the single "
                         "text column, if there is one)")
    ap.add_argument("--unit", default="observations", metavar="NOUN",
                    help="what one row is, for the prose (default: observations)")
    ap.add_argument("-o", "--output", default="csq-model.html",
                    help="output HTML file (default: csq-model.html)")
    ap.add_argument("--stdout", action="store_true",
                    help="write the HTML to stdout instead of a file")
    ap.add_argument("--open", dest="open_", action="store_true",
                    help="open the page in a browser when it is written")
    ap.add_argument("--title", default="csq model", help="page title")
    opts = ap.parse_args(argv)

    try:
        documents = load_documents(opts.inputs)
    except (ValueError, OSError) as e:
        print("csq-predict: %s" % e, file=sys.stderr)
        return 2

    results = []
    for source, doc in documents:
        try:
            results.extend(normalise(doc, source))
        except ValueError as e:
            print("csq-predict: %s" % e, file=sys.stderr)
            return 2
    if not results:
        print("csq-predict: no results in the input", file=sys.stderr)
        return 2
    if len(results) > 1:
        print("csq-predict: %d results given; a model is fitted over one table, "
              "so only the first (%s) is used" % (len(results),
                                                  results[0]["title"]),
              file=sys.stderr)
    res = results[0]

    try:
        prep = prepare(res, opts)
    except ValueError as e:
        print("csq-predict: %s" % e, file=sys.stderr)
        return 2

    model = ols(prep["x"], prep["y"])
    if model is None:
        print("csq-predict: the model could not be fitted — the predictors are "
              "exactly collinear, or there are too few rows for %d predictors"
              % len(prep["names"]), file=sys.stderr)
        return 2
    vifs = vif(prep["x"], prep["names"])

    sub = "%s modelled against %s · %s" % (
        prep["target"], ", ".join(prep["names"]), res["source"])
    doc = page(opts.title, sub, build(res, prep, model, vifs, opts))

    if opts.stdout:
        sys.stdout.write(doc)
        return 0
    with open(opts.output, "w", encoding="utf-8") as fh:
        fh.write(doc)
    print("csq-predict: wrote %s — R² %.3f over %d observations, %d predictors"
          % (opts.output, model["r2"], model["n"], model["k"]), file=sys.stderr)
    if opts.open_:
        webbrowser.open("file://" + os.path.abspath(opts.output))
    return 0


if __name__ == "__main__":
    sys.exit(main())

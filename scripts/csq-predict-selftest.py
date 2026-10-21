#!/usr/bin/env python3
# Copyright (c) 2026 Neomantra Corp
"""Check that csq-predict.py's arithmetic is right.

There is no numpy on the far side of this script to lean on, so the statistics
are implemented by hand — and a quietly wrong regression is worse than none,
because it produces confident output either way. This verifies the parts that
could be silently wrong, against things that are known independently:

  * closed-form algebra for simple regression
  * published t critical values
  * the defining property of least squares (residuals orthogonal to the
    predictors)
  * a planted relationship, recovered end to end through the real CLI

Run it from the repository root:

    python3 scripts/csq-predict-selftest.py

Exits non-zero if anything fails.
"""

import importlib.util
import json
import math
import os
import random
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
TARGET = os.path.join(HERE, "csq-predict.py")

spec = importlib.util.spec_from_file_location("csqpredict", TARGET)
P = importlib.util.module_from_spec(spec)
spec.loader.exec_module(P)

FAILED = []


def check(name, ok, detail=""):
    print("  %-52s %s %s" % (name, "ok" if ok else "FAIL", detail))
    if not ok:
        FAILED.append(name)


def section(title):
    print("\n%s" % title)


# ------------------------------------------------------------------ t dist

def test_t_distribution():
    section("t distribution, against published tables")
    for df, want in ((10, 2.228), (25, 2.060), (30, 2.042),
                     (120, 1.980), (1000, 1.962)):
        got = P.t_critical(df)
        check("two-sided 5%% critical value, df=%d" % df,
              abs(got - want) < 0.002, "%.4f vs %.3f" % (got, want))
    for df, a, want in ((25, 0.01, 2.787), (25, 0.005, 3.078)):
        got = P.t_critical(df, a)
        check("two-sided %.3f critical value, df=%d" % (a, df),
              abs(got - want) < 0.002, "%.4f vs %.3f" % (got, want))
    # A p-value must invert its own critical value.
    t = P.t_critical(17, 0.05)
    check("p-value inverts the critical value",
          abs(P.t_two_sided_p(t, 17) - 0.05) < 1e-6)


# ---------------------------------------------------------------- the fit

def test_exact_recovery():
    section("a model with no noise must come back exactly")
    x = [[float(i), float((i * i) % 7)] for i in range(30)]
    y = [3.0 + 2.5 * r[0] - 1.25 * r[1] for r in x]
    m = P.ols(x, y)
    b = [c["beta"] for c in m["coefs"]]
    check("intercept 3.0", abs(b[0] - 3.0) < 1e-8, "%.10f" % b[0])
    check("slope 2.5", abs(b[1] - 2.5) < 1e-8, "%.10f" % b[1])
    check("slope -1.25", abs(b[2] + 1.25) < 1e-8, "%.10f" % b[2])
    check("R^2 is exactly 1", abs(m["r2"] - 1.0) < 1e-12)


def test_closed_form():
    section("simple regression, against the closed form")
    rng = random.Random(42)
    xs = [rng.uniform(0, 100) for _ in range(60)]
    ys = [5.0 + 0.8 * v + rng.gauss(0, 10) for v in xs]
    m = P.ols([[v] for v in xs], ys)
    mx, my = P.mean(xs), P.mean(ys)
    sxx = sum((v - mx) ** 2 for v in xs)
    beta = sum((v - mx) * (w - my) for v, w in zip(xs, ys)) / sxx
    alpha = my - beta * mx
    resid = [w - (alpha + beta * v) for v, w in zip(xs, ys)]
    se = math.sqrt((sum(r * r for r in resid) / (len(xs) - 2)) / sxx)
    check("slope", abs(m["coefs"][1]["beta"] - beta) < 1e-10)
    check("intercept", abs(m["coefs"][0]["beta"] - alpha) < 1e-10)
    check("standard error of the slope", abs(m["coefs"][1]["se"] - se) < 1e-10)


def test_orthogonality():
    section("least squares residuals are orthogonal to the predictors")
    rng = random.Random(7)
    x = [[rng.gauss(0, 1), rng.gauss(5, 2), rng.uniform(0, 50)]
         for _ in range(80)]
    truth = (1.5, -0.4, 0.05)
    y = [2 + sum(t * v for t, v in zip(truth, r)) + rng.gauss(0, 1) for r in x]
    m = P.ols(x, y)
    e = m["resid"]
    check("residuals sum to zero", abs(sum(e)) < 1e-8, "%.1e" % abs(sum(e)))
    for j in range(3):
        dot = sum(x[i][j] * e[i] for i in range(len(e)))
        scale = max(1.0, sum(abs(x[i][j]) for i in range(len(e))))
        check("orthogonal to predictor %d" % (j + 1),
              abs(dot) / scale < 1e-10, "%.1e" % (abs(dot) / scale))
    check("fitted + residual == observed",
          all(abs(m["fitted"][i] + e[i] - y[i]) < 1e-9 for i in range(len(y))))
    check("the true coefficients fall inside their 95% intervals",
          all(m["coefs"][j + 1]["lo"] <= t <= m["coefs"][j + 1]["hi"]
              for j, t in enumerate(truth)))


def test_refusals():
    section("designs that cannot be fitted are refused, not fitted")
    rng = random.Random(3)
    x = [[rng.gauss(0, 1), rng.gauss(0, 1)] for _ in range(40)]
    y = [rng.gauss(0, 1) for _ in range(40)]
    check("exactly collinear design returns None",
          P.ols([[r[0], r[0] * 3.0] for r in x], y) is None)
    v = P.vif([[r[0], r[1], r[0] * 2 + 1] for r in x], ["a", "b", "copy"])
    check("VIF is infinite for a duplicated predictor",
          v[2] > 1000 or v[2] == float("inf"), "%.1f" % v[2])
    v2 = P.vif(x, ["a", "b"])
    check("VIF is near 1 for independent predictors", max(v2) < 1.4,
          "%.3f" % max(v2))


# ------------------------------------------------------------ end to end

def test_planted_relationship():
    """The test worth trusting: plant an answer, then read it off the page.

    A relationship is generated with known coefficients, alongside a column of
    pure noise. The script must recover the first two inside their intervals
    and must decline to report the third.
    """
    section("a planted relationship, end to end through the CLI")
    rng = random.Random(1)
    rows = []
    for i in range(60):
        a = rng.uniform(0, 100)
        b = rng.uniform(0, 50)
        rows.append({"unit": "u%02d" % i,
                     "a": round(a, 3), "b": round(b, 3),
                     "junk": round(rng.uniform(0, 10), 3),
                     "y": round(3 + 2.0 * a - 0.5 * b + rng.gauss(0, 8), 3)})

    tmp = tempfile.mkdtemp()
    src = os.path.join(tmp, "planted.json")
    with open(src, "w") as fh:
        json.dump(rows, fh)
    out = os.path.join(tmp, "planted.html")

    r = subprocess.run(
        [sys.executable, TARGET, src, "--target", "y", "--label", "unit",
         "-o", out], capture_output=True, text=True)
    check("the script exits cleanly", r.returncode == 0, r.stderr.strip()[-60:])
    if r.returncode != 0:
        return

    x = [[row["a"], row["b"], row["junk"]] for row in rows]
    y = [row["y"] for row in rows]
    m = P.ols(x, y)
    ca, cb, cj = m["coefs"][1], m["coefs"][2], m["coefs"][3]
    check("planted slope 2.0 is inside its interval",
          ca["lo"] <= 2.0 <= ca["hi"],
          "[%.3f, %.3f]" % (ca["lo"], ca["hi"]))
    check("planted slope -0.5 is inside its interval",
          cb["lo"] <= -0.5 <= cb["hi"],
          "[%.3f, %.3f]" % (cb["lo"], cb["hi"]))
    check("the noise column is reported as undetermined",
          cj["lo"] <= 0.0 <= cj["hi"],
          "[%.3f, %.3f]" % (cj["lo"], cj["hi"]))

    page = open(out).read()
    check("the page says so in words",
          "junk: no association distinguishable from zero" in page)
    check("association, not cause, is stated",
          "associations, not causes" in page)
    check("the page carries no external requests",
          "http://" not in page.replace("http://www.w3.org", "") and
          "https://" not in page)


def test_leverage_warning():
    """It must fire when a few rows dominate, and stay quiet when none do.

    A warning that fires on everything is ignored, so both directions are
    checked rather than just the positive case.
    """
    section("the leverage warning, in both directions")
    tmp = tempfile.mkdtemp()

    def page_for(rows):
        path = os.path.join(tmp, "lev.json")
        with open(path, "w") as fh:
            json.dump(rows, fh)
        out = os.path.join(tmp, "lev.html")
        subprocess.run([sys.executable, TARGET, path, "--target", "y",
                        "--label", "id", "-o", out],
                       capture_output=True, text=True)
        return open(out).read()

    rng = random.Random(5)
    even = [{"id": "r%03d" % i, "a": rng.uniform(0, 100), "y": 0.0}
            for i in range(200)]
    for r in even:
        r["y"] = 10 + 2 * r["a"] + rng.gauss(0, 5)
    check("quiet on evenly spread data", "carry" not in page_for(even))

    # One sale a hundred times the size of the rest — the Greenwich shape.
    skewed = list(even)
    skewed[0] = dict(skewed[0], a=99.0, y=2_000_000.0)
    skewed[1] = dict(skewed[1], a=98.0, y=1_500_000.0)
    page = page_for(skewed)
    check("fires when a few rows dominate the variance",
          "carry" in page and "statement about those" in page)


def test_counts_warning_is_conditional():
    """The all-integers notice must not assert that the values are counts."""
    section("the whole-number notice is hedged, not asserted")
    tmp = tempfile.mkdtemp()
    rows = [{"id": "r%03d" % i, "assessed": 100000 + i * 1000,
             "price": 250000 + i * 2500} for i in range(40)]
    path = os.path.join(tmp, "money.json")
    with open(path, "w") as fh:
        json.dump(rows, fh)
    out = os.path.join(tmp, "money.html")
    r = subprocess.run([sys.executable, TARGET, path, "--target", "price",
                        "--label", "id", "-o", out], capture_output=True, text=True)
    # This data fits exactly, which once divided by a zero residual variance.
    check("an exactly-fitting model does not crash", r.returncode == 0,
          r.stderr.strip().splitlines()[-1][:60] if r.returncode else "")
    if r.returncode != 0:
        return
    page = open(out).read()
    check("does not assert the values are counts",
          "Every variable here is a count" not in page)
    check("offers the rate advice conditionally",
          "If these are counts" in page)


def test_cli_refusals():
    section("bad input is refused with an accurate reason")
    tmp = tempfile.mkdtemp()

    def run(records, *args):
        path = os.path.join(tmp, "in.json")
        with open(path, "w") as fh:
            json.dump(records, fh)
        r = subprocess.run(
            [sys.executable, TARGET, path, "-o", os.path.join(tmp, "o.html")]
            + list(args), capture_output=True, text=True)
        return r.returncode, (r.stderr or "")

    code, err = run([{"y": 7, "a": i} for i in range(20)], "--target", "y")
    check("a constant target is named as such", code == 2 and
          "same in every row" in err, err.strip()[-50:])

    code, err = run([{"y": 1, "a": 1, "b": 2}, {"y": 2, "a": 2, "b": 1}],
                    "--target", "y")
    check("too few rows is named as such", code == 2 and
          "cannot support" in err, err.strip()[-50:])

    code, err = run([{"y": "text", "a": 1} for _ in range(20)],
                    "--target", "y")
    check("a non-numeric target is refused", code == 2 and
          "not numeric" in err, err.strip()[-50:])

    code, err = run([{"y": i, "a": i % 5} for i in range(20)],
                    "--target", "nope")
    check("an unknown column is refused", code == 2 and
          "no column named" in err, err.strip()[-50:])


def main():
    print("Checking %s" % TARGET)
    test_t_distribution()
    test_exact_recovery()
    test_closed_form()
    test_orthogonality()
    test_refusals()
    test_planted_relationship()
    test_leverage_warning()
    test_counts_warning_is_conditional()
    test_cli_refusals()
    print()
    if FAILED:
        print("FAILED: %d check%s — %s"
              % (len(FAILED), "" if len(FAILED) == 1 else "s",
                 "; ".join(FAILED)))
        return 1
    print("all checks passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())

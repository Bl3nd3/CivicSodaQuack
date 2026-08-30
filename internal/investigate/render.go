// Copyright (c) 2026 Neomantra Corp

package investigate

import (
	"fmt"
	"io"
	"strings"

	"github.com/neomantra/CivicSodaQuack/internal/confidence"
)

// RenderOptions tunes the text report.
type RenderOptions struct {
	// Width is the wrap column. Zero uses 76.
	Width int
	// ShowSQL prints the statement behind each finding.
	ShowSQL bool
	// ShowWorking prints the plan, the challenges each finding survived, and
	// the dataset profile — everything the verdict rests on.
	ShowWorking bool
}

func (o RenderOptions) width() int {
	if o.Width <= 0 {
		return 76
	}
	return o.Width
}

// Markers for the finding list.
const (
	markWithdrawn = "⊘"
	markNote      = "⚠"
	markBullet    = "•"
)

// RenderText writes the investigation card.
//
// The sections are written as one unit and in this order deliberately. The
// verdict is useless without the confidence, the confidence is misleading
// without the caveats, and the caveats are unfalsifiable without the evidence —
// so there is no code path here that emits an earlier section without the later
// ones. A renderer that could print the verdict alone would eventually be used
// to, and the screenshot would outlive every qualification on it.
func RenderText(w io.Writer, r *Report, opts RenderOptions) {
	width := opts.width()

	renderBox(w, []string{
		"CIVIC INVESTIGATION",
		strings.TrimSpace(r.City + " — " + r.Title),
	})

	fmt.Fprintf(w, "\nQUESTION\n%s\n", r.Question)

	fmt.Fprintf(w, "\nVERDICT\n%s\n", r.Verdict)
	for _, line := range wrap(r.VerdictWhy, width) {
		fmt.Fprintln(w, line)
	}

	fmt.Fprintf(w, "\nCONFIDENCE\n")
	if !r.Assessed {
		fmt.Fprintf(w, "not assessed\n")
		for _, line := range wrap("The evidence behind this answer could not be "+
			"profiled, so there is no share to report. A zero here would say "+
			"something different and worse.", width) {
			fmt.Fprintln(w, line)
		}
	} else {
		fmt.Fprintf(w, "%d%%\n", r.Confidence)
		for _, line := range wrap(ConfidenceNote+".", width) {
			fmt.Fprintln(w, line)
		}
		for _, line := range wrap(fmt.Sprintf(
			"%d%% of the records read were present and usable, across %d%% of the "+
				"planned indicators.", r.Retention, r.Coverage), width) {
			fmt.Fprintln(w, line)
		}
		if r.Validation.Confidence != nil {
			if line := r.Validation.Confidence.FreshnessLine(); line != "" {
				fmt.Fprintf(w, "%s\n", line)
			}
		}
	}

	renderFindings(w, r, width)
	renderCaveats(w, r, width)
	renderEvidence(w, r, opts)

	fmt.Fprintf(w, "\nREPRODUCE\nSnapshot: %s\n", r.Snapshot)
	if r.Reproduce != "" {
		fmt.Fprintf(w, "%s\n", r.Reproduce)
	}
}

// renderFindings lists what moved, then what was withdrawn, then what could
// not be asked.
//
// Withdrawn findings are printed rather than dropped. An investigation that
// looked for something and found a reason not to trust it has told the reader
// something real, and hiding that would make the surviving findings look like
// everything there was.
func renderFindings(w io.Writer, r *Report, width int) {
	fmt.Fprintf(w, "\nFINDINGS\n")

	surviving := r.Surviving()
	if len(surviving) == 0 {
		fmt.Fprintf(w, "None survived challenge.\n")
	}
	for _, f := range surviving {
		fmt.Fprintf(w, "%s %s\n", f.Direction.Arrow(), f.Headline)
	}

	for _, cov := range r.Validation.Coverage {
		if cov.Known && cov.Partial != 0 {
			fmt.Fprintf(w, "%s %s has incomplete %d coverage (ends %s)\n",
				markNote, cov.Table, cov.Partial, cov.Last)
		}
	}

	if wd := r.Withdrawn(); len(wd) > 0 {
		fmt.Fprintf(w, "\nWITHDRAWN UNDER CHALLENGE\n")
		for _, f := range wd {
			fmt.Fprintf(w, "%s %s\n", markWithdrawn, f.Asks)
			for _, line := range wrap(withdrawalReason(f), width-2) {
				fmt.Fprintf(w, "  %s\n", line)
			}
		}
	}

	var blocked []string
	for _, pp := range r.Plan.Probes {
		if pp.Skipped {
			blocked = append(blocked, pp.Asks+" — "+pp.Reason)
		}
	}
	for _, u := range r.Analysis.Unanswered {
		blocked = append(blocked, u.Asks+" — "+u.Reason)
	}
	if len(blocked) > 0 {
		fmt.Fprintf(w, "\nNOT ANSWERED\n")
		for _, s := range blocked {
			for i, line := range wrap(s, width-2) {
				if i == 0 {
					fmt.Fprintf(w, "%s %s\n", markBullet, line)
				} else {
					fmt.Fprintf(w, "  %s\n", line)
				}
			}
		}
	}

	if !r.Readiness.Ready && r.Readiness.FixCommand != "" {
		fmt.Fprintf(w, "\nTO ANSWER THE REST\n%s\n", r.Readiness.FixCommand)
	}
}

func renderCaveats(w io.Writer, r *Report, width int) {
	if len(r.Caveats) == 0 {
		return
	}
	fmt.Fprintf(w, "\nIMPORTANT CAVEATS\n")
	for _, c := range r.Caveats {
		for i, line := range wrap(c, width-2) {
			if i == 0 {
				fmt.Fprintf(w, "%s %s\n", markBullet, line)
			} else {
				fmt.Fprintf(w, "  %s\n", line)
			}
		}
	}
}

// renderEvidence shows the series behind each surviving finding, and on
// request the SQL and the challenges it withstood.
func renderEvidence(w io.Writer, r *Report, opts RenderOptions) {
	surviving := r.Surviving()
	if len(surviving) == 0 && !opts.ShowWorking {
		return
	}
	fmt.Fprintf(w, "\nEVIDENCE\n")

	for _, f := range surviving {
		fmt.Fprintf(w, "\n%s — %s\n", f.Probe, f.Asks)
		renderSeries(w, f)
		if opts.ShowWorking {
			fmt.Fprintf(w, "  challenges:\n")
			for _, c := range f.Challenges {
				status := "survived"
				if c.Withdrew {
					status = "WITHDREW"
				} else if !c.Survived {
					status = "noted"
				}
				fmt.Fprintf(w, "    [%s] %s — %s\n", status, c.Name, c.Verdict)
			}
		}
		if opts.ShowSQL {
			fmt.Fprintf(w, "  sql:\n")
			for _, line := range strings.Split(strings.TrimSpace(f.SQL), "\n") {
				fmt.Fprintf(w, "    %s\n", line)
			}
		}
	}

	if opts.ShowWorking && r.Validation.Confidence != nil {
		fmt.Fprintf(w, "\nPROVENANCE\n")
		confidence.RenderText(w, r.Validation.Confidence, confidence.RenderOptions{
			Width: opts.width(), ShowDetail: true,
		})
	}
}

// renderSeries prints one indicator as a sparkline plus its endpoints.
//
// The whole series is shown rather than the two numbers the finding compares,
// because a percentage between two points is the easiest statistic in the world
// to mislead with, and the shape of the line is what tells a reader whether the
// movement is a trend or a wobble.
func renderSeries(w io.Writer, f Finding) {
	if len(f.Series) == 0 {
		return
	}
	fmt.Fprintf(w, "  %s  %d–%d\n", sparkline(f.Series),
		f.Series[0].Period, f.Series[len(f.Series)-1].Period)

	for _, p := range f.Series {
		flag := ""
		if !p.Complete {
			flag = "  (incomplete — excluded from the measurement)"
		}
		switch {
		case p.Denominator > 0:
			fmt.Fprintf(w, "  %d  %12s  (%s of %s)%s\n", p.Period,
				trimNum(p.Indicator), trimNum(p.Value), trimNum(p.Denominator), flag)
		default:
			fmt.Fprintf(w, "  %d  %12s%s\n", p.Period, trimNum(p.Indicator), flag)
		}
	}
	fmt.Fprintf(w, "  baseline %s (%s mean) → %s (%d)\n",
		trimNum(f.Baseline), periodRange(f.BaselineFrom, f.BaselineTo),
		trimNum(f.Latest), f.LatestPeriod)
}

var sparkChars = []rune("▁▂▃▄▅▆▇█")

// sparkline renders a series as one line of block characters, scaled between
// its own minimum and maximum.
//
// The scale is local to the series and the axis does not start at zero, so this
// shows shape and never magnitude. That is the honest reading of eight
// characters — anything more precise would be a chart pretending to be one.
func sparkline(series []Point) string {
	if len(series) == 0 {
		return ""
	}
	lo, hi := series[0].Indicator, series[0].Indicator
	for _, p := range series {
		if p.Indicator < lo {
			lo = p.Indicator
		}
		if p.Indicator > hi {
			hi = p.Indicator
		}
	}
	var b strings.Builder
	for _, p := range series {
		if hi == lo {
			b.WriteRune(sparkChars[len(sparkChars)/2])
			continue
		}
		idx := int((p.Indicator - lo) / (hi - lo) * float64(len(sparkChars)-1))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sparkChars) {
			idx = len(sparkChars) - 1
		}
		b.WriteRune(sparkChars[idx])
	}
	return b.String()
}

// trimNum formats a number without trailing noise: counts stay integers, rates
// keep two decimals.
func trimNum(f float64) string {
	if f == float64(int64(f)) {
		return commas(int64(f))
	}
	return fmt.Sprintf("%.2f", f)
}

func commas(n int64) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	if len(s) > 3 {
		var out []byte
		for i := 0; i < len(s); i++ {
			if i > 0 && (len(s)-i)%3 == 0 {
				out = append(out, ',')
			}
			out = append(out, s[i])
		}
		s = string(out)
	}
	if neg {
		return "-" + s
	}
	return s
}

// renderBox draws the header card.
func renderBox(w io.Writer, lines []string) {
	inner := 0
	for _, l := range lines {
		if n := len([]rune(l)); n > inner {
			inner = n
		}
	}
	inner += 2 // one space of padding each side
	fmt.Fprintf(w, "╭%s╮\n", strings.Repeat("─", inner))
	for _, l := range lines {
		pad := inner - len([]rune(l)) - 1
		fmt.Fprintf(w, "│ %s%s│\n", l, strings.Repeat(" ", pad))
	}
	fmt.Fprintf(w, "╰%s╯\n", strings.Repeat("─", inner))
}

// wrap breaks text at word boundaries.
func wrap(s string, width int) []string {
	if width <= 0 {
		width = 76
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	var out []string
	line := words[0]
	for _, word := range words[1:] {
		if len([]rune(line))+1+len([]rune(word)) > width {
			out = append(out, line)
			line = word
			continue
		}
		line += " " + word
	}
	return append(out, line)
}

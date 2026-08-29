// Copyright (c) 2026 Neomantra Corp

package confidence

import (
	"fmt"
	"io"
	"strings"
)

// Markers for each level. Unknown gets a marker of its own rather than being
// hidden: a reader who cannot see that a check was skipped will assume it
// passed, which turns a gap in the evidence into a claim about the data.
const (
	markPass    = "✓"
	markWarn    = "⚠"
	markFail    = "✗"
	markUnknown = "·"
)

func mark(l Level) string {
	switch l {
	case Pass:
		return markPass
	case Warn:
		return markWarn
	case Fail:
		return markFail
	}
	return markUnknown
}

// RenderOptions tunes the text block.
type RenderOptions struct {
	// Width is the wrap column for detail lines. Zero uses 76.
	Width int
	// ShowDetail prints the explanatory line under a signal that carries one.
	ShowDetail bool
	// ShowLimits appends what the score does not mean. Callers printing many
	// reports in one run should set this once rather than per query.
	ShowLimits bool
	// Prefix indents every line, for nesting under a query header.
	Prefix string
}

func (o RenderOptions) width() int {
	if o.Width <= 0 {
		return 76
	}
	return o.Width
}

// RenderText writes the confidence block: the score, the evidence that
// produced it, and the freshness of the oldest input.
//
// The block is written as one unit on purpose. Every field here exists to stop
// the score from being quoted alone, and a renderer that could emit the number
// without the signals would defeat the point of computing them.
func RenderText(w io.Writer, r *Report, opts RenderOptions) {
	if r == nil {
		return
	}
	p := opts.Prefix

	if !r.Assessed {
		fmt.Fprintf(w, "%sConfidence: not assessed\n", p)
		if len(r.Datasets) == 0 {
			fmt.Fprintf(w, "%s  This query reads csq's own bookkeeping rather than a synced\n"+
				"%s  dataset, so there is nothing to profile.\n", p, p)
		}
		return
	}

	fmt.Fprintf(w, "%sConfidence: %d%% (%s) — data fitness, not accuracy of the finding\n",
		p, r.Score, r.Band)
	// Coverage is printed only when something could not be checked. At full
	// coverage the line adds nothing; below it, the score is a verdict on less
	// than the whole catalogue and must not be read as if it were not.
	if r.Coverage < 100 {
		fmt.Fprintf(w, "%s%d%% of checks could be run — the score covers only those.\n",
			p, r.Coverage)
	}
	fmt.Fprintln(w)

	multi := len(r.Datasets) > 1
	emit := func(sigs []Signal) {
		for _, s := range sigs {
			label := s.Label
			if multi && s.Dataset != "" {
				label = s.Dataset + ": " + label
			}
			fmt.Fprintf(w, "%s%s %s\n", p, mark(s.Level), label)
			if opts.ShowDetail && s.Detail != "" {
				for _, line := range wrapText(s.Detail, opts.width()-4) {
					fmt.Fprintf(w, "%s    %s\n", p, line)
				}
			}
		}
	}

	if c := r.Confirmations(); len(c) > 0 {
		emit(c)
	}
	if u := r.Unmeasured(); len(u) > 0 {
		fmt.Fprintln(w)
		emit(u)
	}
	if pr := r.Problems(); len(pr) > 0 {
		fmt.Fprintln(w)
		emit(pr)
	}

	if line := r.FreshnessLine(); line != "" {
		fmt.Fprintf(w, "\n%s%s\n", p, line)
	}
	if opts.ShowLimits {
		RenderLimits(w, r, opts)
	}
}

// RenderLimits writes what the score does not mean.
func RenderLimits(w io.Writer, r *Report, opts RenderOptions) {
	if r == nil || len(r.Limits) == 0 {
		return
	}
	p := opts.Prefix
	fmt.Fprintf(w, "\n%sWhat this score does not mean:\n", p)
	for _, l := range r.Limits {
		for i, line := range wrapText(l, opts.width()-4) {
			if i == 0 {
				fmt.Fprintf(w, "%s  * %s\n", p, line)
			} else {
				fmt.Fprintf(w, "%s    %s\n", p, line)
			}
		}
	}
}

// Summary is the one-line form, for a listing where a full block will not fit.
func (r *Report) Summary() string {
	if r == nil || !r.Assessed {
		return "confidence: not assessed"
	}
	out := fmt.Sprintf("confidence %d%% (%s)", r.Score, r.Band)
	switch n := len(r.Problems()); n {
	case 0:
	case 1:
		out += ", 1 caution"
	default:
		out += fmt.Sprintf(", %d cautions", n)
	}
	if r.Coverage < 100 {
		out += fmt.Sprintf(", %d%% coverage", r.Coverage)
	}
	return out
}

// wrapText breaks s into lines of at most width characters on word boundaries.
func wrapText(s string, width int) []string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return nil
	}
	var lines []string
	cur := fields[0]
	for _, f := range fields[1:] {
		if len(cur)+1+len(f) > width {
			lines = append(lines, cur)
			cur = f
			continue
		}
		cur += " " + f
	}
	return append(lines, cur)
}

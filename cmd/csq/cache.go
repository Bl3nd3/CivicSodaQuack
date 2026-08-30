// Copyright (c) 2026 Neomantra Corp

package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/neomantra/CivicSodaQuack/internal/cache"
)

const cacheUsage = `csq cache — the drafted-mode cache

Usage:
  csq cache                    List cached drafts, newest use first
  csq cache verify             Check every entry: parses, checksum, self-consistent
  csq cache show <n>           Print one entry's fingerprint and drafted JSON
  csq cache stats              Size and use
  csq cache prune [--older-than 30d]
                               Remove unused entries and any predating this csq
  csq cache clear              Remove every entry
  csq cache path               Print the cache directory

csq caches what the model drafted, never what a query returned. A draft is
code: given the same question, schema, prompt and model, it means the same
thing tomorrow. A query result is data, and reusing one would put a figure on
screen the portal may since have revised — so 'csq modes run' always
re-executes against DuckDB.

An entry is reused only when every input still matches: the question, the
model, the effort, csq's authoring instructions, the mode-file schema, the
tables you hold (columns, types, and sampled values), and the mode already on
disk. Change any of them and the entry is reported stale, with the reason.

Location: ~/.csq/cache/drafts, or $CSQ_CACHE_DIR.
Bypass for one run with 'csq modes personal --no-cache' or '--refresh'.
`

func runCache(args []string) error {
	if len(args) == 0 {
		return listCache()
	}
	switch args[0] {
	case "list":
		return listCache()
	case "verify":
		return verifyCache()
	case "show":
		return showCacheEntry(args[1:])
	case "stats":
		return cacheStats()
	case "prune":
		return pruneCache(args[1:])
	case "clear":
		return clearCache()
	case "path":
		fmt.Println(cache.DefaultDir())
		return nil
	case "-h", "--help", "help":
		fmt.Print(cacheUsage)
		return nil
	default:
		return fmt.Errorf("unknown action %q\n\n%s", args[0], cacheUsage)
	}
}

func openCache() (*cache.Store, error) {
	return cache.Open(cache.DefaultDir())
}

func listCache() error {
	s, err := openCache()
	if err != nil {
		return err
	}
	entries, problems := s.List()
	if len(entries) == 0 && len(problems) == 0 {
		fmt.Printf("No cached drafts yet.\n\n  %s\n\n", s.Dir())
		fmt.Printf("One appears the first time 'csq modes personal' drafts a mode.\n")
		return nil
	}

	fmt.Printf("Cached drafts in %s\n\n", s.Dir())
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "  #\tMODE\tPORTAL\tAGE\tUSES\tQUESTION\n")
	fmt.Fprintf(tw, "  -\t----\t------\t---\t----\t--------\n")
	for i, e := range entries {
		fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\t%d\t%s\n",
			i+1, e.Fingerprint.ModeName, e.Fingerprint.Portal,
			humaniseAge(time.Since(e.CreatedAt).Round(time.Minute)),
			e.Hits, truncateText(e.Question, 48))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	reportCacheProblems(problems)
	fmt.Printf("\n'csq cache show <#>' prints one; 'csq cache verify' checks them all.\n")
	return nil
}

// verifyCache is the validation surface: it re-derives each entry's identity
// from its own contents and reports anything that does not add up.
//
// This is a different question from "is this entry stale". Staleness is decided
// against the inputs of a run and is entirely normal. What verify looks for is
// an entry that is damaged or lying about itself — truncated by a full disk,
// edited by hand, or written by a csq whose fingerprint means something else —
// because such an entry could be served for a lookup it should never match.
func verifyCache() error {
	s, err := openCache()
	if err != nil {
		return err
	}
	ok, problems := s.Verify()

	fmt.Printf("Verifying %s\n\n", s.Dir())
	if len(ok) == 0 && len(problems) == 0 {
		fmt.Printf("  nothing cached yet\n")
		return nil
	}
	fmt.Printf("  %d entr%s verified: parses, checksum holds, key matches its own inputs\n",
		len(ok), plural(len(ok), "y", "ies"))

	if len(problems) == 0 {
		return nil
	}
	reportCacheProblems(problems)
	return fmt.Errorf("%d cache entr%s unusable; 'csq cache prune' or 'csq cache clear' "+
		"removes them, and csq will simply redraft", len(problems), plural(len(problems), "y", "ies"))
}

func reportCacheProblems(problems []cache.Problem) {
	if len(problems) == 0 {
		return
	}
	fmt.Printf("\n  %d unusable entr%s:\n", len(problems), plural(len(problems), "y", "ies"))
	for _, p := range problems {
		fmt.Printf("    %s\n        %v\n", p.Path, p.Err)
	}
}

func showCacheEntry(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: csq cache show <#>   (numbers come from 'csq cache')")
	}
	s, err := openCache()
	if err != nil {
		return err
	}
	entries, _ := s.List()

	var n int
	if _, err := fmt.Sscanf(args[0], "%d", &n); err != nil || n < 1 || n > len(entries) {
		return fmt.Errorf("no entry %q; 'csq cache' lists %d", args[0], len(entries))
	}
	e := entries[n-1]
	f := e.Fingerprint

	fmt.Printf("Question:  %s\n", e.Question)
	fmt.Printf("Mode:      %s\n", f.ModeName)
	fmt.Printf("Portal:    %s\n", f.Portal)
	fmt.Printf("Drafted:   %s (%s ago)\n",
		e.CreatedAt.Format(time.RFC3339), humaniseAge(time.Since(e.CreatedAt).Round(time.Minute)))
	fmt.Printf("Last used: %s, %d time%s\n",
		e.LastUsedAt.Format(time.RFC3339), e.Hits, plural(e.Hits, "", "s"))
	fmt.Printf("Model:     %s (effort %s)\n", f.Model, f.Effort)
	fmt.Printf("csq:       %s\n", f.CsqVersion)
	fmt.Printf("Samples:   %v\n", f.Samples)
	fmt.Printf("Checksum:  %s\n", e.Checksum[:16])
	fmt.Printf("\nThis entry is reused only while all of the above still hold, along with\n")
	fmt.Printf("the schema of the tables it was drafted against.\n")
	fmt.Printf("\nDrafted document:\n\n%s\n", string(e.Payload))
	return nil
}

func cacheStats() error {
	s, err := openCache()
	if err != nil {
		return err
	}
	st, err := s.Stats()
	if err != nil {
		return err
	}
	fmt.Printf("Draft cache: %s\n\n", s.Dir())
	fmt.Printf("  entries        %d\n", st.Entries)
	fmt.Printf("  on disk        %s\n", humaniseBytes(st.Bytes))
	fmt.Printf("  reuses         %d  (model calls not made)\n", st.Hits)
	if !st.Oldest.IsZero() {
		fmt.Printf("  oldest draft   %s ago\n", humaniseAge(time.Since(st.Oldest).Round(time.Minute)))
		fmt.Printf("  newest draft   %s ago\n", humaniseAge(time.Since(st.Newest).Round(time.Minute)))
	}
	if st.Problems > 0 {
		fmt.Printf("  unusable       %d  (run 'csq cache verify')\n", st.Problems)
	}
	return nil
}

func pruneCache(args []string) error {
	fs := flag.NewFlagSet("cache prune", flag.ContinueOnError)
	var older string
	fs.StringVar(&older, "older-than", "30d", "Remove entries unused for longer than this")
	if err := fs.Parse(args); err != nil {
		return err
	}
	age, err := parseAge(older)
	if err != nil {
		return err
	}
	s, err := openCache()
	if err != nil {
		return err
	}
	n, err := s.Prune(age)
	if err != nil {
		return err
	}
	fmt.Printf("Removed %d entr%s unused for more than %s.\n",
		n, plural(n, "y", "ies"), older)
	return nil
}

func clearCache() error {
	s, err := openCache()
	if err != nil {
		return err
	}
	n, err := s.Clear()
	if err != nil {
		return err
	}
	fmt.Printf("Removed %d cached draft%s from %s.\n", n, plural(n, "", "s"), s.Dir())
	if n > 0 {
		fmt.Printf("Nothing is lost: a saved mode lives in your modes directory, not here.\n")
	}
	return nil
}

// parseAge accepts a plain Go duration plus a day suffix, since "30d" is how
// people think about a cache and time.ParseDuration does not know it.
func parseAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		var days float64
		if _, err := fmt.Sscanf(strings.TrimSuffix(s, "d"), "%g", &days); err != nil {
			return 0, fmt.Errorf("could not read %q as a number of days", s)
		}
		return time.Duration(days * 24 * float64(time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("could not read %q as a duration (try 30d, 12h, or 90m)", s)
	}
	return d, nil
}

func humaniseBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

func truncateText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

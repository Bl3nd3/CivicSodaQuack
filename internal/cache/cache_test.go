// Copyright (c) 2026 Neomantra Corp

package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A cache is only as good as its invalidation, and a wrong hit is worse than
// no cache at all — it serves SQL written for a schema that no longer exists.
// So most of what follows is about what must *not* be reused.

func baseFingerprint() Fingerprint {
	return Fingerprint{
		Format:       FormatVersion,
		CsqVersion:   "0.6.0-test",
		Model:        "claude-opus-5",
		Effort:       "high",
		PromptDigest: DigestString("system prompt"),
		SchemaDigest: DigestJSON(map[string]any{"type": "object"}),
		Question:     NormaliseQuestion("Which vendors got the most money?"),
		ModeName:     "personal",
		Portal:       "data.example.gov",
		InventoryDigest: InventoryDigestOf([]TableShape{{
			Name: "contracts", Rows: 4,
			Columns: []ColumnShape{
				{Name: "vendor_nm", Type: "VARCHAR"},
				{Name: "amt", Type: "VARCHAR"},
			},
		}}),
	}
}

const payload = `{"mode":{"kind":"mode"},"binding":{"kind":"binding"}}`

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
}

func TestLookup_MissOnEmptyStore(t *testing.T) {
	s := newStore(t)
	v := s.Lookup(baseFingerprint())
	if v.State != StateMiss {
		t.Errorf("want miss, got %s", v.State)
	}
	if v.Hit() {
		t.Error("a miss must not report as a hit")
	}
}

func TestPutThenLookup_Hits(t *testing.T) {
	s := newStore(t)
	f := baseFingerprint()

	if _, err := s.Put(f, "Which vendors got the most money?", []byte(payload)); err != nil {
		t.Fatalf("put: %v", err)
	}
	v := s.Lookup(f)
	if v.State != StateHit {
		t.Fatalf("want hit, got %s (%v)", v.State, v.Reasons)
	}
	if string(v.Entry.Payload) != payload {
		t.Errorf("payload came back changed: %s", v.Entry.Payload)
	}
}

// Each of these is an input that changes what the model would produce. Missing
// any one of them from the fingerprint is a cache that serves the wrong draft.
func TestLookup_EveryRelevantInputInvalidates(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Fingerprint)
		expect string // substring the reason must contain
	}{
		{"model", func(f *Fingerprint) { f.Model = "claude-sonnet-5" }, "model changed"},
		{"effort", func(f *Fingerprint) { f.Effort = "max" }, "effort level changed"},
		{"prompt", func(f *Fingerprint) { f.PromptDigest = DigestString("rewritten") },
			"authoring instructions changed"},
		{"schema", func(f *Fingerprint) { f.SchemaDigest = DigestString("v2") },
			"mode file schema changed"},
		{"csq version", func(f *Fingerprint) { f.CsqVersion = "0.7.0" }, "csq changed version"},
		{"inventory", func(f *Fingerprint) {
			f.InventoryDigest = InventoryDigestOf([]TableShape{{Name: "contracts"}})
		}, "tables you hold changed"},
		{"samples", func(f *Fingerprint) { f.Samples = true }, "sample values"},
		{"existing mode", func(f *Fingerprint) { f.ExistingDigest = DigestString("more queries") },
			"mode on disk changed"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			f := baseFingerprint()
			if _, err := s.Put(f, "q", []byte(payload)); err != nil {
				t.Fatalf("put: %v", err)
			}

			changed := f
			tc.mutate(&changed)

			v := s.Lookup(changed)
			if v.State != StateStale {
				t.Fatalf("changing the %s should make the entry stale, got %s", tc.name, v.State)
			}
			// The reason is the whole point: a user must be able to see why
			// they are paying for a question they already asked.
			joined := strings.Join(v.Reasons, "; ")
			if !strings.Contains(joined, tc.expect) {
				t.Errorf("reason should mention %q, got %q", tc.expect, joined)
			}
		})
	}
}

// A different question is a different slot, so it misses rather than going
// stale — there is no prior entry for it to be stale against.
func TestLookup_DifferentQuestionMisses(t *testing.T) {
	s := newStore(t)
	f := baseFingerprint()
	if _, err := s.Put(f, "q", []byte(payload)); err != nil {
		t.Fatalf("put: %v", err)
	}

	other := f
	other.Question = NormaliseQuestion("which departments spend the most?")
	if v := s.Lookup(other); v.State != StateMiss {
		t.Errorf("a different question should miss, got %s", v.State)
	}
}

// Incidental differences in how the same question is typed should still hit;
// a cache that misses on a trailing question mark is not much of a cache.
func TestNormaliseQuestion(t *testing.T) {
	same := []string{
		"Which vendors got the most money?",
		"which vendors got the most money",
		"  Which   vendors got the most money ?  ",
		"WHICH VENDORS GOT THE MOST MONEY!",
	}
	want := NormaliseQuestion(same[0])
	for _, q := range same[1:] {
		if got := NormaliseQuestion(q); got != want {
			t.Errorf("%q normalised to %q, want %q", q, got, want)
		}
	}
	// But a genuinely different question must stay different.
	if NormaliseQuestion("which vendors got the least money") == want {
		t.Error("normalisation merged two different questions")
	}
}

// DuckDB does not promise catalogue order, so an order-sensitive digest would
// turn every run into a miss on the same database.
func TestInventoryDigest_IsOrderIndependent(t *testing.T) {
	a := InventoryDigestOf([]TableShape{
		{Name: "contracts", Columns: []ColumnShape{{Name: "b"}, {Name: "a"}}},
		{Name: "permits"},
	})
	b := InventoryDigestOf([]TableShape{
		{Name: "permits"},
		{Name: "contracts", Columns: []ColumnShape{{Name: "a"}, {Name: "b"}}},
	})
	if a != b {
		t.Error("the same inventory in a different order produced a different digest")
	}
}

// A retyped column changes what SQL is correct, so it must not be invisible.
func TestInventoryDigest_NoticesTypeChange(t *testing.T) {
	a := InventoryDigestOf([]TableShape{{Name: "t",
		Columns: []ColumnShape{{Name: "amt", Type: "VARCHAR"}}}})
	b := InventoryDigestOf([]TableShape{{Name: "t",
		Columns: []ColumnShape{{Name: "amt", Type: "DOUBLE"}}}})
	if a == b {
		t.Error("a column type change should alter the digest")
	}
}

// Sample values reach the prompt, so a resync that respells a category has to
// invalidate: the old filter may still plan while matching nothing.
func TestInventoryDigest_NoticesSampleChange(t *testing.T) {
	a := InventoryDigestOf([]TableShape{{Name: "t",
		Columns: []ColumnShape{{Name: "dept", Samples: []string{"STREETS & SAN"}}}}})
	b := InventoryDigestOf([]TableShape{{Name: "t",
		Columns: []ColumnShape{{Name: "dept", Samples: []string{"Streets and Sanitation"}}}}})
	if a == b {
		t.Error("a changed sample value should alter the digest")
	}
}

// A truncated write must never be served. It is the one corruption that can
// still parse, and a half-draft that parses is worse than one that does not.
func TestLookup_ChecksumMismatchIsCorrupt(t *testing.T) {
	s := newStore(t)
	f := baseFingerprint()
	e, err := s.Put(f, "q", []byte(payload))
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	path := filepath.Join(s.Dir(), e.Slot+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var stored map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	stored["payload"] = `{"mode":{"kind":"tampered"}}`
	tampered, _ := json.Marshal(stored)
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	v := s.Lookup(f)
	if v.State != StateCorrupt {
		t.Fatalf("a tampered payload should be corrupt, got %s", v.State)
	}
	if !strings.Contains(strings.Join(v.Reasons, " "), "checksum") {
		t.Errorf("reason should mention the checksum: %v", v.Reasons)
	}
}

func TestLookup_UnparseableFileIsCorruptNotAHit(t *testing.T) {
	s := newStore(t)
	f := baseFingerprint()
	if _, err := s.Put(f, "q", []byte(payload)); err != nil {
		t.Fatalf("put: %v", err)
	}
	path := filepath.Join(s.Dir(), f.Slot()+".json")
	if err := os.WriteFile(path, []byte(`{"slot":`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if v := s.Lookup(f); v.State != StateCorrupt {
		t.Errorf("a truncated file should be corrupt, got %s", v.State)
	}
}

func TestVerify_ReportsSelfInconsistentEntry(t *testing.T) {
	s := newStore(t)
	f := baseFingerprint()
	e, err := s.Put(f, "q", []byte(payload))
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	// An entry whose key no longer describes its own fingerprint would be
	// served for inputs it was never written for.
	path := filepath.Join(s.Dir(), e.Slot+".json")
	raw, _ := os.ReadFile(path)
	var stored map[string]any
	_ = json.Unmarshal(raw, &stored)
	stored["key"] = "0000000000000000000000000000000000000000000000000000000000000000"
	edited, _ := json.Marshal(stored)
	if err := os.WriteFile(path, edited, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	ok, problems := s.Verify()
	if len(ok) != 0 {
		t.Errorf("the edited entry should not verify, got %d ok", len(ok))
	}
	if len(problems) != 1 {
		t.Fatalf("want 1 problem, got %d", len(problems))
	}
	if !strings.Contains(problems[0].Err.Error(), "does not match its own fingerprint") {
		t.Errorf("problem should explain the mismatch: %v", problems[0].Err)
	}
}

func TestVerify_CleanStorePasses(t *testing.T) {
	s := newStore(t)
	f := baseFingerprint()
	if _, err := s.Put(f, "q", []byte(payload)); err != nil {
		t.Fatalf("put: %v", err)
	}
	ok, problems := s.Verify()
	if len(ok) != 1 || len(problems) != 0 {
		t.Errorf("want 1 ok and no problems, got %d/%d", len(ok), len(problems))
	}
}

// One entry per question: re-drafting must replace, not accumulate.
func TestPut_ReplacesTheSameQuestion(t *testing.T) {
	s := newStore(t)
	f := baseFingerprint()
	if _, err := s.Put(f, "q", []byte(payload)); err != nil {
		t.Fatalf("put: %v", err)
	}
	changed := f
	changed.Model = "claude-sonnet-5"
	if _, err := s.Put(changed, "q", []byte(`{"mode":{},"binding":{}}`)); err != nil {
		t.Fatalf("put: %v", err)
	}

	entries, problems := s.List()
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry per question, got %d", len(entries))
	}
	// The newest write wins, so the old model's draft is gone.
	if entries[0].Fingerprint.Model != "claude-sonnet-5" {
		t.Errorf("the newer draft should have replaced the older one")
	}
}

func TestTouch_CountsReuse(t *testing.T) {
	s := newStore(t)
	f := baseFingerprint()
	e, err := s.Put(f, "q", []byte(payload))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := s.Touch(e); err != nil {
		t.Fatalf("touch: %v", err)
	}
	v := s.Lookup(f)
	if v.Entry.Hits != 1 {
		t.Errorf("want 1 hit recorded, got %d", v.Entry.Hits)
	}
}

// An entry written by a csq with a different notion of an entry must not be
// read under the current rules.
func TestPrune_DropsOutdatedFormatRegardlessOfAge(t *testing.T) {
	s := newStore(t)
	f := baseFingerprint()
	f.Format = FormatVersion - 1
	if _, err := s.Put(f, "q", []byte(payload)); err != nil {
		t.Fatalf("put: %v", err)
	}

	n, err := s.Prune(365 * 24 * time.Hour)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Errorf("an outdated-format entry should be pruned whatever its age, got %d", n)
	}
}

func TestPrune_KeepsRecentEntries(t *testing.T) {
	s := newStore(t)
	if _, err := s.Put(baseFingerprint(), "q", []byte(payload)); err != nil {
		t.Fatalf("put: %v", err)
	}
	n, err := s.Prune(24 * time.Hour)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 0 {
		t.Errorf("a just-written entry should survive pruning, removed %d", n)
	}
}

func TestClearAndStats(t *testing.T) {
	s := newStore(t)
	f := baseFingerprint()
	if _, err := s.Put(f, "a question", []byte(payload)); err != nil {
		t.Fatalf("put: %v", err)
	}

	st, err := s.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.Entries != 1 || st.Bytes == 0 {
		t.Errorf("stats look wrong: %+v", st)
	}

	n, err := s.Clear()
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if n != 1 {
		t.Errorf("want 1 removed, got %d", n)
	}
	if v := s.Lookup(f); v.State != StateMiss {
		t.Errorf("after clear the lookup should miss, got %s", v.State)
	}
}

// A leftover temporary from an interrupted write is not an entry and must not
// be reported as a broken one.
func TestList_IgnoresTempFiles(t *testing.T) {
	s := newStore(t)
	if err := os.WriteFile(filepath.Join(s.Dir(), ".tmp-abc"), []byte("junk"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	entries, problems := s.List()
	if len(entries) != 0 || len(problems) != 0 {
		t.Errorf("a temp file should be ignored, got %d entries and %d problems",
			len(entries), len(problems))
	}
}

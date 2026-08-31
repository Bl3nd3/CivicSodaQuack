// Copyright (c) 2026 Neomantra Corp

package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Entry is one cached draft, together with everything needed to judge whether
// it is still good.
//
// The fingerprint is stored, not just hashed, so a stale entry can explain
// itself. That is the difference between a cache that says "no" and one that
// says "yes, but the schema changed under it" — and the second is the only one
// a user can act on.
type Entry struct {
	Slot        string      `json:"slot"`
	Key         string      `json:"key"`
	Fingerprint Fingerprint `json:"fingerprint"`

	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
	Hits       int       `json:"hits"`

	// Checksum covers Payload. A file can be truncated by a full disk or a
	// crash mid-write, and a half-written draft that still parses is the worst
	// possible outcome — so the bytes are checked, not just the JSON.
	Checksum string `json:"checksum"`
	// Payload is the model's reply verbatim, exactly as it would have arrived
	// from the API. Storing the raw reply rather than a parsed draft means a
	// cache hit re-enters the same code path a fresh call does.
	//
	// It is a string, not a json.RawMessage, so the round trip is byte-exact.
	// Embedded raw JSON is re-indented by json.MarshalIndent when the entry is
	// written, which would mean the bytes read back are never quite the bytes
	// the model sent — and a checksum over them could then only ever compare
	// formatting. Treating the reply as the opaque blob it is avoids the whole
	// question.
	Payload string `json:"payload"`

	// Question is kept in readable form for `csq cache list`; the fingerprint
	// holds only its normalised form.
	Question string `json:"question"`

	// InputTokens and OutputTokens are what the call that produced this entry
	// cost. They are recorded so a hit's saving can be stated as a number
	// rather than asserted — and so the store can be sized against something
	// real. A cache nobody can measure is a cache nobody can tune.
	InputTokens  int64 `json:"input_tokens,omitempty"`
	OutputTokens int64 `json:"output_tokens,omitempty"`

	// path is where this entry was read from. Not serialised.
	path string
}

// State is the outcome of a cache lookup.
type State int

const (
	// StateMiss means nothing was stored for this question.
	StateMiss State = iota
	// StateHit means a stored draft matches every input exactly.
	StateHit
	// StateStale means a draft was stored for this question, but at least one
	// input has changed since. It is reported rather than silently discarded,
	// because "there is nothing here" and "what is here no longer applies" have
	// different causes and different fixes.
	StateStale
	// StateCorrupt means an entry exists but could not be trusted: unreadable,
	// unparseable, or failing its checksum.
	StateCorrupt
)

func (s State) String() string {
	switch s {
	case StateHit:
		return "hit"
	case StateStale:
		return "stale"
	case StateCorrupt:
		return "corrupt"
	default:
		return "miss"
	}
}

// Verdict is what a lookup concluded, and why.
type Verdict struct {
	State State
	// Reasons explains a stale or corrupt verdict in the user's terms.
	Reasons []string
	// Entry is the stored entry when one was found, whatever its state. A stale
	// entry is still returned so a caller can report its age.
	Entry *Entry
}

// Hit reports whether the verdict permits reusing the payload.
func (v Verdict) Hit() bool { return v.State == StateHit }

// Store is a directory of cached drafts, one file per question.
//
// One entry per slot rather than one per fingerprint is deliberate. Keeping
// every historical variant would grow without bound and, worse, would leave the
// newest miss unexplained: csq could only say "not found" when the useful
// answer is "found, but from before you synced two new tables".
type Store struct {
	dir string
}

// DefaultDir is where drafts are cached unless CSQ_CACHE_DIR says otherwise.
func DefaultDir() string {
	if d := strings.TrimSpace(os.Getenv("CSQ_CACHE_DIR")); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".csq", "cache", "drafts")
}

// Open prepares a store rooted at dir, creating it if needed.
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("cache: no directory (set CSQ_CACHE_DIR)")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("cache: create %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// Dir reports the store's root, for `csq cache path`.
func (s *Store) Dir() string { return s.dir }

// Lookup finds the entry for a fingerprint's question and judges it.
//
// It never returns an error for an unusable entry: a broken cache must degrade
// to a miss, never break the command the user actually ran. The reason travels
// in the verdict so it can still be reported.
func (s *Store) Lookup(f Fingerprint) Verdict {
	path := s.pathFor(f.Slot())

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Verdict{State: StateMiss}
	}
	if err != nil {
		return Verdict{State: StateCorrupt,
			Reasons: []string{fmt.Sprintf("the cache file could not be read: %v", err)}}
	}

	var e Entry
	if err := json.Unmarshal(data, &e); err != nil {
		return Verdict{State: StateCorrupt,
			Reasons: []string{"the cache file is not valid JSON; it was probably truncated"}}
	}
	e.path = path

	if got := checksum(e.Payload); got != e.Checksum {
		return Verdict{State: StateCorrupt, Entry: &e,
			Reasons: []string{"the cached draft failed its checksum, so its bytes changed " +
				"since it was written"}}
	}

	if reasons := Diff(e.Fingerprint, f); len(reasons) > 0 {
		return Verdict{State: StateStale, Entry: &e, Reasons: reasons}
	}
	return Verdict{State: StateHit, Entry: &e}
}

// Put stores a draft, replacing whatever was held for the same question.
//
// The write is atomic: a temporary file renamed into place, so an interrupted
// write leaves the previous entry intact rather than a truncated one. Rename
// within a directory is atomic on every platform csq targets.
func (s *Store) Put(f Fingerprint, question string, payload []byte, cost Cost) (*Entry, error) {
	now := time.Now().UTC()
	e := &Entry{
		Slot:         f.Slot(),
		Key:          f.Key(),
		Fingerprint:  f,
		CreatedAt:    now,
		LastUsedAt:   now,
		Checksum:     checksum(string(payload)),
		Payload:      string(payload),
		Question:     strings.TrimSpace(question),
		InputTokens:  cost.InputTokens,
		OutputTokens: cost.OutputTokens,
	}

	body, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("cache: encode entry: %w", err)
	}
	body = append(body, '\n')

	final := s.pathFor(e.Slot)
	tmp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return nil, fmt.Errorf("cache: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed

	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("cache: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("cache: close: %w", err)
	}
	// 0600: a cached draft carries the user's question and their schema.
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return nil, fmt.Errorf("cache: chmod: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		return nil, fmt.Errorf("cache: install %s: %w", final, err)
	}
	e.path = final

	// Bound the store on the way out, not on a schedule. A cache that only
	// shrinks when someone remembers to prune it is one that grows without
	// limit in practice, and the moment a new entry lands is exactly when its
	// size is known to have changed.
	if _, err := s.Enforce(DefaultLimits()); err != nil {
		// Failing to evict is not a reason to lose the draft that was just
		// written; the store is merely larger than intended.
		return e, nil
	}
	return e, nil
}

// Cost is what one model call cost, recorded on the entry it produced.
type Cost struct {
	InputTokens  int64
	OutputTokens int64
}

// Limits bound how large the store may grow.
//
// Both are ceilings rather than targets: eviction runs only when one is
// exceeded. Zero means unbounded on that axis.
type Limits struct {
	MaxEntries int
	MaxBytes   int64
}

// Default bounds. A drafted mode is a few kilobytes, so 200 entries is a long
// working history and still well under a megabyte in practice; the byte ceiling
// exists for the pathological case of a very large draft rather than for the
// ordinary one.
const (
	defaultMaxEntries = 200
	defaultMaxBytes   = 32 << 20 // 32 MiB
)

// DefaultLimits reads the bounds from the environment, falling back to the
// defaults above. CSQ_CACHE_MAX_ENTRIES or CSQ_CACHE_MAX_BYTES set to 0 removes
// that ceiling.
func DefaultLimits() Limits {
	return Limits{
		MaxEntries: envInt("CSQ_CACHE_MAX_ENTRIES", defaultMaxEntries),
		MaxBytes:   int64(envInt("CSQ_CACHE_MAX_BYTES", defaultMaxBytes)),
	}
}

func envInt(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}

// Enforce evicts entries until the store fits within limits, and reports how
// many went.
//
// Least-recently-used first. The entry a user keeps reusing is the one whose
// re-drafting would cost them most, so recency of use is a better proxy for
// value here than age: a draft written months ago and used yesterday is worth
// more than one written yesterday and never used again.
func (s *Store) Enforce(limits Limits) (int, error) {
	if limits.MaxEntries <= 0 && limits.MaxBytes <= 0 {
		return 0, nil
	}
	entries, _ := s.List() // already newest-use-first

	var total int64
	sizes := make(map[string]int64, len(entries))
	for _, e := range entries {
		if fi, err := os.Stat(s.pathFor(e.Slot)); err == nil {
			sizes[e.Slot] = fi.Size()
			total += fi.Size()
		}
	}

	var evicted int
	for i := len(entries) - 1; i >= 0; i-- {
		overCount := limits.MaxEntries > 0 && len(entries)-evicted > limits.MaxEntries
		overBytes := limits.MaxBytes > 0 && total > limits.MaxBytes
		if !overCount && !overBytes {
			break
		}
		e := entries[i]
		if err := os.Remove(s.pathFor(e.Slot)); err != nil && !os.IsNotExist(err) {
			return evicted, err
		}
		total -= sizes[e.Slot]
		evicted++
	}
	return evicted, nil
}

// Touch records a use of an entry. A failure is not worth failing the command
// over — the draft was still served — so the error is returned for logging and
// callers are free to drop it.
func (s *Store) Touch(e *Entry) error {
	e.Hits++
	e.LastUsedAt = time.Now().UTC()
	body, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.pathFor(e.Slot), append(body, '\n'), 0o600)
}

// List returns every entry, newest use first. Unreadable files are reported as
// problems rather than dropped, so `csq cache list` cannot quietly under-report.
func (s *Store) List() ([]*Entry, []Problem) {
	names, err := s.entryFiles()
	if err != nil {
		return nil, []Problem{{Path: s.dir, Err: err}}
	}

	var out []*Entry
	var problems []Problem
	for _, path := range names {
		e, err := readEntry(path)
		if err != nil {
			problems = append(problems, Problem{Path: path, Err: err})
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastUsedAt.After(out[j].LastUsedAt) })
	return out, problems
}

// Problem is one cache file that could not be used.
type Problem struct {
	Path string
	Err  error
}

// Verify checks every stored entry: that it parses, that its checksum holds,
// and that its recorded key still matches its own fingerprint.
//
// The last check catches the subtle failure — an entry hand-edited, or written
// by a csq whose fingerprint definition has since changed, whose key no longer
// describes its contents. Such an entry would otherwise be served on a lookup
// it should never have matched.
func (s *Store) Verify() ([]*Entry, []Problem) {
	names, err := s.entryFiles()
	if err != nil {
		return nil, []Problem{{Path: s.dir, Err: err}}
	}

	var ok []*Entry
	var problems []Problem
	for _, path := range names {
		e, err := readEntry(path)
		if err != nil {
			problems = append(problems, Problem{Path: path, Err: err})
			continue
		}
		if got := checksum(e.Payload); got != e.Checksum {
			problems = append(problems, Problem{Path: path,
				Err: fmt.Errorf("checksum mismatch: the payload changed after it was written")})
			continue
		}
		if want := e.Fingerprint.Key(); want != e.Key {
			problems = append(problems, Problem{Path: path,
				Err: fmt.Errorf("the entry's key does not match its own fingerprint, so it " +
					"would be served for inputs it was not written for")})
			continue
		}
		if want := e.Fingerprint.Slot(); want != e.Slot {
			problems = append(problems, Problem{Path: path,
				Err: fmt.Errorf("the entry is filed under the wrong question")})
			continue
		}
		if !json.Valid([]byte(e.Payload)) {
			problems = append(problems, Problem{Path: path,
				Err: fmt.Errorf("the cached draft is not valid JSON")})
			continue
		}
		ok = append(ok, e)
	}
	sort.Slice(ok, func(i, j int) bool { return ok[i].LastUsedAt.After(ok[j].LastUsedAt) })
	return ok, problems
}

// Remove deletes one entry by slot.
func (s *Store) Remove(slot string) error {
	err := os.Remove(s.pathFor(slot))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Clear removes every entry and reports how many went.
func (s *Store) Clear() (int, error) {
	names, err := s.entryFiles()
	if err != nil {
		return 0, err
	}
	var n int
	for _, path := range names {
		if err := os.Remove(path); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// Prune removes entries unused for longer than age, and every entry whose
// format predates this build.
//
// There is deliberately no expiry on lookup. A draft whose every input is
// unchanged is exactly as valid a year later as it was on the day it was
// written — the fingerprint, not the clock, is what makes a draft stale. Prune
// exists for disk hygiene, and it is the user's call.
func (s *Store) Prune(age time.Duration) (int, error) {
	entries, _ := s.List()
	cutoff := time.Now().UTC().Add(-age)

	var n int
	for _, e := range entries {
		outdated := e.Fingerprint.Format != FormatVersion
		if !outdated && age > 0 && e.LastUsedAt.After(cutoff) {
			continue
		}
		if !outdated && age <= 0 {
			continue
		}
		if err := os.Remove(s.pathFor(e.Slot)); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// Stats summarises the store for `csq cache stats`.
type Stats struct {
	Entries  int
	Bytes    int64
	Hits     int
	Oldest   time.Time
	Newest   time.Time
	Problems int

	// TokensStored is what it cost to produce everything currently held.
	TokensStored int64
	// TokensSaved is what reuse avoided: each entry's own cost, once per hit.
	// It is the number that says whether the cache is earning its disk.
	TokensSaved int64
	// Limits are the ceilings in force, for reporting headroom.
	Limits Limits
}

// Stats reports the store's size and use.
func (s *Store) Stats() (Stats, error) {
	entries, problems := s.List()
	st := Stats{Entries: len(entries), Problems: len(problems), Limits: DefaultLimits()}

	for _, e := range entries {
		st.Hits += e.Hits
		cost := e.InputTokens + e.OutputTokens
		st.TokensStored += cost
		// Each hit avoided re-paying this entry's own cost. Entries written
		// before token accounting existed contribute nothing, which understates
		// the saving rather than inventing one.
		st.TokensSaved += cost * int64(e.Hits)
		if fi, err := os.Stat(s.pathFor(e.Slot)); err == nil {
			st.Bytes += fi.Size()
		}
		if st.Oldest.IsZero() || e.CreatedAt.Before(st.Oldest) {
			st.Oldest = e.CreatedAt
		}
		if e.CreatedAt.After(st.Newest) {
			st.Newest = e.CreatedAt
		}
	}
	return st, nil
}

// entryFiles lists the store's entry files, ignoring temporaries left by an
// interrupted write.
func (s *Store) entryFiles() ([]string, error) {
	des, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, de := range des {
		name := de.Name()
		if de.IsDir() || strings.HasPrefix(name, ".tmp-") ||
			!strings.HasSuffix(name, ".json") {
			continue
		}
		out = append(out, filepath.Join(s.dir, name))
	}
	sort.Strings(out)
	return out, nil
}

func (s *Store) pathFor(slot string) string {
	return filepath.Join(s.dir, slot+".json")
}

func readEntry(path string) (*Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var e Entry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("not valid JSON (probably truncated): %w", err)
	}
	e.path = path
	return &e, nil
}

func checksum(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

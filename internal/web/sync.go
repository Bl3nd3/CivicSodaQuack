// Copyright (c) 2026 Neomantra Corp

package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/neomantra/CivicSodaQuack/internal/config"
	"github.com/neomantra/CivicSodaQuack/internal/duckdb"
	"github.com/neomantra/CivicSodaQuack/internal/modes"
	"github.com/neomantra/CivicSodaQuack/internal/portallock"
	"github.com/neomantra/CivicSodaQuack/internal/socrata"
	syncpkg "github.com/neomantra/CivicSodaQuack/internal/sync"
)

// JobState is the lifecycle of one sync job.
type JobState string

const (
	JobRunning JobState = "running"
	JobDone    JobState = "done"
	JobFailed  JobState = "failed"
	JobStopped JobState = "stopped"
)

// DatasetProgress is one dataset's contribution to a job.
type DatasetProgress struct {
	ID    string `json:"id"`
	Table string `json:"table"`
	Rows  int64  `json:"rows"`
	State string `json:"state"` // waiting | running | ok | failed
	Error string `json:"error,omitempty"`
}

// Job is a running or finished sync, described for a progress display.
type Job struct {
	ID      string             `json:"id"`
	Portal  string             `json:"portal"`
	Alias   string             `json:"alias"`
	Mode    string             `json:"mode,omitempty"`
	State   JobState           `json:"state"`
	Error   string             `json:"error,omitempty"`
	Started time.Time          `json:"started"`
	Ended   *time.Time         `json:"ended,omitempty"`
	Rows    int64              `json:"rows"`
	Sets    []*DatasetProgress `json:"datasets"`
}

// syncManager owns the one sync that may run at a time.
//
// One at a time is a deliberate ceiling rather than a simplification. DuckDB
// permits many readers or a single writer, and every csq database here is also
// attached to the live session — so two concurrent syncs against one portal
// cannot both hold the write lock, and letting a user start them would surface
// as a lock error partway through a long download rather than as a refusal at
// the start.
type syncManager struct {
	mu      sync.Mutex
	current *Job
	cancel  context.CancelFunc
	subs    map[chan *Job]struct{}
}

func newSyncManager() *syncManager {
	return &syncManager{subs: map[chan *Job]struct{}{}}
}

// snapshot returns a copy safe to serialise outside the lock.
func (m *syncManager) snapshot() *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current.clone()
}

func (j *Job) clone() *Job {
	if j == nil {
		return nil
	}
	out := *j
	out.Sets = make([]*DatasetProgress, len(j.Sets))
	for i, d := range j.Sets {
		c := *d
		out.Sets[i] = &c
	}
	return &out
}

// publish sends the current job to every subscriber, dropping the update rather
// than blocking when a browser is slow. A progress display that lags is fine; a
// sync that stalls because nobody drained a channel is not.
func (m *syncManager) publish() {
	snap := m.current.clone()
	for ch := range m.subs {
		select {
		case ch <- snap:
		default:
		}
	}
}

// stop cancels the running job, if any. Safe to call when nothing is running.
func (m *syncManager) stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
	}
}

func (m *syncManager) subscribe() chan *Job {
	ch := make(chan *Job, 8)
	m.mu.Lock()
	m.subs[ch] = struct{}{}
	m.mu.Unlock()
	return ch
}

func (m *syncManager) unsubscribe(ch chan *Job) {
	m.mu.Lock()
	delete(m.subs, ch)
	m.mu.Unlock()
	close(ch)
}

// webReporter turns sync orchestrator callbacks into job updates.
type webReporter struct {
	m   *syncManager
	job *Job
}

func (r *webReporter) find(id string) *DatasetProgress {
	for _, d := range r.job.Sets {
		if d.ID == id {
			return d
		}
	}
	d := &DatasetProgress{ID: id, State: "waiting"}
	r.job.Sets = append(r.job.Sets, d)
	return d
}

func (r *webReporter) DatasetStart(idx, total int, t syncpkg.DatasetTarget) {
	r.m.mu.Lock()
	d := r.find(t.ID)
	d.Table, d.State = t.Effective.Table, "running"
	r.m.publish()
	r.m.mu.Unlock()
}

func (r *webReporter) DatasetProgress(idx, total int, t syncpkg.DatasetTarget, rows int64) {
	r.m.mu.Lock()
	d := r.find(t.ID)
	d.Rows = rows
	r.job.Rows = 0
	for _, s := range r.job.Sets {
		r.job.Rows += s.Rows
	}
	r.m.publish()
	r.m.mu.Unlock()
}

func (r *webReporter) DatasetDone(idx, total int, t syncpkg.DatasetTarget, res syncpkg.DatasetResult) {
	r.m.mu.Lock()
	d := r.find(t.ID)
	d.Rows, d.State = res.RowsWritten, res.Status
	if res.Err != nil {
		d.Error = res.Err.Error()
	}
	r.m.publish()
	r.m.mu.Unlock()
}

// startSync begins a sync for one portal, optionally narrowed to the datasets
// one mode needs.
func (s *Server) startSync(alias string, mode string) (*Job, error) {
	cfg, ok := s.configs[alias]
	if !ok {
		return nil, fmt.Errorf(
			"downloading data is not enabled for this portal; restart with: csq web --db <file> --config <portal.yaml>")
	}
	portal, _ := s.sess.PortalByAlias(alias)

	s.syncs.mu.Lock()
	if s.syncs.current != nil && s.syncs.current.State == JobRunning {
		s.syncs.mu.Unlock()
		return nil, fmt.Errorf("a download is already running; wait for it to finish or stop it first")
	}

	// Narrow to the datasets the mode needs, so "get this data" downloads what
	// the analysis actually requires rather than the portal's whole config.
	var only []string
	if mode != "" {
		ids, err := datasetIDsForMode(mode, portal.Portal)
		if err != nil {
			s.syncs.mu.Unlock()
			return nil, err
		}
		only = ids
	}

	job := &Job{
		ID: fmt.Sprintf("%d", time.Now().UnixNano()), Portal: portal.Portal,
		Alias: alias, Mode: mode, State: JobRunning, Started: time.Now(),
	}
	for _, id := range only {
		job.Sets = append(job.Sets, &DatasetProgress{ID: id, State: "waiting"})
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.syncs.current, s.syncs.cancel = job, cancel
	s.syncs.publish()
	s.syncs.mu.Unlock()

	go s.runSync(ctx, cfg, alias, only, job)
	return job.clone(), nil
}

func (s *Server) runSync(ctx context.Context, cfg *config.Config, alias string, only []string, job *Job) {
	finish := func(state JobState, err error) {
		s.syncs.mu.Lock()
		now := time.Now()
		job.State, job.Ended = state, &now
		if err != nil {
			job.Error = err.Error()
		}
		s.syncs.publish()
		s.syncs.mu.Unlock()
	}

	// Take the same advisory lock csq sync takes, so a download started here
	// and one started in a terminal cannot both write the file.
	lock, err := portallock.Acquire(cfg.DB, portallock.Options{LockWait: 5 * time.Second})
	if err != nil {
		finish(JobFailed, fmt.Errorf("another csq process is using this database: %w", err))
		return
	}
	defer lock.Release()

	w, err := duckdb.Open(cfg.DB)
	if err != nil {
		finish(JobFailed, fmt.Errorf("open database: %w", err))
		return
	}
	writerOpen := true
	defer func() {
		if writerOpen {
			_ = w.Close()
		}
	}()

	scheme := "https"
	if v := os.Getenv("CSQ_SCHEME"); v != "" {
		scheme = v
	}
	deps := syncpkg.Deps{
		DB:       w,
		Client:   &socrata.Client{AppToken: cfg.AppToken},
		Scheme:   scheme,
		Reporter: &webReporter{m: s.syncs, job: job},
		Only:     only,
	}

	summary, runErr := syncpkg.Run(ctx, cfg, deps)

	// Close the writer before re-attaching, and not on a defer.
	//
	// A READ_ONLY attach replays the write-ahead log into the database file,
	// which it cannot do while the read-write handle still holds it — the
	// attach fails with "Bad file descriptor" after a sync that otherwise
	// succeeded completely. Closing here checkpoints the WAL first.
	if cerr := w.Close(); cerr != nil {
		writerOpen = false
		finish(JobFailed, fmt.Errorf("finish writing to the database: %w", cerr))
		return
	}
	writerOpen = false

	// The session's read-only attachment is a snapshot taken when the page
	// opened, so without this the download would succeed and the UI would go on
	// reporting no data — the most confusing possible outcome.
	if rerr := s.sess.Refresh(alias); rerr != nil && runErr == nil {
		finish(JobFailed, fmt.Errorf("data downloaded but the view could not be refreshed: %w", rerr))
		return
	}

	switch {
	case ctx.Err() != nil:
		finish(JobStopped, nil)
	case runErr != nil:
		finish(JobFailed, runErr)
	case summary.Failed > 0:
		finish(JobFailed, fmt.Errorf("%d dataset(s) failed; see Data health for the errors", summary.Failed))
	default:
		finish(JobDone, nil)
	}
}

// datasetIDsForMode lists the Socrata ids one mode needs on one portal.
func datasetIDsForMode(mode, portal string) ([]string, error) {
	m, err := modes.Lookup(mode)
	if err != nil {
		return nil, err
	}
	if len(m.Concepts) == 0 {
		return nil, fmt.Errorf("%q reads your existing sync records and needs no download", mode)
	}
	b, err := modes.LookupBinding(m.Name, portal)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, c := range m.Concepts {
		if bd, ok := b.Concepts[c.Name]; ok {
			ids = append(ids, bd.ID)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("%s publishes none of the datasets %q needs", portal, mode)
	}
	return ids, nil
}

// ---------------------------------------------------------------- handlers

func (s *Server) handleSyncStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Alias string `json:"alias"`
		Mode  string `json:"mode"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Alias == "" && len(s.sess.Portals()) == 1 {
		req.Alias = s.sess.Portals()[0].Alias
	}
	job, err := s.startSync(req.Alias, req.Mode)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, job)
}

func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"job":     s.syncs.snapshot(),
		"enabled": len(s.configs) > 0,
	})
}

func (s *Server) handleSyncStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	s.syncs.stop()
	writeJSON(w, map[string]any{"ok": true})
}

// handleSyncEvents streams job updates as server-sent events.
//
// SSE rather than polling because a sync of a million-row dataset runs for
// minutes and the page should show it moving; and rather than a WebSocket
// because the traffic is one-directional and SSE reconnects on its own.
func (s *Server) handleSyncEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := s.syncs.subscribe()
	defer s.syncs.unsubscribe(ch)

	send := func(j *Job) {
		if j == nil {
			return
		}
		b, err := json.Marshal(j)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	send(s.syncs.snapshot()) // current state, so a late subscriber is not blank

	// A heartbeat keeps proxies and impatient browsers from closing an idle
	// stream between datasets.
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case j := <-ch:
			send(j)
		case <-ticker.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

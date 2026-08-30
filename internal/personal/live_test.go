// Copyright (c) 2026 Neomantra Corp

package personal

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/neomantra/CivicSodaQuack/internal/llm"
	"github.com/neomantra/CivicSodaQuack/internal/modes"
)

// The live test is the one that actually calls the model. Everything else in
// this package proves the machinery around a draft; only this proves that a
// real reply survives it.
//
// It is opt-in twice over — an explicit CSQ_LIVE_API=1 as well as working
// credentials — because a test that silently bills whoever runs `go test ./...`
// is a bug, not a feature. CI never sets the variable.
//
//	export ANTHROPIC_API_KEY=sk-ant-...
//	CSQ_LIVE_API=1 go test ./internal/personal/ -run TestLive -v
//
// Point it at real data to judge the prompt rather than the plumbing, which is
// where a synthetic four-row fixture cannot help:
//
//	CSQ_LIVE_API=1 CSQ_LIVE_DB=data.cityofchicago.org.duckdb \
//	CSQ_LIVE_PORTAL=data.cityofchicago.org \
//	CSQ_LIVE_QUESTION="which vendors got the most money?" \
//	go test ./internal/personal/ -run TestLive -v -timeout 20m

func requireLive(t *testing.T) {
	t.Helper()
	if os.Getenv("CSQ_LIVE_API") != "1" {
		t.Skip("set CSQ_LIVE_API=1 to run the live API test (it makes a billed call)")
	}
	if !llm.HaveCredentials() {
		t.Skip("no Anthropic credentials found; set ANTHROPIC_API_KEY or run `ant auth login`")
	}
}

// liveDatabase returns the database to draft against: the caller's own if
// CSQ_LIVE_DB names one, otherwise the synthetic fixture the rest of this
// package uses.
func liveDatabase(t *testing.T) (path, portal string) {
	t.Helper()
	if p := os.Getenv("CSQ_LIVE_DB"); p != "" {
		host := os.Getenv("CSQ_LIVE_PORTAL")
		if host == "" {
			host = modes.PortalFromDBPath(p)
		}
		return p, host
	}
	return buildTestDB(t), testPortal
}

// TestLive_AuthorDraftsARunnableMode runs the entire path with the model in it:
// inventory, draft, guard, save, plan, execute.
//
// The assertions are deliberately about *runnability*, not about wording. A
// model may reasonably name a query anything, and a test that pinned the prose
// would fail on a better draft. What must hold every time is that the thing it
// wrote can be saved and run.
func TestLive_AuthorDraftsARunnableMode(t *testing.T) {
	requireLive(t)

	dbPath, portal := liveDatabase(t)
	modeName := "live-test"

	host, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatalf("open host: %v", err)
	}
	defer host.Close()
	if _, err := host.Exec(fmt.Sprintf(
		"ATTACH '%s' AS p (READ_ONLY)", strings.ReplaceAll(dbPath, "'", "''"))); err != nil {
		t.Fatalf("attach %s: %v", dbPath, err)
	}

	inv, err := Describe(host, "p", portal)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if len(inv.Tables) == 0 {
		t.Fatalf("%s holds no tables to draft against", dbPath)
	}
	t.Logf("inventory: %d tables (%s)", len(inv.Tables), strings.Join(inv.TableNames(), ", "))

	if os.Getenv("CSQ_LIVE_SAMPLES") == "1" {
		if err := SampleColumns(host, inv); err != nil {
			t.Fatalf("sample: %v", err)
		}
	}

	question := os.Getenv("CSQ_LIVE_QUESTION")
	if question == "" {
		question = "which vendors received the most money in total, and how many contracts each?"
	}

	client, err := llm.New(llm.Options{})
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	// Generous: a high-effort draft over a wide schema is not a fast call, and
	// a timeout here would look like a bug in the code under test.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	start := time.Now()
	draft, err := Author(ctx, client, Request{
		Question: question,
		ModeName: modeName,
		Portal:   inv,
		City:     portal,
	})
	if err != nil {
		t.Fatalf("author: %v", err)
	}
	t.Logf("drafted in %s using %s", time.Since(start).Round(time.Second), client.Model())

	// Author has already applied the guard and the inventory cross-check; these
	// assertions are about what the model chose to produce.
	if len(draft.Mode.Queries) == 0 {
		t.Fatal("the draft has no queries")
	}
	if len(draft.Mode.Caveats) < 2 {
		t.Errorf("expected real caveats plus the provenance one, got %d", len(draft.Mode.Caveats))
	}
	var hasProvenance bool
	for _, c := range draft.Mode.Caveats {
		if c == GeneratedCaveat {
			hasProvenance = true
		}
	}
	if !hasProvenance {
		t.Error("the non-removable provenance caveat is missing")
	}
	if draft.Binding.Population != 0 || draft.Binding.PopulationSource != "" {
		t.Error("a recalled population should have been dropped")
	}

	for _, q := range draft.Mode.Queries {
		t.Logf("\n── %s ──\n%s\n%s", q.Name, q.Desc, strings.TrimSpace(q.SQL))
	}
	for _, c := range draft.Mode.Caveats {
		t.Logf("caveat: %s", c)
	}

	// Save through the real loader, then make DuckDB prove every query resolves.
	dir := t.TempDir()
	paths := PathsFor(dir, modeName, portal)
	if _, err := Save(dir, draft, paths); err != nil {
		t.Fatalf("the drafted mode did not validate: %v", err)
	}
	if problems := VerifyQueries(host, modeName, "p", portal, nil); len(problems) > 0 {
		for _, p := range problems {
			t.Errorf("drafted query %s would not plan: %v", p.Query, p.Err)
		}
		t.FailNow()
	}

	// Finally execute one, because planning is not running: a query can bind
	// cleanly and still fail on a cast at execution time.
	m, err := modes.Lookup(modeName)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	b, err := modes.LookupBinding(modeName, portal)
	if err != nil {
		t.Fatalf("binding: %v", err)
	}
	expanded, err := modes.ExpandConceptsFor(m, m.Queries[0].SQL, "p", b)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	rows, err := host.Query(expanded)
	if err != nil {
		t.Fatalf("executing the drafted query failed: %v", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	var n int
	for rows.Next() {
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	t.Logf("query %q returned %d rows over columns %v", m.Queries[0].Name, n, cols)
}

// TestLive_RefusesCredentiallessRun pins the offline half of the contract: with
// no credentials, csq must fail with guidance rather than a bare API error.
// It runs in the ordinary suite, since it makes no call.
func TestLive_RefusesCredentiallessRun(t *testing.T) {
	if llm.HaveCredentials() {
		t.Skip("credentials are present, so the no-credentials path cannot be exercised here")
	}
	_, err := llm.New(llm.Options{})
	if err == nil {
		t.Fatal("expected an error with no credentials")
	}
	for _, want := range []string{"ANTHROPIC_API_KEY", "ant auth login"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should mention %q, got: %v", want, err)
		}
	}
}

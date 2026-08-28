// Copyright (c) 2026 Neomantra Corp

package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/neomantra/CivicSodaQuack/internal/analysis"
)

func newEmptyServer(t *testing.T, dataDir string) *Server {
	t.Helper()
	srv, err := New(Options{DBs: []analysis.DBSpec{}, DataDir: dataDir})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

// Starting with no database at all must work: it is the first-run case, and the
// flow that fixes it lives inside the page.
func TestEmptyServerServesTheSetupFlow(t *testing.T) {
	srv := newEmptyServer(t, t.TempDir())

	if rec := get(t, srv, "/"); rec.Code != http.StatusOK {
		t.Fatalf("shell status %d", rec.Code)
	}

	rec := get(t, srv, "/api/available")
	if rec.Code != http.StatusOK {
		t.Fatalf("available status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Cities   []AvailableCity `json:"cities"`
		CanSetup bool            `json:"can_setup"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.CanSetup {
		t.Error("a data dir was given, so setup must be offered")
	}
	if len(out.Cities) < 2 {
		t.Fatalf("got %d cities; Chicago and New York are both bound", len(out.Cities))
	}

	var chicago *AvailableCity
	for i := range out.Cities {
		if strings.HasPrefix(out.Cities[i].City, "Chicago") {
			chicago = &out.Cities[i]
		}
	}
	if chicago == nil {
		t.Fatal("Chicago missing from the city list")
	}
	if chicago.Attached {
		t.Error("nothing is attached in an empty session")
	}
	// Every offered analysis must state its cost: the flow asks someone to
	// commit to a download, so it has to say how big it is.
	for _, a := range chicago.Analyses {
		if a.Datasets == 0 || a.ApproxRows == 0 || a.ApproxTime == "" {
			t.Errorf("analysis %q does not state its cost: %+v", a.Mode, a)
		}
	}
}

// Modes with no datasets read the _csq schema and have nothing to set up, so
// offering them in a "download this" flow would send someone after nothing.
func TestAvailableOmitsModesWithNoDatasets(t *testing.T) {
	rec := get(t, newEmptyServer(t, t.TempDir()), "/api/available")
	var out struct {
		Cities []AvailableCity `json:"cities"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, c := range out.Cities {
		for _, a := range c.Analyses {
			if a.Mode == "research" {
				t.Error("research needs no download; it must not appear in setup")
			}
		}
	}
}

// Without a data dir csq must not create files. Naming your own databases is a
// statement that you did not ask csq to invent more.
func TestSetupDisabledWithoutADataDir(t *testing.T) {
	srv := newEmptyServer(t, "")

	rec := get(t, srv, "/api/available")
	var out struct {
		CanSetup bool `json:"can_setup"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.CanSetup {
		t.Error("no data dir, so setup must report itself unavailable")
	}

	rec = post(t, srv, "/api/setup", `{"portal":"data.cityofchicago.org","mode":"corruption"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}

// The portal is looked up in csq's own binding registry rather than trusted, so
// a request cannot name an arbitrary host and have files created for it.
func TestSetupRejectsUnknownPortalAndMode(t *testing.T) {
	srv := newEmptyServer(t, t.TempDir())

	for _, body := range []string{
		`{"portal":"evil.example.com","mode":"corruption"}`,
		`{"portal":"data.cityofchicago.org","mode":"no-such-mode"}`,
		`{"portal":"../../etc","mode":"corruption"}`,
		`{"portal":"datacatalog.cookcountyil.gov","mode":"corruption"}`,
	} {
		if rec := post(t, srv, "/api/setup", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, rec.Code)
		}
	}
}

func TestSetupRequiresPOST(t *testing.T) {
	if rec := get(t, newEmptyServer(t, t.TempDir()), "/api/setup"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", rec.Code)
	}
}

func TestApproxDuration(t *testing.T) {
	cases := map[int64]string{
		0:          "unknown",
		9_000:      "under a minute",
		235_713:    "a few minutes",
		1_349_908:  "tens of minutes",
		23_912_327: "an hour or more",
	}
	for rows, want := range cases {
		if got := approxDuration(rows); got != want {
			t.Errorf("approxDuration(%d) = %q, want %q", rows, got, want)
		}
	}
}

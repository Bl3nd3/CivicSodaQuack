// Copyright (c) 2026 Neomantra Corp

package patterns

import "testing"

// The generated binding must not record a row count.
//
// `rows` is the reference count completeness is measured against — H/E, "did
// the rows arrive". The only count available when building is the size of the
// table already on disk, so writing it sets E to H and completeness becomes the
// ratio of the local copy to itself: a permanent "100% of expected rows
// present" that would read the same after a sync truncated half the data. A
// check that cannot fail is worse than an absent one, because it is shown to
// the reader with a tick beside it.
//
// Left unset, confidence says "expected row count unknown — completeness not
// checked", drops the factor from the product, and lowers coverage to admit it.
func TestBuild_DoesNotPassOffTheLocalCountAsTheReference(t *testing.T) {
	req := buildReq("top-n", map[Role]string{
		RoleEntity: "vendor_nm", RoleMeasure: "paid",
	})
	if req.Table.Rows == 0 {
		t.Fatal("the fixture carries no local row count, so this test could not detect the bug")
	}

	draft, err := Build(req)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := draft.Binding.Datasets["contracts"].Rows; got != 0 {
		t.Errorf("binding records rows = %d (the local table's own size); "+
			"completeness would compare the copy against itself and always pass", got)
	}
}

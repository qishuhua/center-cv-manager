package source

import "testing"

func TestVerifyRemoteSelectionRejectsDriftDeletionAndAmbiguousKind(t *testing.T) {
	versions := []Version{{Ref: "main", Kind: "branch", Commit: "new"}, {Ref: "main", Kind: "tag", Commit: "old"}}
	if err := VerifyRemoteVersion(versions, "main", "branch", "new"); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRemoteVersion(versions, "main", "tag", "old"); err != nil {
		t.Fatal(err)
	}
	for _, selection := range [][3]string{{"main", "branch", "old"}, {"deleted", "branch", "new"}, {"main", "", "old"}} {
		if VerifyRemoteVersion(versions, selection[0], selection[1], selection[2]) == nil {
			t.Fatalf("accepted stale or ambiguous version: %v", selection)
		}
	}
}

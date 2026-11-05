package domain

import "testing"

func TestIDsHaveSingleCanonicalRepresentation(t *testing.T) {
	seen := make(map[string]bool)
	for range 100 {
		id := NewID()
		if !ValidID(id) || id[14] != '4' || seen[id] {
			t.Fatalf("invalid or repeated generated ID %q", id)
		}
		seen[id] = true
	}
	for _, id := range []string{"", "00000000-0000-0000-0000-00000000000", "AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA", "00000000_0000-0000-0000-000000000000", "00000000-0000-0000-0000-00000000000g"} {
		if ValidID(id) {
			t.Errorf("accepted noncanonical ID %q", id)
		}
	}
}

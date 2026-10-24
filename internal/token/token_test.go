package token

import "testing"

func TestNew(t *testing.T) {
	a, b := New(), New()
	if a == b {
		t.Fatal("two tokens are equal")
	}
	if len(a) != 22 {
		t.Errorf("len = %d, want 22", len(a))
	}
	if Hash(a) == a || Hash(a) != Hash(a) {
		t.Error("Hash must be deterministic and differ from the token")
	}
}

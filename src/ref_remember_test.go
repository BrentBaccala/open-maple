package main

import (
	"strings"
	"testing"
)

// TestRememberKeyRefNoMaterialize pins the option-remember key fix: building a
// remember-table key from a live *SageRef uses its handle id, not a stringified
// (and therefore materialized) copy of the polynomial. This is what stops DT's
// MyNormal (option remember) from pulling a multi-MB reduction remainder across
// the wire and into a native AST just to memoize on it.
func TestRememberKeyRefNoMaterialize(t *testing.T) {
	it := sageInterp(t)
	if _, ok := it.cas.(*SageBackend); !ok {
		t.Skip("no Sage backend")
	}
	big := makeBigRef(t, it) // 861-term ref

	k := rememberKey([]Value{big})
	if _, done := big.materialized(); done {
		t.Errorf("rememberKey materialized the ref; it must key by handle id")
	}
	if !strings.HasPrefix(k, "\x01ref") {
		t.Errorf("rememberKey(ref) = %q, want a \\x01ref<id> identity key", k)
	}
	// Stable across calls on the same object (a memo hit must still be possible).
	if k2 := rememberKey([]Value{big}); k2 != k {
		t.Errorf("rememberKey not stable for the same ref: %q vs %q", k, k2)
	}
	// A non-ref arg still uses the value-based canonicalKey (so equal values share
	// a memo entry, as before).
	nat := &Sum{[]Value{Name{"x"}, newInt(1)}}
	if got, want := rememberKey([]Value{nat}), canonicalKey(nat); got != want {
		t.Errorf("rememberKey(non-ref) = %q, want canonicalKey %q", got, want)
	}
}

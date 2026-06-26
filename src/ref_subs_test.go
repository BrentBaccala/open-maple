package main

import "testing"

// TestSageRefSubs exercises the server-side substitution path (refSubs ->
// op_subs): substituting variables into a live *SageRef is done Sage-side on the
// cached polynomial, so the (possibly multi-MB) body is not materialized. The
// load-bearing assertion is that the big ref stays unmaterialized; correctness
// is checked against independently-expanded specializations.
func TestSageRefSubs(t *testing.T) {
	it := sageInterp(t)
	if _, ok := it.cas.(*SageBackend); !ok {
		t.Skip("no Sage backend")
	}
	big := makeBigRef(t, it) // (x+y+1)^40, 861-term ref

	// subs(y=0, big): (x+y+1)^40 -> (x+1)^40.
	r1, ok := it.refSubs(big, [][2]Value{{Name{"y"}, newInt(0)}})
	if !ok {
		t.Fatal("refSubs(y=0) did not take the server-side path")
	}
	want1, err := it.Exec("expand((x+1)^40);")
	if err != nil {
		t.Fatalf("native expand (x+1)^40: %v", err)
	}
	if compareValues(concrete(r1), concrete(want1)) != 0 {
		t.Errorf("subs(y=0, big) != expand((x+1)^40)")
	}

	// subs(x=y, big): (x+y+1)^40 -> (2y+1)^40 (variable->variable).
	r2, ok := it.refSubs(big, [][2]Value{{Name{"x"}, Name{"y"}}})
	if !ok {
		t.Fatal("refSubs(x=y) did not take the server-side path")
	}
	want2, err := it.Exec("expand((2*y+1)^40);")
	if err != nil {
		t.Fatalf("native expand (2y+1)^40: %v", err)
	}
	if compareValues(concrete(r2), concrete(want2)) != 0 {
		t.Errorf("subs(x=y, big) != expand((2*y+1)^40)")
	}

	// The big ref itself must NOT have been materialized by either subs.
	if _, done := big.materialized(); done {
		t.Errorf("server-side subs materialized the big ref")
	}

	// A compound LHS (x+y = 0) is syntactic, not a ring substitution: refSubs must
	// decline (ok=false) so the caller uses the native materializing path.
	if _, ok := it.refSubs(big, [][2]Value{{&Sum{[]Value{Name{"x"}, Name{"y"}}}, newInt(0)}}); ok {
		t.Errorf("refSubs took the server-side path for a compound LHS (x+y); must fall back")
	}
}

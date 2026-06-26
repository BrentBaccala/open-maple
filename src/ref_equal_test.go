package main

import "testing"

// TestSageRefEquality exercises the server-side equality path (refEqual ->
// op_is_zero): equality/is-zero tests on a big *SageRef are answered Sage-side
// and the ref is NOT materialized. This is the dominant ref->native collapse in
// the DifferentialThomas control flow (truth() of `p = 0` in if/and/or), so the
// load-bearing assertion is that refsMaterialized does not move.
//
// Expected truth values are by independent algebraic reasoning, never Maple.
func TestSageRefEquality(t *testing.T) {
	it := sageInterp(t)
	be, ok := it.cas.(*SageBackend)
	if !ok {
		t.Skip("no Sage backend")
	}

	big := makeBigRef(t, it)                 // (x+y+1)^40, 861-term ref
	dbl, err := it.arithMul(big, newInt(2))  // 2*(x+y+1)^40, stays a ref
	if err != nil {
		t.Fatalf("big*2: %v", err)
	}
	indep, err := it.Exec("expand((x+y+1)^40);") // independent ref, == big
	if err != nil {
		t.Fatalf("independent expand: %v", err)
	}

	matBefore := be.refsMaterialized

	cases := []struct {
		name string
		a, b Value
		want bool
	}{
		{"big = big (same ref)", big, big, true},
		{"big = independent equal ref", big, indep, true},
		{"big = 0", big, newInt(0), false},
		{"0 = big (ref on right)", newInt(0), big, false},
		{"big = 2*big", big, dbl, false},
		{"big <> 2*big via !equal", dbl, big, false},
	}
	for _, c := range cases {
		if got := equalValues(c.a, c.b); got != c.want {
			t.Errorf("%s: equalValues = %v, want %v", c.name, got, c.want)
		}
	}

	// The whole point: none of the equality tests pulled a ref across the wire.
	if be.refsMaterialized != matBefore {
		t.Errorf("ref-equality tests materialized %d ref(s); must stay server-side (0)",
			be.refsMaterialized-matBefore)
	}
}

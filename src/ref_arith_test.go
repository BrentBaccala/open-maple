package main

import (
	"testing"
)

// makeBigRef forces the Sage backend to return a *SageRef for a genuinely large
// polynomial, bypassing the native-poly fast paths (which would compute small
// results in Go and never produce a ref). We call the backend op directly with a
// big {"poly"} arg; the size-gated enc_poly refs it because it is far above the
// term threshold.
func makeBigRef(t *testing.T, it *Interp) *SageRef {
	t.Helper()
	// (x+y+1)^40 expands to C(42,2)=861 terms; "factor" is routed to Sage and is
	// not a native-poly op, but it returns a factored form. Instead use "collect"
	// on the expanded form — collect returns a single polynomial. Build the big
	// poly as a Power value and run expand THROUGH the backend (not the native
	// fast path) via cas.Call.
	big := &Power{Base: &Sum{[]Value{Name{"x"}, Name{"y"}, newInt(1)}}, Exp: newInt(40)}
	v, err := it.cas.Call("expand", []Value{big})
	if err != nil {
		t.Fatalf("cas.Call expand: %v", err)
	}
	ref, ok := v.(*SageRef)
	if !ok {
		t.Fatalf("expected a *SageRef for expand of an 861-term poly, got %T", v)
	}
	return ref
}

// TestSageRefArithmetic exercises the server-side ref-operand arithmetic path:
// a big polynomial kept as a *SageRef flows through add/sub/mul/neg/pow without
// being materialized, the chain returns the right value, and a small/constant
// result comes back INLINE (not a ref).
//
// All expected values are computed by independent algebraic reasoning, never
// from Maple.
func TestSageRefArithmetic(t *testing.T) {
	it := sageInterp(t)
	big := makeBigRef(t, it)

	// big - big == 0, and the zero result must be INLINE (Integer 0), not a ref.
	zero, err := it.arithAdd(big, it.neg(big))
	if err != nil {
		t.Fatalf("big - big: %v", err)
	}
	if _, isRef := zero.(*SageRef); isRef {
		t.Errorf("big - big returned a ref; a zero result must be inline")
	}
	if !isZero(concrete(zero)) {
		t.Errorf("big - big = %s, want 0", printValue(concrete(zero)))
	}

	// big * 0 == 0 inline.
	z2, err := it.arithMul(big, newInt(0))
	if err != nil {
		t.Fatalf("big * 0: %v", err)
	}
	if _, isRef := z2.(*SageRef); isRef {
		t.Errorf("big * 0 returned a ref; must be inline 0")
	}
	if !isZero(concrete(z2)) {
		t.Errorf("big * 0 = %s, want 0", printValue(concrete(z2)))
	}

	// big * 2 stays a ref (still 861 terms) — the arithmetic dispatched to Sage
	// and the result was NOT materialized.
	dbl, err := it.arithMul(big, newInt(2))
	if err != nil {
		t.Fatalf("big * 2: %v", err)
	}
	if _, ok := dbl.(*SageRef); !ok {
		t.Errorf("big * 2 should stay a ref, got %T", dbl)
	}

	// (big * 2) - big - big == 0 — a multi-step ref chain.
	t1, err := it.arithAdd(dbl, it.neg(big))
	if err != nil {
		t.Fatalf("dbl - big: %v", err)
	}
	t2, err := it.arithAdd(t1, it.neg(big))
	if err != nil {
		t.Fatalf("(dbl - big) - big: %v", err)
	}
	if !isZero(concrete(t2)) {
		t.Errorf("2*big - big - big = %s, want 0", printValue(concrete(t2)))
	}

	// Round-trip equivalence vs the native-path expansion of 2*(x+y+1)^40.
	native, err := it.Exec("expand(2*(x+y+1)^40);")
	if err != nil {
		t.Fatalf("native expand: %v", err)
	}
	if compareValues(concrete(dbl), concrete(native)) != 0 {
		t.Errorf("ref big*2 != native 2*big (order-independent compare failed)")
	}

	// big^2 stays a ref and equals native expand((x+y+1)^80).
	sq, err := it.arithPow(big, newInt(2))
	if err != nil {
		t.Fatalf("big^2: %v", err)
	}
	if _, ok := sq.(*SageRef); !ok {
		t.Errorf("big^2 should stay a ref, got %T", sq)
	}
	nativeSq, err := it.Exec("expand((x+y+1)^80);")
	if err != nil {
		t.Fatalf("native expand ^80: %v", err)
	}
	if compareValues(concrete(sq), concrete(nativeSq)) != 0 {
		t.Errorf("ref big^2 != native (x+y+1)^80")
	}
}

// TestSageRefArithSmallInline confirms a small result never comes back as a ref.
func TestSageRefArithSmallInline(t *testing.T) {
	it := sageInterp(t)

	// gcd is small and inline.
	if got := execStr(t, it, "gcd(x^2-1, x-1);"); got != "x - 1" {
		t.Fatalf("gcd = %q, want x - 1", got)
	}

	// A ref minus itself plus a small poly: result is small -> inline.
	big := makeBigRef(t, it)
	// big + (-big) + (x+1): the big part cancels, leaving x+1 inline.
	s1, err := it.arithAdd(big, it.neg(big))
	if err != nil {
		t.Fatalf("big-big: %v", err)
	}
	s2, err := it.arithAdd(s1, &Sum{[]Value{Name{"x"}, newInt(1)}})
	if err != nil {
		t.Fatalf("+ (x+1): %v", err)
	}
	if _, isRef := s2.(*SageRef); isRef {
		t.Errorf("small result (x+1) came back as a ref; must be inline")
	}
	if compareValues(concrete(s2), &Sum{[]Value{Name{"x"}, newInt(1)}}) != 0 {
		t.Errorf("big-big+(x+1) = %s, want x + 1", printValue(concrete(s2)))
	}
}

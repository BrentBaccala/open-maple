package main

import "testing"

// TestSageRefType exercises the server-free type() path (refTypeCheck): on a
// live *SageRef the algebraic predicates resolve true and the numeric ones
// false without materializing the polynomial. The load-bearing assertion is
// that refsMaterialized does not move; correctness of the whitelisted answers is
// pinned against the native classification (a ref always materializes to a
// compound *Sum/*Prod/*Power, so these are the native results too).
func TestSageRefType(t *testing.T) {
	it := sageInterp(t)
	be, ok := it.cas.(*SageBackend)
	if !ok {
		t.Skip("no Sage backend")
	}

	big := makeBigRef(t, it) // (x+y+1)^40, 861-term ref
	matBefore := be.refsMaterialized

	cases := []struct {
		typ  string
		want bool
	}{
		{"polynom", true}, {"ratpoly", true}, {"algebraic", true}, {"anything", true},
		{"integer", false}, {"numeric", false}, {"constant", false}, {"realcons", false},
		{"rational", false}, {"fraction", false}, {"float", false}, {"posint", false},
		{"nonnegint", false}, {"positive", false},
	}
	for _, c := range cases {
		got, err := it.checkTypeValue(big, Name{c.typ})
		if err != nil {
			t.Fatalf("type(big, %s): %v", c.typ, err)
		}
		if got != c.want {
			t.Errorf("type(big, %s) = %v, want %v", c.typ, got, c.want)
		}
	}

	// The whole point: deciding these types pulled nothing across the wire.
	if be.refsMaterialized != matBefore {
		t.Errorf("type tests materialized %d ref(s); whitelisted types must stay server-free (0)",
			be.refsMaterialized-matBefore)
	}

	// A form-dependent type (`+`) is NOT whitelisted: it falls back and may
	// materialize. We only assert it answers correctly (big expands to a Sum).
	if got, err := it.checkTypeValue(big, Name{"+"}); err != nil {
		t.Fatalf("type(big, `+`): %v", err)
	} else if !got {
		t.Errorf("type(big, `+`) = false; expanded (x+y+1)^40 is a sum")
	}
}

package main

import "testing"

// TestNativeDegree pins native degree() against Maple semantics, including the
// cancellation case (terms that vanish must not inflate the degree) that the
// verify harness first caught.
func TestNativeDegree(t *testing.T) {
	it := NewInterp()
	cases := []struct{ expr, want string }{
		{"degree(u[1,0]^2*u[0,0] + u[1,0], u[1,0]);", "2"},
		{"degree(u[1,0]^2*u[0,0] + u[1,0]);", "3"}, // total degree
		{"degree(3, u[1,0]);", "0"},
		{"degree(0, u[1,0]);", "-infinity"},
		{"degree(0);", "-infinity"},
		// cancellation: the u[1,1] terms cancel, so degree in u[1,1] is 0
		{"degree(-2*u[0,0]*u[1,0] - u[1,1] - (u[0,1] - u[1,1]), u[1,1]);", "0"},
		// degree over a set/list of variables = max total degree among them
		{"degree(u[1,0]*u[0,1] + u[1,0]^3, {u[1,0],u[0,1]});", "3"},
		{"degree((u[1,0]+1)*(u[1,0]+2), u[1,0]);", "2"}, // unexpanded product
		{"degree((u[1,0]+u[0,1])*(u[1,0]-u[0,1]), u[1,0]);", "2"},
	}
	for _, c := range cases {
		v, err := it.Exec(c.expr)
		if err != nil {
			t.Fatalf("%s err: %v", c.expr, err)
		}
		if got := printValue(v); got != c.want {
			t.Fatalf("%s: got %q, want %q", c.expr, got, c.want)
		}
	}
}

// TestNativeIndets pins native indets(): the set of indeterminates that actually
// appear (cancelled variables are dropped).
func TestNativeIndets(t *testing.T) {
	it := NewInterp()
	cases := []struct{ expr, want string }{
		{"indets(u[1,0]*u[0,1] + u[0,0]);", "{u[0, 0], u[0, 1], u[1, 0]}"},
		{"indets(3);", "{}"},
		{"indets(u[1,0] - u[1,0]);", "{}"},                 // cancels to 0
		{"indets(u[1,1] + u[0,1] - u[1,1]);", "{u[0, 1]}"}, // u[1,1] cancels
		{"indets(x*u[0,0]^2);", "{x, u[0, 0]}"},
	}
	for _, c := range cases {
		v, err := it.Exec(c.expr)
		if err != nil {
			t.Fatalf("%s err: %v", c.expr, err)
		}
		if got := printValue(v); got != c.want {
			t.Fatalf("%s: got %q, want %q", c.expr, got, c.want)
		}
	}
}

// TestNativeExpand pins native expand(): fully distributed, collected, printed
// in descending total degree (matching Sage's str()). Cancellation drops terms.
func TestNativeExpand(t *testing.T) {
	it := NewInterp()
	cases := []struct{ expr, want string }{
		{"expand((u[1,0]+1)^2);", "u[1, 0]^2 + 2*u[1, 0] + 1"},
		// same total degree -> tiebroken by native canonical key (NOT Sage's
		// degrevlex var order); semantically equal, surface order may differ.
		{"expand((u[1,0]+u[0,1])*(u[1,0]-u[0,1]));", "-u[0, 1]^2 + u[1, 0]^2"},
		{"expand(u[0,0]*(u[1,0]+2));", "u[0, 0]*u[1, 0] + 2*u[0, 0]"},
		{"expand(u[1,0] - u[1,0]);", "0"},
		{"expand(3);", "3"},
	}
	for _, c := range cases {
		v, err := it.Exec(c.expr)
		if err != nil { t.Fatalf("%s err: %v", c.expr, err) }
		if got := printValue(v); got != c.want {
			t.Fatalf("%s: got %q, want %q", c.expr, got, c.want)
		}
	}
}

// TestNativeCoeff pins native coeff(p, x, n): the coefficient of x^n, a
// polynomial in the remaining variables.
func TestNativeCoeff(t *testing.T) {
	it := NewInterp()
	cases := []struct{ expr, want string }{
		{"coeff(u[1,0]^2 + 3*u[1,0] + 5, u[1,0], 1);", "3"},
		{"coeff(u[1,0]^2 + 3*u[1,0] + 5, u[1,0], 2);", "1"},
		{"coeff(u[1,0]^2 + 3*u[1,0] + 5, u[1,0], 0);", "5"},
		{"coeff(u[0,0]*u[1,0] + u[1,0], u[1,0]);", "u[0, 0] + 1"},
		{"coeff(u[0,0]*u[1,0] + u[1,0], u[1,0], 1);", "u[0, 0] + 1"},
	}
	for _, c := range cases {
		v, err := it.Exec(c.expr)
		if err != nil { t.Fatalf("%s err: %v", c.expr, err) }
		if got := printValue(v); got != c.want {
			t.Fatalf("%s: got %q, want %q", c.expr, got, c.want)
		}
	}
}

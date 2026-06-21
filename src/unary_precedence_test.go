package main

import "testing"

// TestUnaryMinusPrecedence pins that a prefix +/- binds looser than ^, so
// -b^2 parses as -(b^2), not (-b)^2. This was a latent parser bug: it only
// surfaced when a Sage result string led with a negative power term (e.g.
// "-u_0_1^2 + u_1_0^2", which Sage emits when the ring var order puts u_0_1
// first), and parseBack then silently flipped the sign — corrupting the value.
func TestUnaryMinusPrecedence(t *testing.T) {
	it := NewInterp()
	cases := []struct{ expr, want string }{
		{"-b^2;", "-b^2"},
		{"-b^2 + a^2;", "-b^2 + a^2"},
		{"-b^3 + 1;", "-b^3 + 1"},
		{"-2^2;", "-4"},
		{"a - b^2;", "a - b^2"},
		{"-b^2*c;", "-b^2*c"},
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

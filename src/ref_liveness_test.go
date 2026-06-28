package main

import (
	"strings"
	"testing"
)

// TestEncodeArgPrefersLiveRef pins the ref-liveness fix: a SageRef whose
// server-side cache entry is still live (same generation AND clearEpoch) is sent
// by handle {"ref":N} even after it has been materialized Go-side, instead of
// being downgraded to its (possibly multi-MB) string. A Go-side materialize for
// an inspection (e.g. the indets token-scan) must NOT poison the ref into
// re-shipping its string on every later op — the residual ping-pong that drove
// the cell1+PDE RSS blow-up (sage_server RSS 41->92 GB, tripped the 70 GB guard).
//
// It also pins the two genuine loss cases (a full ClearCache and a server
// restart): a materialized ref falls back to its string, an unmaterialized one
// fails honestly. No Sage backend needed — encodeArg's ref branch is pure Go.
func TestEncodeArgPrefersLiveRef(t *testing.T) {
	san := newSanitizer()
	liveRef := func(s *SageBackend, id int) *SageRef {
		return &SageRef{be: s, id: id, gen: s.generation, ce: s.clearEpoch, san: san}
	}

	// 1. Live, never materialized -> by handle, and refArgsSent bumped so the
	//    request's hasRef flag (refArgsSent delta) is set.
	t.Run("live/unmaterialized", func(t *testing.T) {
		s := &SageBackend{useRefs: true, generation: 1, clearEpoch: 0}
		b, err := s.encodeArg("add", liveRef(s, 7), san)
		if err != nil {
			t.Fatal(err)
		}
		if got := string(b); got != `{"ref":7}` {
			t.Errorf("got %s, want {\"ref\":7}", got)
		}
		if s.refArgsSent != 1 {
			t.Errorf("refArgsSent = %d, want 1 (drives req.hasRef)", s.refArgsSent)
		}
	})

	// 2. Live AND already materialized Go-side -> STILL by handle. This is the fix:
	//    the materialize is a pure server read, so the cache entry is intact.
	t.Run("live/materialized", func(t *testing.T) {
		s := &SageBackend{useRefs: true, generation: 1, clearEpoch: 0}
		r := liveRef(s, 9)
		r.val = newInt(5) // simulate a prior materialize (e.g. an indets inspection)
		b, err := s.encodeArg("mul", r, san)
		if err != nil {
			t.Fatal(err)
		}
		if got := string(b); got != `{"ref":9}` {
			t.Errorf("got %s, want {\"ref\":9} (a materialized-but-live ref must not re-ship its string)", got)
		}
		if s.refArgsSent != 1 {
			t.Errorf("refArgsSent = %d, want 1", s.refArgsSent)
		}
	})

	// 3. After a full ClearCache (clearEpoch bumped), a materialized ref falls back
	//    to its concrete string value and is NOT sent by handle.
	t.Run("cleared/materialized", func(t *testing.T) {
		s := &SageBackend{useRefs: true, generation: 1, clearEpoch: 0}
		r := liveRef(s, 11)
		r.val = newInt(5)
		s.clearEpoch++ // a ClearCache happened after the ref was issued
		b, err := s.encodeArg("mul", r, san)
		if err != nil {
			t.Fatal(err)
		}
		if got := string(b); got != `{"int":"5"}` {
			t.Errorf("got %s, want {\"int\":\"5\"}", got)
		}
		if s.refArgsSent != 0 {
			t.Errorf("refArgsSent = %d, want 0 (a cleared ref must not be sent by handle)", s.refArgsSent)
		}
	})

	// 4. After a full ClearCache, a ref that was never materialized is unrecoverable
	//    and must fail honestly (clear-site invariant says this should not arise).
	t.Run("cleared/unmaterialized", func(t *testing.T) {
		s := &SageBackend{useRefs: true, generation: 1, clearEpoch: 0}
		r := liveRef(s, 13)
		s.clearEpoch++
		if _, err := s.encodeArg("mul", r, san); err == nil ||
			!strings.Contains(err.Error(), "cache clear") {
			t.Errorf("want a cache-clear error, got %v", err)
		}
	})

	// 5. After a server restart (generation bumped) the gen check fires regardless
	//    of clearEpoch: materialized -> string, unmaterialized -> restart error.
	t.Run("restart", func(t *testing.T) {
		s := &SageBackend{useRefs: true, generation: 1, clearEpoch: 0}
		mat := liveRef(s, 15)
		mat.val = newInt(5)
		un := liveRef(s, 16)
		s.generation++ // restart empties the cache
		b, err := s.encodeArg("mul", mat, san)
		if err != nil {
			t.Fatal(err)
		}
		if got := string(b); got != `{"int":"5"}` {
			t.Errorf("materialized after restart: got %s, want {\"int\":\"5\"}", got)
		}
		if _, err := s.encodeArg("mul", un, san); err == nil ||
			!strings.Contains(err.Error(), "server restart") {
			t.Errorf("unmaterialized after restart: want server-restart error, got %v", err)
		}
	})
}

// TestEncodeExprlistPrefersLiveRef pins the same liveness rule for list/set args
// (indets([... bigpoly ...]), coeffs over a list): a live element ref is kept as
// a handle even after materialize; a cleared element falls back / fails like the
// scalar path.
func TestEncodeExprlistPrefersLiveRef(t *testing.T) {
	san := newSanitizer()
	s := &SageBackend{useRefs: true, generation: 1, clearEpoch: 0}
	live := &SageRef{be: s, id: 21, gen: 1, ce: 0, san: san}
	matLive := &SageRef{be: s, id: 22, gen: 1, ce: 0, san: san}
	matLive.val = newInt(8) // materialized but still live

	b, err := s.encodeExprlist([]Value{live, matLive}, san)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, `{"ref":21}`) || !strings.Contains(got, `{"ref":22}`) {
		t.Errorf("exprlist = %s, want both elements sent by handle ({\"ref\":21} and {\"ref\":22})", got)
	}
	if s.refArgsSent != 2 {
		t.Errorf("refArgsSent = %d, want 2", s.refArgsSent)
	}
}

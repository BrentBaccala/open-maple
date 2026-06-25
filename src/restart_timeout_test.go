package main

import (
	"strings"
	"testing"
	"time"
)

// These tests pin the timeout-vs-death and honest-restart fixes (the bug that
// killed the combined-hydrogen run after 2h34m: a heavy server-side `add` blew
// the 120 s timeout, the alive-but-busy Sage server was SIGKILLed, and the op was
// resent to a fresh empty ref cache, surfacing as a misleading
// "unknown expression ref: 292 (cache has 0 entries)" panic). They need no Sage
// backend — they exercise the pure Go dispatch logic directly.

func TestEnvDurationSeconds(t *testing.T) {
	def := 120 * time.Second
	t.Setenv("OM_TEST_DUR", "")
	if got := envDurationSeconds("OM_TEST_DUR", def); got != def {
		t.Fatalf("empty -> default: got %v want %v", got, def)
	}
	t.Setenv("OM_TEST_DUR", "300")
	if got := envDurationSeconds("OM_TEST_DUR", def); got != 300*time.Second {
		t.Fatalf("300 -> 5m: got %v", got)
	}
	t.Setenv("OM_TEST_DUR", "garbage")
	if got := envDurationSeconds("OM_TEST_DUR", def); got != def {
		t.Fatalf("garbage -> default: got %v", got)
	}
	// 0 (and negatives) mean "effectively unbounded".
	t.Setenv("OM_TEST_DUR", "0")
	if got := envDurationSeconds("OM_TEST_DUR", def); got < 100*365*24*time.Hour {
		t.Fatalf("0 -> unbounded: got %v (expected a very large duration)", got)
	}
}

func TestTimeoutForHeavyVsCheap(t *testing.T) {
	s := &SageBackend{timeout: 120 * time.Second, heavyTimeout: 3600 * time.Second}
	// Arithmetic and other big-poly compute ops get the generous budget — a slow
	// add on a multi-MB polynomial is real work, not a hang.
	for _, op := range []string{"add", "sub", "mul", "neg", "pow", "prem", "normal", "factor", "indets", "gcd"} {
		if got := s.timeoutFor(op); got != s.heavyTimeout {
			t.Errorf("timeoutFor(%q) = %v, want heavyTimeout %v", op, got, s.heavyTimeout)
		}
	}
	// Structural / metadata ops keep the short liveness timeout.
	for _, op := range []string{"whattype", "type", "op", "nops", "diff", "subs"} {
		if got := s.timeoutFor(op); got != s.timeout {
			t.Errorf("timeoutFor(%q) = %v, want timeout %v", op, got, s.timeout)
		}
	}
}

// TestStaleRefEncodeGuard: a ref issued in a prior server generation must not be
// sent — after a restart the server-side cache is empty, so the ref body is gone.
// encodeArg must refuse it with a clear, non-misleading error instead of emitting
// {"ref":N} for a cache that no longer holds it.
func TestStaleRefEncodeGuard(t *testing.T) {
	s := &SageBackend{useRefs: true, generation: 2}
	san := newSanitizer()

	// Same-generation ref encodes fine as a handle.
	live := &SageRef{be: s, id: 7, gen: 2}
	enc, err := s.encodeArg("add", live, san)
	if err != nil {
		t.Fatalf("live ref should encode: %v", err)
	}
	if !strings.Contains(string(enc), `"ref"`) {
		t.Fatalf("live ref should encode as a handle, got %s", string(enc))
	}

	// A ref from generation 1, after a restart bumped us to generation 2, is stale.
	stale := &SageRef{be: s, id: 292, gen: 1}
	if _, err := s.encodeArg("add", stale, san); err == nil {
		t.Fatalf("stale ref should be refused, but encodeArg succeeded")
	} else if !strings.Contains(err.Error(), "server restart") {
		t.Fatalf("stale-ref error should name the cause, got: %v", err)
	}

	// Same guard inside a list/set operand (the indets([... bigref ...]) path).
	if _, err := s.encodeExprlist([]Value{stale}, san); err == nil {
		t.Fatalf("stale ref in exprlist should be refused")
	} else if !strings.Contains(err.Error(), "server restart") {
		t.Fatalf("stale-ref exprlist error should name the cause, got: %v", err)
	}
}

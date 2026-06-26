package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// sageCallCount counts Sage round-trips when OPENMAPLE_SAGE_TRACE is set — a
// debugging aid to tell a slow-but-progressing computation from a stuck one.
var sageCallCount int

// SageBackend implements the CAS interface by talking JSON-lines over
// stdin/stdout to a long-lived Sage subprocess (cas/sage_server.py).
//
// One request per line, one response per line. The subprocess does
// `from sage.all import *` once (~9s) then loops; CAS calls are fast.
//
// Domain: characteristic 0 over QQ. The FactorStrong tower-RootOf path is not
// implemented (the server returns a structured error).
type SageBackend struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	nextID  int
	debug   bool
	sageBin string
	script  string
	// timeout / heavyTimeout are per-op wall budgets. Both now default to 0
	// (unbounded): the op timeout is OFF by default. Rationale: with useRefs on,
	// essentially every heavy op carries a server-side ref, and roundtrip can never
	// retry a ref-bearing request across a restart — so a timeout there does not
	// recover anything, it only converts a slow-but-alive op into a dead,
	// unrecoverable run (this killed the cell1+PDE hydrogen staging run: an lcoeff
	// on a multi-MB operand blew the old 120 s liveness bound). An actual server
	// *crash* is still caught instantly by send's read-error path (EOF/broken pipe),
	// independent of any timeout; the timeout only ever guarded against a
	// live-but-wedged server (rare), so removing it costs almost nothing. The knobs
	// remain env-overridable (OPENMAPLE_SAGE_TIMEOUT, OPENMAPLE_SAGE_HEAVY_TIMEOUT,
	// in seconds) for anyone who wants a finite liveness bound back; see
	// timeoutFor / heavyOps.
	timeout      time.Duration
	heavyTimeout time.Duration
	dead         bool
	// generation counts server (re)starts. Each SageRef records the generation it
	// was issued in; a restart empties the server-side ref cache, so any ref from a
	// prior generation is permanently invalid (its body is gone). encodeArg refuses
	// to send a stale-generation ref, and roundtrip refuses to resend a ref-bearing
	// request across a restart — both turning the old silent "unknown ref / cache 0
	// entries" corruption into an honest, clearly-labelled error.
	generation int
	// useRefs enables the expression-handle optimization: poly/rational results
	// are kept Sage-side and returned as {"ref":N} handles, materialized to a
	// string only when the Go side must look inside. On by default; disable with
	// OPENMAPLE_DISABLE_REFS=1 to fall back to the pure-string protocol (identical
	// correctness — a bisection switch like OPENMAPLE_DISABLE_NATIVE).
	useRefs bool
	// ref-traffic counters (OPENMAPLE_SAGE_TRACE / measurement): how many result
	// handles were issued vs materialized, and how many {"ref"} vs {"poly"} args
	// were sent. Read by the example-suite runner via the trailing summary.
	refsIssued      int
	refsMaterialized int
	refArgsSent     int
	polyArgsSent    int
	refsFreed       int

	// pendingFree collects the server-side ids of SageRefs whose Go handle has
	// been garbage-collected (their finalizer ran). They are batch-cleared from
	// the server cache on the next roundtrip — see flushPendingFree. A finalizer
	// must not itself do a blocking round-trip (it runs on the GC's finalizer
	// goroutine and could deadlock against an in-flight call holding s.mu), so it
	// only appends here under freeMu, and the next ordinary call drains the batch.
	freeMu      sync.Mutex
	pendingFree []int
}

// sageRequest / sageResponse are the wire types.
type sageRequest struct {
	ID      int               `json:"id"`
	Op      string            `json:"op"`
	Member  string            `json:"member,omitempty"`
	Vars    []string          `json:"vars"`
	Args    []json.RawMessage `json:"args"`
	Frac    bool              `json:"frac,omitempty"`
	Debug   bool              `json:"debug,omitempty"`
	WantRef bool              `json:"want_ref,omitempty"`
	// hasRef is set (Go-side only, never serialized) when any encoded arg carries a
	// {"ref":N} handle. roundtrip uses it to refuse resending the request across a
	// server restart — the fresh server's cache is empty, so the ref is gone.
	hasRef bool `json:"-"`
}

type sageResponse struct {
	ID     int             `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}

// SageRef is a Value that stands in for a poly/rational expression kept Sage-side
// behind an opaque integer handle. It is produced by decodeResult when a result
// arrives as {"ref":N} and lets a chain of Sage→Sage ops flow without ever
// serializing the (possibly ~200 KB) expression string into Go.
//
// Lazy materialization: any Go code that must inspect the concrete expression
// (printing, equality/comparison, op/nops/whattype, the native polynomial layer,
// arithmetic, save) calls materialize(), which does a single `materialize`
// round-trip and caches the result on the ref — so the string is fetched at most
// once. When a SageRef is passed BACK as an operand to another Sage op,
// encodeArg emits {"ref":N} and no materialization occurs.
type SageRef struct {
	be  *SageBackend
	id  int
	gen int        // server generation this ref was issued in (see SageBackend.generation)
	san *sanitizer // the sanitizer in effect when the ref was created (for parseBack)

	once sync.Once
	val  Value // cached materialized value (set on first materialize)
	err  error
}

func (*SageRef) isValue() {}

// materialize fetches the concrete Value for this ref, at most once. After this
// returns, ref.val holds the parsed expression in the original (unsanitized)
// form, exactly as the string protocol would have produced.
func (r *SageRef) materialize() (Value, error) {
	r.once.Do(func() {
		r.be.mu.Lock()
		r.be.refsMaterialized++
		r.be.mu.Unlock()
		req := &sageRequest{
			Op:   "materialize",
			Args: []json.RawMessage{json.RawMessage(fmt.Sprintf(`{"ref":%d}`, r.id))},
		}
		resp, err := r.be.roundtrip(req)
		if err != nil {
			r.err = err
			return
		}
		if !resp.OK {
			r.err = fmt.Errorf("sage materialize ref %d: %s", r.id, resp.Error)
			return
		}
		v, err := r.be.decodeResult("materialize", resp.Result, r.san)
		if err != nil {
			r.err = err
			return
		}
		r.val = v
		if refTrace {
			// This is a ref->native collapse: ref r.id is now a Go-side AST and
			// every later Sage op on it re-stringifies len(resp.Result) bytes.
			// traceCallers names the inspection (op/nops/type/print/native-poly/
			// comparison) that forced it.
			fmt.Fprintf(stderrW(), "[ref-materialize] ref=%d bytes=%d via %s\n",
				r.id, len(resp.Result), traceCallers(8))
		}
	})
	return r.val, r.err
}

// materialized reports whether this ref has already been resolved to a concrete
// value (and returns it). It does NOT trigger a round-trip — it only reads the
// cached result, so it is safe to call after the server-side handle was cleared.
func (r *SageRef) materialized() (Value, bool) {
	if r.val != nil && r.err == nil {
		return r.val, true
	}
	return nil, false
}

// concrete materializes v if it is a SageRef, otherwise returns it unchanged.
// This is the single guard every interpreter inspection point calls before
// looking inside a Value. A materialization error surfaces as a panic carrying a
// maple error — server restart mid-run loses cache state, and an unknown-ref
// materialize is a hard error per the protocol (not silently recoverable).
func concrete(v Value) Value {
	r, ok := v.(*SageRef)
	if !ok {
		return v
	}
	mv, err := r.materialize()
	if err != nil {
		panic(newMapleError("sage ref materialize failed: " + err.Error()))
	}
	return mv
}

// materializeDeep recursively forces every SageRef reachable from v to its
// concrete value. Used before ClearCache so surviving handles are never
// stranded. `seen` guards against cyclic/shared structures (Tables can be
// shared). It mutates nothing structurally — a SageRef caches its own value — so
// the same Value objects remain in place, now backed by materialized refs.
func materializeDeep(v Value, seen map[Value]bool) {
	switch n := v.(type) {
	case *SageRef:
		mv := concrete(n) // forces materialize (once)
		materializeDeep(mv, seen)
	case Seq:
		for _, it := range n.Items {
			materializeDeep(it, seen)
		}
	case List:
		for _, it := range n.Items {
			materializeDeep(it, seen)
		}
	case Set:
		for _, it := range n.Items {
			materializeDeep(it, seen)
		}
	case *Sum:
		for _, t := range n.Terms {
			materializeDeep(t, seen)
		}
	case *Prod:
		for _, f := range n.Factors {
			materializeDeep(f, seen)
		}
	case *Power:
		materializeDeep(n.Base, seen)
		materializeDeep(n.Exp, seen)
	case *Func:
		materializeDeep(n.Head, seen)
		for _, a := range n.Args {
			materializeDeep(a, seen)
		}
	case *Indexed:
		materializeDeep(n.Head, seen)
		for _, ix := range n.Idx {
			materializeDeep(ix, seen)
		}
	case *Equation:
		materializeDeep(n.Lhs, seen)
		materializeDeep(n.Rhs, seen)
	case *Relation:
		materializeDeep(n.Lhs, seen)
		materializeDeep(n.Rhs, seen)
	case *Range:
		materializeDeep(n.Lo, seen)
		materializeDeep(n.Hi, seen)
	case *Table:
		if seen[v] {
			return
		}
		seen[v] = true
		for _, val := range n.Vals {
			materializeDeep(val, seen)
		}
		for _, key := range n.Keys {
			materializeDeep(key, seen)
		}
	}
}

// concreteSlice materializes any SageRefs in a slice (shallow), returning a new
// slice only if a substitution occurred.
func concreteSlice(vs []Value) []Value {
	changed := false
	out := vs
	for i, v := range vs {
		if _, ok := v.(*SageRef); ok {
			if !changed {
				out = make([]Value, len(vs))
				copy(out, vs)
				changed = true
			}
			out[i] = concrete(v)
		}
	}
	return out
}

// newSageBackend constructs (but does not start) a Sage backend. The Sage
// binary defaults to ~/miniforge3/envs/sage/bin/sage, overridable via
// OPENMAPLE_SAGE. The server script is cas/sage_server.py relative to the
// open-maple root (found via OPENMAPLE_ROOT or by walking up from cwd).
func newSageBackend() (*SageBackend, error) {
	sageBin := os.Getenv("OPENMAPLE_SAGE")
	if sageBin == "" {
		home, _ := os.UserHomeDir()
		sageBin = filepath.Join(home, "miniforge3", "envs", "sage", "bin", "sage")
	}
	script, err := findSageServerScript()
	if err != nil {
		return nil, err
	}
	b := &SageBackend{
		sageBin:      sageBin,
		script:       script,
		debug:        os.Getenv("OPENMAPLE_CAS_DEBUG") != "",
		timeout:      envDurationSeconds("OPENMAPLE_SAGE_TIMEOUT", 0),
		heavyTimeout: envDurationSeconds("OPENMAPLE_SAGE_HEAVY_TIMEOUT", 0),
		useRefs:      os.Getenv("OPENMAPLE_DISABLE_REFS") == "",
	}
	return b, nil
}

// envDurationSeconds reads an integer-seconds env var, falling back to def. A
// value of 0 means "no timeout" (an effectively-unbounded wait), for the user
// who would rather a genuinely-slow op block than abort.
func envDurationSeconds(name string, def time.Duration) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	if n <= 0 {
		// Effectively unbounded: a very large duration the run will never reach.
		return time.Duration(1<<62 - 1)
	}
	return time.Duration(n) * time.Second
}

// heavyOps are compute-heavy CAS ops that operate on big polynomials: a slow call
// here is real work, not a hung server, so it gets heavyTimeout rather than the
// short liveness timeout. Centered on the server-side arithmetic ops (the ones
// task 435 moved off the instant native path and onto the Sage round-trip), plus
// the pseudo-division / normalization / factoring family that the combined
// no-end-reduction system drives on multi-MB operands. Structural/metadata ops
// (whattype, type, op, nops, …) are intentionally absent: they must stay snappy,
// and a hang there is a real bug worth catching fast.
var heavyOps = map[string]bool{
	// arithmetic (server-side ref arithmetic — task 435)
	"add": true, "sub": true, "mul": true, "neg": true, "pow": true,
	// pseudo-division and division
	"prem": true, "pquo": true, "sprem": true, "quo": true, "rem": true,
	// normalization / structure on big polys
	"normal": true, "numer": true, "denom": true, "expand": true,
	"simplify": true, "collect": true, "content": true, "primpart": true,
	"indets": true, "evala": true, "coeff": true, "coeffs": true,
	"degree": true, "ldegree": true,
	// gcd / factoring
	"gcd": true, "lcm": true, "factor": true, "factors": true, "gcdex": true,
}

// timeoutFor returns the wall budget for one call of op: the generous heavyTimeout
// for compute-heavy poly ops, the short liveness timeout otherwise.
func (s *SageBackend) timeoutFor(op string) time.Duration {
	if heavyOps[op] {
		return s.heavyTimeout
	}
	return s.timeout
}

// findSageServerScript locates cas/sage_server.py.
func findSageServerScript() (string, error) {
	if r := os.Getenv("OPENMAPLE_ROOT"); r != "" {
		p := filepath.Join(r, "cas", "sage_server.py")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	// Walk up from cwd looking for cas/sage_server.py.
	dir, _ := os.Getwd()
	for i := 0; i < 6; i++ {
		p := filepath.Join(dir, "cas", "sage_server.py")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// Fall back to ~/open-maple/cas/sage_server.py.
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, "open-maple", "cas", "sage_server.py")
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("cannot locate cas/sage_server.py (set OPENMAPLE_ROOT)")
}

// start launches the Sage subprocess.
func (s *SageBackend) start() error {
	if _, err := os.Stat(s.sageBin); err != nil {
		return fmt.Errorf("sage binary not found at %s: %w", s.sageBin, err)
	}
	cmd := exec.Command(s.sageBin, "-python", s.script)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	s.cmd = cmd
	s.stdin = stdin
	s.stdout = bufio.NewReaderSize(stdout, 1<<20)
	s.dead = false
	// A (re)start gives a fresh, empty ref cache. Bumping the generation marks
	// every previously-issued SageRef as stale so it can no longer be sent.
	s.generation++
	return nil
}

// ensureStarted lazily starts the server on first use.
func (s *SageBackend) ensureStarted() error {
	if s.cmd != nil && !s.dead {
		return nil
	}
	if s.cmd != nil && s.dead {
		_ = s.cmd.Process.Kill()
		s.cmd = nil
	}
	return s.start()
}

// ClearCache drops the ENTIRE Sage-side expression-handle cache. Called at
// top-level decomposition-statement boundaries (see runFile) to bound memory on
// long runs. After a clear, any still-live SageRef whose value has NOT yet been
// materialized would become a dangling handle — so the interpreter only clears
// at points where no SageRef from a prior statement can still be referenced
// (statement results that survive are bound names whose values, if refs, were
// already materialized by printing/inspection during the statement).
func (s *SageBackend) ClearCache() error {
	if !s.useRefs {
		return nil
	}
	req := &sageRequest{Op: "clear", Args: []json.RawMessage{}}
	resp, err := s.roundtrip(req)
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("sage clear: %s", resp.Error)
	}
	return nil
}

// scheduleFree records that the SageRef with this server-side id is no longer
// reachable in Go (its finalizer ran). The id is freed from the server cache on
// the next roundtrip (flushPendingFree), batching frees so a GC sweep that
// finalizes many refs costs one clear, not one per ref. Safe to call from the
// finalizer goroutine: it only appends under freeMu and never blocks on a
// round-trip. A ref whose value was already materialized still owns its
// server-side cache entry, so we free it the same way.
func (s *SageBackend) scheduleFree(id int) {
	if !s.useRefs {
		return
	}
	s.freeMu.Lock()
	s.pendingFree = append(s.pendingFree, id)
	s.freeMu.Unlock()
}

// flushPendingFree sends a single clear[id...] for every id queued by a
// finalizer since the last flush. Called at the top of roundtrip (under s.mu),
// so the server cache tracks exactly the live Go refs without ever
// materializing. It builds its own request and writes/reads directly via send so
// it does not recurse into roundtrip (and is already under s.mu).
func (s *SageBackend) flushPendingFree() {
	s.freeMu.Lock()
	ids := s.pendingFree
	s.pendingFree = nil
	s.freeMu.Unlock()
	if len(ids) == 0 {
		return
	}
	args := make([]json.RawMessage, 0, len(ids))
	for _, id := range ids {
		args = append(args, json.RawMessage(fmt.Sprintf(`{"ref":%d}`, id)))
	}
	req := &sageRequest{Op: "clear", Args: args}
	// Best-effort: a failed free leaks one cache entry but is not a correctness
	// problem, so we do not propagate the error up through the caller's op.
	if _, err := s.send(req); err != nil {
		s.dead = true
	} else {
		s.refsFreed += len(ids)
	}
}

// drainPendingFree flushes the finalizer free-queue from outside a roundtrip
// (the statement-boundary drain in ExecProgram). It acquires s.mu and only
// flushes when the server is alive — a freed id no longer exists after a restart
// anyway. No-op when refs are off.
func (s *SageBackend) drainPendingFree() {
	if !s.useRefs {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd == nil || s.dead {
		// Nothing started yet, or dead: drop the queue (the ids do not exist
		// server-side). Clear it so it cannot grow unbounded.
		s.freeMu.Lock()
		s.pendingFree = nil
		s.freeMu.Unlock()
		return
	}
	s.flushPendingFree()
}

// reportRefStats prints a one-line summary of the expression-handle traffic for
// the run (to stderr), so the example-suite runner and a human can see refs'
// effect: how many result handles were issued, how many had to be materialized,
// and the {"ref"} vs {"poly"} arg split. No-op when the backend has no refs.
func (it *Interp) reportRefStats() {
	s, ok := it.cas.(*SageBackend)
	if !ok || !s.useRefs {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(stderrW(),
		"[ref-stats] issued=%d materialized=%d freed=%d ref-args=%d poly-args=%d\n",
		s.refsIssued, s.refsMaterialized, s.refsFreed, s.refArgsSent, s.polyArgsSent)
}

// Close terminates the subprocess.
func (s *SageBackend) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.stdin.Close()
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
		s.cmd = nil
		s.dead = true
	}
	return nil
}

// roundtrip sends one request and reads one response, with a timeout and a
// single restart attempt on server death.
func (s *SageBackend) roundtrip(req *sageRequest) (*sageResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if os.Getenv("OPENMAPLE_SAGE_TRACE") != "" {
		sageCallCount++
		fmt.Fprintf(stderrW(), "[sage %d] %s vars=%v args=%s\n", sageCallCount, req.Op, req.Vars, req.Args)
	}

	if err := s.ensureStarted(); err != nil {
		return nil, err
	}
	// Free any refs whose Go handle has been GC'd since the last call, so the
	// server cache tracks exactly the live Go refs. Skipped right after a restart
	// (the cache is empty, so the freed ids no longer exist — harmless either way).
	if !s.dead {
		s.flushPendingFree()
	}
	resp, err := s.send(req)
	if err != nil {
		// The server is unresponsive (a true death, or a call that blew its
		// timeout). A ref-bearing request can NEVER be retried: the restart below
		// gives a fresh, empty ref cache, so resending {"ref":N} would resolve
		// against an empty cache and fail with the misleading "unknown expression
		// ref / cache has 0 entries". Fail honestly instead — the run is
		// unrecoverable once the ref cache is gone. (This is the bug that killed
		// the combined-hydrogen run after 2h34m: a heavy add timed out, the alive
		// server was SIGKILLed, and the op was resent to an empty cache.)
		if req.hasRef {
			s.dead = true
			return nil, fmt.Errorf("sage %s carried a server-side ref but the server became unresponsive "+
				"(%w); the ref cache is lost on restart, so this op is unrecoverable", req.Op, err)
		}
		// one restart attempt for a ref-free request (it does not depend on cache)
		s.dead = true
		if rerr := s.ensureStarted(); rerr != nil {
			return nil, fmt.Errorf("sage server died and restart failed: %v (orig: %w)", rerr, err)
		}
		resp, err = s.send(req)
		if err != nil {
			return nil, err
		}
	}
	return resp, nil
}

func (s *SageBackend) send(req *sageRequest) (*sageResponse, error) {
	s.nextID++
	req.ID = s.nextID
	req.Debug = s.debug
	line, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if s.debug {
		fmt.Fprintf(os.Stderr, "[sage>] %s\n", line)
	}

	type readResult struct {
		line []byte
		err  error
	}
	done := make(chan readResult, 1)
	// Write then read in a goroutine so we can apply a timeout.
	go func() {
		if _, werr := s.stdin.Write(append(line, '\n')); werr != nil {
			done <- readResult{nil, werr}
			return
		}
		respLine, rerr := s.stdout.ReadBytes('\n')
		done <- readResult{respLine, rerr}
	}()

	select {
	case rr := <-done:
		if rr.err != nil {
			return nil, fmt.Errorf("sage protocol error: %w", rr.err)
		}
		if s.debug {
			fmt.Fprintf(os.Stderr, "[sage<] %s", rr.line)
		}
		var resp sageResponse
		if err := json.Unmarshal(rr.line, &resp); err != nil {
			return nil, fmt.Errorf("bad sage response %q: %w", string(rr.line), err)
		}
		return &resp, nil
	case <-time.After(s.timeoutFor(req.Op)):
		s.dead = true
		return nil, fmt.Errorf("sage call timed out after %s (op=%s)", s.timeoutFor(req.Op), req.Op)
	}
}

// ---------------------------------------------------------------------------
// CAS.Call — the single dispatch point
// ---------------------------------------------------------------------------

func (s *SageBackend) Call(op string, args []Value) (Value, error) {
	// Resolve package-member ops (LinearAlgebra:-Rank etc.).
	member := ""
	if i := indexColon(op); i >= 0 {
		member = op[i+2:]
		op = op[:i]
	}

	// Maple-atomic short-circuits: denom/numer/normal of an expression that
	// contains an inert non-arithmetic application (e.g. the DT-source quirk
	// `Joseph/StandardForm(table)` at main:241, which stays unevaluated) follow
	// Maple's "treat as having denominator 1" semantics rather than failing to
	// parse in Sage. denom -> 1, numer -> self, normal -> self.
	if len(args) >= 1 && containsOpaque(args[0]) {
		switch op {
		case "denom":
			return newInt(1), nil
		case "numer", "normal", "simplify", "expand", "collect":
			return args[0], nil
		}
	}

	// Build a name-sanitization context from all operands.
	san := newSanitizer()
	san.collect(args)

	// Some ops want a fraction-field ring.
	frac := false
	switch op {
	case "normal", "numer", "denom", "simplify", "evala":
		frac = true
	}

	refArgsBefore := s.refArgsSent
	reqArgs, err := s.encodeArgs(op, member, args, san)
	if err != nil {
		return nil, err
	}

	req := &sageRequest{
		Op:      op,
		Member:  member,
		Vars:    san.varList(),
		Args:    reqArgs,
		Frac:    frac,
		WantRef: s.useRefs,
		// A ref-bearing request cannot survive a server restart (the fresh cache is
		// empty), so roundtrip must not blindly resend it. encodeArg bumps
		// refArgsSent for each {"ref":N} it emits.
		hasRef: s.refArgsSent > refArgsBefore,
	}

	resp, err := s.roundtrip(req)
	if err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("sage %s: %s", op, resp.Error)
	}
	v, err := s.decodeResult(op, resp.Result, san)
	if err != nil {
		return nil, err
	}
	if mathTrace {
		traceMathCall(op, member, reqArgs, v, len(resp.Result))
	}
	// indets returns a *set* in Maple (DT does set ops on it: `indets(p) minus
	// {...}`, `intersect`). The server returns a list; convert to a Set.
	if op == "indets" {
		if l, ok := v.(List); ok {
			return makeSet(l.Items), nil
		}
	}
	return v, nil
}

// mathFuncs are transcendental/elementary function heads Sage's symbolic ring
// understands; an inert Func with one of these heads is NOT opaque.
var mathFuncs = map[string]bool{
	"cos": true, "sin": true, "tan": true, "exp": true, "log": true,
	"ln": true, "sqrt": true, "cot": true, "sec": true, "csc": true,
	"arctan": true, "arcsin": true, "arccos": true, "sinh": true,
	"cosh": true, "tanh": true, "abs": true,
}

// containsOpaque reports whether v contains an inert function application whose
// head is not a known math function (and is not a jet variable u(x,y), which is
// handled as a symbol). Such expressions cannot be sent to Sage as polynomials.
func containsOpaque(v Value) bool {
	switch n := v.(type) {
	case *Func:
		head := ""
		if hn, ok := n.Head.(Name); ok {
			head = hn.Val
		}
		if !mathFuncs[head] {
			return true
		}
		for _, a := range n.Args {
			if containsOpaque(a) {
				return true
			}
		}
		return false
	case *Sum:
		for _, t := range n.Terms {
			if containsOpaque(t) {
				return true
			}
		}
	case *Prod:
		for _, f := range n.Factors {
			if containsOpaque(f) {
				return true
			}
		}
	case *Power:
		return containsOpaque(n.Base) || containsOpaque(n.Exp)
	case List:
		for _, it := range n.Items {
			if containsOpaque(it) {
				return true
			}
		}
	case *Equation:
		// An equation is opaque only if one of its sides is — a plain
		// polynomial equation a = b must reach the CAS so normal/expand/numer/
		// denom can map over the sides (the op handlers split on the relation).
		return containsOpaque(n.Lhs) || containsOpaque(n.Rhs)
	case *Relation:
		return containsOpaque(n.Lhs) || containsOpaque(n.Rhs)
	case *Table:
		// A table has no expression-string form; genuinely opaque.
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Argument encoding
// ---------------------------------------------------------------------------

// laOps are the ops where a List argument denotes a matrix/vector. For all
// other ops a List is a Maple list of expressions (e.g. indets([p1,p2])).
var laOps = map[string]bool{
	"Matrix": true, "Vector": true, "array": true, "Array": true,
	"LinearAlgebra": true,
}

func (s *SageBackend) encodeArgs(op, member string, args []Value, san *sanitizer) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(args))
	for _, a := range args {
		enc, err := s.encodeArg(op, a, san)
		if err != nil {
			return nil, err
		}
		out = append(out, enc)
	}
	return out, nil
}

// encodeArg serializes one Value operand to the wire arg form.
func (s *SageBackend) encodeArg(op string, v Value, san *sanitizer) (json.RawMessage, error) {
	switch n := v.(type) {
	case *SageRef:
		// If this ref has already been materialized (its server-side handle may
		// since have been cleared at a statement boundary), send the concrete
		// value as a string — sending the stale {"ref":N} would be a cache miss.
		// Otherwise send the handle by-reference: the server resolves it from its
		// cache and coerces into the op's ring, with no string serialization.
		if mv, ok := n.materialized(); ok {
			return s.encodeArg(op, mv, san)
		}
		if n.gen != s.generation {
			return nil, fmt.Errorf("sage ref %d lost to a server restart (issued in generation %d, now %d); "+
				"the server-side ref cache was emptied and this expression is unrecoverable", n.id, n.gen, s.generation)
		}
		s.refArgsSent++
		return json.Marshal(map[string]int{"ref": n.id})
	case Integer:
		return json.Marshal(map[string]string{"int": n.Val.String()})
	case Name:
		// a bare name: could be a variable or a function name
		return json.Marshal(map[string]string{"name": san.sanitizeName(n.Val)})
	case *Indexed:
		// jet variable -> sanitized single name
		return json.Marshal(map[string]string{"name": san.sanitizeIndexed(n)})
	case List:
		if laOps[op] {
			// a matrix (list of rows) or a vector
			if isMatrixList(n) {
				return s.encodeMatrix(n, san)
			}
			return s.encodeVector(n.Items, san)
		}
		// a Maple list of expressions (e.g. indets([p1, p2]), coeffs over a
		// list): send each element by-reference when it is a live SageRef.
		return s.encodeExprlist(n.Items, san)
	case Set:
		return s.encodeExprlist(n.Items, san)
	default:
		// polynomial / rational / symbolic expression -> sanitized string
		str := san.sanitizeExpr(v)
		s.polyArgsSent++
		if refTrace && len(str) >= refTracePolyMin {
			// A big native AST is being marshalled+shipped as a string for this
			// op — the per-op cost of an earlier [ref-materialize].
			fmt.Fprintf(stderrW(), "[ref-polyarg] op=%s bytes=%d via %s\n",
				op, len(str), traceCallers(8))
		}
		return json.Marshal(map[string]string{"poly": str})
	}
}

// encodeExprlist serializes a Maple list/set of expressions for the wire as
// {"exprlist": [elem, ...]} where each elem is normally a sanitized string but
// a LIVE SageRef is kept as a {"ref":N} handle. Materializing + stringifying a
// swollen server-side ref into the list is what made indets([... bigpoly ...])
// re-parse a multi-MB string and blow the 120 s sage-call timeout; sending the
// ref keeps the element server-side so the handler reads it from the cache.
// (Mirrors the top-level SageRef policy in encodeArg: a ref whose handle may
// have been cleared at a statement boundary — materialized() true — is sent as
// its concrete string instead, to avoid a stale-handle cache miss.)
func (s *SageBackend) encodeExprlist(items []Value, san *sanitizer) (json.RawMessage, error) {
	elems := make([]json.RawMessage, len(items))
	for i, it := range items {
		if ref, ok := it.(*SageRef); ok {
			if _, materialized := ref.materialized(); !materialized {
				if ref.gen != s.generation {
					return nil, fmt.Errorf("sage ref %d lost to a server restart (issued in generation %d, now %d); "+
						"the server-side ref cache was emptied and this expression is unrecoverable", ref.id, ref.gen, s.generation)
				}
				s.refArgsSent++
				b, err := json.Marshal(map[string]int{"ref": ref.id})
				if err != nil {
					return nil, err
				}
				elems[i] = b
				continue
			}
		}
		b, err := json.Marshal(san.sanitizeExpr(it))
		if err != nil {
			return nil, err
		}
		elems[i] = b
	}
	return json.Marshal(map[string][]json.RawMessage{"exprlist": elems})
}

func isMatrixList(l List) bool {
	if len(l.Items) == 0 {
		return false
	}
	for _, it := range l.Items {
		if _, ok := it.(List); !ok {
			return false
		}
	}
	return true
}

func (s *SageBackend) encodeMatrix(l List, san *sanitizer) (json.RawMessage, error) {
	rows := make([][]string, 0, len(l.Items))
	for _, r := range l.Items {
		row := r.(List)
		cells := make([]string, len(row.Items))
		for j, c := range row.Items {
			cells[j] = san.sanitizeExpr(c)
		}
		rows = append(rows, cells)
	}
	return json.Marshal(map[string][][]string{"matrix": rows})
}

func (s *SageBackend) encodeVector(items []Value, san *sanitizer) (json.RawMessage, error) {
	ents := make([]string, len(items))
	for i, c := range items {
		ents[i] = san.sanitizeExpr(c)
	}
	return json.Marshal(map[string][]string{"vector": ents})
}

// ---------------------------------------------------------------------------
// Result decoding
// ---------------------------------------------------------------------------

func (s *SageBackend) decodeResult(op string, raw json.RawMessage, san *sanitizer) (Value, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("bad sage result for %s: %w", op, err)
	}

	if r, ok := m["ref"]; ok {
		var id int
		if err := json.Unmarshal(r, &id); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.refsIssued++
		gen := s.generation
		s.mu.Unlock()
		ref := &SageRef{be: s, id: id, gen: gen, san: san}
		// Per-ref lifecycle: when this Go handle becomes unreachable, free its
		// server-side cache entry so the cache stays bounded to exactly the live
		// Go refs — no statement-boundary materialize-all. The finalizer only
		// queues the id (scheduleFree); the next roundtrip batch-clears it.
		runtime.SetFinalizer(ref, func(r *SageRef) { r.be.scheduleFree(r.id) })
		return ref, nil
	}
	if r, ok := m["poly"]; ok {
		var str string
		if err := json.Unmarshal(r, &str); err != nil {
			return nil, err
		}
		return san.parseBack(str)
	}
	if r, ok := m["int"]; ok {
		var str string
		if err := json.Unmarshal(r, &str); err != nil {
			return nil, err
		}
		bi, ok := new(big.Int).SetString(str, 10)
		if !ok {
			return nil, fmt.Errorf("bad int result %q", str)
		}
		return Integer{bi}, nil
	}
	if r, ok := m["bool"]; ok {
		var b bool
		_ = json.Unmarshal(r, &b)
		return mkBool(b), nil
	}
	if r, ok := m["list"]; ok {
		var elems []json.RawMessage
		if err := json.Unmarshal(r, &elems); err != nil {
			return nil, err
		}
		items := make([]Value, len(elems))
		for i, e := range elems {
			v, err := s.decodeResult(op, e, san)
			if err != nil {
				return nil, err
			}
			items[i] = v
		}
		// Maple's coeffs returns an expression SEQUENCE, not a list, so that
		// {coeffs(p, x)} forms a set of the coefficients (DT's usage:
		// {coeffs(collect(expand(p-q),s),s)}). A List would give a set of one
		// list. Every other list-returning op (indets, ...) keeps the List form.
		if op == "coeffs" {
			return Seq{items}, nil
		}
		return List{items}, nil
	}
	if r, ok := m["factors"]; ok {
		return s.decodeFactors(r, san)
	}
	if r, ok := m["divide"]; ok {
		return s.decodeDivide(r, san)
	}
	if r, ok := m["matrix"]; ok {
		return s.decodeMatrix(r, san)
	}
	if r, ok := m["vector"]; ok {
		return s.decodeVector(r, san)
	}
	if r, ok := m["name"]; ok {
		var str string
		_ = json.Unmarshal(r, &str)
		return san.unsanitizeName(str), nil
	}
	if _, ok := m["neg_infinity"]; ok {
		// Maple -infinity, represented as the product (-1)*infinity (matches
		// isNegInfinityVal / the symbolic-infinity simplification in eval_ops).
		return &Prod{[]Value{newInt(-1), Name{"infinity"}}}, nil
	}
	if _, ok := m["infinity"]; ok {
		return Name{"infinity"}, nil
	}
	if _, ok := m["pos_infinity"]; ok {
		// Maple +infinity (ldegree of the zero polynomial). Positive analog of
		// neg_infinity; same surface form as a bare "infinity".
		return Name{"infinity"}, nil
	}
	return nil, fmt.Errorf("unrecognized sage result for %s: %s", op, raw)
}

// decodeFactors builds Maple's factors() return form: [unit, [[f,m],...]].
func (s *SageBackend) decodeFactors(raw json.RawMessage, san *sanitizer) (Value, error) {
	var f struct {
		Unit    string              `json:"unit"`
		Factors [][]json.RawMessage `json:"factors"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	unitV, err := san.parseBack(f.Unit)
	if err != nil {
		return nil, err
	}
	facItems := make([]Value, 0, len(f.Factors))
	for _, fm := range f.Factors {
		var facStr string
		var mult int
		if err := json.Unmarshal(fm[0], &facStr); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(fm[1], &mult); err != nil {
			return nil, err
		}
		facV, err := san.parseBack(facStr)
		if err != nil {
			return nil, err
		}
		facItems = append(facItems, List{[]Value{facV, newInt(int64(mult))}})
	}
	return List{[]Value{unitV, List{facItems}}}, nil
}

// decodeDivide returns the quotient on exact division, else FAIL boolean.
// Maple's divide(a,b,'q') returns true/false and binds the quotient via the
// out-param; here we return a small list [exact(bool), quotient] that the
// caller-side glue interprets. But to keep CAS.Call uniform, we return the
// quotient Value when exact and Boolean(false) when not — matching the
// common `if divide(a,b,'q') then ... q ...` idiom is handled in builtins.
func (s *SageBackend) decodeDivide(raw json.RawMessage, san *sanitizer) (Value, error) {
	var d struct {
		Exact    bool   `json:"exact"`
		Quotient string `json:"quotient"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	q, err := san.parseBack(d.Quotient)
	if err != nil {
		return nil, err
	}
	// Return a 2-element list [bool, quotient]; the divide glue in the
	// evaluator turns this into the boolean + out-param assignment.
	return List{[]Value{mkBool(d.Exact), q}}, nil
}

func (s *SageBackend) decodeMatrix(raw json.RawMessage, san *sanitizer) (Value, error) {
	var rows [][]string
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	rowVals := make([]Value, len(rows))
	for i, row := range rows {
		cells := make([]Value, len(row))
		for j, c := range row {
			v, err := san.parseBack(c)
			if err != nil {
				return nil, err
			}
			cells[j] = v
		}
		rowVals[i] = List{cells}
	}
	return List{rowVals}, nil
}

func (s *SageBackend) decodeVector(raw json.RawMessage, san *sanitizer) (Value, error) {
	var ents []string
	if err := json.Unmarshal(raw, &ents); err != nil {
		return nil, err
	}
	vals := make([]Value, len(ents))
	for i, e := range ents {
		v, err := san.parseBack(e)
		if err != nil {
			return nil, err
		}
		vals[i] = v
	}
	return List{vals}, nil
}

// ---------------------------------------------------------------------------
// Name sanitization
// ---------------------------------------------------------------------------
//
// Sage indeterminates must be valid Python/Sage identifiers. DifferentialThomas
// jet variables are indexed names like u[1,0] (an *Indexed value). We map them
// reversibly to flat identifiers:
//
//   u[1,0]    <->  u_DT_1_0
//   phi[0]    <->  phi_DT_0
//   x         <->  x          (already an identifier)
//
// The "_DT_" infix and "_" separators are chosen so the reverse map is
// unambiguous as long as the original head/indices don't themselves contain
// "_DT_". DT's variable heads are plain identifiers and indices are integers,
// so this holds. We additionally keep an explicit forward/back table so even
// pathological names round-trip.

type sanitizer struct {
	fwd  map[string]string // original-key -> sanitized
	back map[string]Value  // sanitized    -> original Value
	vars map[string]bool   // sanitized var set
}

func newSanitizer() *sanitizer {
	return &sanitizer{
		fwd:  map[string]string{},
		back: map[string]Value{},
		vars: map[string]bool{},
	}
}

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// collect walks all operands and registers every Indexed / Name indeterminate.
func (s *sanitizer) collect(args []Value) {
	for _, a := range args {
		s.walkCollect(a)
	}
}

func (s *sanitizer) walkCollect(v Value) {
	switch n := v.(type) {
	case *SageRef:
		// A ref's expression lives Sage-side in a ring whose generators were
		// sanitized by the ref's own sanitizer. Merge those sanitized var names
		// (and their reverse mapping) so this request's vars list covers every
		// generator the cached object uses — otherwise the server's coercion ring
		// would be missing generators and the ref-coerce fallback would fail.
		if n.san != nil {
			for san, orig := range n.san.back {
				if n.san.vars[san] {
					s.vars[san] = true
					if _, ok := s.back[san]; !ok {
						s.back[san] = orig
					}
				}
			}
		}
	case *Indexed:
		s.sanitizeIndexed(n)
	case Name:
		// only register plausible variable names (lowercase-ish); function
		// heads like cos are registered lazily by sanitizeName when seen as
		// operands, which is harmless.
		s.sanitizeName(n.Val)
	case *Sum:
		for _, t := range n.Terms {
			s.walkCollect(t)
		}
	case *Prod:
		for _, f := range n.Factors {
			s.walkCollect(f)
		}
	case *Power:
		s.walkCollect(n.Base)
		s.walkCollect(n.Exp)
	case *Func:
		// transcendental: register the *arguments* as variables, not the head
		for _, a := range n.Args {
			s.walkCollect(a)
		}
	case List:
		for _, it := range n.Items {
			s.walkCollect(it)
		}
	case Set:
		for _, it := range n.Items {
			s.walkCollect(it)
		}
	case Seq:
		for _, it := range n.Items {
			s.walkCollect(it)
		}
	case *Equation:
		s.walkCollect(n.Lhs)
		s.walkCollect(n.Rhs)
	}
}

// sanitizeIndexed maps u[1,0] -> u_DT_1_0 and records the reverse.
func (s *sanitizer) sanitizeIndexed(n *Indexed) string {
	key := "x:" + printValue(n)
	if san, ok := s.fwd[key]; ok {
		return san
	}
	head := ""
	if hn, ok := n.Head.(Name); ok {
		head = hn.Val
	} else {
		head = printValue(n.Head)
	}
	parts := []string{head, "DT"}
	for _, ix := range n.Idx {
		parts = append(parts, sanIndexPart(ix))
	}
	san := strings.Join(parts, "_")
	san = ensureIdent(san)
	san = s.dedupe(san, key)
	s.fwd[key] = san
	s.back[san] = n
	s.vars[san] = true
	return san
}

// sanitizeName maps a bare name; identifiers pass through, others get encoded.
func (s *sanitizer) sanitizeName(name string) string {
	key := "n:" + name
	if san, ok := s.fwd[key]; ok {
		return san
	}
	var san string
	if identRe.MatchString(name) {
		san = name
	} else {
		san = ensureIdent("nm_" + encodeWeird(name))
	}
	san = s.dedupe(san, key)
	s.fwd[key] = san
	s.back[san] = Name{name}
	s.vars[san] = true
	return san
}

// dedupe ensures the sanitized name is unique to this key (avoids two distinct
// originals colliding on the same sanitized form).
func (s *sanitizer) dedupe(san, key string) string {
	cur := san
	i := 1
	for {
		if exist, ok := s.back[cur]; !ok {
			return cur
		} else if printValue(exist) == revKeyPrint(key) {
			return cur
		}
		cur = fmt.Sprintf("%s_%d", san, i)
		i++
	}
}

func revKeyPrint(key string) string {
	// best-effort: for collision check only
	if strings.HasPrefix(key, "n:") {
		return printName(key[2:])
	}
	if strings.HasPrefix(key, "x:") {
		return key[2:]
	}
	return key
}

func sanIndexPart(ix Value) string {
	switch n := ix.(type) {
	case Integer:
		s := n.Val.String()
		if strings.HasPrefix(s, "-") {
			return "neg" + s[1:]
		}
		return s
	default:
		return ensureIdent(encodeWeird(printValue(ix)))
	}
}

func ensureIdent(s string) string {
	if s == "" {
		return "v_"
	}
	if !identRe.MatchString(s) {
		return "v_" + encodeWeird(s)
	}
	return s
}

// encodeWeird turns arbitrary text into an identifier-safe suffix.
func encodeWeird(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteString(strconv.Itoa(int(r)))
		}
	}
	return b.String()
}

// varList returns the sorted sanitized variable names for the request ring.
func (s *sanitizer) varList() []string {
	out := make([]string, 0, len(s.vars))
	for v := range s.vars {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// sanitizeExpr prints a Value as a Sage/Maple-parseable string with all
// indexed names replaced by their sanitized forms.
func (s *sanitizer) sanitizeExpr(v Value) string {
	return printSanitized(v, s)
}

// parseBack re-parses a Sage output string into a Value, then unsanitizes the
// indexed names back to *Indexed.
func (s *sanitizer) parseBack(str string) (Value, error) {
	str = strings.TrimSpace(str)
	if str == "" {
		return NULL(), nil
	}
	root, err := frontEnd(str)
	if err != nil {
		return nil, fmt.Errorf("reparse sage output %q: %w", str, err)
	}
	tmp := NewInterp()    // fresh interp: unbound names stay symbolic
	tmp.inertParse = true // already-reduced Sage output: don't re-dispatch CAS ops
	v, err := tmp.execBlock(root.nodes)
	if err != nil {
		return nil, fmt.Errorf("re-eval sage output %q: %w", str, err)
	}
	return s.unsanitizeValue(v), nil
}

// unsanitizeName maps a sanitized identifier back to its original Value.
func (s *sanitizer) unsanitizeName(san string) Value {
	if orig, ok := s.back[san]; ok {
		return orig
	}
	return Name{san}
}

// unsanitizeValue walks a re-parsed Value tree replacing sanitized Names with
// their original (possibly Indexed) values.
func (s *sanitizer) unsanitizeValue(v Value) Value {
	switch n := v.(type) {
	case Name:
		if orig, ok := s.back[n.Val]; ok {
			return orig
		}
		return n
	case *Sum:
		ts := make([]Value, len(n.Terms))
		for i, t := range n.Terms {
			ts[i] = s.unsanitizeValue(t)
		}
		return &Sum{ts}
	case *Prod:
		fs := make([]Value, len(n.Factors))
		for i, f := range n.Factors {
			fs[i] = s.unsanitizeValue(f)
		}
		return &Prod{fs}
	case *Power:
		return &Power{s.unsanitizeValue(n.Base), s.unsanitizeValue(n.Exp)}
	case *Func:
		args := make([]Value, len(n.Args))
		for i, a := range n.Args {
			args[i] = s.unsanitizeValue(a)
		}
		return &Func{Head: s.unsanitizeValue(n.Head), Args: args}
	case *Indexed:
		idx := make([]Value, len(n.Idx))
		for i, ix := range n.Idx {
			idx[i] = s.unsanitizeValue(ix)
		}
		return &Indexed{Head: s.unsanitizeValue(n.Head), Idx: idx}
	case List:
		items := make([]Value, len(n.Items))
		for i, it := range n.Items {
			items[i] = s.unsanitizeValue(it)
		}
		return List{items}
	case Set:
		items := make([]Value, len(n.Items))
		for i, it := range n.Items {
			items[i] = s.unsanitizeValue(it)
		}
		return makeSet(items)
	case Seq:
		items := make([]Value, len(n.Items))
		for i, it := range n.Items {
			items[i] = s.unsanitizeValue(it)
		}
		return Seq{items}
	default:
		return v
	}
}

// printSanitized renders a Value to a string with sanitized indeterminate
// names. It mirrors print.go but substitutes Indexed/Name via the sanitizer.
func printSanitized(v Value, s *sanitizer) string {
	return printSanPrec(v, 0, s)
}

func printSanPrec(v Value, parent int, s *sanitizer) string {
	switch n := v.(type) {
	case *SageRef:
		// A SageRef nested inside a container arg (List/Set/exprlist) must be
		// materialized before it can be stringified for the wire. Top-level ref
		// args take the {"ref":N} path in encodeArg and never reach here.
		return printSanPrec(concrete(n), parent, s)
	case Integer:
		return n.Val.String()
	case Rational:
		return n.Val.Num().String() + "/" + n.Val.Denom().String()
	case Name:
		return s.sanitizeName(n.Val)
	case *Indexed:
		return s.sanitizeIndexed(n)
	case *Func:
		// transcendental function: keep head name (NOT registered as a ring
		// variable — it is a function symbol), recurse args
		head := funcHeadString(n.Head)
		args := make([]string, len(n.Args))
		for i, a := range n.Args {
			args[i] = printSanPrec(a, precSeq, s)
		}
		return head + "(" + strings.Join(args, ", ") + ")"
	case *Sum:
		return wrap(printSanSum(n, s), precSum, parent)
	case *Prod:
		return wrap(printSanProd(n, s), precProd, parent)
	case *Power:
		// `^` is RIGHT-associative in Sage/Python, so a base that is itself a
		// Power (or any composite at <= precPow) MUST be parenthesized: the
		// reciprocal (a^2)^-1 serialized bare as a^2^-1 re-parses in Sage as
		// a^(2^-1) = sqrt(a), which then errors "not a 2nd power".  Render the
		// base at precPow+1 so an inner Power gets wrapped.
		return wrap(printSanPrec(n.Base, precPow+1, s)+"^"+printSanPrec(n.Exp, precUnary, s), precPow, parent)
	case List:
		parts := make([]string, len(n.Items))
		for i, it := range n.Items {
			parts[i] = printSanPrec(it, precSeq, s)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	default:
		// fall back to the standard printer (numbers, etc.)
		return printPrec(v, parent)
	}
}

func printSanSum(sm *Sum, s *sanitizer) string {
	if len(sm.Terms) == 0 {
		return "0"
	}
	var b strings.Builder
	for i, t := range sm.Terms {
		ts := printSanPrec(t, precSum, s)
		if i == 0 {
			b.WriteString(ts)
			continue
		}
		if strings.HasPrefix(ts, "-") {
			b.WriteString(" - ")
			b.WriteString(ts[1:])
		} else {
			b.WriteString(" + ")
			b.WriteString(ts)
		}
	}
	return b.String()
}

// funcHeadString renders a function head (e.g. cos) as a plain name without
// registering it as a polynomial-ring variable.
func funcHeadString(h Value) string {
	if n, ok := h.(Name); ok {
		return n.Val
	}
	return printValue(h)
}

func printSanProd(p *Prod, s *sanitizer) string {
	if len(p.Factors) == 0 {
		return "1"
	}
	parts := make([]string, len(p.Factors))
	for i, f := range p.Factors {
		parts[i] = printSanPrec(f, precProd, s)
	}
	return strings.Join(parts, "*")
}

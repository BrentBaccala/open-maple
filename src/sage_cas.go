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
	timeout time.Duration
	dead    bool
}

// sageRequest / sageResponse are the wire types.
type sageRequest struct {
	ID     int               `json:"id"`
	Op     string            `json:"op"`
	Member string            `json:"member,omitempty"`
	Vars   []string          `json:"vars"`
	Args   []json.RawMessage `json:"args"`
	Frac   bool              `json:"frac,omitempty"`
	Debug  bool              `json:"debug,omitempty"`
}

type sageResponse struct {
	ID     int             `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
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
	return &SageBackend{
		sageBin: sageBin,
		script:  script,
		debug:   os.Getenv("OPENMAPLE_CAS_DEBUG") != "",
		timeout: 120 * time.Second,
	}, nil
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
		fmt.Fprintf(stderrW(), "[sage %d] %s\n", sageCallCount, req.Op)
	}

	if err := s.ensureStarted(); err != nil {
		return nil, err
	}
	resp, err := s.send(req)
	if err != nil {
		// one restart attempt
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
	case <-time.After(s.timeout):
		s.dead = true
		return nil, fmt.Errorf("sage call timed out after %s (op=%s)", s.timeout, req.Op)
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

	reqArgs, err := s.encodeArgs(op, member, args, san)
	if err != nil {
		return nil, err
	}

	req := &sageRequest{
		Op:     op,
		Member: member,
		Vars:   san.varList(),
		Args:   reqArgs,
		Frac:   frac,
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
	case *Equation, *Relation, *Table:
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
		// list): send each element's string form.
		strs := make([]string, len(n.Items))
		for i, it := range n.Items {
			strs[i] = san.sanitizeExpr(it)
		}
		return json.Marshal(map[string][]string{"exprlist": strs})
	case Set:
		strs := make([]string, len(n.Items))
		for i, it := range n.Items {
			strs[i] = san.sanitizeExpr(it)
		}
		return json.Marshal(map[string][]string{"exprlist": strs})
	default:
		// polynomial / rational / symbolic expression -> sanitized string
		str := san.sanitizeExpr(v)
		return json.Marshal(map[string]string{"poly": str})
	}
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
	return nil, fmt.Errorf("unrecognized sage result for %s: %s", op, raw)
}

// decodeFactors builds Maple's factors() return form: [unit, [[f,m],...]].
func (s *SageBackend) decodeFactors(raw json.RawMessage, san *sanitizer) (Value, error) {
	var f struct {
		Unit    string          `json:"unit"`
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
	tmp := NewInterp() // fresh interp: unbound names stay symbolic
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
		return wrap(printSanPrec(n.Base, precPow, s)+"^"+printSanPrec(n.Exp, precUnary, s), precPow, parent)
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

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
)

// Diagnostic data-flow traces for the CAS/ref layer. Each is gated by an env
// var and costs a single bool test when off. They exist to answer "how did this
// expression end up crossing the wire as a multi-MB string instead of staying a
// server-side ref" — i.e. where a SageRef got materialized into a native AST.
//
//	OPENMAPLE_REF_TRACE  ref lifecycle: every materialize() (ref id, materialized
//	                     size in bytes, and the interpreter call chain that forced
//	                     it) and every large {"poly":...} arg shipped to Sage (op,
//	                     size, trigger). A materialize line is a ref->native
//	                     collapse; the poly-arg lines that follow are the per-op
//	                     re-marshalling cost it created.
//	OPENMAPLE_MATH_TRACE broader per-op view (see Call): op/member with each arg's
//	                     kind+size and the result's kind+size, size-bounded.
var (
	refTrace  = os.Getenv("OPENMAPLE_REF_TRACE") != ""
	mathTrace = os.Getenv("OPENMAPLE_MATH_TRACE") != ""
)

// refTracePolyMin is the smallest {"poly":...} arg (bytes) REF_TRACE reports.
// Below it the value is a bare name / tiny expression — noise for the question
// of why big polynomials re-cross the wire.
const refTracePolyMin = 65536

// tracePlumbing are this file's and the materialize/concrete plumbing frames,
// dropped from a traced call chain so the first frame shown is the op, builtin,
// or proc handler that actually triggered the event.
var tracePlumbing = map[string]bool{
	"traceCallers":               true,
	"(*SageRef).materialize":     true,
	"(*SageRef).materialize.func1": true,
	"concrete":                   true,
}

// traceCallers returns up to n interpreter-level (main.*) frames above the
// caller, newest first, as "fn(file:line)" joined by " <- ", with runtime,
// standard-library, and trace/materialize plumbing frames dropped. It is only
// called behind a *Trace gate, so the runtime.Callers cost is never paid in a
// normal run.
func traceCallers(n int) string {
	pcs := make([]uintptr, n+24)
	got := runtime.Callers(2, pcs) // skip runtime.Callers + traceCallers
	frames := runtime.CallersFrames(pcs[:got])
	var out []string
	for len(out) < n {
		f, more := frames.Next()
		if name, ok := strings.CutPrefix(f.Function, "main."); ok && !tracePlumbing[name] {
			out = append(out, fmt.Sprintf("%s(%s:%d)", name, shortTraceFile(f.File), f.Line))
		}
		if !more {
			break
		}
	}
	return strings.Join(out, " <- ")
}

func shortTraceFile(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// mathArgDesc summarizes one encoded CAS arg for MATH_TRACE: a {"ref":N} handle
// as "ref:N" (server-side, no bytes crossed), a small literal by its kind, and
// anything carrying an expression body ({"poly"}, {"exprlist"}, {"matrix"}, ...)
// as "kind:bytes" using the encoded wire length. It parses only the one-key
// outer object, never the (possibly multi-MB) body — so tracing a 21 MB poly
// arg costs an int compare, not another stringify.
func mathArgDesc(raw json.RawMessage) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Sprintf("?:%d", len(raw))
	}
	for k, val := range m {
		switch k {
		case "ref":
			return "ref:" + strings.TrimSpace(string(val))
		case "int", "name":
			return k
		default:
			return fmt.Sprintf("%s:%d", k, len(raw))
		}
	}
	return "empty"
}

// traceMathCall emits one MATH_TRACE line for a CAS Call: the op (with package
// member), each arg via mathArgDesc, and the result as "ref:N" (kept server-
// side) or "poly:bytes" (it came back as a string). resultBytes is the result's
// wire length. Gated by the caller (mathTrace).
func traceMathCall(op, member string, reqArgs []json.RawMessage, result Value, resultBytes int) {
	parts := make([]string, len(reqArgs))
	for i, ra := range reqArgs {
		parts[i] = mathArgDesc(ra)
	}
	name := op
	if member != "" {
		name = op + ":-" + member
	}
	res := fmt.Sprintf("poly:%d", resultBytes)
	if r, ok := result.(*SageRef); ok {
		res = fmt.Sprintf("ref:%d", r.id)
	}
	fmt.Fprintf(stderrW(), "[math] %s(%s) -> %s\n", name, strings.Join(parts, ", "), res)
}

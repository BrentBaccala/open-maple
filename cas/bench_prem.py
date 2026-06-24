"""Standalone parse-vs-compute timing for a captured op=prem request.

Reads a JSON [sage>] request line (the {"id":...,"op":"prem",...} object) from
a file or stdin, then times:
  (i)  parsing the operands into the univariate-in-x ring (parse_in_ring +
       _univariate_in_x coercion) -- the "parse" cost
  (ii) the pseudo_quo_rem compute on the already-parsed operands -- the
       "compute" cost
  (iii) _back_to_R on the result -- the result round-trip cost

Usage:
  sage -python bench_prem.py /tmp/prem-operand.json
  (or pipe the single JSON object on stdin)

The input is the JSON object only (strip the leading "[sage>] ").
"""
from __future__ import print_function
import sys
import json
import time

# Import the server module so we reuse its exact ring/parse/prem machinery.
import sage_server as S


def load_request(text):
    text = text.strip()
    if text.startswith("[sage>]"):
        text = text[len("[sage>]"):].strip()
    return json.loads(text)


def main():
    if len(sys.argv) > 1:
        with open(sys.argv[1]) as f:
            text = f.read()
    else:
        text = sys.stdin.read()
    req = load_request(text)
    assert req["op"] == "prem", "expected op=prem, got %s" % req["op"]

    varnames = req["vars"]
    args = req["args"]
    print("vars: %s" % varnames)
    for i, a in enumerate(args):
        if "poly" in a:
            print("arg[%d] poly len=%d chars" % (i, len(a["poly"])))
        else:
            print("arg[%d] = %r" % (i, a))

    R = S.make_ring(varnames)
    def flush(msg):
        print(msg); sys.stdout.flush()

    # --- (i) parse: decode each operand into R (this is what decode_arg does) ---
    t0 = time.time()
    a = S.decode_arg(args[0], R)
    flush("decoded arg0 into R: %.3f" % (time.time() - t0))
    t0b = time.time()
    b = S.decode_arg(args[1], R)
    flush("decoded arg1 into R: %.3f" % (time.time() - t0b))
    x = S.decode_arg(args[2], R)
    t_parse_R = (time.time() - t0)

    # coerce into the univariate-in-x ring (also part of the op's parse cost)
    t0 = time.time()
    Ru = S._univariate_in_x(R, x)
    au = Ru(a)
    bu = Ru(b)
    t_to_univ = time.time() - t0
    flush("coerced into Frac(other)[x]: %.3f (deg_a=%s deg_b=%s)"
          % (t_to_univ, au.degree(), bu.degree()))

    # --- (ii) compute: pseudo_quo_rem ---
    t0 = time.time()
    q, r = au.pseudo_quo_rem(bu)
    t_compute = time.time() - t0
    flush("pseudo_quo_rem: %.3f" % t_compute)

    # --- (iii) result round-trip back to R ---
    t0 = time.time()
    rr = r.numerator() if hasattr(r, "numerator") else r
    res = S._back_to_R(R, rr, x)  # pass x so the structural path (the fix) is used
    t_back = time.time() - t0

    # measure str() of result (the materialize/serialize cost)
    t0 = time.time()
    s_res = str(res)
    t_str = time.time() - t0

    print("")
    print("=== timings (seconds) ===")
    print("parse  decode_arg into R   : %.3f" % t_parse_R)
    print("parse  coerce into Frac[x] : %.3f" % t_to_univ)
    print("       PARSE TOTAL         : %.3f" % (t_parse_R + t_to_univ))
    print("compute pseudo_quo_rem     : %.3f" % t_compute)
    print("result _back_to_R          : %.3f" % t_back)
    print("result str() len=%d        : %.3f" % (len(s_res), t_str))
    print("")
    total = t_parse_R + t_to_univ + t_compute + t_back
    print("verdict: parse=%.3f compute=%.3f back=%.3f  -> %s-bound"
          % (t_parse_R + t_to_univ, t_compute, t_back,
             "PARSE" if (t_parse_R + t_to_univ) > t_compute else "COMPUTE"))
    print("total (parse+compute+back) : %.3f" % total)


if __name__ == "__main__":
    main()

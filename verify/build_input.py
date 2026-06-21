"""build_input.py — construct the INPUT differential system in jet notation,
independent of the open-maple/DT engine.

The verifier must not trust the engine to tell it what the input was.  So we
re-derive the input equations directly from the *mathematical* statement of each
example (the ansatz), expressing differentiation as the jet-index-raising map.

A jet is  name[i,j,k]  = d^(i+j+k) name / dx^i dy^j dz^k  (3 ivars x,y,z).
For 2 ivars (x,y) it is name[i,j]; for 1 ivar (x) it is name[i].

We emit each input as a dict:
   { "ivars":[...], "dvars":[...], "equations":[poly_str,...],
     "inequations":[poly_str,...] }
with poly_str in the SAME normalized form maple_parse produces (name__i_j_k, **).

Differentiation of a *product/sum* of jets is done by sympy (pure CAS, not DT),
operating on flat indeterminates with a custom derivation that raises indices.
"""

import re
import sympy

from maple_parse import jet_to_var, normalize_poly_string, JET_RE


def _mk_deriv(ivars):
    """Return a function d(expr, axis) that differentiates a sympy expression in
    flat jet variables (name__i_j_k) w.r.t. ivar index `axis`, treating the bare
    ivars (x,y,z) as the actual coordinates and jets as functions of them.

    Rule:  d/dx_axis of  name__i_j_k  =  name__(i+e_axis)   (raise that index)
           d/dx_axis of  x_axis       =  1
           plus ordinary product/sum/power rules.
    """
    nv = len(ivars)
    ivar_syms = {v: sympy.Symbol(v) for v in ivars}

    def raise_index(sym):
        nm = sym.name
        base, _, tail = nm.partition("__")
        if not tail:
            return None  # not a jet (a bare ivar or constant symbol handled elsewhere)
        idx = [int(t) for t in tail.split("_")]
        return base, idx

    def d(expr, axis):
        expr = sympy.expand(expr)
        # total derivative: sum over each free symbol of (d expr/d sym)*(d sym/dx_axis)
        res = sympy.Integer(0)
        for sym in expr.free_symbols:
            partial = sympy.diff(expr, sym)
            if partial == 0:
                continue
            name = sym.name
            if name in ivar_syms:
                # d(x_j)/d(x_axis) = delta
                if name == ivars[axis]:
                    res += partial
                continue
            ri = raise_index(sym)
            if ri is None:
                # opaque constant symbol -> derivative 0
                continue
            base, idx = ri
            idx2 = list(idx)
            idx2[axis] += 1
            newsym = sympy.Symbol(jet_to_var(base, tuple(idx2)))
            res += partial * newsym
        return sympy.expand(res)

    return d, ivar_syms


def _jet(name, idx):
    return sympy.Symbol(jet_to_var(name, tuple(idx)))


def build_hydrogen():
    """The JOCA hydrogen ansatz, re-derived independently in jet notation.

    ivars = x,y,z.  Functions (jets): DDPs, DPs, Ps, Vf, rho.
    Parameters (constant): V1,V2,V3,V4,a0,a1,b0,b1,c0,c1.

    ansatz (Maple, J(s)=s(x,y,z), Dd(s,v)=diff(s(x,y,z),v)):
      Dd(Ps,x)  - J(DPs)*Dd(Vf,x),   (and y,z)
      Dd(DPs,x) - J(DDPs)*Dd(Vf,x),  (and y,z)
      (a0+a1*Vf)*DDPs + (b0+b1*Vf)*DPs + (c0+c1*Vf)*Ps,
      Vf - (V1*x + V2*y + V3*z + V4*rho),
      rho^2 - x^2 - y^2 - z^2
    constancy: diff(par, ivar) = 0  for every par, ivar (30 equations).
    """
    ivars = ["x", "y", "z"]
    jets = ["DDPs", "DPs", "Ps", "Vf", "rho"]
    pars = ["V1", "V2", "V3", "V4", "a0", "a1", "b0", "b1", "c0", "c1"]
    dvars = jets + pars
    d, isym = _mk_deriv(ivars)
    x, y, z = isym["x"], isym["y"], isym["z"]

    def J(name):
        return _jet(name, (0, 0, 0))

    eqs = []
    # Dd(Ps, axis) - DPs * Dd(Vf, axis)
    for axis in range(3):
        eqs.append(d(J("Ps"), axis) - J("DPs") * d(J("Vf"), axis))
    for axis in range(3):
        eqs.append(d(J("DPs"), axis) - J("DDPs") * d(J("Vf"), axis))
    # (a0+a1*Vf)*DDPs + (b0+b1*Vf)*DPs + (c0+c1*Vf)*Ps
    eqs.append((J("a0") + J("a1") * J("Vf")) * J("DDPs")
               + (J("b0") + J("b1") * J("Vf")) * J("DPs")
               + (J("c0") + J("c1") * J("Vf")) * J("Ps"))
    # Vf - (V1 x + V2 y + V3 z + V4 rho)
    eqs.append(J("Vf") - (J("V1") * x + J("V2") * y + J("V3") * z + J("V4") * J("rho")))
    # rho^2 - x^2 - y^2 - z^2
    eqs.append(J("rho") ** 2 - x ** 2 - y ** 2 - z ** 2)
    # constancy: each parameter's first derivatives vanish
    for p in pars:
        for axis in range(3):
            eqs.append(d(J(p), axis))

    eq_strs = [str(sympy.expand(e)).replace("**", "^") for e in eqs]
    eq_strs = [normalize_poly_string(s) for s in eq_strs]
    return {"ivars": ivars, "dvars": dvars,
            "equations": eq_strs, "inequations": []}


# ---- small known-correct systems (algebraic / few-jet differential) ---------

def build_readme_smoke():
    # ComputeRanking([x,y],[u]); [u[1,0]-u[0,0], u[0,1]-u[0,0]^2]
    return {"ivars": ["x", "y"], "dvars": ["u"],
            "equations": [normalize_poly_string("u[1,0]-u[0,0]"),
                          normalize_poly_string("u[0,1]-u[0,0]^2")],
            "inequations": [], "expected_cells": 1}


def build_cauchy_riemann():
    # Cauchy-Riemann, 2 dvars u,v over x,y; sum-of-squares form documented as 2 cells.
    # u_x - v_y = 0, u_y + v_x = 0  -> the canonical CR system (a linear, 1-cell
    # system); the test corpus's "2-component" CR is the sum-of-squares variant.
    # We use the standard CR (consistent, passive) for the structural/soundness
    # checks; expected count is informational.
    return {"ivars": ["x", "y"], "dvars": ["u", "v"],
            "equations": [normalize_poly_string("u[1,0]-v[0,1]"),
                          normalize_poly_string("u[0,1]+v[1,0]")],
            "inequations": []}


def build_overview_3var():
    # ComputeRanking([x,y,z],[u,v,w]); 4 components, documented in DT worksheet.
    eqs = ["u[1,0,0]-2*u[1,0,0]*v[0,1,0]+v[0,1,0]",
           "u[0,1,0]*w[0,0,1]+2*u[0,1,0]*v[1,0,0]+v[1,0,0]",
           "w[0,0,0]-u[0,0,0]*u[0,1,0]"]
    return {"ivars": ["x", "y", "z"], "dvars": ["u", "v", "w"],
            "equations": [normalize_poly_string(e) for e in eqs],
            "inequations": [], "expected_cells": 4}


def build_alg_xu2():
    # x*u^2 - u = 0  (treat u as a single jet u[0], x in field) -> 2 cells:
    #   u=0  and  x*u-1=0 (u<>0).
    return {"ivars": ["x"], "dvars": ["u"],
            "equations": [normalize_poly_string("x*u[0]^2-u[0]")],
            "inequations": [], "expected_cells": 2}


def build_alg_factored():
    # (x*u-1)*(u-x) = 0 -> 2 cells
    return {"ivars": ["x"], "dvars": ["u"],
            "equations": [normalize_poly_string("(x*u[0]-1)*(u[0]-x)")],
            "inequations": [], "expected_cells": 2}


SYSTEMS = {
    "readme_smoke": build_readme_smoke,
    "cauchy_riemann": build_cauchy_riemann,
    "overview_3var": build_overview_3var,
    "alg_xu2": build_alg_xu2,
    "alg_factored": build_alg_factored,
    "hydrogen": build_hydrogen,
}


if __name__ == "__main__":
    import json
    import sys
    name = sys.argv[1] if len(sys.argv) > 1 else "hydrogen"
    sysm = SYSTEMS[name]()
    print(json.dumps(sysm, indent=1))

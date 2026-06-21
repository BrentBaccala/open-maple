"""known_cells.py — hand-checked reference decompositions for the small systems,
written independently of the open-maple/DT engine.

These are the KNOWN-CORRECT decompositions used to validate the verifier itself
(methodology requirement: the harness must PASS on known-good decompositions
before any hydrogen conclusion is trusted).  Cells are in normalized jet form.
"""

KNOWN = {
    # x*u^2 - u = 0  ->  u=0  OR  (x*u-1=0, u<>0)
    "alg_xu2": [
        {"equations": ["u__0"], "inequations": []},
        {"equations": ["x*u__0 - 1"], "inequations": ["u__0"]},
    ],
    # (x*u-1)*(u-x) = 0 -> u=x  OR  (x*u-1=0, u<>x)
    # branch split on the two factors; second carries u<>x to stay disjoint.
    "alg_factored": [
        {"equations": ["u__0 - x"], "inequations": []},
        {"equations": ["x*u__0 - 1"], "inequations": ["u__0 - x"]},
    ],
    # readme smoke: u_x - u = 0, u_y - u^2 = 0; integrability u_xy forces u=0.
    # single cell {u = 0}.
    "readme_smoke": [
        {"equations": ["u__0_0"], "inequations": []},
    ],
    # Cauchy-Riemann u_x - v_y, u_y + v_x : a single consistent passive linear
    # system (1 cell).  (The corpus's "2-component" variant is a different,
    # sum-of-squares input; the canonical CR system is 1 cell.)
    "cauchy_riemann": [
        {"equations": ["u__1_0 - v__0_1", "u__0_1 + v__1_0"], "inequations": []},
    ],
}

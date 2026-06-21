"""maple_parse.py — engine-independent parsing of Maple decomposition data.

Parses two kinds of inputs into a common, Sage-friendly representation:

  1. The cell result file produced by the hydrogen worksheet, of the form
         EI := [[[eq, eq, ...], [ineq, ...]], [[...],[...]], ...]:
     i.e. a Maple list of cells, each cell a 2-list [Equations, Inequations]
     of polynomials written in JET NOTATION  name[i, j, k]  with bare
     independent variables x, y, z appearing as coefficient-field elements.

  2. Individual polynomial strings in the same jet notation.

The output representation deliberately uses ONLY Python string manipulation and
the standard library, so it carries no dependency on the open-maple / DT engine
(the whole point: the verifier must be independent of what it checks).

Jet variables  name[i, j, k]  are mapped to flat algebraic indeterminate names
   name__i_j_k     (e.g.  DDPs[0, 0, 0] -> DDPs__0_0_0 ,  V1[1, 0, 0] -> V1__1_0_0)
so that they can be fed to a Sage polynomial ring.  The mapping is reversible
(jet_to_var / var_to_jet) so witnesses can be printed back in jet notation.

This module is plain Python (no Sage import) so it can be unit-tested with the
system python; the Sage layer (verify_core.py) imports it.
"""

import re


JET_RE = re.compile(r'([A-Za-z]\w*)\s*\[\s*(-?\d+)\s*,\s*(-?\d+)\s*,\s*(-?\d+)\s*\]')
# 1-index and 2-index jets too (for the 2-ivar / 1-ivar known-good systems)
JET2_RE = re.compile(r'([A-Za-z]\w*)\s*\[\s*(-?\d+)\s*,\s*(-?\d+)\s*\]')
JET1_RE = re.compile(r'([A-Za-z]\w*)\s*\[\s*(-?\d+)\s*\]')


def jet_to_var(name, idx):
    """('DDPs',(0,0,0)) -> 'DDPs__0_0_0'.  idx is a tuple of ints (any arity)."""
    return name + "__" + "_".join(str(i) for i in idx)


def var_to_jet(varname):
    """'DDPs__0_0_0' -> 'DDPs[0, 0, 0]'.  Inverse of jet_to_var."""
    base, _, tail = varname.partition("__")
    if not tail:
        return varname
    idx = tail.split("_")
    return "%s[%s]" % (base, ", ".join(idx))


def normalize_poly_string(s):
    """Convert a Maple polynomial string in jet notation to a Python/Sage
    parseable expression string.

    - name[i, j, k]  -> name__i_j_k       (3-index jets)
    - name[i, j]     -> name__i_j         (2-index)
    - name[i]        -> name__i           (1-index)
    - ^  -> **
    Bare x, y, z and rationals pass through unchanged.
    """
    def sub3(m):
        return jet_to_var(m.group(1), (m.group(2), m.group(3), m.group(4)))

    def sub2(m):
        return jet_to_var(m.group(1), (m.group(2), m.group(3)))

    def sub1(m):
        return jet_to_var(m.group(1), (m.group(2),))

    s = JET_RE.sub(sub3, s)
    s = JET2_RE.sub(sub2, s)
    s = JET1_RE.sub(sub1, s)
    s = s.replace("^", "**")
    return s


def collect_jet_vars(s):
    """Return the set of flat variable names (name__i_j_k) appearing in s."""
    out = set()
    for m in JET_RE.finditer(s):
        out.add(jet_to_var(m.group(1), (m.group(2), m.group(3), m.group(4))))
    for m in JET2_RE.finditer(s):
        # only count if not already captured as part of a 3-index (JET_RE ran first
        # on the original string, so re-scan here is on the raw string -> guard)
        pass
    return out


def split_top_level(s, opener="[", closer="]", sep=","):
    """Split string s (the *contents* of one bracket level, brackets stripped)
    on top-level separators, respecting nested [] and () balance.
    Returns list of substrings (trimmed)."""
    parts = []
    depth = 0
    cur = []
    for ch in s:
        if ch in "[(":
            depth += 1
            cur.append(ch)
        elif ch in ")]":
            depth -= 1
            cur.append(ch)
        elif ch == sep and depth == 0:
            parts.append("".join(cur).strip())
            cur = []
        else:
            cur.append(ch)
    last = "".join(cur).strip()
    if last:
        parts.append(last)
    return parts


def _strip_outer(s, opener="[", closer="]"):
    """Strip one matched outer pair of brackets from a trimmed string."""
    s = s.strip()
    assert s.startswith(opener) and s.endswith(closer), (
        "expected %s...%s, got: %r" % (opener, closer, s[:40]))
    return s[1:-1]


def parse_EI_file(path):
    """Parse a hydrogen_thomas_result.m-style file.

    Returns a list of cells; each cell is a dict
        {"equations": [poly_str, ...], "inequations": [poly_str, ...]}
    where poly_str is in NORMALIZED (Sage-parseable) form (jets flattened, ^->**).
    """
    text = open(path).read()
    # Strip the leading "NAME :=" and trailing ":" / ";"
    m = re.match(r'\s*\w+\s*:=\s*(.*?)\s*[;:]\s*$', text, re.DOTALL)
    assert m, "file does not look like 'NAME := ... :'"
    body = m.group(1).strip()
    inner = _strip_outer(body, "[", "]")          # contents: cell, cell, cell
    cell_strs = split_top_level(inner)
    cells = []
    for cs in cell_strs:
        cinner = _strip_outer(cs, "[", "]")        # contents: eqs_list, ineqs_list
        two = split_top_level(cinner)
        assert len(two) == 2, "cell is not [Equations, Inequations]: %r" % cs[:60]
        eqs = [normalize_poly_string(p)
               for p in split_top_level(_strip_outer(two[0]))] if two[0].strip() != "[]" else []
        ineqs = [normalize_poly_string(p)
                 for p in split_top_level(_strip_outer(two[1]))] if two[1].strip() != "[]" else []
        cells.append({"equations": eqs, "inequations": ineqs})
    return cells


if __name__ == "__main__":
    import sys
    path = sys.argv[1] if len(sys.argv) > 1 else None
    if path:
        cells = parse_EI_file(path)
        print("parsed %d cells" % len(cells))
        for i, c in enumerate(cells):
            print("cell %d: %d eqs, %d ineqs" % (i + 1, len(c["equations"]), len(c["inequations"])))
    else:
        # self-tests
        assert jet_to_var("DDPs", (0, 0, 0)) == "DDPs__0_0_0"
        assert var_to_jet("DDPs__0_0_0") == "DDPs[0, 0, 0]"
        assert normalize_poly_string("x*u[1, 0]^2 - u[0,0]") == "x*u__1_0**2 - u__0_0"
        s = "[[a, b], [c]]"
        assert split_top_level(_strip_outer(s)) == ["[a, b]", "[c]"]
        print("maple_parse self-tests OK")

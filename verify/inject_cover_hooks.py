#!/usr/bin/env python3
# inject_cover_hooks.py <dt-src-dir>
#
# Adds COVER-verification instrumentation to an (already prune-instrumented) copy
# of the DifferentialThomas source.  Two kinds of printf hook:
#
#   OMRI_SPLIT|<operator>          — emitted once per invocation of each of the
#                                    split operators, so a run produces a census
#                                    of which branch-creating operators actually
#                                    fired.  Used to confirm the runtime split
#                                    behaviour matches the static enumeration
#                                    (only the known tautological-binary operators
#                                    + Factorize ever create a system).
#
#   OMRI_FACTOR|<q>|<fak1>|<fak2>  — emitted at the Factorize equation split,
#                                    the ONE split whose exhaustiveness is not a
#                                    logical tautology: q=0 <=> (fak1=0) | (fak2=0
#                                    & fak1!=0) holds iff fak1*fak2 = q.  The
#                                    verifier checks that product identity.
#
# Every hook is anchored on a string that must occur EXACTLY ONCE in its file;
# the script aborts if an anchor is missing or ambiguous, so silent mis-patches
# can't happen.  Insertions are idempotent-safe only on a fresh copy (run via
# make_cover_instrumented.sh, which re-copies first).

import sys, os, re

DST = sys.argv[1] if len(sys.argv) > 1 else "/tmp/dt-cover-instr"
PKG = "@@PACKAGE@@"
SF  = f"`{PKG}/StandardForm`"

# Each entry: (file, anchor, where, hook_lines)
#   where = "after"       -> insert hook immediately AFTER the anchor line
#           "before"      -> insert hook immediately BEFORE the anchor line
#           "after_local" -> insert AFTER the first `local ...` line at/after the
#                            anchor (so the census printf lands inside the proc body,
#                            anchored on the proc's UNIQUE definition line)
HOOKS = []

def split_census(opname, file, proc_def):
    HOOKS.append((file, proc_def, "after_local",
        [f'printf("OMRI_SPLIT|{opname}\\n");']))

# --- split-operator census hooks: anchor on each operator's UNIQUE proc-def line,
#     insert the census printf just after its `local` declaration. ---
split_census("SplitByInitial", "algebraic",
             "`@@PACKAGE@@/SplitByInitial` := proc(DifferentialSystem,q)")
split_census("SplitBySquarefreeOld", "algebraic",
             "`@@PACKAGE@@/SplitBySquarefreeOld` := proc(DifferentialSystem, p)")
split_census("DivideByInequationOld", "algebraic",
             "`@@PACKAGE@@/DivideByInequationOld` := proc(DifferentialSystem,p,q)")
split_census("InequationLCM", "algebraic",
             "`@@PACKAGE@@/InequationLCM` := proc(DifferentialSystem,q,listp2)")
split_census("Factorize", "factor",
             "`@@PACKAGE@@/Factorize` := proc(DifferentialSystem,q)")
# reduction PRSGCD split (inline, not its own proc): anchor on the unique comment.
HOOKS.append(("reduction",
             "#this will be the special case system, where the degree of the gcd is higher",
             "after", ['printf("OMRI_SPLIT|ReductionPRSGCD\\n");']))

# --- Factorize product-identity capture: emit q, fak1, fak2 at the point fak has
#     been reduced to [fak1, fak2] (or [fak1]) and BEFORE q is substituted to
#     fak1.  Anchor: the line that substitutes q (insert the printf just before). ---
HOOKS.append((
    "factor",
    "    if `@@PACKAGE@@/Leader`(fak[1])=`@@PACKAGE@@/Leader`(q) then",
    "before",
    [f'    if nops(fak)=2 then printf("OMRI_FACTOR|%a|%a|%a\\n",{SF}(q),{SF}(fak[1]),{SF}(fak[2])); fi;'],
))


def main():
    for file, anchor, where, hook in HOOKS:
        path = os.path.join(DST, file)
        with open(path) as f:
            lines = f.read().split("\n")
        idxs = [i for i, ln in enumerate(lines) if anchor in ln]
        if len(idxs) != 1:
            sys.exit(f"ERROR: anchor in {file} matched {len(idxs)} times "
                     f"(need exactly 1): {anchor!r}")
        i = idxs[0]
        if where == "after_local":
            loc = next((j for j in range(i, len(lines))
                        if re.match(r"\s*local\b", lines[j])), None)
            if loc is None:
                sys.exit(f"ERROR: no `local` line after anchor in {file}: {anchor!r}")
            at = loc + 1
        else:
            at = i + 1 if where == "after" else i
        lines[at:at] = hook
        with open(path, "w") as f:
            f.write("\n".join(lines))
        print(f"  hooked {file}: {where} {anchor[:50]!r}")
    print(f"injected {len(HOOKS)} cover hook(s) into {DST}")


if __name__ == "__main__":
    main()

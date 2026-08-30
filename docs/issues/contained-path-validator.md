# Root-contained path validation: two spellings of one rule

Lands: when a third root-contained path field is introduced

`genfile.checkOut` (generation `out`: clean, relative, forward slashes,
never escaping the root) and `workspace.cleanUseDir` (workspace `use`
entries) enforce the same "root-contained forward-slash path" rule
with slightly different tolerances (`use` cleans in place; `out`
demands the written spelling already be clean).

Collapse sketch: one shared validator with a require-clean switch,
both call sites pointed at it. Invariant preserved: every accepted
value resolves strictly inside the resolution root. At two sites the
drift risk is small; a third field (plugin override sources, export
targets) is the trigger.

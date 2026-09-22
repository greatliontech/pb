# The checker and the migration scan proto lines twice

Lands: the migrate plan's chunk 11

The checker reads a file's lines once to find each line's comment
and whether it holds code or nothing (internal/check/eval, the text
type), and the migration reads them again to rewrite directives and
to say which the checker will not read where they stand
(internal/migrate, scanLines): two scanners of one lexical structure
— strings, block comments, line comments — whose answers must agree,
since a directive the migration reports as placed is a claim about
the checker's reading. They have disagreed once already, on a blank
line inside a block comment, and each carries its own spelling of
what a line of code is. One scanner both consume — a line's span,
whether it holds code, where a line comment opens, its block comment
segments, whether it holds nothing at all — makes the migration's
claim the checker's own reading, and the disagreement
unrepresentable.

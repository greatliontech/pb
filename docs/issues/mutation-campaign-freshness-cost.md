# Delta mutation campaigns stall in gomutant's freshness proofs

Lands: gomutant's freshness-proofs-stall-on-large-record-sets
resolved (its diagnosis is scheduled after the pb plans, gomutant
being this work's own)

Four attempts at the generation work's delta campaign — the staged
tree over nine packages, then the clean tree over three — never
measured a mutant: each spent its whole run in `prepare … freshness`
and "freshness proofs (union over N subjects)", with N at 155 for the
broad selection and 1608 for the narrow one, holding roughly 8 GiB
of resident memory on a 30 GiB host with two other tool servers
resident, until cancelled by hand or by a watchdog at 6 GiB / 25
minutes. The prior change sets' campaigns completed on the same host; the
record set has since grown to 292 records whose oracle closures now
union over most of the tree.

The change set's mutation evidence therefore rests on hand probes through
`gomutant ephemeral` (every load-bearing edge killed, one equivalence
attested) rather than a campaign record, and the clean-tree
promotion commit the previous change sets made is outstanding for this
one.

The acquisition suite's campaign, once its registries were served
in-process, measured its one target twice (eleven probes, forty-seven
confirmations, one narrowed survivor re-scored under the full oracle
with no disagreement) and was refused at the record both times for a
tree change under it — a sibling's edits through a replace, then a
re-staged binding; a third and fourth run were stopped by the
session's memory guard, and a fifth, capped at 6 GiB with another
campaign resident, died in gofresh's package-graph load. The
oracle attributes; the record is unbanked for the same class of
cost.

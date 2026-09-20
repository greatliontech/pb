# Plan: the check domain

Spec: docs/specs/check-rules.md; the tidy clause in
docs/specs/dep-verbs.md (REQ-dep-tidy-rulesets).

- [x] 1. The spec: targets, finding locations, environment 1's
      bindings and library, rule-file discovery, rulesets as declared
      dependencies with tidy keeping them, the base materialized, the
      two verbs
- [x] 2. Rule files: `*.rules.yaml` parsed and validated, the
      environment version refused where unprovided
- [x] 3. Environment 1: cel-go over descriptor protos, the bindings
      and the library, bounded evaluation, the segmentation rule
- [ ] 4. Lint evaluation: entities bound per target, verdicts located
      through source info, no defaults, suppression comments
- [ ] 5. The lint file: rulesets, selection, severities, path
      ignores; rulesets read from the build and a bad rule file named
      with its ruleset, tidy keeping them
- [ ] 6. Breaking: the pairing, oneofs and extensions included; the
      base's three forms materialized and compiled
- [ ] 7. The verbs: `pb lint` and `pb breaking` with the command's
      assembly
- [ ] 8. The catalog: buf's lint and breaking rules in CEL as the
      engine's fixture corpus, the environment proven closed
- [ ] 9. Plan close-out

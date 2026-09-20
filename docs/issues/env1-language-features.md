# Environment 1's features stop at FeatureSet's own fields

Lands: user decision

buf's `FIELD_SAME_JAVA_UTF8_VALIDATION` and `FIELD_SAME_CPP_STRING_TYPE`
compare a string field's resolved language features,
`(pb.java).utf8_validation` and `(pb.cpp).string_type`: extensions
of `google.protobuf.FeatureSet` declared by `java_features.proto` and
`cpp_features.proto`, inherited through the editions chain as the
standard features are, and in proto2 and proto3 implied by the
`java_string_check_utf8` file option and the `ctype` field option.
Environment 1's `features(entity)` resolves the message's own fields
and no extension (check-rules.md REQ-env1-library), so the catalog's
two rules read the legacy options alone and an editions file setting
the language features passes them. Environment 1 is unreleased and
may still gain the function.

The fork. One: `features` resolves extensions too, spelled by their
extension name as `options` spells a custom option
(`features(field)['(pb.java)'].utf8_validation`), the extension's
descriptor found among the checked files and their imports, the
inheritance walk pb's own since protoreflect resolves only the
registered feature extensions; the two rules become translations and
the environment answers every language feature a schema declares.
Two: the environment stays as specified and the two rules stay
approximations the corpus discloses. The tradeoff the user weighs:
a resolver pb owns and tests for every feature extension against a
surface a rule author can already reach through `options` for the
written, uninherited value.

# The fixture corpus

buf's lint and breaking catalogs rewritten as pb rule files under
`buf/lint` and `buf/breaking`, tagged with buf's categories, and two
schemas built to trip every rule: `lint/` for the lint rules and
`breaking/{old,new}` for the breaking rules. `lint.txt` and
`breaking.txt` are the findings the catalog test holds the runs to,
in the verbs' output form; `go test -update` rewrites them from a run
after the diff has been reviewed.

Two of buf's rules are not in the corpus: `PROTOVALIDATE`, which judges
constraint expressions in another language, and
`FILE_SAME_PHP_GENERIC_SERVICES`, whose option protobuf removed from
`descriptor.proto`.

`IMPORT_USED` binds the file, so a file with several unused imports
gets one finding. `DIRECTORY_SAME_PACKAGE` and
`RPC_REQUEST_RESPONSE_UNIQUE` group through `distinct()`, which CEL
charges by the square of the list, so over a schema of some ten
thousand files or rpcs the two exceed the cost limit and the run
fails rather than judges.

Three readings the corpus makes explicit. The FILE variants of the
deletion rules (`MESSAGE_NO_DELETE` and its kin) hold an entity to its
file: under pb's pairing by fully qualified name a move within the
package pairs the entity with its new home, so the finding sits at the
new declaration; the PACKAGE variants hold it to the package alone.
`extensions 4 to max` ends at a different number with
`message_set_wire_format` than without, so removing that option
shrinks the range and `EXTENSION_MESSAGE_NO_DELETE` fires beside
`MESSAGE_SAME_MESSAGE_SET_WIRE_FORMAT`. The `RESERVED_*_NO_DELETE`
and `EXTENSION_MESSAGE_NO_DELETE` rules hold each old range to one
new range containing it, so a range split in two fires, as buf's
comparison of range spellings fires.

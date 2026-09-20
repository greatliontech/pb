# The plugin evidence store has no lifecycle

Lands: when pb gains a verb or a step that collects the plugin store
(ocifs's collection invoked by pb), the evidence store swept with it

Kept evidence (`<user cache>/pb/plugin-evidence`) is written on every
fetch and never removed: an entry outlives the image it vouches for
once the plugin store collects that image, a replaced entry's
predecessor is gone but an image never acquired again keeps its
entry, and a keep interrupted between the temporary file and the
rename leaves the temporary file. Entries are kilobytes and the store
carries no authority, so the cost is disk alone. The sweep belongs
beside the plugin store's own collection: an entry whose digest the
store no longer holds goes with it, and temporary files older than
any live write go too.

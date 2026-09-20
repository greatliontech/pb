# Plugin evidence is fetched on every acquisition

Lands: user decision

A plugin image governed by a `plugins` identity rule has its
signature evidence fetched from its registry on every acquisition —
a warm store and a pinned digest included — because
REQ-plugin-verify-before-run judges every acquisition and the
evidence is not kept beside the content it vouches for. A registry
error fetching a carrier fails the acquisition
(REQ-prov-plugin-carriers), so a build with such a rule needs the
registry reachable every time, where an ungoverned plugin runs from
the store alone. The module pipeline keeps a module's provenance
envelope in the module cache and re-judges from it.

The fork:

- Keep the evidence with the content: carriers found for a digest are
  stored beside the image and judged from the store on later
  acquisitions, fetched again only when the store holds none the
  policy accepts — a build with a warm store and an unchanged pin
  runs offline, as an ungoverned plugin does, and a policy tightening
  still gates immediately since the judgement runs every time. A
  signature added after the store has an accepted one is seen only
  on a refetch, as a module's envelope is; an unsigned image under
  `allow-unsigned` is asked for again each time, so it still needs
  the registry.
- Fetch on every acquisition, as now: what the registry holds today is
  what is judged, a signature revoked by removal is noticed at once,
  and a governed plugin needs the registry every build.

The externally visible tradeoff is whether a governed plugin builds
offline from a warm store, against how soon a signature's removal
from the registry is noticed.

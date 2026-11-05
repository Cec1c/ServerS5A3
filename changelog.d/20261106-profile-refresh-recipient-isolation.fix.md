Automatic party and raid profile refreshes now release the shared actor
appearance lock before writing their single immutable frame to a recipient.
A slow recipient no longer prevents another recipient from receiving the same
actor or prevents appearance invalidation. Each recipient retains its wire
lock and the existing authentication, generation, membership, owner, HP and
appearance checks at frame admission. Explicit compound inspection keeps its
existing locking and ordering. No packet layout or gameplay transaction changes.

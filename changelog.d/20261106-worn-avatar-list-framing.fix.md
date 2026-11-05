Worn avatar rows in inventory notifications now include the two length-prefixed
extension fields consumed by the current client for equipment types 0..11.
Previously list 3 omitted both fields, allowing the next item row to be read as
an unchecked blob length. Ordinary equipment retains its existing 119-byte row;
avatar socket and colour payloads are preserved. Both full container pages and
slot updates use the same projection metadata. No saved item data changes.

The archived client op13 consumer and protected blob reader reproduce the
oversized copy when the fields are absent, and consume correctly framed avatar
and ordinary rows at their exact boundaries. This establishes a protocol defect;
the reported shop-close incident has no retained packet body, and fourth-hatchery
runtime acceptance is still separate from this isolated replay.

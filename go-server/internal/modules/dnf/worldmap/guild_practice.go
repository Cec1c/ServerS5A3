package worldmap

// IsFriendlyGuildPractice follows the two independent native DGN fields.
// V176 parses [agit war dungeon] as an integer boolean and [friendly agit
// dungeon] as a presence flag. The selector writes runtime state 1 only when
// both are set. This classifies content; it does not authorize admission.
func IsFriendlyGuildPractice(dungeon Dungeon) bool {
	return dungeon.Metadata.IntegerValues["agit war dungeon"] != 0 &&
		dungeon.Metadata.Flags["friendly agit dungeon"]
}

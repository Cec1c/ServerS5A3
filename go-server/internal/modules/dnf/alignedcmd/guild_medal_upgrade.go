package alignedcmd

// GuildMedalUpgradePolicy is resolved from the dedicated flag table, never
// from ordinary equipment metadata or caller-provided material/fee values.
type GuildMedalUpgradePolicy struct {
	MaterialID    int64
	MaterialCount int64
	// Main inventory page resolved from the actual PVF material family.
	MaterialSlotStart int16
	MaterialSlotEnd   int16
	SuccessWeight     int
	PenaltyType       int
	PenaltyValue      int
}

type GuildMedalUpgradeResolver func(itemID int64, level byte) (GuildMedalUpgradePolicy, error)

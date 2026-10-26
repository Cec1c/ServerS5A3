package alignedcmd

// GuildMedalUpgradePolicy is resolved from the dedicated flag table, never
// from ordinary equipment metadata or caller-provided material/fee values.
type GuildMedalUpgradePolicy struct {
	MaterialID    int64
	MaterialCount int64
	SuccessWeight int
	PenaltyType   int
	PenaltyValue  int
}

type GuildMedalUpgradeResolver func(itemID int64, level byte) (GuildMedalUpgradePolicy, error)

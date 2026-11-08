package dnfbridge

import "longheng.io/server/internal/modules/dnf/alignedcmd"

// The group price applies once, after the authoritative native quotation and
// any NPC material conversion. Portable consumables bypass the gold resolver.
func privateGroupUpgradeGoldQuote(mode string, quote alignedcmd.UpgradeCostResolution) alignedcmd.UpgradeCostResolution {
	if (mode != "reinforce" && mode != "amplify") || !quote.Resolved || quote.GoldCost <= 0 {
		return quote
	}
	// Divide before rounding so a valid int64 price cannot overflow.
	cost := quote.GoldCost / 7
	if quote.GoldCost%7 != 0 {
		cost++
	}
	quote.GoldCost = cost
	return quote
}

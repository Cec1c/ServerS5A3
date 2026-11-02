package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/pvf"
)

// Malformed random-ticket data stays resolved but has no usable policy. The
// inventory owner rejects it without consuming anything or falling back to NPC
// reinforcement. The count prefix is part of [er_enchant], not a candidate.
func parseUpgradeRandomTicket(d *pvf.Document) *alignedcmd.UpgradeRandomTicket {
	ints := func(key string) ([]int64, bool) {
		tokens, _ := d.Section(key)
		values := make([]int64, 0, len(tokens))
		for _, t := range tokens {
			if t.Kind != pvf.TokenInt {
				return nil, false
			}
			values = append(values, t.Int)
		}
		return values, true
	}
	entries, ok := ints("er_enchant")
	if !ok || len(entries) < 3 || entries[0] <= 0 || entries[0] > 32 || int64(len(entries)) != 1+entries[0]*2 {
		return nil
	}
	rule := &alignedcmd.UpgradeRandomTicket{}
	var total int64
	for i := 1; i < len(entries); i += 2 {
		level, weight := entries[i], entries[i+1]
		if level < 0 || level > 31 || weight < 0 || weight > 100000 {
			return nil
		}
		total += weight
		if total > 100000 {
			return nil
		}
		rule.Chances = append(rule.Chances, alignedcmd.UpgradeTicketChance{Level: level, Weight: weight})
	}
	if total == 0 {
		return nil
	}
	if rule.Rarities, ok = ints("er_grade"); !ok {
		return nil
	}
	for _, rarity := range rule.Rarities {
		if rarity < 0 || rarity > 6 {
			return nil
		}
	}
	for key, target := range map[string]*int64{"er_slot": &rule.Slot, "er_seal": &rule.Seal} {
		values, valid := ints(key)
		if !valid || len(values) > 1 {
			return nil
		}
		if len(values) == 1 {
			*target = values[0]
		}
	}
	if rule.Slot < 0 || rule.Slot > 3 || rule.Seal < 0 || rule.Seal > 2 {
		return nil
	}
	return rule
}

package dnfbridge

import (
	"fmt"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

// Crystal Light is a native ID-specific reinforce potion (effect 99), not an
// action packet. Its paired PVF specifies 10%, 30 seconds and 60s cooldown.
// Do not infer effects for the other town potions from their common type/name.
func resolveTownPotion002(c *pvfDungeonDropCatalog, id int64) (*alignedcmd.TownPotion002Definition, error) {
	if id != 10007343 && id != 2600025 && id != 2749959 {
		return nil, nil
	}
	d, err := c.ResolveItem(uint32(id))
	if err != nil {
		return nil, err
	}
	path := "stackable/cash/chn_20141111_sao_package/crystallight_10007343.stk"
	expectedCool, expectedDuration := int64(60000), int64(30000)
	switch id {
	case 2600025:
		path = "stackable/professional/potion/secret_reinforcement.stk"
		expectedCool, expectedDuration = 1800000, 1800000
	case 2749959:
		path = "stackable/event/vendingmachine/chn_secret_reinforcement.stk"
		expectedCool, expectedDuration = 1800000, 1800000
	}
	if d.Kind != dungeonDropItemStackable || !strings.EqualFold(d.PVFPath, path) || currentUpgradeRateBuffStackableToken(d.StackableType) != "[expert town potion]" || d.ActionType != "" {
		return nil, fmt.Errorf("town potion 10007343: unsupported identity")
	}
	doc, err := parseDungeonCardPVFDocument(c.source, d.PVFPath)
	if err != nil {
		return nil, err
	}
	// Refuse newly introduced actions, materials, events, job/place restrictions
	// and limits until their owner is implemented; never debit and ignore them.
	allowed := map[string]bool{}
	for _, key := range []string{"name", "explain", "flavor text", "grade", "attach type", "rarity", "weight", "usable job", "/usable job", "minimum level", "stack limit", "icon", "field image", "stackable type", "expert type", "item group name", "cool time", "move wav", "cooltime maintenance", "effect maintenance", "stat change duration", "expiration date"} {
		allowed[key] = true
	}
	if id == 2600025 {
		allowed["cooltime group"], allowed["need material"], allowed["/need material"] = true, true, true
		materials := doc.Ints("need material")
		group, present := doc.Int("cooltime group")
		if !present || group != 351 || len(materials) != 2 || materials[0] != 10007531 || materials[1] != 4 {
			return nil, fmt.Errorf("town potion %d: unsupported profession contract", id)
		}
	}
	if id == 2749959 {
		allowed["name2"] = true
	}
	seen := map[string]bool{}
	for _, s := range doc.Sections {
		key := normalizeMagicBoxSectionName(s.Name)
		if !allowed[key] || seen[key] {
			return nil, fmt.Errorf("town potion 10007343: unhandled/duplicate field %s", key)
		}
		seen[key] = true
	}
	level, lok := doc.Int("minimum level")
	cool, cok := doc.Int("cool time")
	duration, dok := doc.Int("stat change duration")
	jobs, _ := doc.Text("usable job")
	group, _ := doc.Text("item group name")
	for key, count := range map[string]int{"minimum level": 1, "cool time": 1, "stat change duration": 2, "usable job": 1, "item group name": 1} {
		values, _ := doc.Section(key)
		if len(values) != count {
			return nil, fmt.Errorf("town potion 10007343: malformed %s", key)
		}
	}
	targets := doc.Texts("stat change duration")
	if len(targets) != 1 || targets[0] != "myself" {
		return nil, fmt.Errorf("town potion 10007343: unsupported target")
	}
	if !lok || level != 1 || !cok || cool != expectedCool || !dok || duration != expectedDuration || strings.Trim(jobs, "` \r\n\t") != "[all]" || strings.Trim(group, "` \r\n\t") != "potion" {
		return nil, fmt.Errorf("town potion 10007343: unsupported effect contract")
	}
	return &alignedcmd.TownPotion002Definition{ExpirationDate: d.ExpirationDate, ItemID: id, PVFPath: d.PVFPath, MinimumLevel: level, CooldownMillis: cool,
		Buff: alignedcmd.UpgradeRateBuffResolution{Mode: "reinforce", BonusWeight: 10000, DurationMillis: duration, PVFPath: d.PVFPath}}, nil
}

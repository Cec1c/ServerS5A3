package dnfbridge

import (
	"fmt"
	"math"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// v150: actor kind32 -> table type5 at 0x257455f..569; 0x3a3c784
// scales flag material count by float32 rarity weight with truncation.
// UI mode24 selects 0x3316610/0x33146d0: both check material only, bypassing
// ordinary gold validation at 0x33124a0. The guild NPC also declares
// [upgrade item cost free]. Do not charge the common calculator's internal 1.
// Destroy/payback is deliberately unavailable until independently
// verified, rather than deleting a medal with an invented compensation rule.
func (s *Service) currentGuildMedalUpgradeResolver() alignedcmd.GuildMedalUpgradeResolver {
	return func(id int64, level byte) (alignedcmd.GuildMedalUpgradePolicy, error) {
		var result alignedcmd.GuildMedalUpgradePolicy
		if id <= 0 || id > math.MaxUint32 {
			return result, fmt.Errorf("flag target ID invalid")
		}
		catalog, err := s.currentPVFItemCatalog()
		if err != nil {
			return result, err
		}
		def, err := catalog.ResolveItem(uint32(id))
		if err != nil {
			return result, err
		}
		if !currentPVFItemTargetContainer(def).GuildMedal {
			return result, fmt.Errorf("PVF target is not a guild medal")
		}
		item, err := parseDungeonCardPVFDocument(catalog.source, def.PVFPath)
		if err != nil {
			return result, err
		}
		for _, x := range item.Texts("impossible contents") {
			if strings.Trim(x, "` []") == "upgrade" {
				return result, fmt.Errorf("medal forbids upgrade")
			}
		}
		rarity := item.Ints("rarity")
		if len(rarity) != 1 || rarity[0] < 0 || rarity[0] > 6 {
			return result, fmt.Errorf("flag rarity invalid")
		}
		raw, err := catalog.source.ReadText(currentUpgradePVFPath)
		if err != nil {
			return result, err
		}
		doc, err := dnfpvf.Parse(currentUpgradePVFPath, raw)
		if err != nil {
			return result, err
		}
		marker := "`flag_upgrade_table`"
		start := strings.Index(raw, marker)
		if start < 0 || strings.Count(raw, marker) != 1 {
			return result, fmt.Errorf("flag table absent or ambiguous")
		}
		end := strings.Index(raw[start:], "[/table]")
		if end < 0 {
			return result, fmt.Errorf("flag table unterminated")
		}
		flagText := "[table]\n[table type]\n" + raw[start:start+end] + "[/table]"
		flagDoc, err := dnfpvf.Parse("flag_upgrade_table", flagText)
		if err != nil {
			return result, err
		}
		table := parseCurrentUpgradeTable(flagDoc, flagText)
		row, ok := table.RowForLevel(int(level) + 1)
		if !ok || row.FailureWeight < 0 || row.FailureWeight > currentUpgradeSuccessBase || row.PenaltyType < 0 || row.PenaltyType > 2 || row.PenaltyValue < 0 || row.MaterialItemID <= 0 || row.MaterialCount <= 0 {
			return result, fmt.Errorf("flag row %d unavailable (destroy/payback not verified)", int(level)+1)
		}
		weights := doc.Numbers("cost weights by guild flag rarity")
		if len(weights) != 7 {
			return result, fmt.Errorf("flag rarity cost weights incomplete")
		}
		count := float64(row.MaterialCount) * float64(float32(weights[rarity[0]]))
		if math.IsNaN(count) || math.IsInf(count, 0) || count < 1 || count > math.MaxInt32 {
			return result, fmt.Errorf("flag material count invalid")
		}
		material, err := catalog.ResolveItem(uint32(row.MaterialItemID))
		if err != nil || material.Kind != dungeonDropItemStackable {
			return result, fmt.Errorf("flag material metadata unavailable: %v", err)
		}
		container := currentPVFItemTargetContainer(material)
		family := normalizeDungeonDropStackableType(material.StackableType)
		if container.ListType != 0 || (family != "[quest]" && family != "[material]") {
			return result, fmt.Errorf("flag material family unsupported: %s", family)
		}
		return alignedcmd.GuildMedalUpgradePolicy{MaterialSlotStart: container.SlotStart, MaterialSlotEnd: container.SlotEnd, MaterialID: int64(row.MaterialItemID), MaterialCount: int64(count), SuccessWeight: row.SuccessWeight(), PenaltyType: row.PenaltyType, PenaltyValue: row.PenaltyValue}, nil
	}
}

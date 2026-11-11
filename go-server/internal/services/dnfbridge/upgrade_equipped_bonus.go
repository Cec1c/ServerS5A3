package dnfbridge

import (
	"fmt"
	"math"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

func (s *Service) alignedUpgradeEquippedBonusResolver() alignedcmd.UpgradeEquippedBonusResolver {
	return func(id int64) (alignedcmd.UpgradeEquippedBonus, error) {
		var result alignedcmd.UpgradeEquippedBonus
		catalog, err := s.currentPVFItemCatalog()
		if err != nil {
			return result, err
		}
		if id <= 0 || id > int64(^uint32(0)) {
			return result, fmt.Errorf("invalid upgrade equipped item")
		}
		d, err := catalog.ResolveItem(uint32(id))
		if err != nil {
			return result, err
		}
		if d.Kind != dungeonDropItemEquipment {
			return result, nil
		}
		doc, err := parseDungeonCardPVFDocument(catalog.source, d.PVFPath)
		if err != nil {
			return result, err
		}
		kind, _ := doc.Text("equipment type")
		switch strings.Trim(kind, "` \t\n\r") {
		case "[title name]":
			result.Slot = 13
		case "[creature]":
			if subtype, ok := doc.Int("sub type"); !ok || subtype != 0 {
				return result, nil
			}
			result.Slot = 26
		default:
			return result, nil
		}
		for key, dest := range map[string]*int{"increase upgrade compound rate": &result.CompoundRate, "upgrade prob increase": &result.AddWeight, "upgrade cost discount": &result.GoldDiscount} {
			_, present := doc.Section(key)
			if !present {
				continue
			}
			values := doc.Numbers(key)
			if len(values) != 1 || values[0] < 0 || values[0] > 100000 || math.IsNaN(values[0]) || values[0] != math.Trunc(values[0]) {
				return result, fmt.Errorf("invalid equipped upgrade field %s", key)
			}
			*dest = int(values[0])
		}
		result.ExpiresAt = d.ExpirationDate
		return result, nil
	}
}

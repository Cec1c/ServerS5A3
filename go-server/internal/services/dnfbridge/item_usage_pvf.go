package dnfbridge

import (
	"fmt"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/itemusage"
)

func resolveCurrentItemUsageDefinition(catalog *pvfDungeonDropCatalog, itemID int64) (*itemusage.Definition, error) {
	if catalog == nil || catalog.source == nil || itemID <= 0 || itemID > int64(^uint32(0)) {
		return nil, itemusage.ErrDefinition
	}
	item, err := catalog.ResolveItem(uint32(itemID))
	if err != nil {
		return nil, err
	}
	if item.Kind != dungeonDropItemStackable {
		return nil, nil
	}
	// Fatigue use already has durable counters and its own atomic owner. Do not
	// count an op44 fallback as a second fatigue use or change its shared policy.
	if strings.EqualFold(item.ActionType, "[add fatigue]") {
		return nil, nil
	}
	doc, err := parseDungeonCardPVFDocument(catalog.source, item.PVFPath)
	if err != nil {
		return nil, err
	}
	return itemusage.FromPVF(uint32(itemID), doc)
}

func (s *Service) alignedItemUsageResolverForCommand(opcode dnfenum.CmdPacket) alignedcmd.ItemUsageResolver {
	if opcode != dnfenum.CmdPacketUseStackable && opcode != dnfenum.CmdPacketUseSharedEffectItem {
		return nil
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil || catalog == nil {
		return nil
	} // Preserve the existing metadata fallback.
	return func(itemID int64) (*itemusage.Definition, error) {
		d, err := resolveCurrentItemUsageDefinition(catalog, itemID)
		if err != nil {
			return nil, fmt.Errorf("resolve item usage %d: %w", itemID, err)
		}
		return d, nil
	}
}

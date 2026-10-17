package dnfbridge

import (
	"math"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
)

// The second instance of CNDespairTowerModule registers the Grief notice
// 1263 -> 1C57C70 in the class0 table. Its readers consume the same 7-byte
// prefix and 8-byte reward rows as 348, but its UI uses floor+5114 and the
// independent manager+e4 control. Never send the Despair opcode for Grief.
const currentGriefResultMsgID uint16 = 1263

func currentDungeonUsesDedicatedTowerResult(runtime *runtimeDungeonState) bool {
	return currentDungeonIsDespairTower(runtime) || (runtime != nil && runtime.Dungeon.Metadata.IntegerValues["tower of grief"] == 1)
}

func buildCurrentGriefResultBody(clearTimeMS uint32, floor uint16, items []currentDespairResultItem) ([]byte, error) {
	if floor < 1 || floor > 100 || len(items) != dnfdungeon.GriefRewardSlots {
		return nil, errCurrentDungeonSettlementPlanShape
	}
	var out packetWriter
	out.writeUint32(clearTimeMS)
	out.writeUint16(floor)
	out.writeByte(dnfdungeon.GriefRewardSlots)
	for _, item := range items {
		id := item.ItemID
		if id == 0 && item.Quantity == 0 {
			id = math.MaxUint32
		}
		if id == 0 || (id == math.MaxUint32 && item.Quantity != 0) || (id != math.MaxUint32 && item.Quantity == 0) {
			return nil, errCurrentDungeonSettlementPlanShape
		}
		out.writeUint32(id)
		out.writeUint32(item.Quantity)
	}
	return out.bytes(), nil
}

func (s *Service) sendCurrentGriefSettlementLocked(session *gameSession, runtime *runtimeDungeonState, plan *currentDungeonSettlementPacketPlan) error {
	if len(plan.GriefResultBody) != 7+8*dnfdungeon.GriefRewardSlots || plan.GriefResultBody[6] != dnfdungeon.GriefRewardSlots {
		return errCurrentDungeonSettlementPlanShape
	}
	if !runtime.settlementGriefItemsSent {
		if len(plan.GriefItemSlots) > 0 {
			if err := s.sendSelectedIncrementalItemSlotRefreshes(session, "grief-settlement", plan.GriefItemSlots); err != nil {
				return err
			}
		}
		runtime.settlementGriefItemsSent = true
	}
	if !runtime.settlementGriefResultSent {
		if err := s.sendGameUpperRawClass(session, currentGriefResultMsgID, plan.GriefResultBody, 0); err != nil {
			return err
		}
		runtime.settlementGriefResultSent = true
		s.logGameEvent(session, "game-dungeon-grief-result-op1263-sent", "char_id", plan.CharacterID, "dungeon_id", runtime.Dungeon.ID, "completion_key", plan.CompletionKey, "body_len", len(plan.GriefResultBody))
	}
	return nil
}

package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"strconv"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type dungeonConsumptionSnapshot struct {
	runtime     *runtimeDungeonState
	characterID string
	slot        int16
	itemID      int64
	drops       []*runtimeDungeonDrop
}

// 只在本局已有拾取时核对使用前的库存，移动后的物品沿用既有严格解析规则。
func (s *Service) prepareDungeonConsumptionSnapshot(session *gameSession, repos dnfrepo.Group, opcode uint16, body []byte) dungeonConsumptionSnapshot {
	if session == nil || repos.Inventory == nil || opcode != uint16(dnfenum.CmdPacketUseStackable) {
		return dungeonConsumptionSnapshot{}
	}
	request, err := dnfinventory.DecodeUseStackableRequest(body)
	if err != nil || request.ListType != 0 {
		return dungeonConsumptionSnapshot{}
	}
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	runtime := session.dungeon.runtime
	if runtime == nil || runtime.DropOwner == nil || !dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) {
		return dungeonConsumptionSnapshot{}
	}
	instance := runtime.lockPartyDungeonInstance()
	hasPickup := false
	for _, drop := range runtime.DropOwner.byObjectKey {
		if drop != nil && drop.ConsumptionReceipt == nil && drop.Item.ItemID == uint32(request.ItemCode) && committedDungeonPickupProof(drop, session.selectedCharacterID) {
			hasPickup = true
			break
		}
	}
	unlockPartyDungeonInstance(instance)
	if !hasPickup {
		return dungeonConsumptionSnapshot{}
	}
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	inventory, found, err := repos.Inventory.Load(ctx, characterID)
	if err != nil || !found || inventory.CharacterID != characterID {
		return dungeonConsumptionSnapshot{}
	}
	stack := inventory.Slots[currentDungeonPickupMainSlotKey(request.SlotIndex)]
	// op44一次扣一件；只有最后一件会使已拾取物品不再拥有背包行。
	if stack.ItemID != int64(request.ItemCode) || stack.Count != 1 {
		return dungeonConsumptionSnapshot{}
	}
	instance = runtime.lockPartyDungeonInstance()
	defer unlockPartyDungeonInstance(instance)
	snapshot := dungeonConsumptionSnapshot{runtime: runtime, characterID: characterID, slot: request.SlotIndex, itemID: stack.ItemID}
	for _, drop := range runtime.DropOwner.byObjectKey {
		if drop == nil || drop.ConsumptionReceipt != nil || drop.Item.ItemID != uint32(stack.ItemID) || !committedDungeonPickupProof(drop, session.selectedCharacterID) {
			continue
		}
		slot, _, valid := currentDungeonCommittedPickupSlot(drop, inventory)
		if valid && int16(slot) == request.SlotIndex {
			snapshot.drops = append(snapshot.drops, drop)
		}
	}
	return snapshot
}

// 必须在领域事务成功后、ACK写入前记录；写包失败不撤销真实消耗或丢失凭证。
func (s *Service) commitDungeonConsumptionSnapshot(session *gameSession, snapshot dungeonConsumptionSnapshot, result alignedcmd.Result) {
	fact := result.StackableConsumption
	if session == nil || snapshot.runtime == nil || !result.Handled || !result.ResponseAllowed || fact == nil ||
		fact.CharacterID != snapshot.characterID || fact.ListType != 0 || fact.SlotIndex != snapshot.slot || fact.ItemID != snapshot.itemID || fact.PreviousCount != 1 || fact.RemainingCount != 0 {
		return
	}
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	runtime := session.dungeon.runtime
	if runtime != snapshot.runtime || runtime.DropOwner == nil || !dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) {
		return
	}
	instance := runtime.lockPartyDungeonInstance()
	defer unlockPartyDungeonInstance(instance)
	for _, drop := range snapshot.drops {
		if runtime.DropOwner.byObjectKey[drop.ObjectKey] != drop || drop.ConsumptionReceipt != nil || !committedDungeonPickupProof(drop, session.selectedCharacterID) {
			continue
		}
		copy := *fact
		drop.ConsumptionReceipt = &copy
		s.logGameEvent(session, "game-dungeon-picked-consumable-used-up",
			"char_id", session.selectedCharacterID, "dungeon_id", runtime.Dungeon.ID,
			"drop_object_key", drop.ObjectKey, "item_id", fact.ItemID, "use_slot", fact.SlotIndex,
			"message", "本局已拾取消耗品正常用完，已保留使用事务凭证")
	}
}

func committedDungeonPickupProof(drop *runtimeDungeonDrop, picker uint16) bool {
	if drop == nil || picker == 0 || drop.Status != runtimeDungeonDropConsumed || drop.DestinationSlot == 0 || len(drop.PickupResponseBody) != currentDungeonPickupResultSize {
		return false
	}
	if binary.LittleEndian.Uint16(drop.PickupResponseBody[4:6]) != picker {
		return false
	}
	proof, err := buildCurrentDungeonNormalPickupResultBody(drop.ObjectKey, picker, drop.DestinationSlot)
	return err == nil && bytes.Equal(proof, drop.PickupResponseBody)
}

func committedDungeonPickupUsedUp(drop *runtimeDungeonDrop, inventory dnfrepo.InventoryRecord) bool {
	if drop == nil || drop.ConsumptionReceipt == nil {
		return false
	}
	fact := drop.ConsumptionReceipt
	picker, err := strconv.ParseUint(inventory.CharacterID, 10, 16)
	return err == nil && fact.CharacterID == inventory.CharacterID && fact.ListType == 0 &&
		fact.SlotIndex > 0 && fact.ItemID == int64(drop.Item.ItemID) && fact.PreviousCount == 1 && fact.RemainingCount == 0 && committedDungeonPickupProof(drop, uint16(picker))
}

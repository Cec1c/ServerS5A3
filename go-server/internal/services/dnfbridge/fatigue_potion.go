package dnfbridge

import (
	"context"
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnffatigue "longheng.io/server/internal/modules/dnf/fatigue"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"strconv"
	"strings"
)

func fatiguePotionDefinition(itemID int64, path string, document *dnfpvf.Document) (dnffatigue.PotionDefinition, bool) {
	definition := dnffatigue.PotionDefinition{ItemID: itemID, PVFPath: path}
	action, found := document.Section("action type")
	if !found || len(action) < 2 || strings.ToLower(strings.Trim(action[0].Value, "` \t")) != "[add fatigue]" || action[1].Kind != dnfpvf.TokenInt || action[1].Int <= 0 {
		return definition, false
	}
	definition.Points = action[1].Int
	definition.DailyLimit, _ = document.Int("total usable count")
	definition.MinimumLevel, _ = document.Int("minimum level")
	definition.CoolTimeMS, _ = document.Int("cool time")
	definition.Group, _ = document.Text("cooltime group")
	if group, found := document.Int("cooltime group"); found {
		definition.Group = strconv.FormatInt(group, 10)
	}
	definition.Group = strings.Trim(definition.Group, "` \t\r\n")
	return definition, true
}

// op515's proved 19-byte action envelope names an inventory slot. Dispatch
// this family by the actual persisted item/PVF action, rather than trusting
// an opaque action index or an amount supplied by the client.
func (s *Service) handleCurrentFatiguePotionUse(session *gameSession, body []byte) (bool, error) {
	if session == nil || session.selectedCharacterID == 0 || len(body) != currentQuestClearCouponBodySize || body[2] != 0 {
		return false, nil
	}
	slot := int16(binary.LittleEndian.Uint16(body))
	if slot < 0 {
		return false, nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Inventory == nil || repos.Account == nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	inventory, found, err := repos.Inventory.Load(ctx, characterID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	stack, found := inventory.Slots["0:"+strconv.Itoa(int(slot))]
	if !found || stack.Count <= 0 {
		return false, nil
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil || catalog == nil {
		return false, err
	}
	item, err := catalog.ResolveItem(uint32(stack.ItemID))
	if err != nil {
		return false, nil
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return false, err
	}
	text, err := archive.ReadText(item.PVFPath)
	if err != nil {
		return false, err
	}
	document, err := dnfpvf.Parse(item.PVFPath, text)
	if err != nil {
		return false, err
	}
	definition, supported := fatiguePotionDefinition(stack.ItemID, item.PVFPath, document)
	if !supported {
		return false, nil
	}
	session.dungeon.mu.Lock()
	inDungeon := session.dungeon.runtime != nil
	session.dungeon.mu.Unlock()
	if inDungeon {
		return true, s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketUseStackableAction), 1)
	}
	account, found, err := repos.Account.Load(ctx, s.accountIDForSession(session))
	if err != nil {
		return true, err
	}
	if !found {
		return true, nil
	}
	result, err := dnffatigue.RestorePotion(ctx, repos, account, characterID, slot, definition, s.gameplayNow())
	if err != nil {
		s.logGameEvent(session, "game-fatigue-potion-rejected", "char_id", session.selectedCharacterID, "item_id", stack.ItemID, "action_index", binary.LittleEndian.Uint32(body[7:11]), "error", err)
		return true, s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketUseStackableAction), 1)
	}
	s.logGameEvent(session, "game-fatigue-potion-used", "char_id", session.selectedCharacterID, "item_id", stack.ItemID, "points", definition.Points, "daily_limit", definition.DailyLimit, "cooltime_ms", definition.CoolTimeMS, "remaining_fatigue", result.Character.Stats["fatigue"])
	// Correlate the reply with this request; the quest coupon owns action 167.
	// The persisted item and PVF above remain the authority for the mutation.
	var ack packetWriter
	ack.writeByte(1)
	ack.writeUint16(uint16(slot))
	ack.writeByte(body[2])
	ack.writeUint32(binary.LittleEndian.Uint32(body[7:11]))
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketUseStackableAction), ack.bytes(), 1); err != nil {
		return true, err
	}
	if err := s.sendSelectedIncrementalItemSlotRefreshes(session, "fatigue_potion_after_ack", []alignedcmd.ItemSlotRefresh{{ListType: 0, SlotIndex: slot}}); err != nil {
		return true, err
	}
	return true, s.sendGameUpperRawClass(session, currentFatigueMsgID, buildCurrentFatigueBody(result.Character, true), 0)
}

package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"

	dnfonlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// sub_10DD340: u8 action=0, u32 NPC, u8 inventory, u16 cube slot.
// sub_1D25180: success, action, NPC, u16 affection gain, u32 total, u8 used.
func (s *Service) handleCurrentReturneeNPCGift(session *gameSession, body []byte) error {
	if session == nil {
		return nil
	}
	fail := func(code byte, reason string) error {
		s.logGameEvent(session, "回归NPC赠礼拒绝", "code", code, "reason", reason, "body", fmt.Sprintf("%x", body))
		return s.sendGameUpperRawClass(session, 812, []byte{0, code}, 1)
	}
	if session.selectedCharacterID == 0 || len(body) != 8 || body[0] != 0 || (body[5] != 0 && body[5] != 36) {
		return fail(17, "invalid_request")
	}
	npc := binary.LittleEndian.Uint32(body[1:5])
	slot := int16(binary.LittleEndian.Uint16(body[6:8]))
	session.dungeon.mu.Lock()
	inDungeon := session.dungeon.runtime != nil
	session.dungeon.mu.Unlock()
	if inDungeon {
		return fail(100, "dungeon_scene")
	}
	session.townMu.Lock()
	position := session.townPositionSnapshot
	ready := session.townSceneReadyCharacterID == session.selectedCharacterID && position.CharacterID == session.selectedCharacterID
	session.townMu.Unlock()
	// Clicking an NPC is permitted throughout its current area. The quest
	// meeting trigger's 120x80 proximity rectangle is not an interaction limit.
	if !ready || !s.currentTownHasGiftNPC(position, npc) {
		return fail(100, fmt.Sprintf("npc_outside_current_area town=%d area=%d ready=%t", position.TownID, position.AreaID, ready))
	}
	s.initialEquipmentMu.Lock()
	archive := s.initialEquipmentArchive
	s.initialEquipmentMu.Unlock()
	if archive == nil {
		return fail(101, "pvf_unavailable")
	}
	rules, err := dnfonlineevent.LoadReturneeGiftRules(archive, npc)
	if err != nil {
		return fail(101, err.Error())
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return fail(101, "repository_unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	result, err := dnfonlineevent.GiveReturneeNPCGift(ctx, repos, rules, s.accountIDForSession(session), strconv.Itoa(int(session.selectedCharacterID)), body[5], slot, s.gameplayNow())
	if err != nil {
		code := byte(100)
		switch {
		case errors.Is(err, dnfonlineevent.ErrNPCGiftLevel):
			code = 14
		case errors.Is(err, dnfonlineevent.ErrNPCGiftItem):
			code = 17
		case errors.Is(err, dnfonlineevent.ErrNPCGiftCount):
			code = 22
		case errors.Is(err, dnfonlineevent.ErrNPCGiftLimit):
			code = 102
		}
		return fail(code, err.Error())
	}
	entry := currentItemListEntryFromStack(0, slot, result.Stack)
	if result.Remaining == 0 {
		if slot >= 354 {
			entry.patchCore(slot, uint32(result.Stack.ItemID), 0)
		} else {
			entry = currentItemListEntry{}
			entry.patchCore(slot, 0xFFFFFFFF, 0)
		}
	}
	if err := s.sendGameUpperRawClass(session, 14, buildCurrentItemUpdateBody(0, []currentItemListEntry{entry}), 0); err != nil {
		return err
	}
	var w packetWriter
	w.writeByte(1)
	w.writeByte(0)
	w.writeUint32(npc)
	w.writeUint16(uint16(result.Added))
	w.writeUint32(uint32(result.Points))
	w.writeByte(byte(result.Used))
	if err := s.sendGameUpperRawClass(session, 812, w.bytes(), 1); err != nil {
		return err
	}
	s.logGameEvent(session, "回归NPC赠礼提交", "npc", npc, "container", body[5], "slot", slot, "consumed", rules.Count, "remaining", result.Remaining, "affection_added", result.Added, "affection", result.Points, "daily_used", result.Used)
	return s.sendCurrentReturneeState(session, true)
}

func (s *Service) currentTownHasGiftNPC(position currentTownPositionSnapshot, npc uint32) bool {
	area, ok := s.townArea(int64(position.TownID), int64(position.AreaID))
	if !ok || npc == 0 {
		return false
	}
	s.initialEquipmentMu.Lock()
	archive := s.initialEquipmentArchive
	s.initialEquipmentMu.Unlock()
	if archive == nil {
		return false
	}
	// Physical NPCs can be declared by the room's imported common map.
	// Use the existing strict placement resolver, including revised paths,
	// import bounds/cycles and malformed placement rejection.
	resolved, _, err := readDungeonDropText(archive, "map", area.MapPath)
	if err != nil {
		return false
	}
	m, err := worldmap.LoadTownPlacementMap(archive, resolved)
	if err != nil {
		return false
	}
	for _, spawn := range m.NPCs {
		if spawn.NPCID == int64(npc) {
			return true
		}
	}
	return false
}

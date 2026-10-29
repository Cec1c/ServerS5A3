package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfequip "longheng.io/server/internal/modules/dnf/equip"
)

// V179 VA 0255FF30 writes C2S48 and one u8 equipment slot. The native
// class1 receiver 01FA6BE0 consumes the slot after the shared success byte.
func (s *Service) handleCurrentDecreaseDurability(session *gameSession, request gameplayRequest) error {
	if s == nil || session == nil {
		return nil
	}
	op := uint16(dnfenum.CmdPacketDecreaseDurability)
	s.logInfo("dnfbridge durability request received", "conn_id", session.connID,
		"character_id", session.selectedCharacterID, "classification", request.Classification,
		"body_len", len(request.Body), "has_sequence", request.Sequence != nil)
	reject := func(reason any) error {
		s.logGameEvent(session, "game-durability-rejected", "reason", reason, "body_len", len(request.Body))
		s.logInfo("dnfbridge durability request rejected", "conn_id", session.connID,
			"character_id", session.selectedCharacterID, "reason", reason,
			"body_len", len(request.Body))
		return s.sendGameUpperFailure(session, op, 17)
	}
	if len(request.Body) != 1 || request.Body[0] > 32 {
		return reject("invalid_slot_body")
	}
	if !session.loginPacketSeen || !session.accountFromToken || request.Sequence == nil {
		return reject("authenticated_sequence_required")
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return reject("stale_character_binding")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	account := s.accountIDForSession(session)
	// Match the established equipment mutation order and retain the character
	// binding while committing. No global service lock is held during SQL.
	session.equipmentProjectionMu.Lock()
	defer session.equipmentProjectionMu.Unlock()
	session.dungeon.mu.Lock()
	r := session.dungeon.runtime
	if r == nil || r.Session == nil || r.Room == nil || r.townReturnPending || !dungeonRuntimeOwnsCharacter(r, identity.character) || r.AccountID != account {
		session.dungeon.mu.Unlock()
		return reject("owned_dungeon_required")
	}
	high, initialized := session.guild.purchaseSequence.currentHigh()
	sequence := *request.Sequence
	if !initialized || sequence != high || r.durabilitySequenceKnown && sequence <= r.durabilityLastSequence {
		session.dungeon.mu.Unlock()
		s.logInfo("dnfbridge durability request replay suppressed", "conn_id", session.connID,
			"character_id", session.selectedCharacterID, "sequence", sequence,
			"high", high, "high_initialized", initialized,
			"last_sequence", r.durabilityLastSequence, "last_known", r.durabilitySequenceKnown)
		// A repeated transport event has already received its ACK. Replaying
		// that relative ACK would decrement the client a second time.
		return nil
	}
	reservation, err := s.reserveCurrentCharacterAssets(ctx, identity, account)
	if err != nil {
		session.dungeon.mu.Unlock()
		return reject(err)
	}
	result, err := s.commitCurrentDurability(ctx, identity.character, account, int16(request.Body[0]))
	if err == nil {
		r.durabilitySequenceKnown = true
		r.durabilityLastSequence = sequence
	}
	s.releaseCurrentCharacterAssets(reservation)
	session.dungeon.mu.Unlock()
	if err != nil {
		return reject(err)
	}
	s.logGameEvent(session, "game-durability-decreased", "slot", result.Entry.SlotIndex, "item", result.Entry.ItemID, "before", result.Previous, "after", result.Current, "auto_repaired", result.AutoRepaired)
	s.logInfo("dnfbridge durability request settled", "conn_id", session.connID,
		"character_id", session.selectedCharacterID, "slot", result.Entry.SlotIndex,
		"item_id", result.Entry.ItemID, "before", result.Previous,
		"after", result.Current, "auto_repaired", result.AutoRepaired)
	if result.Previous > 0 {
		if err := s.sendGameUpperSuccess(session, op, []byte{request.Body[0]}); err != nil {
			return err
		}
	} else if !result.AutoRepaired {
		// A zero-durability row cannot be decremented by the relative reader.
		return s.sendGameUpperFailure(session, op, 17)
	}
	if !result.AutoRepaired {
		return nil
	}
	// Same post-commit repair sequence as the existing repair flow. Zero cost,
	// unchanged wallet; premium usage was committed with the equipment.
	ack := make([]byte, 10)
	ack[0] = 1
	binary.LittleEndian.PutUint32(ack[1:5], sceneInventoryUint32FromInt64(result.Gold))
	ack[5] = 3
	binary.LittleEndian.PutUint16(ack[6:8], uint16(result.Entry.SlotIndex))
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketRepairEquipment), ack, 1); err != nil {
		return err
	}
	entry, ok := currentItemListEntryFromEquipment(result.Entry)
	if !ok {
		return fmt.Errorf("committed durability equipment cannot project")
	}
	return s.sendGameUpperRawClass(session, 14, buildCurrentItemUpdateBody(3, []currentItemListEntry{entry}), 0)
}

func (s *Service) commitCurrentDurability(ctx context.Context, character uint16, account string, slot int16) (dnfequip.DecreaseDurabilityResult, error) {
	repos, ok := s.repositoryGroup()
	if !ok {
		return dnfequip.DecreaseDurabilityResult{}, dnfequip.ErrOwnerUnavailable
	}
	owner, err := dnfequip.NewDurabilityOwner(repos)
	if err != nil {
		return dnfequip.DecreaseDurabilityResult{}, err
	}
	catalog, err := s.currentRepairCostCatalog()
	if err != nil {
		return dnfequip.DecreaseDurabilityResult{}, err
	}
	resolve := func(id int64) (alignedcmd.RepairCostEvidence, error) {
		s.repairCostMu.Lock()
		defer s.repairCostMu.Unlock()
		return catalog.resolve(id)
	}
	return owner.Decrease(ctx, dnfequip.DecreaseDurabilityCommand{AccountID: account, CharacterID: character, Slot: slot, Now: s.gameplayNow()}, resolve)
}

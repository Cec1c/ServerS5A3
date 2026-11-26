package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
)

// These immutable rows belong to one prepared entry, shared by its receivers.
// Build each character once; never load all peers again for each recipient.
type partyEntryActorRow struct {
	owner        boundGameSessionCharacter
	current      func() bool
	mode0, mode1 []byte
}

func (s *Service) preparePartyEntryActorBatch(ctx context.Context, leader *gameSession, runtime *runtimeDungeonState, followers []*runtimePartyPreparedDungeonEntry) error {
	if len(followers) == 0 {
		return nil
	}
	participants := append([]*runtimePartyPreparedDungeonEntry{{session: leader, runtime: runtime}}, followers...)
	rows := make([]partyEntryActorRow, 0, len(participants))
	for _, member := range participants {
		owner, ok := s.boundGameSessionCharacterSnapshot(member.session)
		if !ok || (member.current != nil && !member.current()) {
			return fmt.Errorf("party entry actor owner changed")
		}
		current := s.currentPartyProjectionMembershipGuard(owner, nil)
		character := member.runtime.Character
		mode0, err := s.buildCurrentSceneObjectListBodyForSessionInContextStrict(ctx, member.session, owner.character, character.Name, character, true, currentSceneObjectContext)
		if err != nil {
			return err
		}
		adventure := s.currentAccountAdventureGroupSummaryForPacket(ctx, member.session, character, true)
		mode1 := s.buildCurrentActorBindingMode1BodyForSelectedInContext(ctx, member.session, character, true, owner.character, uint32(adventure.ManageLevel), currentSceneObjectContext)
		rows = append(rows, partyEntryActorRow{owner: owner, current: current, mode0: mode0, mode1: mode1})
	}
	for _, member := range participants {
		member.runtime.entryActorRows = rows
	}
	return nil
}

func (s *Service) sendPartyEntryActorBatch(ctx context.Context, session *gameSession, runtime *runtimeDungeonState, source string) error {
	rows := runtime.entryActorRows
	bodies := [2][]byte{{0, 0, 0}, {1, 0, 0}}
	// Native op2 loops count rows for mode0 and mode1. Each row retains its
	// route/owner prefix. Create every CUser before binding any actor; self first.
	for _, self := range []bool{true, false} {
		for _, row := range rows {
			if (row.owner.session == session) != self {
				continue
			}
			bodies[0] = append(bodies[0], row.mode0[3:]...)
			bodies[1] = append(bodies[1], row.mode1[3:]...)
		}
	}
	for _, body := range bodies {
		binary.LittleEndian.PutUint16(body[1:3], uint16(len(rows)))
	}
	if err := s.withGameWire(session, func() error {
		for _, row := range rows {
			if !s.boundGameSessionCharacterCurrent(row.owner) || !row.current() {
				return fmt.Errorf("party entry actor membership changed before write")
			}
		}
		for _, body := range bodies {
			if err := s.sendGameUpperRawClassCodecLocked(session, 2, body, 0, true); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err := s.prepareCurrentSnakeDungeonSkillInfo(session, ctx, runtime.Character, true, source); err != nil {
		return err
	}
	session.preDungeonContextPlayerStateSent = true
	s.logGameEvent(session, "game-party-entry-actor-batch-sent", "source", source, "char_id", session.selectedCharacterID,
		"members", len(rows), "sequence", "mode0_all_then_mode1_all_before_op27")
	return nil
}

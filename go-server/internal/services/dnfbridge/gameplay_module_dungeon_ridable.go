package dnfbridge

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func dungeonRidableGameplayModule() gameplayModuleDefinition {
	return gameplayModuleDefinition{
		Name: "dungeon-ridable",
		UpperHandlers: map[uint16]gameplayHandler{
			uint16(dnfenum.CmdPacketOperateRidableObject): func(s *Service, session *gameSession, request gameplayRequest) error {
				if !requireDefaultGameplayClassification(s, session, request, "game-dungeon-ridable-rejected", "classification_mismatch") {
					return nil
				}
				return s.handleCurrentDungeonRidableReport(session, request)
			},
		},
	}
}

// The current native client mounts/releases locally and reports destruction
// without waiting for an ACK. Consume its exact upper request, validate the
// committed current room, then retire only a zero-regeneration frozen source.
// No 296/317 packet, replacement, occupancy arbitration, reward or room clear
// is inferred from this notification.
func (s *Service) handleCurrentDungeonRidableReport(session *gameSession, request gameplayRequest) error {
	if s == nil || session == nil {
		return nil
	}
	report, err := decodeCurrentDungeonRidableRequest(request.Body)
	if err != nil {
		s.logGameEvent(session, "game-dungeon-ridable-rejected", "reason", "invalid_body", "error", err)
		return nil
	}
	reject := func(reason string) error {
		s.logGameEvent(session, "game-dungeon-ridable-rejected", "reason", reason, "state", report.State, "definition", report.DefinitionKeyRaw, "room_x", report.RoomX, "room_y", report.RoomY)
		return nil
	}
	if s.options.currentClientVersion != "official-24.9" || s.gameUpperClientBodyCodecMode() != gameUpperClientBodyCodecPlain || request.Sequence == nil ||
		!session.loginPacketSeen || !session.accountFromToken || strings.TrimSpace(session.accountID) == "" {
		return reject("current_plain_authenticated_upper_required")
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return reject("stale_character_binding")
	}
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	repos, available := s.repositoryGroup()
	if !available || repos.Character == nil {
		return reject("character_repository_unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	character, found, err := repos.Character.Load(ctx, strconv.Itoa(int(identity.character)))
	if err != nil || !found || accountID == "" || strings.TrimSpace(character.AccountID) != accountID {
		return reject("character_account_mismatch")
	}
	// No socket write or peer-session lock is taken while committing the state.
	session.dungeon.mu.Lock()
	err = s.applyCurrentDungeonRidableReportLocked(session, identity, accountID, request, report)
	session.dungeon.mu.Unlock()
	if err != nil {
		return reject(err.Error())
	}
	return nil
}

func (s *Service) applyCurrentDungeonRidableReportLocked(session *gameSession, identity boundGameSessionCharacter, accountID string, request gameplayRequest, report currentDungeonRidableRequest) error {
	r := session.dungeon.runtime
	finishGates := session.finishLoadingGateSnapshot()
	if r == nil || r.Session == nil || r.Room == nil || r.townReturnPending || !dungeonRuntimeOwnsCharacter(r, identity.character) ||
		r.lifecycleToken == 0 || r.lifecycleToken != session.dungeon.runToken || r.AccountID != accountID ||
		r.ridableReportChannelID != session.channel.ID || r.ridableReportChannelType != session.channel.Type ||
		!finishGates.State || !finishGates.Completion || !s.boundGameSessionCharacterCurrent(identity) {
		return fmt.Errorf("current_committed_runtime_required")
	}
	scene, hasScene := r.Session.Scene()
	room := r.Room.Snapshot()
	run := r.Session.Snapshot().Run
	if !hasScene || scene.Map.Map.ID <= 0 || scene.Coordinate != room.Coordinate || scene.Map.Map.ID != room.MapID ||
		run.Status != worldmap.DungeonRunActive || run.DungeonID != r.Dungeon.ID || run.MazeIndex != r.MazeIndex || r.randomizedObjects == nil ||
		r.randomizedObjects.validate(r.Dungeon, r.MazeIndex) != nil {
		return fmt.Errorf("current_frozen_room_required")
	}
	// Older wire values from a previous room/run cannot become new events.
	// Duplicate reports are harmless; a new run owns a fresh terminal owner.
	high, initialized := session.guild.purchaseSequence.currentHigh()
	if request.Sequence == nil || !initialized || *request.Sequence != high || *request.Sequence <= r.ridableReportLastSequence {
		return fmt.Errorf("current_connection_sequence_required")
	}
	party := runtimePartyStateSnapshot(session)
	if instance := r.partyInstance; instance != nil {
		instance.mu.Lock()
		defer instance.mu.Unlock()
		_, member := instance.members[identity.character]
		if !member || party.PartyID <= 0 || party.PartyID > 0xffff || uint16(party.PartyID) != instance.key.PartyID ||
			instance.sessions[identity.character] != session || instance.scenes[identity.character] != r.Session ||
			instance.key.DungeonID != r.Dungeon.ID || instance.key.MazeIndex != r.MazeIndex || instance.key.Seed != r.Seed ||
			instance.ridableTerminals != r.randomizedObjects.ridableTerminals || instance.ridableSelectionSignature != r.randomizedObjects.entrySignature {
			return fmt.Errorf("current_party_instance_required")
		}
		if _, ok := runtimePartyMemberIndex(identity.character, party); !ok {
			return fmt.Errorf("current_party_member_required")
		}
	} else if party.PartyID > 0 && len(runtimePartyMembers(party)) > 1 {
		return fmt.Errorf("shared_party_instance_required")
	}
	changed, err := r.randomizedObjects.applyRidableReport(scene.Coordinate, report)
	if err != nil {
		return err
	}
	r.ridableReportLastSequence = *request.Sequence
	s.logGameEvent(session, "game-dungeon-ridable-report", "state", report.State, "definition", report.DefinitionKeyRaw, "room", scene.Coordinate.String(), "retired", changed)
	return nil
}

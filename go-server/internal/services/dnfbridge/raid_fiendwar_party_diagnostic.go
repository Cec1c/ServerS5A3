package dnfbridge

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// FiendWar party admission diagnostics are deliberately narrower than the
// packet log. Keep only identity, party/run ownership and scene readiness so a
// disabled packet log can still distinguish a same-run regroup from a
// different-run merge without recording payloads, accounts or endpoints.
const fiendWarPartyAdmissionDiagnosticKind = "game-fiendwar-party-admission"

func (s *Service) fiendWarPartyAdmissionDiagnosticEligible(session *gameSession, mode byte, peerID uint16) bool {
	if s == nil || session == nil || s.options.currentClientVersion != "official-24.9" ||
		!isCurrentFiendWarChannel(session) || (mode != 0 && mode != 12) ||
		peerID == 0 || peerID == session.selectedCharacterID {
		return false
	}
	if s.fiendWarPartySessionHasRuntime(session) {
		return true
	}
	peer, online := s.onlineGameSession(peerID)
	return online && s.fiendWarPartySessionHasRuntime(peer)
}

func (s *Service) fiendWarPartySessionHasRuntime(session *gameSession) bool {
	if session == nil {
		return false
	}
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	return session.dungeon.runtime != nil
}

func (s *Service) logFiendWarPartyAdmission(phase string, session *gameSession, peerID uint16, peer *gameSession, mode byte, reason string, extra ...any) {
	if s == nil || session == nil {
		return
	}
	fields := []any{"phase", phase, "mode", mode, "reason", reason}
	fields = append(fields, s.fiendWarPartyAdmissionSessionFields(session, "char_id", "")...)
	if peer != nil {
		fields = append(fields, s.fiendWarPartyAdmissionSessionFields(peer, "peer_char_id", "peer_")...)
	} else {
		fields = append(fields, "peer_char_id", peerID)
	}
	for i := 0; i+1 < len(extra); i += 2 {
		key, ok := extra[i].(string)
		if !ok || key != "had_invite" {
			continue
		}
		fields = append(fields, key, extra[i+1])
	}
	s.logFiendWarPartyAdmissionFields(fields...)
}

func (s *Service) fiendWarPartyAdmissionSessionFields(session *gameSession, charKey, prefix string) []any {
	if session == nil {
		return nil
	}
	state := runtimePartyStateSnapshot(session)
	partyID, memberCount := state.PartyID, len(runtimePartyMembers(state))
	if identity, bound := s.boundGameSessionCharacterSnapshot(session); bound {
		s.mu.Lock()
		manager := s.runtimePartyManager
		s.mu.Unlock()
		if manager != nil {
			if snapshot, found := manager.SnapshotByUser(identity.character, identity.generation); found {
				partyID, memberCount = int(snapshot.ID), len(snapshot.Members)
			}
		}
	}

	var raidKey uint32
	var raidInstanceID, runID, sceneKey string
	var participants []uint16
	var partyInstanceID uint64
	runtimePresent, scenePresent := false, false
	var postStart, finish, postFinish bool
	finishGates := session.finishLoadingGateSnapshot()
	session.dungeon.mu.Lock()
	runtime := session.dungeon.runtime
	postStart = session.postStartMapPlayerStateSent
	finish = finishGates.Completion
	postFinish = finishGates.PostFinish
	if runtime != nil {
		runtimePresent = true
		if runtime.Session != nil {
			if scene, ok := runtime.Session.Scene(); ok {
				scenePresent = true
				// This is the raw dungeon scene, independent of a managed
				// party run key. Solo runtimes need this most.
				sceneKey = fmt.Sprintf("%d/%d/%d/%d/%d/%d",
					runtime.Dungeon.ID, runtime.MazeIndex,
					scene.Coordinate.X, scene.Coordinate.Y,
					scene.Map.Map.ID, runtime.Seed)
			}
		}
		if runtime.raidEntry != nil {
			raidKey = runtime.raidEntry.RaidKey
			raidInstanceID = runtime.raidEntry.InstanceID
			runID = runtime.raidEntry.RunID
			participants = append([]uint16(nil), runtime.raidEntry.Participants...)
		}
		if runtime.partyInstance != nil {
			partyInstanceID = runtime.partyInstance.id
		}
	}
	session.dungeon.mu.Unlock()

	_, runKeyPresent := s.currentDungeonPartyRunKey(session)
	// Readiness is reported from the live runtime and finish-loading gates.
	// run_key is a separate fact: solo runtimes legitimately have no party key.
	ready := runtimePresent && scenePresent && postStart && finish && postFinish

	return []any{
		charKey, session.selectedCharacterID,
		prefix + "party_id", partyID,
		prefix + "party_members", memberCount,
		prefix + "raid_key", raidKey,
		prefix + "raid_instance_id", raidInstanceID,
		prefix + "run_id", runID,
		prefix + "participants", fiendWarPartyAdmissionParticipants(participants),
		prefix + "scene_key", sceneKey,
		prefix + "run_key", runKeyPresent,
		prefix + "post_start", postStart,
		prefix + "finish", finish,
		prefix + "post_finish", postFinish,
		prefix + "ready", ready,
		prefix + "party_instance_id", partyInstanceID,
	}
}

func fiendWarPartyAdmissionParticipants(ids []uint16) string {
	if len(ids) == 0 {
		return ""
	}
	sorted := append([]uint16(nil), ids...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	parts := make([]string, len(sorted))
	for i, id := range sorted {
		parts[i] = strconv.FormatUint(uint64(id), 10)
	}
	return strings.Join(parts, ",")
}

// Application logging remains useful with raw packet logging disabled.
func (s *Service) logFiendWarPartyAdmissionFields(fields ...any) {
	args := []any{"kind", fiendWarPartyAdmissionDiagnosticKind}
	for i := 0; i+1 < len(fields); i += 2 {
		key, ok := fields[i].(string)
		if !ok {
			continue
		}
		switch key {
		case "phase", "char_id", "peer_char_id", "mode", "reason", "party_id", "peer_party_id", "party_members", "peer_party_members",
			"raid_key", "peer_raid_key", "raid_instance_id", "peer_raid_instance_id",
			"run_id", "peer_run_id", "participants", "peer_participants",
			"scene_key", "peer_scene_key", "run_key", "peer_run_key",
			"post_start", "peer_post_start", "finish", "peer_finish",
			"post_finish", "peer_post_finish", "ready", "peer_ready",
			"party_instance_id", "peer_party_instance_id", "had_invite":
			args = append(args, key, fields[i+1])
		}

	}
	s.logInfo("dnfbridge fiendwar party diagnostic", args...)
}

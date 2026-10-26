package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"sort"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func (s *Service) currentGuildPracticeSessionOwnsRoom(session *gameSession, runtime *runtimeDungeonState, scene worldmap.DungeonRoomScene) bool {
	if session == nil || validateCurrentGuildPracticeEntryRequest(runtime) != nil || session.dungeon.runtime != runtime ||
		!dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) || !currentDungeonRoomOwnsScene(runtime, scene) {
		return false
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	status := runtime.Session.Snapshot().Run.Status
	return ok && identity == runtime.guildPractice.identity && identity.generation == runtime.guildPractice.Generation &&
		(status == worldmap.DungeonRunActive || status == worldmap.DungeonRunCompleted)
}

func (s *Service) handleCurrentGuildPracticeDeathLocked(session *gameSession, runtime *runtimeDungeonState, scene worldmap.DungeonRoomScene, request dungeoncmd.DieMonsterRequest) error {
	if runtime.townReturnPending || !s.currentGuildPracticeSessionOwnsRoom(session, runtime, scene) {
		return nil
	}
	owner := request.OwnerObjectKey
	if owner == ^uint16(0) {
		if request.Layout != dungeoncmd.DieMonsterRequestLayoutVariableCombat || request.CombatEntryCount != 0 {
			return nil
		}
	} else if owner != currentSceneActorObjectKey(session.selectedCharacterID) {
		return nil
	}
	if request.RuntimeObjectKey > 65535 || len(request.OpaqueTail) != 0 {
		return nil
	}
	if err := commitCurrentGuildPracticeDeath(runtime.Room, runtime.Session, request.RuntimeObjectKey); err != nil {
		s.logGameEvent(session, "game-guild-practice-death-rejected", "object_key", request.RuntimeObjectKey, "error", err)
		return nil
	}
	body := buildCurrentGuildPracticeDeathBody(uint16(request.RuntimeObjectKey))
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketNotifyDieMonster), body, 0); err != nil {
		return err
	}
	s.broadcastCurrentGuildPracticeDeath(session, runtime, request.RuntimeObjectKey, body)
	return s.completeCurrentGuildPracticeLocked(session, runtime)
}

// Key zero is valid only for this already registered native local actor table.
// Ordinary dungeon builders retain their nonzero-key invariant.
func buildCurrentGuildPracticeDeathBody(key uint16) []byte {
	body := []byte{0, 0, 0, 0, 0, 0xff, 0}
	binary.LittleEndian.PutUint16(body, key)
	return body
}

func commitCurrentGuildPracticeDeath(room *runtimeDungeonRoom, session *worldmap.DungeonSession, key uint32) error {
	if room == nil || session == nil {
		return errDungeonActorOwnerConflict
	}
	room.mu.Lock()
	defer room.mu.Unlock()
	if room.guildPractice == nil {
		return errDungeonActorOwnerConflict
	}
	if _, ok := room.announcedSessions[session]; !ok {
		return errDungeonMonsterNotAnnounced
	}
	scene, ok := session.Scene()
	if !ok || scene.Coordinate != room.coordinate || scene.Map.Map.ID != room.mapID {
		return errDungeonActorOwnerConflict
	}
	target, ok := room.guildPractice.targets[key]
	if !ok {
		return errDungeonMonsterObjectNotFound
	}
	if !target.Local {
		static, state, err := room.deathTargetLocked(key)
		if err != nil {
			return err
		}
		if *state != runtimeDungeonMonsterAnnounced && *state != runtimeDungeonMonsterDefeated {
			return errDungeonMonsterNotAnnounced
		}
		if reference, ok := scene.RuntimeObjects[key]; !ok || reference != static.Reference {
			return errDungeonActorOwnerConflict
		}
		if _, err := session.MarkHostileDefeated(key); err != nil && !errors.Is(err, worldmap.ErrHostileAlreadyDefeated) {
			return err
		}
		*state = runtimeDungeonMonsterDefeated
	}
	target.Defeated = true
	room.guildPractice.targets[key] = target
	return nil
}

func (s *Service) completeCurrentGuildPracticeLocked(session *gameSession, runtime *runtimeDungeonState) error {
	scene, ok := runtime.Session.Scene()
	if !ok || runtime.townReturnPending || !s.currentGuildPracticeSessionOwnsRoom(session, runtime, scene) || !scene.Cleared || !scene.Boss ||
		!runtime.BossSet || runtime.BossCoordinate != scene.Coordinate || !currentDungeonStoryFinalStageMatches(runtime, scene) {
		return nil
	}
	// A local placed defender is nonblocking and cannot supply the final-map
	// completion proof. At least one announced static objective must be dead.
	proved := false
	runtime.Room.mu.RLock()
	if runtime.Room.guildPractice != nil {
		for _, target := range runtime.Room.guildPractice.targets {
			if !target.Local && target.Defeated {
				proved = true
				break
			}
		}
	}
	runtime.Room.mu.RUnlock()
	if !proved {
		return nil
	}
	if runtime.Session.Snapshot().Run.Status == worldmap.DungeonRunActive {
		if err := runtime.Session.Complete(); err != nil {
			return err
		}
	}
	if runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted || runtime.guildPractice.Finished {
		return nil
	}
	// Native 01FEC7A0 enters practice result phase. No ordinary settlement,
	// card, EXP, quest, guild contribution or reward transaction is involved.
	if err := s.sendGameUpperRawClass(session, 31, nil, 0); err != nil {
		return err
	}
	runtime.guildPractice.Finished = true
	return nil
}

func (s *Service) handleCurrentGuildPracticeBossCheckLocked(session *gameSession, runtime *runtimeDungeonState, request dungeoncmd.BossDieCheckRequest) error {
	scene, ok := runtime.Session.Scene()
	if !ok || runtime.townReturnPending || !s.currentGuildPracticeSessionOwnsRoom(session, runtime, scene) || !scene.Boss || !scene.Cleared ||
		!runtime.BossSet || runtime.BossCoordinate != scene.Coordinate || !currentDungeonStoryFinalStageMatches(runtime, scene) ||
		request.RelatedActorObjectKey != currentSceneActorObjectKey(session.selectedCharacterID) {
		return nil
	}
	runtime.Room.mu.RLock()
	target, found := currentGuildPracticeTarget{}, false
	if runtime.Room.guildPractice != nil {
		target, found = runtime.Room.guildPractice.targets[uint32(request.TargetObjectKey)]
	}
	runtime.Room.mu.RUnlock()
	if !found || target.Local || !target.Defeated {
		return nil
	}
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketNotifyBossDieCheck), buildCurrentDungeonBossDieCheckResponse(request.TargetObjectKey), 0); err != nil {
		return err
	}
	return s.completeCurrentGuildPracticeLocked(session, runtime)
}

func (s *Service) broadcastCurrentGuildPracticeDeath(session *gameSession, runtime *runtimeDungeonState, key uint32, body []byte) {
	room := runtime.Room
	for _, peer := range s.partyDungeonInstancePeers(runtime, session.selectedCharacterID) {
		peer := peer
		run := func() error {
			peer.Session.dungeon.mu.Lock()
			defer peer.Session.dungeon.mu.Unlock()
			r := peer.Session.dungeon.runtime
			if r == nil || r.Session == nil || r.Room != room {
				return nil
			}
			scene, ok := r.Session.Scene()
			if !ok || !s.currentGuildPracticeSessionOwnsRoom(peer.Session, r, scene) {
				return nil
			}
			if err := commitCurrentGuildPracticeDeath(room, r.Session, key); err != nil {
				return nil
			}
			if err := s.sendGameUpperRawClass(peer.Session, uint16(dnfenum.CmdPacketNotifyDieMonster), body, 0); err != nil {
				return err
			}
			return s.completeCurrentGuildPracticeLocked(peer.Session, r)
		}
		if peer.Session.events == nil || peer.Session.events.loop == nil {
			if err := run(); err != nil {
				s.logGameEvent(peer.Session, "game-guild-practice-peer-death-failed", "error", err)
			}
			continue
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
			defer cancel()
			if err := s.callGameSession(ctx, peer.Session, "guild-practice-peer-death", run); err != nil {
				s.logGameEvent(peer.Session, "game-guild-practice-peer-death-failed", "error", err)
			}
		}()
	}
}

// The native placement producer runs when the room is built, including a
// revisit. Restore shared defeated local actors after the op29 commit.
func (s *Service) sendCurrentGuildPracticeDefeatedLocalActors(session *gameSession, runtime *runtimeDungeonState) error {
	if !currentFriendlyGuildPractice(runtime) || runtime.Room == nil {
		return nil
	}
	room := runtime.Room
	room.mu.RLock()
	keys := make([]int, 0)
	if room.guildPractice != nil {
		for key, target := range room.guildPractice.targets {
			if target.Local && target.Defeated {
				keys = append(keys, int(key))
			}
		}
	}
	room.mu.RUnlock()
	sort.Ints(keys)
	for _, key := range keys {
		if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketNotifyDieMonster), buildCurrentGuildPracticeDeathBody(uint16(key)), 0); err != nil {
			return err
		}
	}
	return nil
}

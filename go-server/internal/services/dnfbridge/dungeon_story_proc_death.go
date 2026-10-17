package dnfbridge

import (
	"context"
	"path"
	"reflect"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

type dungeonStoryProcDeathEvidence struct {
	QuestID         int64
	AICharacterID   int64
	ActionPath      string
	PassiveObjectID int64
	BehaviorIndex   int
}

type dungeonStoryProcDeathReceipt struct {
	Evidence   dungeonStoryProcDeathEvidence
	MapID      int64
	Coordinate worldmap.RoomCoordinate
	Reference  worldmap.HostileReference
}

// Script actions own HP changes on the client, including kills with no combat
// contribution owner. Prove the particular announced hostile APC, the current
// connected clear-map quest, and a passive-triggered self-HP-zero action before
// allowing this otherwise rejected owner-sentinel request. This does not run the
// action server-side or pretend to have observed its trigger firing.
func (s *Service) currentDungeonStoryProcDeath(session *gameSession, runtime *runtimeDungeonState, scene worldmap.DungeonRoomScene, request dungeoncmd.DieMonsterRequest) (dungeonStoryProcDeathEvidence, bool) {
	if s == nil || s.options.currentClientVersion != "official-24.9" ||
		session == nil || runtime == nil || runtime.Session == nil || runtime.Room == nil ||
		session.dungeon.runtime != runtime || !dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) {
		return dungeonStoryProcDeathEvidence{}, false
	}
	if request.OwnerObjectKey != ^uint16(0) || request.Layout != dungeoncmd.DieMonsterRequestLayoutVariableCombat ||
		request.CombatEntryCount != 0 || len(request.OpaqueTail) != 0 {
		return dungeonStoryProcDeathEvidence{}, false
	}
	if isPVFTutorialDungeon(runtime) || currentDungeonPathUsesTutorialNamespace(runtime.Dungeon.Path) || request.RuntimeObjectKey == 0 || request.RuntimeObjectKey > uint32(^uint16(0)) {
		return dungeonStoryProcDeathEvidence{}, false
	}
	snapshot := runtime.Session.Snapshot()
	if snapshot.Run.DungeonID != runtime.Dungeon.ID || snapshot.Run.MazeIndex != runtime.MazeIndex {
		return dungeonStoryProcDeathEvidence{}, false
	}
	room := runtime.Room.Snapshot()
	if room.Coordinate != scene.Coordinate || room.MapID != scene.Map.Map.ID {
		return dungeonStoryProcDeathEvidence{}, false
	}
	reference, bound := scene.RuntimeObjects[request.RuntimeObjectKey]
	if !bound || reference.Kind != worldmap.HostileAICharacter || reference.Index < 0 ||
		reference.Index >= len(scene.AICharacters) || reference.Index >= len(scene.Map.Map.AICharacters) ||
		!runtimeSceneContainsHostile(scene.ExpectedHostiles, reference) {
		return dungeonStoryProcDeathEvidence{}, false
	}
	var actor *runtimeDungeonExtendedActor
	for i := range room.ExtendedActors {
		candidate := &room.ExtendedActors[i]
		if candidate.ObjectKey == request.RuntimeObjectKey {
			actor = candidate
			break
		}
	}
	if actor == nil || actor.Kind != runtimeDungeonActorAICharacter || actor.HostileReference == nil ||
		*actor.HostileReference != reference || actor.AICharacter == nil || actor.AICharacterMetadata == nil {
		return dungeonStoryProcDeathEvidence{}, false
	}
	if !reflect.DeepEqual(*actor.AICharacter, scene.AICharacters[reference.Index]) ||
		!reflect.DeepEqual(*actor.AICharacter, scene.Map.Map.AICharacters[reference.Index]) ||
		normalizeDungeonPVFSymbol(actor.AICharacter.Faction) != "monster" ||
		actor.AICharacterMetadata.ID != actor.AICharacter.Code || int64(actor.Packet.Code) != actor.AICharacter.Code {
		return dungeonStoryProcDeathEvidence{}, false
	}
	defeated := dungeonSceneObjectDefeated(scene.DefeatedObjects, request.RuntimeObjectKey)
	if actor.State == runtimeDungeonMonsterDefeated && defeated {
		receipt, ok := runtime.storyProcDeaths[request.RuntimeObjectKey]
		if ok && receipt.MapID == scene.Map.Map.ID && receipt.Coordinate == scene.Coordinate &&
			receipt.Reference == reference && receipt.Evidence.AICharacterID == actor.AICharacter.Code &&
			(snapshot.Run.Status == worldmap.DungeonRunActive || snapshot.Run.Status == worldmap.DungeonRunCompleted) {
			return receipt.Evidence, true
		}
		return dungeonStoryProcDeathEvidence{}, false
	}
	if actor.State != runtimeDungeonMonsterAnnounced || defeated || snapshot.Run.Status != worldmap.DungeonRunActive {
		return dungeonStoryProcDeathEvidence{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	quests, err := s.loadQuestCatalog(ctx)
	if err != nil || !currentDungeonConnectedClearMapQuestOwnsBaseMap(runtime, scene.Map.Map.ID, -1, quests) {
		return dungeonStoryProcDeathEvidence{}, false
	}
	questID := runtime.Dungeon.Mazes[runtime.MazeIndex].QuestConnection[1]
	repos, ok := s.repositoryGroup()
	if !ok || repos.Quest == nil {
		return dungeonStoryProcDeathEvidence{}, false
	}
	record, found, err := repos.Quest.Load(ctx, strconv.FormatUint(uint64(session.selectedCharacterID), 10))
	if err != nil || !found || record.States[questID].Status != "active" || record.States[questID].ProgressValue <= 0 {
		return dungeonStoryProcDeathEvidence{}, false
	}
	catalog, err := s.dungeonAICharacterCatalog()
	if err != nil || catalog.source == nil {
		return dungeonStoryProcDeathEvidence{}, false
	}
	definition, found, err := catalog.Find(actor.AICharacter.Code)
	if err != nil || !found || definition.Path != actor.AICharacterMetadata.Path {
		return dungeonStoryProcDeathEvidence{}, false
	}
	actionPath := dungeonAIProcActionPath(definition)
	if actionPath == "" {
		return dungeonStoryProcDeathEvidence{}, false
	}
	text, err := catalog.source.ReadText(actionPath)
	if err != nil {
		return dungeonStoryProcDeathEvidence{}, false
	}
	document, err := worldmap.ParseDocument(actionPath, text)
	if err != nil {
		return dungeonStoryProcDeathEvidence{}, false
	}
	evidence, ok := parseDungeonStoryProcDeath(document)
	if !ok {
		return dungeonStoryProcDeathEvidence{}, false
	}
	evidence.QuestID, evidence.AICharacterID, evidence.ActionPath = questID, definition.ID, actionPath
	return evidence, true
}

func (s *Service) replayCurrentDungeonStoryProcDeathLocked(session *gameSession, runtime *runtimeDungeonState, objectKey uint32) error {
	if err := s.replayCurrentDungeonMonsterDeathLocked(session, runtime, objectKey); err != nil {
		return err
	}
	if runtime.bossDieCheckAccepted {
		return s.completeCurrentDungeonBossDieCheckLocked(session, runtime, "story_proc_death_write_retry")
	}
	if scene, ok := runtime.Session.Scene(); ok {
		return s.completeCurrentDungeonOrdinaryFinalRoomAfterDeathLocked(session, runtime, scene, objectKey)
	}
	return nil
}

func dungeonAIProcActionPath(definition pvfDungeonAICharacterDefinition) string {
	raw := ""
	for _, section := range definition.Sections {
		if strings.EqualFold(section.Name, "proc action") {
			if raw != "" || len(section.Tokens) != 1 || section.Tokens[0].Kind != dnfpvf.TokenString {
				return ""
			}
			raw = strings.ReplaceAll(strings.TrimSpace(section.Tokens[0].Value), "\\", "/")
			if raw == "" {
				return ""
			}
		}
	}
	if raw == "" || path.IsAbs(raw) {
		return ""
	}
	resolved := path.Clean(path.Join(path.Dir(definition.Path), raw))
	if !strings.HasPrefix(strings.ToLower(resolved), "aicharacter/") {
		return ""
	}
	return resolved
}

// ACT has flat sections: [TRIGGER] inside a behavior is a command, not a new
// top-level trigger. Split only outside blocks and reject incomplete blocks.
// Only the explicit passive-index/radius -> ME behavior -> SET HP 0 shape is
// recognized; arbitrary text, zero HP tests and another object's behavior are
// insufficient evidence.
func parseDungeonStoryProcDeath(document *dnfpvf.Document) (dungeonStoryProcDeathEvidence, bool) {
	if document == nil {
		return dungeonStoryProcDeathEvidence{}, false
	}
	var triggers, behaviors [][]dnfpvf.Section
	kind := ""
	var block []dnfpvf.Section
	for _, section := range document.Sections {
		key := strings.ToLower(strings.TrimSpace(section.Name))
		if section.Start < 0 || section.End < section.Start || section.End > len(document.Tokens) {
			return dungeonStoryProcDeathEvidence{}, false
		}
		if kind == "" {
			if key != "trigger" && key != "behavior" {
				return dungeonStoryProcDeathEvidence{}, false
			}
			if len(document.Tokens[section.Start:section.End]) != 0 {
				return dungeonStoryProcDeathEvidence{}, false
			}
			kind = key
			block = nil
			continue
		}
		if key == "/"+kind {
			if kind == "trigger" {
				triggers = append(triggers, block)
			} else {
				behaviors = append(behaviors, block)
			}
			kind = ""
			block = nil
			continue
		}
		block = append(block, section)
	}
	if kind != "" {
		return dungeonStoryProcDeathEvidence{}, false
	}
	names := []string{"which", "passive", "checkup", "is index", "/is index", "distance", "<=", "/checkup", "checked no", ">=", "do proc behavior", "me"}
	for _, trigger := range triggers {
		for i, section := range trigger {
			if !strings.EqualFold(section.Name, "which") || len(trigger)-i != len(names) {
				continue
			}
			values := make([]int64, len(names))
			valid := true
			for j, name := range names {
				part := trigger[i+j]
				if !strings.EqualFold(part.Name, name) {
					valid = false
					break
				}
				tokens := document.Tokens[part.Start:part.End]
				needsInt := j == 3 || j == 6 || j == 9 || j == 11
				if needsInt {
					if len(tokens) != 1 || tokens[0].Kind != dnfpvf.TokenInt {
						valid = false
						break
					}
					values[j] = tokens[0].Int
				} else if len(tokens) != 0 {
					valid = false
					break
				}
			}
			if !valid || values[3] <= 0 || values[6] <= 0 || values[9] != 1 || values[11] < 0 || values[11] >= int64(len(behaviors)) {
				continue
			}
			behavior := behaviors[values[11]]
			if len(behavior) != 1 || !strings.EqualFold(behavior[0].Name, "set hp") {
				continue
			}
			tokens := document.Tokens[behavior[0].Start:behavior[0].End]
			if len(tokens) == 1 && tokens[0].Kind == dnfpvf.TokenInt && tokens[0].Int == 0 {
				return dungeonStoryProcDeathEvidence{PassiveObjectID: values[3], BehaviorIndex: int(values[11])}, true
			}
		}
	}
	return dungeonStoryProcDeathEvidence{}, false
}

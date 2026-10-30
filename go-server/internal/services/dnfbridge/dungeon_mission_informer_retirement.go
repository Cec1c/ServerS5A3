package dnfbridge

import (
	"context"
	"reflect"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

const missionInformerDirectionPath = "aicharacter/missionquest/gunner/codenameinfo/codenameinfo_direction.aic"

type dungeonMissionInformerRetirement struct {
	MapID      int64
	Coordinate worldmap.RoomCoordinate
	Reference  worldmap.HostileReference
	Body       []byte
}

// 3797 的现场在九怪清完前后都可能收到已宣告情报员的零贡献报告；
// 结合专用 direction 定义，按离场兼容处理，不推断编译后 AI 的内部动作或不透明字段语义。
// 该演员属于 monster 阵营，不能交给只接非敌对 APC 的 RetireNonHostileAIActor。
// 这里只同步这一演员的生命周期和零掉落 op38，不进入杀怪、任务、经验或结算流程。
// 调用方持有 session.dungeon.mu；普通演员和其它任务仍走原有处理器。
func (s *Service) handleCurrentMissionInformerRetirementLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	request dungeoncmd.DieMonsterRequest,
) (bool, error) {
	if s == nil || s.options.currentClientVersion != "official-24.9" ||
		session == nil || runtime == nil || runtime.Room == nil || runtime.Session == nil ||
		session.dungeon.runtime != runtime || runtime.lifecycleToken == 0 || runtime.lifecycleToken != session.dungeon.runToken ||
		!dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) ||
		runtime.AccountID != s.accountIDForSession(session) ||
		runtime.partyInstance != nil || !currentMissionInformerRetirementShape(request) {
		return false, nil
	}
	if runtime.Dungeon.ID != 50 || normalizeDungeonRuntimePath(runtime.Dungeon.Path) != "dungeon/act6/astraythief.dgn" ||
		scene.Map.Map.ID != 400076 || normalizeDungeonRuntimePath(scene.Map.Map.Path) != "map/astraythief/m3797_e6201.map" ||
		scene.Coordinate != (worldmap.RoomCoordinate{X: 2, Y: 0}) || scene.Boss {
		return false, nil
	}
	run := runtime.Session.Snapshot().Run
	room := runtime.Room.Snapshot()
	if run.Status != worldmap.DungeonRunActive || run.DungeonID != runtime.Dungeon.ID || run.MazeIndex != runtime.MazeIndex ||
		room.MapID != scene.Map.Map.ID || room.Coordinate != scene.Coordinate {
		return false, nil
	}
	reference, bound := scene.RuntimeObjects[request.RuntimeObjectKey]
	if !bound || reference.Kind != worldmap.HostileAICharacter || reference.Index != 0 ||
		!runtimeSceneContainsHostile(scene.ExpectedHostiles, reference) {
		return false, nil
	}
	if receipt, found := runtime.missionInformerRetirements[request.RuntimeObjectKey]; found {
		if receipt.MapID != scene.Map.Map.ID || receipt.Coordinate != scene.Coordinate || receipt.Reference != reference ||
			!dungeonSceneObjectDefeated(scene.DefeatedObjects, request.RuntimeObjectKey) {
			return false, nil
		}
		if target, defeated := runtime.Room.DefeatedDeathTarget(request.RuntimeObjectKey); !defeated || target.Reference != receipt.Reference {
			return false, nil
		}
		// 使用提交前冻结的同一份通知；不再依赖可能已经变化的任务账，也不补算资产。
		return true, s.sendMissionInformerRetirementLocked(session, runtime, request.RuntimeObjectKey, receipt.Body, true)
	}
	// 情报员是本房专用的非阻挡演员，其退场不依赖普通怪先清完。
	// 身份由当前任务、run、房间和已宣告的专用定义绑定，不能由不透明运行字段授权。
	if len(scene.BlockingHostiles) != 9 || len(scene.Map.Map.Monsters) != 9 || len(room.Monsters) != 9 ||
		len(scene.AICharacters) != 1 || len(scene.Map.Map.AICharacters) != 1 {
		return false, nil
	}
	for _, blocker := range scene.BlockingHostiles {
		monster, found := runtime.Room.MonsterByReference(blocker, runtimeDungeonMonsterAnnounced, runtimeDungeonMonsterDefeated)
		if blocker.Kind != worldmap.HostileMonster || !found || scene.RuntimeObjects[monster.ObjectKey] != blocker ||
			(monster.State == runtimeDungeonMonsterDefeated) != dungeonSceneObjectDefeated(scene.DefeatedObjects, monster.ObjectKey) {
			return false, nil
		}
	}
	var actor *runtimeDungeonExtendedActor
	for index := range room.ExtendedActors {
		if room.ExtendedActors[index].ObjectKey == request.RuntimeObjectKey {
			actor = &room.ExtendedActors[index]
			break
		}
	}
	if actor == nil || actor.Kind != runtimeDungeonActorAICharacter || actor.State != runtimeDungeonMonsterAnnounced ||
		actor.HostileReference == nil || *actor.HostileReference != reference || actor.Packet.Blocking != 0 ||
		actor.AICharacter == nil || actor.AICharacterMetadata == nil || actor.AICharacter.Code != 11117 || int64(actor.Packet.Code) != actor.AICharacter.Code ||
		!reflect.DeepEqual(*actor.AICharacter, scene.AICharacters[0]) || !reflect.DeepEqual(*actor.AICharacter, scene.Map.Map.AICharacters[0]) ||
		normalizeDungeonPVFSymbol(actor.AICharacter.Faction) != "monster" {
		return false, nil
	}
	if runtime.MazeIndex < 0 || runtime.MazeIndex >= len(runtime.Dungeon.Mazes) || runtime.Request.Difficulty < 1 {
		return false, nil
	}
	connection := runtime.Dungeon.Mazes[runtime.MazeIndex].QuestConnection
	if !reflect.DeepEqual(connection, []int64{0, 3797, 1}) {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	quests, err := s.loadQuestCatalog(ctx)
	if err != nil {
		return false, nil
	}
	definition, found := quests.Find(3797)
	if !found || normalizeDungeonPVFSymbol(definition.Grade) != "mission" || normalizeDungeonPVFSymbol(definition.Type) != "clear map" ||
		definition.MainQuestID != 0 || !reflect.DeepEqual(definition.IntData, []int64{400080, 25110}) {
		return false, nil
	}
	repos, available := s.repositoryGroup()
	if !available || repos.Quest == nil {
		return false, nil
	}
	record, found, err := repos.Quest.Load(ctx, strconv.Itoa(int(session.selectedCharacterID)))
	if err != nil || !found || record.CharacterID != runtime.Character.CharacterID {
		return false, nil
	}
	state, found := record.States[3797]
	if !found {
		state, found = record.Progress[3797]
	}
	if !found || state.Status != "active" || state.ProgressValue <= 0 {
		return false, nil
	}
	catalog, err := s.dungeonAICharacterCatalog()
	if err != nil {
		return false, nil
	}
	ai, found, err := catalog.Find(actor.AICharacter.Code)
	if err != nil || !found || normalizeDungeonRuntimePath(ai.Path) != missionInformerDirectionPath ||
		!reflect.DeepEqual(ai, *actor.AICharacterMetadata) || !missionInformerDirectionPattern(ai) {
		return false, nil
	}
	body, err := buildCurrentDungeonDeathNotificationBody(request.RuntimeObjectKey, currentDungeonDeathResponseAICharacter)
	if err != nil {
		return true, err
	}
	// 复用原子对象提交，仅维护这一非阻挡APC的权威死亡/场景绑定，九怪保持原状态。
	// 随即返回，绝不继续普通死亡处理器的经验、掉落、击杀任务和清图奖励链。
	target, _, err := runtime.Room.commitActorDeathReport(request.RuntimeObjectKey, runtime.Session, false)
	if err != nil {
		return true, err
	}
	if runtime.missionInformerRetirements == nil {
		runtime.missionInformerRetirements = make(map[uint32]dungeonMissionInformerRetirement)
	}
	runtime.missionInformerRetirements[request.RuntimeObjectKey] = dungeonMissionInformerRetirement{
		MapID: scene.Map.Map.ID, Coordinate: scene.Coordinate, Reference: target.Reference, Body: append([]byte(nil), body...),
	}
	runtime.Room.RecordDeathResponseBody(request.RuntimeObjectKey, body)
	return true, s.sendMissionInformerRetirementLocked(session, runtime, request.RuntimeObjectKey, body, false)
}

func currentMissionInformerRetirementShape(request dungeoncmd.DieMonsterRequest) bool {
	// codec 已证明 74 字节 = 31 字节前缀 + 零贡献 + 39 字节运行块和4字节footer。
	// 运行块没有领域语义；三次现场的值不同，不能把任一录包值作为固定身份凭据。
	// 此处只核对结构和零战斗前缀，演员、任务和场景归属由调用方完整核验。
	return request.Layout == dungeoncmd.DieMonsterRequestLayoutVariableCombat && request.OwnerObjectKey == ^uint16(0) &&
		request.RuntimeObjectKey != 0 && request.RuntimeObjectKey <= uint32(^uint16(0)) &&
		request.CombatEntryCount == 0 && len(request.CombatEntries) == 0 && len(request.OpaqueTail) == 0 &&
		request.OfficialCombatValue64 == [2]uint64{} && request.OfficialCombatValue32 == [2]uint32{} &&
		len(request.RuntimeTail) == 43
}

func missionInformerDirectionPattern(definition pvfDungeonAICharacterDefinition) bool {
	matched := false
	for _, section := range definition.Sections {
		if strings.EqualFold(strings.TrimSpace(section.Name), "ai pattern") {
			if matched || len(section.Tokens) != 1 || section.Tokens[0].Kind != dnfpvf.TokenString || section.Tokens[0].Value != "CodenameInfo_AI.h" {
				return false
			}
			matched = true
		}
	}
	return matched
}

func (s *Service) sendMissionInformerRetirementLocked(session *gameSession, runtime *runtimeDungeonState, objectKey uint32, body []byte, replay bool) error {
	s.logGameEvent(session, "使命情报员-退场通知", "quest_id", 3797, "map_id", 400076,
		"object_key", objectKey, "run_token", runtime.lifecycleToken, "replay", replay, "reward_assets", "unchanged")
	if err := s.sendGameUpperRawClass(session, 38, body, 0); err != nil {
		return err
	}
	s.cancelCurrentDungeonReturnAfterDungeonEvidenceLocked(session, runtime, "accepted_mission_informer_retirement_after_pending_op24")
	return nil
}

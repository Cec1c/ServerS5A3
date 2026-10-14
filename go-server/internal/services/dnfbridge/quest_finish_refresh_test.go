package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
	dnfprofession "longheng.io/server/internal/modules/dnf/profession"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestProfessionFinishIdentityUsesCommittedGrowAndCurrentOwner(t *testing.T) {
	for _, grow := range []byte{1, 0x11, 0x21, 0x22} {
		for _, dungeon := range []bool{false, true} {
			repositories := finishBridgeSeed(t)
			before, _, _ := repositories.Character.Load(context.Background(), "19")
			character := dnfrepo.CloneCharacter(before)
			character.Name = "转职测试"
			character.Stats["grow_type"] = int64(grow)
			connection := &bufferConn{}
			service := &Service{
				options:            options{gameUpperBodyCodec: gameUpperBodyCodecPlain},
				repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
			}
			session := &gameSession{conn: connection, selectedCharacterID: 19,
				townActorOwnerChannel: 9, connectionTownActorOwnerChannel: 9,
				preDungeonContextPlayerStateSent: dungeon}
			result := dnfquest.FinishCommitResult{AtomicCommitted: true, CharacterID: "19", HasProfession: true,
				Profession:          dnfprofession.Transition{NewGrowType: grow},
				PostCommitCharacter: character, PostCommitSkill: dnfrepo.SkillRecord{CharacterID: "19"}}
			if err := service.sendCurrentProfessionFinishActorIdentity(session, result); err != nil {
				t.Fatal(err)
			}
			packets := splitAllFinishBridgePackets(t, connection.write.Bytes())
			if len(packets) != 1 || packets[0].Header.Classification != 0 ||
				packets[0].Header.MsgID != uint16(dnfenum.CmdPacketSetUDPIPPort) {
				t.Fatalf("职业刷新包错误: %v", finishBridgePacketIDs(packets))
			}
			body := packets[0].Body
			if body[0] != 0 || binary.LittleEndian.Uint16(body[1:3]) != 1 ||
				body[4] != currentSelectedActorAppearanceOwnerContext(session) ||
				binary.LittleEndian.Uint16(body[5+0x47:5+0x47+2]) != 19 || body[actorGrowByteStart(character.Name)] != grow {
				t.Fatalf("提交身份/觉醒段/归属未保留 grow=%x dungeon=%t body=%x", grow, dungeon, body)
			}
			count, _, ok := currentSceneObjectTailSummaryForLog(body)
			if !ok || count != 14 {
				t.Fatalf("外观快照应包括全部14格（含空格）：count=%d ok=%t", count, ok)
			}
			after, _, _ := repositories.Character.Load(context.Background(), "19")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("投影职业身份不应改写角色仓储")
			}
		}
	}
}

func TestProfessionFinishIdentityRejectsWrongOwner(t *testing.T) {
	repos := finishBridgeSeed(t)
	character, _, _ := repos.Character.Load(context.Background(), "19")
	character.Stats["grow_type"] = 1
	conn := &bufferConn{}
	service := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	session := &gameSession{conn: conn, selectedCharacterID: 20}
	result := dnfquest.FinishCommitResult{AtomicCommitted: true, CharacterID: "19", HasProfession: true,
		Profession: dnfprofession.Transition{NewGrowType: 1}, PostCommitCharacter: character,
		PostCommitSkill: dnfrepo.SkillRecord{CharacterID: "19"}}
	if err := service.sendCurrentProfessionFinishActorIdentity(session, result); err == nil || conn.write.Len() != 0 {
		t.Fatalf("错角色投影必须拒绝，err=%v len=%d", err, conn.write.Len())
	}
}

// 本机实际连接地图的出口条件切片；不是人工地图放行或伪造玩家任务。
const questGateMapText = "[town movable area]\n116 134 120 40 38 0 730 322 100 80 39 0\n" +
	"[quest condition]\n3156\n[/quest condition]\n[/town movable area]\n"

func TestTownQuestGateParserLimitsScope(t *testing.T) {
	for _, test := range []struct {
		text string
		want bool
	}{
		{questGateMapText, true},
		{"[town movable area with animation]\n[movable area]\n[area]\n1 2 3 4\n[quest condition]\n3156\n[/quest condition]\n[/movable area]\n[/town movable area with animation]\n", true},
		{"[quest condition]\n3156\n[/quest condition]\n", false},
		{"[quest animation]\n3156 1\n[/quest animation]\n", false},
		{"[town movable area]\n1 2 3 4 38 0\n[quest condition]\n3155\n[/quest condition]\n[/town movable area]\n", false},
	} {
		doc, err := dnfpvf.Parse("gate.map", test.text)
		if err != nil {
			t.Fatal(err)
		}
		parsed := worldmap.ParseMap(3, "gate.map", doc)
		if got := currentTownMapExitUsesQuest(parsed, 3156); got != test.want {
			t.Fatalf("出口作用域判定=%t want=%t text=%s", got, test.want, test.text)
		}
	}
}

func questGateRefreshFixture(t *testing.T) (*Service, *gameSession, dnfrepo.Group, dnfquest.FinishCommitResult) {
	t.Helper()
	service, session, repos := newTownMoveTest(t)
	service.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
	service.questCatalog = finishBridgeCatalog(t)
	service.seriaRoomDoorMapReader = fakePVFMapReader(t, map[string]string{"Elvengard_hendon.map": questGateMapText})
	character, _, _ := repos.Character.Load(context.Background(), "29")
	character.Stats["area_id"] = 3
	if err := repos.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	quest := dnfrepo.QuestRecord{CharacterID: "29", States: map[int64]dnfrepo.QuestState{3156: {Status: "completed"}}}
	if err := repos.Quest.Save(context.Background(), quest); err != nil {
		t.Fatal(err)
	}
	skill, _, _ := repos.Skill.Load(context.Background(), "29")
	session.townPositionSnapshot = currentTownPositionSnapshot{
		CharacterID: 29, TownID: 38, AreaID: 3, PositionX: 562, PositionY: 321, MovementCode: 5, PositionValid: true}
	return service, session, repos, dnfquest.FinishCommitResult{AtomicCommitted: true, CharacterID: "29",
		QuestID: 3156, Source: "n_quest/test.qst", CompletionKey: "quest-finish/29/3156",
		PreviousLevel: 1, NewLevel: 1, PostCommitCharacter: character, PostCommitSkill: skill, PostCommitQuest: quest}
}

func TestTownQuestGateFinishRefreshKeepsPositionAndQuestOrder(t *testing.T) {
	service, session, repos, result := questGateRefreshFixture(t)
	beforeChar, _, _ := repos.Character.Load(context.Background(), "29")
	beforeQuest, _, _ := repos.Quest.Load(context.Background(), "29")
	beforePosition := session.townPositionSnapshot
	session.currentFinishLoadingStateSent = true
	session.currentFinishLoadingCompletionSent = true
	session.postFinishLoadingPlayerStateSent = true
	if err := service.sendCurrentFinishQuestPostCommitSnapshots(session, result); err != nil {
		t.Fatal(err)
	}
	data := session.conn.(*bufferConn).write.Bytes()
	clear, data := splitCurrentGameServerUpperPacketAuto(t, data)
	assertFinishBridgeClearQuestProjection(t, clear, 3156)
	transition, data := splitTownAreaTransitionSequence(t, data, 29, 38, 3, 562, 321, 5, 3)
	if binary.LittleEndian.Uint16(transition.Body[2:4]) != 1 {
		t.Fatalf("当前区域名册应只有真实本机角色: %x", transition.Body)
	}
	active, data := splitCurrentGameServerUpperPacketAuto(t, data)
	acceptable, rest := splitCurrentGameServerUpperPacketAuto(t, data)
	if active.Header.MsgID != currentActiveQuestSnapshotMsgID || acceptable.Header.MsgID != currentAcceptableQuestListMsgID || len(rest) != 0 {
		t.Fatalf("出口刷新后的任务列表顺序错误 active=%d acceptable=%d rest=%x", active.Header.MsgID, acceptable.Header.MsgID, rest)
	}
	afterChar, _, _ := repos.Character.Load(context.Background(), "29")
	afterQuest, _, _ := repos.Quest.Load(context.Background(), "29")
	if !reflect.DeepEqual(beforeChar, afterChar) || !reflect.DeepEqual(beforeQuest, afterQuest) || session.townPositionSnapshot != beforePosition {
		t.Fatal("原地场景刷新不得改写任务、位置或真实角色")
	}
	if session.currentFinishLoadingStateSent || session.currentFinishLoadingCompletionSent || session.postFinishLoadingPlayerStateSent {
		t.Fatal("当前地图的 finish-loading 收尾必须重新允许")
	}
}

func TestTownQuestGateRefreshRejectsUnrelatedOrStaleState(t *testing.T) {
	for _, reason := range []string{"other_quest", "incomplete", "canonical_active", "wrong_character", "wrong_scene", "no_position", "cold_scene", "replay", "dungeon", "party", "store", "scene_changed"} {
		t.Run(reason, func(t *testing.T) {
			service, session, _, result := questGateRefreshFixture(t)
			switch reason {
			case "other_quest":
				result.QuestID = 3155
				result.PostCommitQuest.States[3155] = dnfrepo.QuestState{Status: "completed"}
			case "incomplete":
				result.PostCommitQuest.States[3156] = dnfrepo.QuestState{Status: "active"}
			case "canonical_active":
				result.PostCommitQuest.States[3156] = dnfrepo.QuestState{Status: "active"}
				result.PostCommitQuest.Progress = map[int64]dnfrepo.QuestState{3156: {Status: "completed"}}
			case "wrong_character":
				session.selectedCharacterID = 30
			case "wrong_scene":
				result.PostCommitCharacter.Stats["area_id"] = 0
			case "no_position":
				session.townPositionSnapshot.PositionValid = false
			case "cold_scene":
				session.townSceneReadyCharacterID = 0
			case "replay":
				result.Replayed = true
			case "dungeon":
				session.dungeon.runtime = &runtimeDungeonState{}
			case "party":
				session.party.state.PartyID = 2
			case "store":
				service.expertJobVisitors = map[*gameSession]uint16{session: 18}
			case "scene_changed":
				reader := service.seriaRoomDoorMapReader
				service.seriaRoomDoorMapReader = func(area dnftown.Area) (worldmap.Map, bool) {
					parsed, found := reader(area)
					session.townPositionSnapshot.AreaID = 0
					return parsed, found
				}
			}
			if err := service.refreshCurrentTownQuestGateScene(session, result); err != nil {
				t.Fatal(err)
			}
			if session.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("无关/过期场景不得发刷新包")
			}
		})
	}
}

package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	dnfprofession "longheng.io/server/internal/modules/dnf/profession"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Exercise the post-commit senders, not the skill builder with a caller-chosen
// correct layout. These are isolated committed-state fixtures, not live players.
func TestSkillTreeQuestRefreshUsesActiveSecondPage(t *testing.T) {
	for _, scenario := range []string{"level_gain", "sp_reward"} {
		t.Run(scenario, func(t *testing.T) {
			service, session, repositories, result := skillTreeQuestRefreshFixture(t)
			if scenario == "level_gain" {
				result.PreviousLevel = result.NewLevel - 1
			} else {
				result.SPDelta = 1
			}
			if err := service.sendCurrentFinishQuestPostCommitSnapshots(session, result); err != nil {
				t.Fatalf("ordinary post-commit refresh: %v", err)
			}
			assertSkillTreeQuestRefreshPackets(t, session)
			assertSkillTreeQuestRefreshDidNotSave(t, repositories, result.PostCommitSkill)
		})
	}
}

func TestSkillTreeProfessionQuestRefreshUsesActiveSecondPage(t *testing.T) {
	service, session, repositories, result := skillTreeQuestRefreshFixture(t)
	result.HasProfession = true
	result.Profession = dnfprofession.Transition{
		Kind: dnfprofession.KindAwakening, PreviousGrowType: 1,
		NewGrowType: 0x11, FirstGrowType: 1, AwakeningStage: 1,
	}
	if err := service.sendCurrentProfessionFinishPostCommitSnapshots(session, result); err != nil {
		t.Fatalf("profession post-commit refresh: %v", err)
	}
	assertSkillTreeQuestRefreshPackets(t, session)
	assertSkillTreeQuestRefreshDidNotSave(t, repositories, result.PostCommitSkill)
}

func skillTreeQuestRefreshFixture(t *testing.T) (*Service, *gameSession, dnfrepo.Group, dnfquest.FinishCommitResult) {
	t.Helper()
	ctx := context.Background()
	repositories := finishBridgeSeed(t)
	character, found, err := repositories.Character.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load fixture character: found=%t err=%v", found, err)
	}
	character.Name = "SkillRefresh"
	character.Level = 20
	character.Stats["grow_type"] = 0x11
	character.Stats["skill_tree_index"] = 1
	character.Stats["skill_tree_expanded"] = 1
	skill := dnfrepo.SkillRecord{
		CharacterID: "19",
		Skills: map[int64]dnfrepo.SkillState{
			46: {Level: 3, Enabled: true},
			48: {Level: 4, Enabled: true},
		},
		Points: dnfrepo.SkillPointState{TotalSP: 200, RemainingSP: 60, TotalTP: 10, RemainingTP: 2, SyncedLevel: 20},
		Layouts: map[int]dnfrepo.SkillLayout{
			0: {0: 46, 4: 47},
			1: {1: 46, 5: 48},
		},
		Trees: dnfrepo.SkillTreeState{
			Expanded: true, Active: 1,
			Other: dnfrepo.SkillProfile{
				Skills: map[int64]dnfrepo.SkillState{
					46: {Level: 1, Enabled: true},
					47: {Level: 2, Enabled: true},
				},
				Points: dnfrepo.SkillPointState{TotalSP: 200, RemainingSP: 140, TotalTP: 10, RemainingTP: 8, SyncedLevel: 20},
			},
		},
	}
	quest := dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{3145: {Status: "completed"}}}
	if err := repositories.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Skill.Save(ctx, skill); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Quest.Save(ctx, quest); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:            options{gameUpperBodyCodec: gameUpperBodyCodecPlain},
		questCatalog:       finishBridgeCatalog(t),
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{
		conn: &bufferConn{}, connID: "skill-tree-quest-refresh", selectedCharacterID: 19,
		accountID: character.AccountID, townActorOwnerChannel: 9, connectionTownActorOwnerChannel: 9,
	}
	return service, session, repositories, dnfquest.FinishCommitResult{
		AtomicCommitted: true, CharacterID: "19", QuestID: 3145,
		CompletionKey: "quest-finish/19/3145/skill-tree-refresh",
		PreviousLevel: character.Level, NewLevel: character.Level,
		PostCommitCharacter: character, PostCommitSkill: dnfrepo.CloneSkill(skill), PostCommitQuest: quest,
	}
}

func assertSkillTreeQuestRefreshPackets(t *testing.T, session *gameSession) {
	t.Helper()
	packets := splitAllFinishBridgePackets(t, session.conn.(*bufferConn).write.Bytes())
	var body []byte
	count := 0
	for _, packet := range packets {
		if packet.Header.Classification == 0 && packet.Header.MsgID == currentSkillInfoMsgID {
			body = packet.Body
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one class0/op19, got=%d packets=%v", count, finishBridgePacketIDs(packets))
	}
	if len(body) < 4 || int(binary.LittleEndian.Uint32(body[:4])) != len(body)-4 {
		t.Fatalf("op19 protobuf length mismatch: %x", body)
	}
	outerFields, outer := consumeCurrentSkillInfoFields(t, body[4:])
	if !reflect.DeepEqual(outerFields[1], []uint64{19}) || len(outer[2]) != 2 {
		t.Fatalf("op19 type/pages: fields=%v pages=%d", outerFields, len(outer[2]))
	}
	type row struct {
		Slot, Skill, Level uint64
	}
	wantRows := [][]row{
		{{0, 46, 1}, {4, 47, 2}},
		{{1, 46, 3}, {5, 48, 4}},
	}
	for page, raw := range outer[2] {
		fields, nested := consumeCurrentSkillInfoFields(t, raw)
		wantSP, wantTP := []uint64{140, 60}[page], []uint64{8, 2}[page]
		if !reflect.DeepEqual(fields[1], []uint64{wantSP}) || !reflect.DeepEqual(fields[2], []uint64{wantTP}) {
			t.Errorf("page=%d SP/TP crossed: got=%v wantSP=%d wantTP=%d", page, fields, wantSP, wantTP)
		}
		var gotRows []row
		var quickSlots []int
		for _, rawSkill := range nested[3] {
			entry, _ := consumeCurrentSkillInfoFields(t, rawSkill)
			if len(entry[1]) != 1 || len(entry[2]) != 1 || len(entry[3]) != 1 {
				t.Fatalf("page=%d invalid skill entry: %v", page, entry)
			}
			gotRows = append(gotRows, row{entry[1][0], entry[2][0], entry[3][0]})
			if entry[1][0] < 6 {
				quickSlots = append(quickSlots, int(entry[1][0]))
			}
		}
		if !reflect.DeepEqual(gotRows, wantRows[page]) {
			t.Errorf("op19 page=%d rows(slot,skill,level)=%v want=%v", page, gotRows, wantRows[page])
		}
		// Check active quick slots from the emitted bytes, not a second call to
		// buildCurrentSceneSkillInfoBody which could hide the caller's error.
		if page == 1 && !reflect.DeepEqual(quickSlots, []int{1, 5}) {
			t.Errorf("op19 active page=1 quick slots=%v want=[1 5]", quickSlots)
		}
	}
}

func assertSkillTreeQuestRefreshDidNotSave(t *testing.T, repositories dnfrepo.Group, before dnfrepo.SkillRecord) {
	t.Helper()
	after, found, err := repositories.Skill.Load(context.Background(), before.CharacterID)
	if err != nil || !found {
		t.Fatalf("read skills after projection: found=%t err=%v", found, err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("post-commit projection changed persisted skill profiles")
	}
}

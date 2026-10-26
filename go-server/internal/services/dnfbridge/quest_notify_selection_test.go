package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func questNotifyTestScope(id string) string { return "character:" + id + ":quest_notify" }

func questNotifyTestBody(ids ...uint32) []byte {
	body := make([]byte, 1+len(ids)*4)
	body[0] = byte(len(ids))
	for i, id := range ids {
		binary.LittleEndian.PutUint32(body[1+i*4:], id)
	}
	return body
}

func questNotifyTestCatalog(t *testing.T) *dnfquest.Catalog {
	t.Helper()
	source := questListTestSource{dnfquest.DefaultList: ""}
	for _, id := range []int{3797, 4874, 2711, 8643} {
		file := fmt.Sprintf("notify_%d.qst", id)
		source[dnfquest.DefaultList] += fmt.Sprintf("%d `%s`\n", id, file)
		source["n_quest/"+file] = "[grade]\n`[mission]`\n[level]\n1 99\n[type]\n`[clear map]`\n[int data]\n400080\n"
	}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func questNotifyTestFixture(t *testing.T) (*Service, *gameSession, *bufferConn, dnfrepo.Group) {
	t.Helper()
	s, session, conn, repos := testGameOptionFixture(t)
	s.questCatalog = questNotifyTestCatalog(t)
	session.townSceneReadyCharacterID = session.selectedCharacterID
	for _, id := range []string{"19", "20"} {
		if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{CharacterID: id, AccountID: "account-1", Level: 90}); err != nil {
			t.Fatal(err)
		}
		if err := repos.Quest.Save(context.Background(), dnfrepo.QuestRecord{
			CharacterID: id,
			States: map[int64]dnfrepo.QuestState{
				3797: {Status: "active", ProgressValue: 1},
				4874: {Status: "active", ProgressValue: 1},
				2711: {Status: "active", ProgressValue: 0},
			},
			Progress: map[int64]dnfrepo.QuestState{8643: {Status: "active", ProgressValue: 1}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	return s, session, conn, repos
}

func questNotifyTestSend(t *testing.T, s *Service, session *gameSession, sequence uint16, body []byte) error {
	t.Helper()
	raw := make([]byte, dnfproto.LegacyGameHeaderSize+len(body))
	raw[0] = 1
	binary.LittleEndian.PutUint16(raw[1:3], 506)
	binary.LittleEndian.PutUint32(raw[3:7], uint32(len(raw)))
	binary.LittleEndian.PutUint16(raw[11:13], sequence)
	copy(raw[dnfproto.LegacyGameHeaderSize:], body)
	return s.handleLegacyGamePacket(session, raw)
}

func questNotifyTestStored(t *testing.T, repos dnfrepo.Group, characterID string) string {
	t.Helper()
	record, found, err := repos.Settings.Load(context.Background(), questNotifyTestScope(characterID))
	if err != nil || !found {
		t.Fatalf("任务追踪存档不存在:角色=%s found=%t err=%v", characterID, found, err)
	}
	return record.Values["selection_hex"]
}

func TestQuestNotifySelectionLiveLegacyFramePersistsAndClearsPerCharacter(t *testing.T) {
	s, session, conn, repos := questNotifyTestFixture(t)
	// 冻结现场: 13字节头，count2，按勾选顺序4874、2711；后者已达成待交付。
	raw, err := hex.DecodeString("01fa011600000038a2f4cb2902020a130000970a0000")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.handleLegacyGamePacket(session, raw); err != nil {
		t.Fatal(err)
	}
	if got := questNotifyTestStored(t, repos, "19"); got != "020a130000970a0000" {
		t.Fatalf("真实506未保存: %s", got)
	}
	// 角色B入场时不能沿用角色A的ready，也不能用A的旧副本放行。
	session.selectedCharacterID = 20
	session.dungeon.runtime = &runtimeDungeonState{Character: dnfrepo.CharacterRecord{CharacterID: "19"}}
	if err := questNotifyTestSend(t, s, session, 554, []byte{0}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := repos.Settings.Load(context.Background(), questNotifyTestScope("20")); err != nil || found {
		t.Fatalf("bootstrap空列表写入角色B found=%t err=%v", found, err)
	}
	session.dungeon.runtime = nil
	session.townSceneReadyCharacterID = 20
	if err := questNotifyTestSend(t, s, session, 555, questNotifyTestBody(3797, 8643)); err != nil {
		t.Fatal(err)
	}
	if got := questNotifyTestStored(t, repos, "20"); got != "02d50e0000c3210000" {
		t.Fatalf("角色B/旧progress任务未保存: %s", got)
	}
	session.selectedCharacterID, session.townSceneReadyCharacterID = 19, 19
	if err := questNotifyTestSend(t, s, session, 556, []byte{0}); err != nil {
		t.Fatal(err)
	}
	if got := questNotifyTestStored(t, repos, "19"); got != "00" {
		t.Fatalf("真实取消未清空: %s", got)
	}
	if got := questNotifyTestStored(t, repos, "20"); got != "02d50e0000c3210000" {
		t.Fatalf("跨角色污染: %s", got)
	}
	if _, found, err := repos.Settings.Load(context.Background(), "character:19:character_option"); err != nil || found {
		t.Fatalf("任务追踪误写447 blob found=%t err=%v", found, err)
	}
	if conn.write.Len() != 0 {
		t.Fatalf("506意外下发%d字节", conn.write.Len())
	}
}

func TestQuestNotifySelectionStrictShapeAndInactiveIDsDoNotOverwrite(t *testing.T) {
	for name, body := range map[string][]byte{
		"missing_count": nil, "more_than_four": questNotifyTestBody(3797, 4874, 2711, 8643, 100),
		"short_id": {1, 0xd5}, "trailing_bytes": {0, 0}, "zero": questNotifyTestBody(0),
		"negative": questNotifyTestBody(0xffffffff), "duplicate": questNotifyTestBody(4874, 4874),
		"unknown": questNotifyTestBody(999999), "not_active": questNotifyTestBody(3797),
	} {
		t.Run(name, func(t *testing.T) {
			s, session, _, repos := questNotifyTestFixture(t)
			seed := questNotifyTestBody(4874)
			if err := questNotifyTestSend(t, s, session, 1, seed); err != nil {
				t.Fatal(err)
			}
			quests, _, _ := repos.Quest.Load(context.Background(), "19")
			quests.States[3797] = dnfrepo.QuestState{Status: "completed"}
			quests.Progress[3797] = dnfrepo.QuestState{Status: "active", ProgressValue: 1}
			if err := repos.Quest.Save(context.Background(), quests); err != nil {
				t.Fatal(err)
			}
			if err := questNotifyTestSend(t, s, session, 2, body); err != nil {
				t.Fatal(err)
			}
			if got := questNotifyTestStored(t, repos, "19"); got != hex.EncodeToString(seed) {
				t.Fatalf("非法请求覆盖已有勾选: %s", got)
			}
		})
	}
}

func TestQuestNotifySelectionLegacyWrongClassificationDoesNotSave(t *testing.T) {
	for _, classification := range []byte{0, 2} {
		t.Run(fmt.Sprintf("class_%d", classification), func(t *testing.T) {
			s, session, conn, repos := questNotifyTestFixture(t)
			body := questNotifyTestBody(4874)
			raw := make([]byte, dnfproto.LegacyGameHeaderSize+len(body))
			raw[0] = classification
			binary.LittleEndian.PutUint16(raw[1:3], 506)
			binary.LittleEndian.PutUint32(raw[3:7], uint32(len(raw)))
			binary.LittleEndian.PutUint16(raw[11:13], 1)
			copy(raw[dnfproto.LegacyGameHeaderSize:], body)
			if err := s.handleLegacyGamePacket(session, raw); err != nil {
				t.Fatal(err)
			}
			if _, found, err := repos.Settings.Load(context.Background(), questNotifyTestScope("19")); err != nil || found {
				t.Fatalf("错误分类写入任务追踪 found=%t err=%v", found, err)
			}
			if conn.write.Len() != 0 {
				t.Fatal("错误分类意外回包")
			}
		})
	}
}

func TestQuestNotifySelectionBootstrapDoesNotEraseSavedSelection(t *testing.T) {
	s, session, _, repos := questNotifyTestFixture(t)
	seed := questNotifyTestBody(4874, 2711)
	if err := questNotifyTestSend(t, s, session, 1, seed); err != nil {
		t.Fatal(err)
	}
	session.townSceneReadyCharacterID = 0
	// 服务端已发角色状态，仍未收到客户端场景确认，不把这个阶段当玩家取消。
	session.initialTownRouteStage = currentInitialTownRoutePlayerStateSent
	session.initialTownRouteCharacterID = session.selectedCharacterID
	for seq, body := range [][]byte{{0}, questNotifyTestBody(3797)} {
		if err := questNotifyTestSend(t, s, session, uint16(seq+2), body); err != nil {
			t.Fatal(err)
		}
	}
	if got := questNotifyTestStored(t, repos, "19"); got != hex.EncodeToString(seed) {
		t.Fatalf("入场上报覆盖存档: %s", got)
	}
	session.townSceneReadyCharacterID = session.selectedCharacterID
	if err := questNotifyTestSend(t, s, session, 4, []byte{0}); err != nil {
		t.Fatal(err)
	}
	if got := questNotifyTestStored(t, repos, "19"); got != "00" {
		t.Fatalf("入场后真实取消被吞掉: %s", got)
	}
}

type questNotifyFaultSettings struct {
	dnfrepo.SettingsRepository
	loadErr, saveErr error
	saveAttempts     int
}

func (r *questNotifyFaultSettings) Load(ctx context.Context, scope string) (dnfrepo.SettingsRecord, bool, error) {
	if strings.HasSuffix(scope, ":quest_notify") && r.loadErr != nil {
		return dnfrepo.SettingsRecord{}, false, r.loadErr
	}
	return r.SettingsRepository.Load(ctx, scope)
}

func (r *questNotifyFaultSettings) Save(ctx context.Context, record dnfrepo.SettingsRecord) error {
	if strings.HasSuffix(record.Scope, ":quest_notify") {
		r.saveAttempts++
		if r.saveErr != nil {
			return r.saveErr
		}
	}
	return r.SettingsRepository.Save(ctx, record)
}

func TestQuestNotifySelectionRepositoryFailuresPreserveSavedSelection(t *testing.T) {
	for _, kind := range []string{"load", "save"} {
		t.Run(kind, func(t *testing.T) {
			s, session, _, repos := questNotifyTestFixture(t)
			if err := questNotifyTestSend(t, s, session, 1, questNotifyTestBody(4874)); err != nil {
				t.Fatal(err)
			}
			base := repos.Settings
			fault := &questNotifyFaultSettings{SettingsRepository: base}
			if kind == "load" {
				fault.loadErr = errors.New("读取失败夹具")
			} else {
				fault.saveErr = errors.New("保存失败夹具")
			}
			repos.Settings = fault
			s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
			if err := questNotifyTestSend(t, s, session, 2, []byte{0}); err != nil {
				t.Fatalf("偏好保存故障导致连接错误: %v", err)
			}
			if kind == "load" && fault.saveAttempts != 0 {
				t.Fatal("读取失败后继续覆盖存档")
			}
			repos.Settings = base
			if got := questNotifyTestStored(t, repos, "19"); got != "010a130000" {
				t.Fatalf("失败改写已有存档: %s", got)
			}
			if err := questNotifyTestSend(t, s, session, 3, []byte{0}); err != nil {
				t.Fatalf("仓储恢复后取消不可重试: %v", err)
			}
		})
	}
}

func TestQuestNotifySelectionACKFourSlotsPreserveAdjacentBytes(t *testing.T) {
	s, _, _, repos := questNotifyTestFixture(t)
	ctx := context.Background()
	quests, _, _ := repos.Quest.Load(ctx, "19")
	quests.States[3797] = dnfrepo.QuestState{Status: "completed"}
	// 旧Progress不能使已完成任务复活；未知PVF条目也不恢复。
	quests.Progress[3797] = dnfrepo.QuestState{Status: "active", ProgressValue: 1}
	quests.States[999999] = dnfrepo.QuestState{Status: "active", ProgressValue: 1}
	seed := hex.EncodeToString(questNotifyTestBody(3797, 4874, 2711, 999999))
	if err := repos.Settings.Save(ctx, dnfrepo.SettingsRecord{Scope: questNotifyTestScope("19"), Values: map[string]string{"selection_hex": seed}}); err != nil {
		t.Fatal(err)
	}
	ids, err := s.loadCurrentQuestNotifySelection(ctx, "19", quests, true)
	if err != nil || !reflect.DeepEqual(ids, []uint32{4874, 2711}) {
		t.Fatalf("恢复未过滤失效任务/丢失待交付任务 ids=%v err=%v", ids, err)
	}
	for _, overflow := range []bool{false, true} {
		t.Run(fmt.Sprintf("overflow_%t", overflow), func(t *testing.T) {
			rows := dnfrepo.CloneQuest(quests)
			if overflow {
				for id := int64(1000); id < 1040; id++ {
					rows.States[id] = dnfrepo.QuestState{Status: "active", ProgressValue: id}
				}
			}
			premium := append([]byte{2}, make([]byte, 18)...)
			before := buildCurrentSelectCharacterAckBody(dnfrepo.CharacterRecord{Level: 90, Stats: map[string]int64{currentDungeonTutorialCompletedKey: 1, "fatigue_limit": 188, "fatigue": 180}}, true, rows, true, 19, 38, 1234, premium)
			after, err := projectCurrentSelectAckQuestNotify(before, ids)
			if err != nil {
				t.Fatal(err)
			}
			values, offset, ok := currentSelectAckIntermediateState(after)
			if !ok || values != [4]uint32{4874, 2711, 0, 0} {
				t.Fatalf("四槽=%v offset=%d ok=%t", values, offset, ok)
			}
			if len(before) != len(after) || !bytes.Equal(before[:offset], after[:offset]) || !bytes.Equal(before[offset+16:], after[offset+16:]) {
				t.Fatal("任务行、premium或town/tutorial/疲劳邻接字节被修改")
			}
			if !bytes.Equal(before[offset:offset+16], make([]byte, 16)) {
				t.Fatal("投影原地改写了原始ACK")
			}
		})
	}
	if got := questNotifyTestStored(t, repos, "19"); got != seed {
		t.Fatalf("只读恢复过滤改写存档: %s", got)
	}
}

package dnfbridge

import (
	"context"
	"errors"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Exercise the real selection producer rather than only the packet helper:
// restoring the four slots must not be lost at its sole production call site.
func TestQuestNotifySelectionProductionSelectACK(t *testing.T) {
	for _, scenario := range []string{"saved", "load_error", "corrupt", "unsaved", "cleared"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			s, repos := newAccountRecordBootstrapFixture(t)
			s.questCatalog = questNotifyTestCatalog(t)
			session := newAccountRecordBootstrapSession()
			quests := dnfrepo.QuestRecord{CharacterID: "10", States: map[int64]dnfrepo.QuestState{
				4874: {Status: "active", ProgressValue: 1},
				2711: {Status: "active", ProgressValue: 0},
			}}
			if err := repos.Quest.Save(ctx, quests); err != nil {
				t.Fatal(err)
			}
			seed := "020a130000970a0000"
			if scenario == "corrupt" {
				seed = "010a13"
			}
			if scenario == "cleared" {
				seed = "00"
			}
			if scenario != "unsaved" {
				if err := repos.Settings.Save(ctx, dnfrepo.SettingsRecord{Scope: questNotifyTestScope("10"), Values: map[string]string{"selection_hex": seed}}); err != nil {
					t.Fatal(err)
				}
			}
			base := repos.Settings
			fault := &questNotifyFaultSettings{SettingsRepository: base}
			if scenario == "load_error" {
				fault.loadErr = errors.New("选角偏好读取失败夹具")
			}
			repos.Settings = fault
			s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
			if err := s.sendUpperCSharpSelectInit(session, selectFirstSlotRequest()); err != nil {
				t.Fatalf("选角被任务追踪偏好阻断: %v", err)
			}
			if session.selectedCharacterID != 10 {
				t.Fatalf("未走真实选角生产者: %d", session.selectedCharacterID)
			}
			packets := splitAllCurrentGameServerUpperPackets(t, session.conn.(*bufferConn).write.Bytes())
			found := false
			want := [4]uint32{}
			if scenario == "saved" {
				want = [4]uint32{4874, 2711, 0, 0}
			}
			for _, packet := range packets {
				if packet.Header.MsgID != 4 || packet.Header.Classification != 1 {
					continue
				}
				found = true
				values, _, ok := currentSelectAckIntermediateState(packet.Body)
				if !ok || values != want {
					t.Fatalf("生产ACK追踪槽=%v want=%v ok=%t", values, want, ok)
				}
			}
			if !found {
				t.Fatal("未发送真实class1/op4选角ACK")
			}
			if fault.saveAttempts != 0 {
				t.Fatal("选角恢复写回了任务追踪存档")
			}
			record, exists, err := base.Load(ctx, questNotifyTestScope("10"))
			if err != nil || exists != (scenario != "unsaved") {
				t.Fatalf("存档存在性被修改 exists=%t err=%v", exists, err)
			}
			if exists && record.Values["selection_hex"] != seed {
				t.Fatalf("选角恢复覆盖原件: %+v", record.Values)
			}
		})
	}
}

func TestQuestNotifySelectionReadyDungeonAndFourNativeSlots(t *testing.T) {
	s, session, _, repos := questNotifyTestFixture(t)
	session.townSceneReadyCharacterID = 0
	session.dungeon.runtime = &runtimeDungeonState{Character: dnfrepo.CharacterRecord{CharacterID: "19"}}
	ids := []uint32{8643, 2711, 3797, 4874}
	if err := questNotifyTestSend(t, s, session, 1, questNotifyTestBody(ids...)); err != nil {
		t.Fatal(err)
	}
	quests, _, _ := repos.Quest.Load(context.Background(), "19")
	restored, err := s.loadCurrentQuestNotifySelection(context.Background(), "19", quests, true)
	if err != nil || !reflect.DeepEqual(restored, ids) {
		t.Fatalf("合法图内四槽顺序丢失 ids=%v err=%v", restored, err)
	}
	if err := questNotifyTestSend(t, s, session, 2, []byte{0}); err != nil {
		t.Fatal(err)
	}
	if got := questNotifyTestStored(t, repos, "19"); got != "00" {
		t.Fatalf("图内取消未清空: %s", got)
	}
}

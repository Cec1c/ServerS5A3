package dnfbridge

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestFreeCardRefreshesCompletedStoryAfterCardWithoutFinishingOrAccepting(t *testing.T) {
	for _, test := range []struct {
		name       string
		clearedID  int64
		state      dnfrepo.QuestState
		wantNotify bool
	}{
		{"direct_main", 3145, dnfrepo.QuestState{Status: "active", ProgressValue: 0}, true},
		{"linked_main", 3054, dnfrepo.QuestState{Status: "active", ProgressValue: 0}, true},
		{"manual_finish_before_card", 3145, dnfrepo.QuestState{Status: "completed", ProgressValue: 0}, false},
		{"unfinished", 3145, dnfrepo.QuestState{Status: "active", ProgressValue: 1}, false},
		{"unrelated_zero_main", 6000, dnfrepo.QuestState{Status: "active", ProgressValue: 0}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, runtime, session, conn, _ := prepareCompletedSettlementRuntime(t)
			completeCurrentDungeonRatingForCardPhaseTest(t, service, runtime, session, conn)
			service.questCatalog = storyCardTestCatalog(t)
			repositories, ok := service.repositoryGroup()
			if !ok {
				t.Fatal("missing repositories")
			}
			record := dnfrepo.QuestRecord{CharacterID: "99", States: map[int64]dnfrepo.QuestState{
				3145: test.state,
				3146: test.state,
				3054: {Status: "completed", ProgressValue: 0},
			}}
			if err := repositories.Quest.Save(context.Background(), record); err != nil {
				t.Fatal(err)
			}
			before, _, err := repositories.Quest.Load(context.Background(), "99")
			if err != nil {
				t.Fatal(err)
			}
			runtime.clearMapCompletionPhaseAPersisted = true
			runtime.clearMapCompletionQuestIDs = []int64{test.clearedID}
			for _, op := range []uint16{uint16(dnfenum.CmdPacketScoreScrollState), uint16(dnfenum.CmdPacketCardSelectRightState)} {
				if err := service.handleGameUpper(session, buildCurrentDungeonClientUpperForPhaseTest(t, op, nil)); err != nil {
					t.Fatal(err)
				}
			}
			conn.write.Reset()
			request := buildCurrentDungeonClientUpperForPhaseTest(t, uint16(dnfenum.CmdPacketSelectCard), []byte{0})
			if err := service.handleGameUpper(session, request); err != nil {
				t.Fatal(err)
			}
			card, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if card.Header.MsgID != uint16(dnfenum.CmdPacketSelectCard) {
				t.Fatalf("first response=%d, want card", card.Header.MsgID)
			}
			if test.wantNotify {
				quest, tail := splitGameServerUpperPacket(t, rest)
				if quest.Header.MsgID != currentActiveQuestSnapshotMsgID || quest.Header.Classification != 0 ||
					!bytes.Equal(quest.Body, buildCurrentActiveQuestSnapshotBody(before, true)) || len(tail) != 0 {
					t.Fatalf("post-card quest packet=%+v body=%x tail=%x", quest.Header, quest.Body, tail)
				}
			} else if len(rest) != 0 {
				t.Fatalf("ineligible quest produced notification=%x", rest)
			}
			after, _, err := repositories.Quest.Load(context.Background(), "99")
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("card refresh mutated quests: before=%+v after=%+v err=%v", before, after, err)
			}
			conn.write.Reset()
			if err := service.handleGameUpper(session, request); err != nil {
				t.Fatal(err)
			}
			_, rest = splitGameServerUpperPacket(t, conn.write.Bytes())
			if len(rest) != 0 {
				t.Fatalf("card replay reopened quest UI=%x", rest)
			}
		})
	}
}

func storyCardTestCatalog(t *testing.T) *dnfquest.Catalog {
	t.Helper()
	index, err := dnfpvf.Build(context.Background(), activeQuestRepairTestSource{
		dnfquest.DefaultList: "3145 `direct.qst`\n3146 `parent.qst`\n3054 `child.qst`\n6000 `side.qst`\n",
		"n_quest/direct.qst": "[grade]\n`[epic]`\n[type]\n`[clear map]`\n[int data]\n76126\n[/int data]\n",
		"n_quest/parent.qst": "[grade]\n`[epic]`\n[type]\n`[quest clear]`\n[int data]\n3054\n[/int data]\n",
		"n_quest/child.qst":  "[grade]\n`[sub]`\n[type]\n`[clear map]`\n[main quest]\n3146\n[int data]\n76136\n[/int data]\n",
		"n_quest/side.qst":   "[grade]\n`[side]`\n[type]\n`[clear map]`\n[int data]\n76126\n[/int data]\n",
	}, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

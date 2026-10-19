package dnfbridge

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestSeekerIncompleteQuestRetryStaysInCurrentDungeon(t *testing.T) {
	for _, tc := range []struct {
		name     string
		progress int64
		extra    map[string]string
	}{
		{name: "objective-incomplete", progress: 1},
		{name: "objective-complete-not-handed-in", progress: 0},
		{name: "reward-pending", progress: 0, extra: map[string]string{"reward_state": "pending"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, rt, session, conn, repos := prepareStoryChainCompletedSettlementRuntime(t)
			ctx := context.Background()
			character, _, err := repos.Character.Load(ctx, "99")
			if err != nil {
				t.Fatal(err)
			}
			character.Job = "0"
			if err = repos.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			if err = repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "99", States: map[int64]dnfrepo.QuestState{3148: {Status: "active", ProgressValue: tc.progress, Extra: tc.extra}}}); err != nil {
				t.Fatal(err)
			}
			rt.settlementPlayResultReceived, rt.settlementStatisticReceived = true, true
			markCurrentDungeonCardRewardCommittedForTest(rt)
			conn.write.Reset()
			if err = svc.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketEplpCommand), []byte{1, 0}); err != nil {
				t.Fatal(err)
			}
			next := session.dungeon.runtime
			if next == nil || next == rt || next.Dungeon.ID != 700 || next.Request.Difficulty != rt.Request.Difficulty {
				if next == nil {
					t.Fatal("retry runtime missing")
				}
				t.Fatalf("retry dungeon=%d difficulty=%d want current700/difficulty%d", next.Dungeon.ID, next.Request.Difficulty, rt.Request.Difficulty)
			}
			record, _, err := repos.Quest.Load(ctx, "99")
			if err != nil {
				t.Fatal(err)
			}
			if record.States[3148].Status != "active" {
				t.Fatalf("current state=%+v", record.States[3148])
			}
			if state, ok := record.States[3149]; ok {
				t.Fatalf("successor invented=%+v", state)
			}
		})
	}
}

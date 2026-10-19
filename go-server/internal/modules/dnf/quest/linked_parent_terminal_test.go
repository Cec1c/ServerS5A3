package quest

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestTownParentCallbackPreservesTerminalRewardSettlement(t *testing.T) {
	for _, test := range []struct {
		name     string
		main     int64
		progress int64
		want     int64
	}{
		{"ready-reward-child", 3458, 0, 0},
		{"unresolved-reward-child", 3458, 1, 1},
		{"wrong-parent-child", 3459, 0, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := catalogTestSource{
				DefaultList:          "3458 `parent.qst`\n3473 `child.qst`\n",
				"n_quest/parent.qst": "[type]\n`[quest clear]`\n[int data]\n3473\n",
				"n_quest/child.qst":  "[grade]\n`[sub]`\n[attribute]\n`not give exp quest`\n[/attribute]\n[type]\n`[seeking]`\n[main quest]\n3458\n[int data]\n10164800 3\n[reward type]\n`[item]`\n[reward int data]\n10164800 4\n",
			}
			source["n_quest/child.qst"] = strings.Replace(source["n_quest/child.qst"], "[main quest]\n3458", "[main quest]\n"+strconv.FormatInt(test.main, 10), 1)
			index, err := pvf.Build(context.Background(), source, pvf.BuildOptions{Lists: []string{DefaultList}})
			if err != nil {
				t.Fatal(err)
			}
			catalog, err := Load(context.Background(), index)
			if err != nil {
				t.Fatal(err)
			}
			record := dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
				3458: {Status: "active", ProgressValue: 1},
				3473: {Status: "active", ProgressValue: test.progress},
			}}
			plan, err := catalog.PlanTownLinkedProgress(record, 3458, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			if !plan.Handled || plan.ParentProgress != test.want {
				t.Fatalf("parent progress=%d want=%d handled=%v", plan.ParentProgress, test.want, plan.Handled)
			}
			state := plan.Record.States[3473]
			if state.Status != "active" || state.Extra["reward_state"] != "" || len(plan.CompletedQuestIDs) != 0 {
				t.Fatalf("op33 must leave reward settlement to op34: %+v", state)
			}
		})
	}
}

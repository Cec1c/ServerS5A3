package quest

import (
	"context"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func rentalGuideTestCatalog(t *testing.T, questType string, intData string) *Catalog {
	t.Helper()
	index, err := dnfpvf.Build(context.Background(), catalogTestSource{
		DefaultList: "8581 `common/chn_system_10_starpoint_guide.qst`\n",
		"n_quest/common/chn_system_10_starpoint_guide.qst": "[grade]\n`[system]`\n" +
			"[type]\n`" + questType + "`\n" +
			"[sub type]\n0\n" +
			"[int data]\n" + intData + "\n" +
			"[level]\n10 99\n" +
			"[job]\n`[all]`\n" +
			"[grow type]\n-1\n" +
			"[reward type]\n`[item]`\n" +
			"[reward int data]\n0 0\n" +
			"[gold multiple]\n10\n",
	}, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestAdvanceRentalGuideQuestOnlyMovesExactAcceptedDefinition(t *testing.T) {
	for _, projection := range []string{"states", "progress"} {
		t.Run(projection, func(t *testing.T) {
			ctx := context.Background()
			repositories := dnfrepomemory.NewMemoryGroup()
			guide := dnfrepo.QuestState{
				Status:        "active",
				ProgressValue: 1,
				UpdatedAt:     time.Unix(1_600_000_000, 0).UTC(),
				Extra: map[string]string{
					"keep":       "metadata",
					"pvf_path":   rentalGuideQuestPath,
					"quest_type": "[custom quest]",
				},
			}
			record := dnfrepo.QuestRecord{
				CharacterID: "19",
				States: map[int64]dnfrepo.QuestState{
					2901: {Status: "active", ProgressValue: 7, Extra: map[string]string{"keep": "other"}},
				},
			}
			if projection == "states" {
				record.States[RentalGuideQuestID] = guide
			} else {
				record.Progress = map[int64]dnfrepo.QuestState{RentalGuideQuestID: guide}
			}
			if err := repositories.Quest.Save(ctx, record); err != nil {
				t.Fatal(err)
			}
			catalog := rentalGuideTestCatalog(t, "[custom quest]", "1 0 1")
			updatedAt := time.Unix(1_700_000_000, 0).UTC()
			changed, err := AdvanceRentalGuideQuest(ctx, repositories.Quest, "19", catalog, updatedAt)
			if err != nil {
				t.Fatal(err)
			}
			if !changed {
				t.Fatal("exact accepted guide definition was not advanced")
			}
			if _, err := catalog.PlanFinishReward(
				CharacterEligibility{Level: 20, Job: 0, GrowType: 0},
				RentalGuideQuestID,
				^uint16(0),
				false,
			); err != nil {
				t.Fatalf("advanced guide is not finishable: %v", err)
			}
			persisted, found, err := repositories.Quest.Load(ctx, "19")
			if err != nil || !found {
				t.Fatalf("load quest found=%t err=%v", found, err)
			}
			got, known := questStateFor(persisted, RentalGuideQuestID)
			if !known || got.Status != "active" || got.ProgressValue != 0 ||
				!got.UpdatedAt.Equal(updatedAt) || got.Extra["keep"] != "metadata" {
				t.Fatalf("advanced guide state = %+v", got)
			}
			if persisted.States[2901].ProgressValue != 7 ||
				persisted.States[2901].Extra["keep"] != "other" {
				t.Fatalf("unrelated quest changed: %+v", persisted.States)
			}
		})
	}
}

func TestAdvanceRentalGuideQuestLeavesEveryOtherStateUnchanged(t *testing.T) {
	tests := []struct {
		name       string
		questType  string
		intData    string
		status     string
		progress   int64
		withRecord bool
	}{
		{name: "unaccepted", questType: "[custom quest]", intData: "1 0 1", status: "", progress: 0},
		{name: "completed", questType: "[custom quest]", intData: "1 0 1", status: "completed", progress: 1, withRecord: true},
		{name: "already advanced", questType: "[custom quest]", intData: "1 0 1", status: "active", progress: 0, withRecord: true},
		{name: "wrong type", questType: "[normal]", intData: "1 0 1", status: "active", progress: 1, withRecord: true},
		{name: "wrong int data", questType: "[custom quest]", intData: "1 0 2", status: "active", progress: 1, withRecord: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			repositories := dnfrepomemory.NewMemoryGroup()
			if test.withRecord {
				if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
					CharacterID: "19",
					States: map[int64]dnfrepo.QuestState{
						RentalGuideQuestID: {
							Status:        test.status,
							ProgressValue: test.progress,
							Extra: map[string]string{
								"keep":       "metadata",
								"pvf_path":   rentalGuideQuestPath,
								"quest_type": test.questType,
							},
						},
					},
				}); err != nil {
					t.Fatal(err)
				}
			}
			catalog := rentalGuideTestCatalog(t, test.questType, test.intData)
			changed, err := AdvanceRentalGuideQuest(
				ctx,
				repositories.Quest,
				"19",
				catalog,
				time.Unix(1_700_000_000, 0).UTC(),
			)
			if err != nil {
				t.Fatal(err)
			}
			if changed {
				t.Fatalf("%s state was unexpectedly advanced", test.name)
			}
			if !test.withRecord {
				return
			}
			persisted, _, err := repositories.Quest.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			got := persisted.States[RentalGuideQuestID]
			if got.Status != test.status || got.ProgressValue != test.progress ||
				got.Extra["keep"] != "metadata" {
				t.Fatalf("%s state changed: %+v", test.name, got)
			}
		})
	}
}

package quest

import (
	"context"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
	"time"
)

func TestPVPTrainingInitialObjectivesUseThreeIndependentChannels(t *testing.T) {
	for _, tc := range []struct {
		path string
		data []int64
		want uint32
	}{
		{"n_quest/pvp/pvp_tookuk_03.qst", []int64{1, 1, 1, 0}, 1 | 1<<9 | 1<<18},
		{"n_quest/pvp/pvp_tookuk_04.qst", []int64{1, 0, 0, 2}, 1},
		{"n_quest/pvp/pvp_tookuk_05.qst", []int64{1, 1, 1, 1}, 1 | 1<<9 | 1<<18},
		{"n_quest/other.qst", []int64{1, 1, 1, 0}, 1},
	} {
		d := Definition{Path: tc.path, Type: "[custom multi condition]", IntData: tc.data}
		got := definitionInitialTrigger(d, nil)
		if got != tc.want {
			t.Fatalf("%s 初始进度 %d，期望 %d", tc.path, got, tc.want)
		}
		if tc.want != 1 {
			for channel := 0; channel < 3; channel++ {
				next := decrementPackedTriggerChannel(got, channel)
				if next == 0 || next&(1<<uint(channel*9)) != 0 {
					t.Fatalf("单项完成提前完成整条任务: %08x", next)
				}
				if decrementPackedTriggerChannel(next, channel) != next {
					t.Fatal("重复条件上报减少其他条件")
				}
			}
			for channel := 0; channel < 3; channel++ {
				got = decrementPackedTriggerChannel(got, channel)
			}
			if got != 0 {
				t.Fatal("三个条件完成后未归零")
			}
		}
	}
}

func TestPVPTrainingRepairOnlyUnprogressedOwnedActiveRecord(t *testing.T) {
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	if err := r.Character.Save(ctx, repo.CharacterRecord{CharacterID: "1", AccountID: "owner"}); err != nil {
		t.Fatal(err)
	}
	d := Definition{ID: 2672, Path: "n_quest/pvp/pvp_tookuk_03.qst", Type: "[custom multi condition]", IntData: []int64{1, 1, 1, 0}}
	catalog := &Catalog{byID: map[int64]Definition{2672: d}}
	owner, err := NewOwner(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, status string
		progress     int64
		trigger      byte
		observed     bool
		want         int64
	}{
		{"未推进", "active", 1, 0, false, 0x40201}, {"已有上报", "active", 1, 16, true, 1},
		{"完成保留", "completed", 0, 0, false, 0}, {"条件已完成", "active", 262656, 16, true, 262656},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := repo.QuestState{Status: tc.status, ProgressValue: tc.progress, TriggerType: tc.trigger, Extra: map[string]string{"pvf_path": d.Path}}
			if tc.observed {
				state.Extra["last_set_trigger_type"] = "16"
			}
			if err := r.Quest.Save(ctx, repo.QuestRecord{CharacterID: "1", States: map[int64]repo.QuestState{2672: state}}); err != nil {
				t.Fatal(err)
			}
			got, changed, err := owner.RepairUnprogressedPVPTraining(ctx, catalog, "owner", "1", now)
			if err != nil || got.States[2672].ProgressValue != tc.want || changed != (tc.want != tc.progress) {
				t.Fatalf("修炼修复 %+v，改变 %t，错误 %v", got, changed, err)
			}
			again, changed, err := owner.RepairUnprogressedPVPTraining(ctx, catalog, "owner", "1", now.Add(time.Second))
			if err != nil || changed || again.States[2672].ProgressValue != tc.want {
				t.Fatal("重复修复改变任务进度")
			}
		})
	}
	if _, _, err := owner.RepairUnprogressedPVPTraining(ctx, catalog, "foreign", "1", now); err == nil {
		t.Fatal("跨账号修复任务")
	}
}

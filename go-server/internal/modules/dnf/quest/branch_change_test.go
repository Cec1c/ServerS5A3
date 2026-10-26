package quest

import (
	"reflect"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestBranchChangeQuestsRebuildOnlyProfessionAndDeclaredMain(t *testing.T) {
	source := professionOverrideTestSource()
	source[DefaultList] += "\n2702 `expert.qst`"
	source["n_quest/expert.qst"] = questCatalogTestDefinition("[common unique]", 20, 99, "[gunner]", "[job change quest] 20")
	catalog := loadProfessionOverrideTestCatalog(t, source)
	now := time.Date(2026, 10, 21, 14, 0, 0, 0, time.UTC)
	before := repo.QuestRecord{CharacterID: "2", States: map[int64]repo.QuestState{6926: {Status: "completed"}, 6884: {Status: "active"}, 3356: {Status: "active", ProgressValue: 7}, 2702: {Status: "active"}}, Progress: map[int64]repo.QuestState{6900: {Status: "active"}, 99999: {Status: "active", ProgressValue: 5}}}
	for _, clear := range []bool{false, true} {
		label := "保留主线"
		if clear {
			label = "完成主线"
		}
		t.Run(label, func(t *testing.T) {
			q, err := catalog.PlanBranchChangeQuests(before, 2, 0x12, 60, clear, now)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []int64{6926, 6884} {
				if _, found := q.States[id]; found {
					t.Fatal("保留了旧分支职业任务", id)
				}
			}
			if _, found := q.Progress[6900]; found {
				t.Fatal("旧二觉激活投影未清理")
			}
			for _, id := range []int64{6881, 6927, 6888} {
				if q.States[id].Status != "completed" || q.States[id].Extra["reward_state"] != "granted" {
					t.Fatal("目标职业/觉醒账本未补齐或可重复发奖", id, q.States[id])
				}
			}
			if !reflect.DeepEqual(q.States[2702], before.States[2702]) || !reflect.DeepEqual(q.Progress[99999], before.Progress[99999]) {
				t.Fatal("误改专家职业或无关任务")
			}
			if clear {
				if q.States[3356].Status != "completed" {
					t.Fatal("高级书没有清主线")
				}
			} else if !reflect.DeepEqual(q.States[3356], before.States[3356]) {
				t.Fatal("普通变更误清主线")
			}
		})
	}
	if !catalog.HasActiveAwakeningQuest(before) {
		t.Fatal("漏检正在进行的觉醒")
	}
	q, err := catalog.PlanBranchChangeQuests(before, 2, 0, 25, false, now)
	if err != nil || q.States[3356].Status != "active" || q.States[2702].Status != "active" {
		t.Fatal("退回基础职业时越界修改", err, q)
	}
	if _, found := q.States[6926]; found {
		t.Fatal("基础职业还保留转职账")
	}
}

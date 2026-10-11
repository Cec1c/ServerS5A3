package quest

import "testing"

func TestActualEpicRoadEligibilityExactDefinitionControls(t *testing.T) {
	original := loadRealPVFQuestCatalog(t)
	ch := CharacterEligibility{Level: 90, Job: 0}
	eligibility, e := original.WithEpicRoadEvent350Eligibility(ch, true)
	if e != nil || !eligibility.epicRoadEvent350Enabled {
		t.Fatal(e)
	}
	if d, _ := original.Find(5578); !d.IsEvent {
		t.Fatal("policy mutated original event bit")
	}
	mutations := map[string]func(*Definition){
		"foreign_path":      func(d *Definition) { d.Path = "n_quest/event/foreign.qst" },
		"foreign_event":     func(d *Definition) { d.ShowQuestOnEvent = 351 },
		"foreign_target":    func(d *Definition) { d.IntData = []int64{3136} },
		"extra_target":      func(d *Definition) { d.IntData = []int64{3135, 3136} },
		"depend_grant":      func(d *Definition) { d.HasDependGiveItem = true },
		"wrong_grade":       func(d *Definition) { d.Grade = "[special daily]" },
		"wrong_predecessor": func(d *Definition) { d.PreRequiredGroups = [][]int64{{5577}} },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			copyMap := make(map[int64]Definition)
			for id, d := range original.byID {
				copyMap[id] = d
			}
			d := cloneDefinition(copyMap[5579])
			mutate(&d)
			copyMap[5579] = d
			bad := &Catalog{byID: copyMap, ordered: original.ordered}
			got, e := bad.WithEpicRoadEvent350Eligibility(eligibility, true)
			if e == nil || got.epicRoadEvent350Enabled || got.epicRoadEvent350Recognized {
				t.Fatal("foreign contract authorized")
			}
			got, e = bad.WithEpicRoadEvent350Eligibility(eligibility, false)
			if e != nil || got.epicRoadEvent350Enabled || got.epicRoadEvent350Recognized {
				t.Fatal("disabled invalid policy leaked authorization")
			}
		})
	}
}

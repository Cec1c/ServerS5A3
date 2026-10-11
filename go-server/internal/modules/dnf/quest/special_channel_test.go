package quest

import (
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestSpecialChannelPermissionRequiresFinishedPersistedQuest(t *testing.T) {
	c := &Catalog{byID: map[int64]Definition{12829: {ID: 12829}, 2903: {ID: 2903}}}
	rule := SpecialChannelRule{AntonQuestID: 12829, LukeQuestID: 2903}
	for _, tc := range []struct {
		name, status string
		want         bool
	}{
		{"pending_finish", "active", false}, {"eligible_only", "acceptable", false}, {"finished", "completed", true}, {"legacy_finished", "done", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := dnfrepo.QuestRecord{CharacterID: "1", States: map[int64]dnfrepo.QuestState{12829: {Status: tc.status, ProgressValue: 0}}, Progress: map[int64]dnfrepo.QuestState{2903: {Status: "active", ProgressValue: 0}}}
			before := dnfrepo.CloneQuest(record)
			got := c.SpecialChannelPermissionForRecord(rule, record)
			if got.Anton != tc.want || got.Luke || !reflect.DeepEqual(record, before) {
				t.Fatalf("permission=%+v quest mutation=%t", got, !reflect.DeepEqual(record, before))
			}
		})
	}
	if got := c.SpecialChannelPermissionForRecord(SpecialChannelRule{AntonQuestID: 555}, dnfrepo.QuestRecord{CharacterID: "1", States: map[int64]dnfrepo.QuestState{555: {Status: "completed"}}}); got.Anton {
		t.Fatal("unknown PVF quest granted permission")
	}
}

package quest

import (
	"reflect"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestLukeEntrancePreservesOriginalThreeRequirements(t *testing.T) {
	text := "[luke]\n2903\n[lukeAntonQuest]\n12830\n[lukerequireditem]\n10159142\n[lukerequireditemcount]\n40\n"
	rule, err := LoadLukeEntryRequirement(catalogTestSource{SpecialChannelRulePath: text})
	if err != nil || rule != (LukeEntryRequirement{2903, 12830, 10159142, 40}) {
		t.Fatal(rule, err)
	}
	c := &Catalog{byID: map[int64]Definition{2903: {ID: 2903}, 12830: {ID: 12830}}}
	for _, status := range []string{"", "active", "acceptable", "completed", "done"} {
		record := dnfrepo.QuestRecord{CharacterID: "31", States: map[int64]dnfrepo.QuestState{2903: {Status: "completed"}, 12830: {Status: status}}}
		before := dnfrepo.CloneQuest(record)
		if got := c.LukeEntryQuestsCompleted(rule, record); got != (status == "completed" || status == "done") || !reflect.DeepEqual(before, record) {
			t.Fatal("entrance must use real completed quests without mutation", status, got)
		}
	}
	for _, bad := range []string{strings.Replace(text, "12830", "0", 1), strings.Replace(text, "40\n", "", 1), strings.Replace(text, "10159142", "-1", 1)} {
		if _, err := LoadLukeEntryRequirement(catalogTestSource{SpecialChannelRulePath: bad}); err == nil {
			t.Fatal("incomplete entrance requirement accepted")
		}
	}
}

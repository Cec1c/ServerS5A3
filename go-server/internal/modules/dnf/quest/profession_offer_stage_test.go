package quest

import (
	"errors"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestProfessionOffersHideNewClassRewardsButKeepActiveAndFollowup(t *testing.T) {
	c := loadProfessionOverrideTestCatalog(t, professionOverrideTestSource())
	for _, grow := range []int{1, 2, 0x11, 0x12, 0x21, 0x22} {
		character := CharacterEligibility{Level: 90, Job: 2, GrowType: grow}
		if _, err := c.PlanAccept(character, dnfrepo.QuestRecord{}, 6926); !errors.Is(err, ErrQuestNotAcceptable) {
			t.Fatalf("grow=%x offered second class change: %v", grow, err)
		}
		active := dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{6926: {Status: "active"}}}
		if !containsProfessionOffer(c.QuestList(character, active).IDs, 6926) {
			t.Fatal("active class-change task hidden from abandon UI")
		}
		completed := dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{6926: {Status: "completed"}}}
		if !containsProfessionOffer(c.QuestList(character, completed).IDs, 6927) {
			t.Fatal("non-transition congratulations followup hidden")
		}
	}
	if _, err := c.PlanAccept(CharacterEligibility{Level: 15, Job: 2}, dnfrepo.QuestRecord{}, 6926); err != nil {
		t.Fatalf("base class lost first transfer: %v", err)
	}
}

func containsProfessionOffer(ids []int32, id int64) bool {
	for _, value := range ids {
		if int64(value) == id {
			return true
		}
	}
	return false
}

func TestRealPVFRenewalTransferDoesNotOfferOldTransferTerminals(t *testing.T) {
	c, _ := loadRealScriptPVFCatalogForClientList(t)
	record := dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{6932: {Status: "completed"}, 3155: {Status: "completed"}, 3427: {Status: "completed"}}}
	character := CharacterEligibility{Level: 90, Job: 0, GrowType: 2}
	for _, id := range []int64{7803, 7807, 7810, 7814} {
		d, ok := c.Find(id)
		if !ok || d.JobChangeQuest != 1 || normalizeQuestTag(d.RewardType) != "grow type" {
			t.Fatalf("real transfer terminal changed: %+v", d)
		}
		if containsProfessionOffer(c.QuestList(character, record).IDs, id) {
			t.Fatalf("old terminal %d offered after renewal transfer", id)
		}
		if _, err := c.PlanAccept(character, record, id); !errors.Is(err, ErrQuestNotAcceptable) {
			t.Fatalf("old terminal accept id=%d err=%v", id, err)
		}
		if _, err := c.PlanAccept(CharacterEligibility{Level: 15, Job: 0}, record, id); err != nil {
			t.Fatalf("base class terminal %d rejected: %v", id, err)
		}
	}
	if !containsProfessionOffer(c.QuestList(character, record).IDs, 981) {
		t.Fatal("own first awakening 981 hidden")
	}
	for _, grow := range []int{1, 3, 4, 0x12, 0x22} {
		if containsProfessionOffer(c.QuestList(CharacterEligibility{Level: 90, Job: 0, GrowType: grow}, record).IDs, 981) {
			t.Fatalf("wrong branch/stage %x offered soul awakening", grow)
		}
	}
	active := dnfrepo.CloneQuest(record)
	active.States[7803] = dnfrepo.QuestState{Status: "active"}
	if !containsProfessionOffer(c.QuestList(character, active).IDs, 7803) {
		t.Fatal("already-active transfer task unavailable for normal abandon")
	}
}

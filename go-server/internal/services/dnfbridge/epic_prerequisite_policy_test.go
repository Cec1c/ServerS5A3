package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/json"
	oe "longheng.io/server/internal/modules/dnf/onlineevent"
	quest "longheng.io/server/internal/modules/dnf/quest"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestActualEpic350PrerequisiteOfferedToNormalClient(t *testing.T) {
	s, a, _, _ := prepareMonthlyBridge(t)
	g, _ := s.repositoryGroup()
	id := strconv.Itoa(int(a.selectedCharacterID))
	ch, ok, e := g.Character.Load(context.Background(), id)
	if e != nil || !ok {
		t.Fatal(e)
	}
	ch.Level = 90
	if e = g.Character.Save(context.Background(), ch); e != nil {
		t.Fatal(e)
	}
	if !s.currentEpicRoadActivityAvailable() {
		t.Fatal("actual350 unavailable")
	}
	body, ok := s.buildCurrentAcceptableQuestListBodyForSession(context.Background(), a)
	if !ok {
		t.Fatal("list missing")
	}
	_, m := consumeCurrentSkillInfoFields(t, body[4:])
	listed := consumePackedQuestIDs(t, m[4][0])
	found := false
	for _, q := range listed {
		if q == 5578 {
			found = true
		}
		if q == 5579 {
			t.Fatal("successor offered before predecessor")
		}
	}
	if !found {
		t.Fatal("enabled actual350 legitimate level90 actor never offered5578")
	}
}

func TestActualEpic350NormalAcceptMeetFinishChain(t *testing.T) {
	s, a, _, conn := prepareMonthlyBridge(t)
	ctx := context.Background()
	g, _ := s.repositoryGroup()
	id := strconv.Itoa(int(a.selectedCharacterID))
	ch, _, _ := g.Character.Load(ctx, id)
	ch.Level = 90
	if ch.Stats == nil {
		ch.Stats = map[string]int64{}
	}
	ch.Stats["exp"] = 0
	if e := g.Character.Save(ctx, ch); e != nil {
		t.Fatal(e)
	}
	skill, _, _ := g.Skill.Load(ctx, id)
	skill.CharacterID = id
	skill.Points.SyncedLevel = 90
	if e := g.Skill.Save(ctx, skill); e != nil {
		t.Fatal(e)
	}
	if e := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: id}); e != nil {
		t.Fatal(e)
	}
	a.townSceneReadyCharacterID = a.selectedCharacterID
	catalog, e := s.loadQuestCatalog(ctx)
	if e != nil {
		t.Fatal(e)
	}
	activeControls := s.activityControls
	resourcesCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tables, items, e := s.currentQuestFinishResources(resourcesCtx)
	if e != nil {
		t.Fatal(e)
	}
	for _, q := range []uint16{5578, 5579} {
		req := make([]byte, 4)
		binary.LittleEndian.PutUint16(req, 31)
		binary.LittleEndian.PutUint16(req[2:], q)
		if e = s.handleCurrentAcceptQuest(a, req); e != nil {
			t.Fatal(e)
		}
		record, found, e := g.Quest.Load(ctx, id)
		if e != nil || !found || record.States[int64(q)].Status != "active" || record.States[int64(q)].ProgressValue != 1 {
			t.Fatalf("normal accept %d record%+v err%v", q, record.States[int64(q)], e)
		}
		before := conn.write.Len()
		if e = s.handleCurrentAcceptQuest(a, req); e != nil {
			t.Fatal(e)
		}
		if conn.write.Len() != before {
			t.Fatal("same-session accept replay emitted packets")
		}
		if q == 5578 {
			s.activityControls, e = oe.NewControls(ctx, g.Settings, []oe.ControlDefinition{{ID: 350, Name: "disabled", DefaultEnabled: false}})
			if e != nil {
				t.Fatal(e)
			}
			body, ok := s.buildCurrentAcceptableQuestListBodyForSession(ctx, a)
			if !ok {
				t.Fatal("disabled lost active list")
			}
			_, fields := consumeCurrentSkillInfoFields(t, body[4:])
			present := false
			for _, id := range consumePackedQuestIDs(t, fields[4][0]) {
				if id == 5578 {
					present = true
				}
			}
			if !present {
				t.Fatal("disabled hides lawful already-active prerequisite")
			}
		}
		trigger := make([]byte, 6)
		binary.LittleEndian.PutUint16(trigger, 33)
		binary.LittleEndian.PutUint16(trigger[2:], q)
		trigger[4] = 1
		if e = s.handleCurrentSetQuestTrigger(a, trigger); e != nil {
			t.Fatal(e)
		}
		record, _, _ = g.Quest.Load(ctx, id)
		if record.States[int64(q)].ProgressValue != 0 {
			t.Fatal("normal meet trigger not ready")
		}
		finish := quest.FinishQuestRequest{QuestID: q, RewardSelectIndex: ^uint16(0), Multiplier: 1, Reserved: quest.CurrentFinishQuestObservedTailMarker}
		finishWire := make([]byte, 10)
		binary.LittleEndian.PutUint16(finishWire, 34)
		binary.LittleEndian.PutUint16(finishWire[2:], q)
		binary.LittleEndian.PutUint16(finishWire[4:], ^uint16(0))
		binary.LittleEndian.PutUint16(finishWire[6:], 1)
		binary.LittleEndian.PutUint16(finishWire[8:], ^uint16(0))
		if e = s.handleCurrentFinishQuest(a, finishWire); e != nil {
			t.Fatal(e)
		}
		record, _, _ = g.Quest.Load(ctx, id)
		if record.States[int64(q)].Status != "completed" {
			t.Fatalf("normal finish %d not persisted %+v", q, record.States[int64(q)])
		}
		state := record.States[int64(q)]
		var receipt struct {
			CharacterID   string `json:"character_id"`
			QuestID       int64  `json:"quest_id"`
			NewExperience uint32 `json:"new_experience"`
		}
		if e = json.Unmarshal([]byte(state.Extra["settlement_receipt_v1"]), &receipt); e != nil || state.Extra["reward_state"] != "granted" || receipt.CharacterID != id || receipt.QuestID != int64(q) {
			t.Fatalf("actual reward receipt ownership invalid: %+v %v", receipt, e)
		}
		beforeCharacter, _, _ := g.Character.Load(ctx, id)
		beforeInventory, _, _ := g.Inventory.Load(ctx, id)
		beforeSkill, _, _ := g.Skill.Load(ctx, id)
		if int64(receipt.NewExperience) != beforeCharacter.Stats["exp"] {
			t.Fatal("persisted reward projection disagrees with exact receipt")
		}
		before = conn.write.Len()
		if e = s.handleCurrentFinishQuestWithResources(a, finish, g, catalog, tables, currentQuestFinishItemAllocator(items)); e != nil {
			t.Fatal(e)
		}
		if conn.write.Len() != before {
			t.Fatal("finish replay emitted packets")
		}
		afterCharacter, _, _ := g.Character.Load(ctx, id)
		afterInventory, _, _ := g.Inventory.Load(ctx, id)
		afterSkill, _, _ := g.Skill.Load(ctx, id)
		afterQuest, _, _ := g.Quest.Load(ctx, id)
		if !reflect.DeepEqual(beforeCharacter, afterCharacter) || !reflect.DeepEqual(beforeInventory, afterInventory) || !reflect.DeepEqual(beforeSkill, afterSkill) || !reflect.DeepEqual(record, afterQuest) {
			t.Fatal("replay changed reward/quest assets")
		}
		s.activityControls = activeControls
	}
	ok, e := s.townAreaNeedQuestsCompleted(ctx, g, id, []int64{5579})
	if e != nil || !ok {
		t.Fatalf("completed prerequisite gate %t %v", ok, e)
	}
}

func TestActualEpic350DisabledRejectsNewButPreservesActive(t *testing.T) {
	s, a, _, _ := prepareMonthlyBridge(t)
	ctx := context.Background()
	g, _ := s.repositoryGroup()
	id := strconv.Itoa(int(a.selectedCharacterID))
	ch, _, _ := g.Character.Load(ctx, id)
	ch.Level = 90
	g.Character.Save(ctx, ch)
	c, e := s.loadQuestCatalog(ctx)
	if e != nil {
		t.Fatal(e)
	}
	eligibility, e := c.WithEpicRoadEvent350Eligibility(quest.CharacterEligibility{Level: 90, Job: 0}, false)
	if e != nil {
		t.Fatal(e)
	}
	empty := repo.QuestRecord{CharacterID: id}
	if _, e = c.PlanAccept(eligibility, empty, 5578); e == nil {
		t.Fatal("disabled new accept allowed")
	}
	empty.States = map[int64]repo.QuestState{5578: {Status: "active", ProgressValue: 1}}
	decision, e := c.DecideAccept(eligibility, empty, 5578)
	if e != nil || decision != quest.AcceptIdempotent {
		t.Fatal("disabled broke active contract")
	}
	enabled, e := c.WithEpicRoadEvent350Eligibility(quest.CharacterEligibility{Level: 16, Job: 0}, true)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.PlanAccept(enabled, repo.QuestRecord{}, 5578); e == nil {
		t.Fatal("low level allowed")
	}
	enabled, e = c.WithEpicRoadEvent350Eligibility(quest.CharacterEligibility{Level: 90, Job: 0}, true)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.PlanAccept(enabled, repo.QuestRecord{}, 5579); e == nil {
		t.Fatal("missing5578 allowed successor")
	}
	for candidateID := int64(1); candidateID < 30000; candidateID++ {
		d, found := c.Find(candidateID)
		if !found {
			continue
		}
		if d.IsEvent && d.ID != 5578 && d.ID != 5579 {
			if _, e = c.PlanAccept(enabled, repo.QuestRecord{}, d.ID); e == nil {
				t.Fatalf("unrelated historical event%d enabled", d.ID)
			}
		}
	}
	s.activityControls, e = oe.NewControls(ctx, g.Settings, []oe.ControlDefinition{{ID: 350, Name: "disabled", DefaultEnabled: false}})
	if e != nil {
		t.Fatal(e)
	}
	if s.currentEpicRoadActivityAvailable() {
		t.Fatal("disabled350 still available")
	}
}

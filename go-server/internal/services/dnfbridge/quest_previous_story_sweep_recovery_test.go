package dnfbridge

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func previousSweepRecoveryBridgeFixture(t *testing.T) (*Service, *gameSession, dnfrepo.Group) {
	t.Helper()
	s, session, repos := newQuestClearCouponTestService(t, 1)
	s.options.currentClientVersion = "official-24.9"
	bindAutoCompleteMainTestSession(s, session)
	session.accountFromToken = true
	character, _, err := repos.Character.Load(context.Background(), "17")
	if err != nil {
		t.Fatal(err)
	}
	character.Job = "0"
	character.Stats["grow_type"] = 0x21
	if err := repos.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	return s, session, repos
}

func previousSweepRecoveryBridgeCatalog(t *testing.T) *dnfquest.Catalog {
	t.Helper()
	common := "[grade] `[epic]`\n[type] `[meet npc]`\n[level] 50 99\n[job] `[swordman]`\n[exposed by npc] 1\n"
	source := questListTestSource{
		dnfquest.DefaultList:    "100 `start.qst`\n3368 `awakening.qst`\n3427 `dock.qst`\n4000 `later.qst`\n",
		"n_quest/start.qst":     common,
		"n_quest/awakening.qst": common + "[pre required quest] 100\n[grow type] 1\n[job change quest] 2\n[reward type] `[awakening type]`\n[reward int data] 1\n",
		"n_quest/dock.qst":      common + "[pre required quest] 3368\n",
		"n_quest/later.qst":     "[grade] `[epic]`\n[type] `[meet npc]`\n[level] 90 99\n[job] `[swordman]`\n[exposed by npc] 1\n[pre required quest] 3427\n",
	}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestPreviousStorySweepLoginBitmapRepairsOldCompletedRecord(t *testing.T) {
	s, session, repos := previousSweepRecoveryBridgeFixture(t)
	s.questCatalog = previousSweepRecoveryBridgeCatalog(t)
	ctx := context.Background()
	beforeCharacter, _, _ := repos.Character.Load(ctx, "17")
	beforeInventory, _, _ := repos.Inventory.Load(ctx, "17")
	record := dnfrepo.QuestRecord{CharacterID: "17", States: map[int64]dnfrepo.QuestState{
		100: {Status: "completed", Extra: map[string]string{
			"auto_completed": "true", "auto_complete_reason": "main_story_level_clear_no_reward", "auto_complete_level_cutoff": "89", "reward_state": "granted",
		}},
	}}
	if err := repos.Quest.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		body, err := s.buildCurrentClearQuestListTransportBodyForSession(ctx, session, "login_old_completed_sweep")
		if err != nil {
			t.Fatal(err)
		}
		plain, err := zlibDecompress(body)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []int{3368, 3427} {
			if plain[4+id] != 1 {
				t.Fatalf("login attempt %d still blocks completed gate %d", attempt, id)
			}
		}
		stored, _, _ := repos.Quest.Load(ctx, "17")
		if attempt > 0 && !reflect.DeepEqual(record, stored) {
			t.Fatal("repeat login rewrote recovered quests")
		}
		record = stored
	}
	afterCharacter, _, _ := repos.Character.Load(ctx, "17")
	afterInventory, _, _ := repos.Inventory.Load(ctx, "17")
	if !reflect.DeepEqual(beforeCharacter, afterCharacter) || !reflect.DeepEqual(beforeInventory, afterInventory) {
		t.Fatal("login repair changed character or granted/consumed inventory")
	}
}

func TestRealPVFPreviousStorySweepLoginSnapshot(t *testing.T) {
	path, snapshot := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"), os.Getenv("DNF_QUEST_RECOVERY_SNAPSHOT")
	if path == "" || snapshot == "" {
		t.Skip("set real PVF and a private saved quest snapshot for login recovery")
	}
	data, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		States []struct {
			Group    string
			ID       int64
			Status   string
			Progress int64
		}
		Extra []struct {
			Group string
			ID    int64
			Key   string
			Value string
		}
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	record := dnfrepo.QuestRecord{CharacterID: "17", States: map[int64]dnfrepo.QuestState{}, Progress: map[int64]dnfrepo.QuestState{}}
	group := func(name string) map[int64]dnfrepo.QuestState {
		if name == "states" {
			return record.States
		}
		if name == "progress" {
			return record.Progress
		}
		t.Fatalf("unknown quest group %q", name)
		return nil
	}
	for _, row := range saved.States {
		group(row.Group)[row.ID] = dnfrepo.QuestState{Status: row.Status, ProgressValue: row.Progress, Extra: map[string]string{}}
	}
	for _, row := range saved.Extra {
		state := group(row.Group)[row.ID]
		if state.Extra == nil {
			t.Fatal("extra without state")
		}
		state.Extra[row.Key] = row.Value
		group(row.Group)[row.ID] = state
	}
	if dnfquest.IsCompleted(record, 3368) || dnfquest.IsCompleted(record, 3427) {
		t.Fatal("fixture is not the old completed sweep with missing dock gates")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	index, err := dnfpvf.Build(context.Background(), archive, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	s, session, repos := previousSweepRecoveryBridgeFixture(t)
	s.questCatalog = catalog
	if err := repos.Quest.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	body, err := s.buildCurrentClearQuestListTransportBodyForSession(context.Background(), session, "real_old_completed_sweep_login")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := zlibDecompress(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{3368, 3427, 3515, 3779} {
		if plain[4+id] != 1 {
			t.Errorf("real login gate %d is absent", id)
		}
	}
	if plain[4+3868] != 0 {
		t.Fatal("recovery answered the player's faction question")
	}
	stored, _, _ := repos.Quest.Load(context.Background(), "17")
	if !dnfquest.IsCompleted(stored, 3427) {
		t.Fatal("bitmap repair was not persisted")
	}
}

func TestPreviousStorySweepAcceptableListUsesRecoveredPredecessors(t *testing.T) {
	s, session, repos := previousSweepRecoveryBridgeFixture(t)
	s.questCatalog = previousSweepRecoveryBridgeCatalog(t)
	ctx := context.Background()
	record := dnfrepo.QuestRecord{CharacterID: "17", States: map[int64]dnfrepo.QuestState{
		100: {Status: "completed", Extra: map[string]string{"auto_completed": "true", "auto_complete_reason": "main_story_level_clear_no_reward", "auto_complete_level_cutoff": "89"}},
	}}
	if err := repos.Quest.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	body, ok := s.buildCurrentAcceptableQuestListBodyForSession(ctx, session)
	if !ok {
		t.Fatal("initial acceptable snapshot was skipped")
	}
	_, messages := consumeCurrentSkillInfoFields(t, body[4:])
	listed := []int32{}
	for _, packed := range messages[4] {
		listed = append(listed, consumePackedQuestIDs(t, packed)...)
	}
	hasSuccessor := false
	for _, id := range listed {
		if id == 3368 || id == 3427 {
			t.Fatalf("initial op21 offers recovered quest %d", id)
		}
		if id == 4000 {
			hasSuccessor = true
		}
	}
	if !hasSuccessor {
		t.Fatalf("initial op21 omitted successor of recovered dock gate: %v", listed)
	}
	stored, _, _ := repos.Quest.Load(ctx, "17")
	if !dnfquest.IsCompleted(stored, 3427) || dnfquest.IsCompleted(stored, 4000) {
		t.Fatal("recovery was not persisted within the historic cutoff")
	}
}

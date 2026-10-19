package dnfbridge

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Diagnostic uses exact extracted PVF texts. The first child encounter is a
// completed fixture; acquisition, acceptance and medicine delivery are native
// server entry points. It does not assert the observed player's progress.
func TestSeekerQST028MedicineSourceDiagnostic(t *testing.T) {
	path := os.Getenv("SEEKER_QST028_SOURCE_JSON")
	if path == "" {
		t.Skip("set SEEKER_QST028_SOURCE_JSON to extracted resources")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var source finishBridgePVFSource
	if err := json.Unmarshal(payload, &source); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	index, err := dnfpvf.Build(ctx, source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := progression.Load(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}

	for _, skipped := range []bool{false, true} {
		name := "normal-source"
		if skipped {
			name = "no-reward-source"
		}
		t.Run(name, func(t *testing.T) {
			repositories := qst019BoxRepositories(t, ctx)
			character, _, _ := repositories.Character.Load(ctx, "19")
			character.Level = 48
			if err := repositories.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			skill, _, _ := repositories.Skill.Load(ctx, "19")
			skill.Points.SyncedLevel = 48
			if err := repositories.Skill.Save(ctx, skill); err != nil {
				t.Fatal(err)
			}
			record := dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{3345: {Status: "completed"}}}
			if err := repositories.Quest.Save(ctx, record); err != nil {
				t.Fatal(err)
			}
			conn, service, session := qst019BoxBridge(repositories, catalog, tables, items)
			if skipped {
				record.States[3346] = dnfrepo.QuestState{Status: "active", ProgressValue: 1}
				plan, err := catalog.PlanAutoCompleteMainQuests(record, dnfquest.CharacterEligibility{Level: 49, Job: 2}, 48, 3346, dnfquest.CouponClearScopeMainStorySweep, time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				if got := plan.Record.States[3346]; got.Status != "completed" || got.Extra["auto_complete_reason"] != "epic_below_character_level_no_reward" {
					t.Fatalf("source plan=%+v", got)
				}
				if err := repositories.Quest.Save(ctx, plan.Record); err != nil {
					t.Fatal(err)
				}
			} else {
				inventory, _, _ := repositories.Inventory.Load(ctx, "19")
				allocate := currentQuestFinishItemAllocator(items)
				for _, material := range []struct{ id, count int64 }{{3033, 30}, {3166, 10}} {
					_, err := allocate(&inventory, dnfquest.FinishItemGrantRequest{ItemID: material.id, Count: material.count, QuestID: 3346, Source: "qst028-material-fixture", CompletionKey: "qst028-material-fixture"})
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := repositories.Inventory.Save(ctx, inventory); err != nil {
					t.Fatal(err)
				}
				if err := service.handleCurrentAcceptQuest(session, qst019BoxAcceptBody(3346)); err != nil {
					t.Fatal(err)
				}
				boxCallTrigger(t, service, session, conn, 3346, 0)
				qst006AssertFinishSuccess(t, boxCallFinish(t, service, session, conn, 3346), 3346)
			}
			count := func() int64 {
				inventory, _, err := repositories.Inventory.Load(ctx, "19")
				if err != nil {
					t.Fatal(err)
				}
				var n int64
				for _, item := range inventory.Slots {
					if item.ItemID == 10164764 {
						n += item.Count
					}
				}
				return n
			}
			t.Logf("before parent: skipped=%t medicine=%d", skipped, count())
			if err := service.handleCurrentAcceptQuest(session, qst019BoxAcceptBody(3347)); err != nil {
				t.Fatal(err)
			}
			record, _, err = repositories.Quest.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("after accept: medicine=%d parent=%+v meet=%+v delivery=%+v", count(), record.States[3347], record.States[3425], record.States[3426])
			// Complete encounter separately to isolate the final item gate.
			record.States[3425] = dnfrepo.QuestState{Status: "completed", Extra: map[string]string{"reward_state": "granted"}}
			if err := repositories.Quest.Save(ctx, record); err != nil {
				t.Fatal(err)
			}
			boxCallTrigger(t, service, session, conn, 3426, 0)
			packets := boxCallFinish(t, service, session, conn, 3426)
			t.Logf("delivery packets=%v remaining=%d", finishBridgePacketIDs(packets), count())
			qst006AssertFinishSuccess(t, packets, 3426)
			if count() != 0 {
				t.Fatalf("medicine left=%d", count())
			}
			qst006AssertFinishSuccess(t, boxCallFinish(t, service, session, conn, 3347), 3347)
		})
	}
}

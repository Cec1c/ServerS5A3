package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type qst003RepairQuestStore struct {
	dnfrepo.QuestRepository
	fail   bool
	writes int
}

func (s *qst003RepairQuestStore) SaveFields(ctx context.Context, r dnfrepo.QuestRecord, fields ...dnfrepo.QuestField) error {
	s.writes++
	if s.fail {
		return errors.New("qst003 injected repair save failure")
	}
	return dnfrepo.SaveQuestFields(ctx, s.QuestRepository, r, fields...)
}

func TestSeekerQST003RealPVFMaterialTriggerLifecycle(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	ctx := context.Background()
	a, err := platformpvf.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	index, err := dnfpvf.Build(ctx, a, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := catalog.Find(3763)
	if !ok || !reflect.DeepEqual(d.IntData, []int64{3033, 200, 3034, 200, 3035, 200, 3036, 200, 79}) {
		t.Fatalf("definition=%+v", d)
	}
	t.Run("new-accept-and-bounded-callbacks", func(t *testing.T) {
		svc, session, repos := newTownMoveTest(t)
		svc.questCatalog = catalog
		svc.options.accountID = "dnf:1"
		svc.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
		if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "29", States: map[int64]dnfrepo.QuestState{3710: {Status: "completed"}}}); err != nil {
			t.Fatal(err)
		}
		owner, err := dnfquest.NewOwner(repos)
		if err != nil {
			t.Fatal(err)
		}
		_, err = owner.ApplyAccept(ctx, catalog, dnfquest.CharacterEligibility{Level: 81, Job: 0}, dnfquest.NewQuestIDCommand(alignedcmd.Request{AccountID: "dnf:1", SelectedCharacterID: 29}, "accept_quest", dnfquest.QuestIDRequest{QuestID: 3729}))
		if err != nil {
			t.Fatal(err)
		}
		record, _, err := repos.Quest.Load(ctx, "29")
		if err != nil || record.States[3763].ProgressValue != 513 {
			t.Fatalf("accepted child=%+v err=%v", record.States[3763], err)
		}
		conn := &bufferConn{}
		session.conn = conn
		// Model the native all-materials-satisfied decision as one callback.
		// This test does not treat the callback as proof of authoritative inventory.
		body := []byte{33, 0, 0xb3, 0x0e, 0x10, 0}
		if err := svc.handleCurrentSetQuestTrigger(session, body); err != nil {
			t.Fatal(err)
		}
		record, _, err = repos.Quest.Load(ctx, "29")
		if err != nil || record.States[3763].ProgressValue != 512 {
			t.Fatalf("material child=%+v err=%v", record.States[3763], err)
		}
		if got := finishBridgePacketIDs(splitAllFinishBridgePackets(t, conn.write.Bytes())); !reflect.DeepEqual(got, []string{"1/33", "0/574"}) {
			t.Fatalf("material packets=%v", got)
		}
		conn.write.Reset()
		for i := 0; i < 200; i++ {
			if err := svc.handleCurrentSetQuestTrigger(session, body); err != nil {
				t.Fatal(err)
			}
		}
		if conn.write.Len() != 0 {
			t.Fatalf("material replays emitted %d bytes", conn.write.Len())
		}
		body[4] = 0x20
		if err := svc.handleCurrentSetQuestTrigger(session, body); err != nil {
			t.Fatal(err)
		}
		record, _, err = repos.Quest.Load(ctx, "29")
		if err != nil || record.States[3763].ProgressValue != 0 || record.States[3763].Status != "active" {
			t.Fatalf("npc child=%+v err=%v", record.States[3763], err)
		}
		conn.write.Reset()
		for i := 0; i < 200; i++ {
			if err := svc.handleCurrentSetQuestTrigger(session, body); err != nil {
				t.Fatal(err)
			}
		}
		if conn.write.Len() != 0 {
			t.Fatal("terminal replays emitted bytes")
		}
	})
	for _, tc := range []struct {
		name         string
		before, want int64
		fail         bool
	}{
		{"legacy-untouched", 712, 513, false}, {"legacy-npc-done", 200, 1, false}, {"save-failure", 712, 712, true}, {"already-material-done", 512, 512, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repos := dnfrepomemory.NewMemoryGroup()
			r := dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{3763: {Status: "active", ProgressValue: tc.before, Extra: map[string]string{"pvf_path": d.Path, "quest_type": d.Type}}}}
			if err := repos.Quest.Save(ctx, r); err != nil {
				t.Fatal(err)
			}
			store := &qst003RepairQuestStore{QuestRepository: repos.Quest, fail: tc.fail}
			repos.Quest = store
			conn := &bufferConn{}
			svc := &Service{questCatalog: catalog, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
			session := &gameSession{conn: conn, selectedCharacterID: 19}
			if err := svc.sendCurrentActiveQuestSnapshotForSession(session, "qst003-reload"); err != nil {
				t.Fatal(err)
			}
			loaded, _, err := repos.Quest.Load(ctx, "19")
			if err != nil || loaded.States[3763].ProgressValue != tc.want {
				t.Fatalf("stored=%+v err=%v", loaded, err)
			}
			packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if len(rest) != 0 || packet.Header.MsgID != 574 || !bytes.Equal(packet.Body, buildCurrentActiveQuestSnapshotBody(loaded, true)) {
				t.Fatalf("snapshot not persisted state: %+v", packet)
			}
			if tc.fail && !reflect.DeepEqual(loaded, r) {
				t.Fatal("failed save changed durable state")
			}
			if !tc.fail {
				writes := store.writes
				if err := svc.sendCurrentActiveQuestSnapshotForSession(session, "qst003-reload-again"); err != nil {
					t.Fatal(err)
				}
				if store.writes != writes {
					t.Fatal("second load rewrote repaired state")
				}
			}
		})
	}
}

// Probe uses real task/town definitions with isolated records. It does not
// model the native client's UI or claim to reproduce its callback schedule.
func TestSeekerQST003RealPVFTownMatrix(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	ctx := context.Background()
	a, err := platformpvf.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	index, err := dnfpvf.Build(ctx, a, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	towns, err := dnftown.Load(ctx, a, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{3729, 3762, 3763} {
		d, ok := catalog.Find(id)
		if !ok {
			t.Fatalf("missing definition %d", id)
		}
		t.Logf("definition=%d type=%s subtype=%d data=%v main=%d noexp=%v", id, d.Type, d.SubType, d.IntData, d.MainQuestID, d.NoExperience)
	}
	city, ok := towns.Find(14)
	if !ok {
		t.Fatal("missing town14")
	}
	t.Logf("town14=%+v", city)
	for _, scenario := range []struct {
		name                     string
		accept, dialogue, coupon bool
		counts                   [4]int64
	}{
		{name: "before-accept"},
		{name: "accepted-empty", accept: true},
		{name: "dialogue-empty", accept: true, dialogue: true},
		{name: "first-only", accept: true, dialogue: true, counts: [4]int64{200, 0, 0, 0}},
		{name: "four-short", accept: true, dialogue: true, counts: [4]int64{199, 199, 199, 199}},
		{name: "four-enough", accept: true, dialogue: true, counts: [4]int64{200, 200, 200, 200}},
		{name: "coupon-control", accept: true, coupon: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			svc, session, repos := newTownMoveTest(t)
			svc.townCatalog, svc.questCatalog, svc.initialEquipmentArchive = towns, catalog, a
			svc.options.accountID = "dnf:1"
			svc.options.currentClientVersion = "official-24.9"
			svc.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
			ch, _, err := repos.Character.Load(ctx, "29")
			if err != nil {
				t.Fatal(err)
			}
			ch.Level = 81
			ch.Stats["town_id"], ch.Stats["area_id"] = 14, 0
			ch.Stats["pos_x"], ch.Stats["pos_y"] = 441, 207
			if err := repos.Character.Save(ctx, ch); err != nil {
				t.Fatal(err)
			}
			record := dnfrepo.QuestRecord{CharacterID: "29", States: map[int64]dnfrepo.QuestState{3710: {Status: "completed"}}}
			for _, area := range city.Areas {
				if area.ID == 0 {
					for _, id := range area.NeedQuests {
						record.States[id] = dnfrepo.QuestState{Status: "completed"}
					}
				}
			}
			if err := repos.Quest.Save(ctx, record); err != nil {
				t.Fatal(err)
			}
			inventory := dnfrepo.InventoryRecord{CharacterID: "29", Slots: map[string]dnfrepo.ItemStack{}}
			for i, count := range scenario.counts {
				if count > 0 {
					inventory.Slots[fmt.Sprintf("0:%d", i+9)] = dnfrepo.ItemStack{ItemID: 3033 + int64(i), Count: count}
				}
			}
			if err := repos.Inventory.Save(ctx, inventory); err != nil {
				t.Fatal(err)
			}
			owner, err := dnfquest.NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			if scenario.accept {
				_, err = owner.ApplyAccept(ctx, catalog, dnfquest.CharacterEligibility{Level: 81, Job: 0}, dnfquest.NewQuestIDCommand(alignedcmd.Request{AccountID: "dnf:1", SelectedCharacterID: 29}, "accept_quest", dnfquest.QuestIDRequest{QuestID: 3729}))
				if err != nil {
					t.Fatal(err)
				}
			}
			if scenario.dialogue {
				for _, id := range []uint16{3762, 3729} {
					if err := svc.handleCurrentSetQuestTrigger(session, []byte{33, 0, byte(id), byte(id >> 8), 0, 0}); err != nil {
						t.Fatal(err)
					}
				}
			}
			if scenario.coupon {
				record, _, err = repos.Quest.Load(ctx, "29")
				if err != nil {
					t.Fatal(err)
				}
				plan, e := catalog.PlanCouponMainQuestClear(record, dnfquest.CharacterEligibility{Level: 81, Job: 0}, 81, dnfquest.CouponClearScopeMainStorySweep, time.Now().UTC())
				if e != nil {
					t.Fatal(e)
				}
				if e = repos.Quest.Save(ctx, plan.Record); e != nil {
					t.Fatal(e)
				}
			}
			record, _, err = repos.Quest.Load(ctx, "29")
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.sendCurrentActiveQuestSnapshotForSession(session, "qst003-probe"); err != nil {
				t.Fatal(err)
			}
			t.Logf("parent=%+v child3763=%+v active=%x", record.States[3729], record.States[3763], buildCurrentActiveQuestSnapshotBody(record, true))
			before := dnfrepo.CloneQuest(record)
			// Normal cross-town portal into Seria, then bodyless prev-village.
			if err := svc.handleTownSetUserArea(session, buildTownSeriaRoomPortalRequest(38, 1, 433, 311, 5, 14)); err != nil {
				t.Fatal(err)
			}
			ch, _, err = repos.Character.Load(ctx, "29")
			if err != nil || ch.Stats["town_id"] != 38 || ch.Stats["area_id"] != 1 {
				t.Fatalf("Seria entry stats=%v err=%v", ch.Stats, err)
			}
			if err := svc.handleCurrentPrevVillage(session, nil); err != nil {
				t.Fatal(err)
			}
			ch, _, err = repos.Character.Load(ctx, "29")
			if err != nil || ch.Stats["town_id"] != 14 || ch.Stats["area_id"] != 0 {
				t.Fatalf("Seria exit stats=%v err=%v", ch.Stats, err)
			}
			// Re-login uses the normal login landing selection and persists its origin.
			session.townPositionSnapshot = currentTownPositionSnapshot{}
			session.townPrevVillageSnapshot = currentTownPositionSnapshot{}
			_, townID, areaID, _, _, err := svc.currentCharacterListLoginTransition(ctx, session, 29, ch)
			if err != nil || townID != 38 || areaID != 1 {
				t.Fatalf("relogin town=%d/%d err=%v", townID, areaID, err)
			}
			if err := svc.handleCurrentPrevVillage(session, nil); err != nil {
				t.Fatal(err)
			}
			ch, _, err = repos.Character.Load(ctx, "29")
			if err != nil || ch.Stats["town_id"] != 14 || ch.Stats["area_id"] != 0 {
				t.Fatalf("relogin exit stats=%v err=%v", ch.Stats, err)
			}
			after, _, err := repos.Quest.Load(ctx, "29")
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("town operations mutated quests: err=%v", err)
			}
		})
	}
}

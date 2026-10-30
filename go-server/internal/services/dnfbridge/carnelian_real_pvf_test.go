package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFCarnelianAcceptHuntFinishInventory(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	index, err := dnfpvf.Build(ctx, a, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := dnfquest.Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := progression.Load(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	world, err := worldmap.LoadSource(ctx, a, worldmap.Options{SkipMaps: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{13031, 13033, 13036, 13037, 13040, 13041, 13044, 13047, 13050, 13053, 13056} {
		d, ok := c.Find(id)
		if !ok {
			t.Fatalf("missing daily %d", id)
		}
		doc, ok := index.Document(d.Path)
		if !ok {
			t.Fatal("missing daily document")
		}
		locations := doc.Ints("dungeon info")
		for n := 0; n+1 < len(locations); n += 2 {
			area, found := world.FindAreaByDungeon(locations[n])
			if found && area.ID != d.IntData[1] {
				t.Fatalf("quest %d dungeon %d conflicting area=%d", id, locations[n], area.ID)
			}
			accept, err := c.PlanAccept(dnfquest.CharacterEligibility{Level: d.LevelMin}, r.QuestRecord{}, id)
			if err != nil {
				t.Fatal(err)
			}
			record := r.QuestRecord{CharacterID: "19", States: map[int64]r.QuestState{id: {Status: "active", ProgressValue: int64(accept.InitTrigger)}}}
			kill, err := c.PlanHuntMonsterKill(record, dnfquest.HuntMonsterKillInput{DungeonID: locations[n], Difficulty: max(int64(0), locations[n+1]), WorldMapAreaID: area.ID, MonsterCode: 9600, MonsterKind: dnfquest.WorldMapHuntMonsterOrdinary, CompletionKey: "actual-mission-dungeon", CompletedAt: time.Now()})
			if err != nil || kill.Record.States[id].ProgressValue != int64(accept.InitTrigger)-1 {
				t.Fatalf("quest %d dungeon %d did not count its declared region: %v", id, locations[n], err)
			}
		}
	}
	g := qst006Repositories(t, ctx)
	ch, _, _ := g.Character.Load(ctx, "19")
	ch.Level = 90
	if err := g.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	sk, _, _ := g.Skill.Load(ctx, "19")
	sk.Points.SyncedLevel = 90
	if err := g.Skill.Save(ctx, sk); err != nil {
		t.Fatal(err)
	}
	conn, s, p := qst006Bridge(g, c, progress, items)
	s.initialEquipmentArchive = a
	carnelianBridgeAccept(t, s, p)
	// Calling op34 before the authoritative objectives must not mint materials.
	packets := qst006CallFinish(t, s, p, conn, 13056)
	qst006AssertFinishFailure(t, packets, 13056)
	owner, err := dnfquest.NewOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	area, ok := world.FindAreaByDungeon(310)
	if !ok {
		t.Fatal("missing metrocenter dungeon")
	}
	for n := 0; n < 153; n++ {
		kind := dnfquest.WorldMapHuntMonsterOrdinary
		if n >= 150 {
			kind = dnfquest.WorldMapHuntMonsterBoss
		}
		_, err := owner.ApplyHuntMonsterKill(ctx, c, "19", dnfquest.HuntMonsterKillInput{DungeonID: 310, WorldMapAreaID: area.ID, MonsterCode: 9600, MonsterKind: kind, CompletionKey: fmt.Sprintf("real-pvf-fixture/kill-%d", n), CompletedAt: at})
		if err != nil {
			t.Fatal(err)
		}
	}
	packets = qst006CallFinish(t, s, p, conn, 13056)
	if len(packets) == 0 || packets[0].Header.MsgID != 34 || packets[0].Body[0] != 1 {
		t.Fatalf("finish packet ids=%v", finishBridgePacketIDs(packets))
	}
	verifyBag := func() {
		t.Helper()
		bag, _, err := g.Inventory.Load(ctx, "19")
		if err != nil {
			t.Fatal(err)
		}
		var total int64
		for _, slot := range bag.Slots {
			if slot.ItemID == 10163959 {
				total += slot.Count
				if len(slot.RawEntry) != 0x77 || binary.LittleEndian.Uint32(slot.RawEntry[2:6]) != 10163959 || binary.LittleEndian.Uint32(slot.RawEntry[6:10]) != uint32(slot.Count) {
					t.Fatal("invalid material row")
				}
			}
		}
		if total != 15 {
			t.Fatalf("actual material allocation=%d want=15", total)
		}
	}
	verifyBag()
	if st := carnelianBridgeState(t, g, 19); st.Status != "completed" || st.Extra[dnfquest.QuestDailyCompletionDayKey] == "" {
		t.Fatalf("completed daily=%+v", st)
	}
	conn, s, p = qst006Bridge(g, c, progress, items)
	s.initialEquipmentArchive = a
	qst006CallFinish(t, s, p, conn, 13056)
	verifyBag()
}

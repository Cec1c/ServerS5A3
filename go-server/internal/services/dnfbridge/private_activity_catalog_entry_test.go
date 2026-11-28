package dnfbridge

import (
	"context"
	"os"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/activeness"
	"longheng.io/server/internal/modules/dnf/fatigue"
	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestPrivateActivityCatalogChangeRealPVFDungeonEntry(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
	ch := repo.CharacterRecord{CharacterID: "99", AccountID: "account-1", Level: 20, Stats: map[string]int64{
		"fatigue": 100, fatigue.DayKey: premium.ServiceDayID(now), "town_id": 38, "area_id": 1, "pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3,
	}}
	s, session := newDungeonSelectRefusalFixture(t, ch, nil)
	s.initialEquipmentArchive = archive
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = now
	s.gameplayTimers = queue
	catalog, err := s.privateActivityCatalog()
	if err != nil || catalog == nil {
		t.Fatal("activity catalog", err)
	}
	old := *catalog
	old.Fingerprint = catalog.Fingerprint ^ 1
	if old.Fingerprint == 0 {
		old.Fingerprint = 2
	}
	if _, err = activeness.Apply(&ch, &old, 4, 1, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err = activeness.Apply(&ch, &old, 6, 50, now, now); err != nil {
		t.Fatal(err)
	}
	ch.Stats["private_activity_v1_claimed"] = 1
	beforePoints := ch.Stats["private_activity_v1_points"]
	g, _ := s.repositoryGroup()
	ctx := context.Background()
	if err = g.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	if err = g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "99", Slots: map[string]repo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	bindDungeonSelectorOriginForTestAt(t, s, session, 38, 1, 450, 234)
	if err = s.handleDungeonSelectUpper(session, doc104SelectBody()); err != nil {
		t.Fatal(err)
	}
	if session.dungeon.runtime == nil {
		t.Fatal("catalog change still prevented dungeon entry", testPacketLogText(t, s))
	}
	after, _, err := g.Character.Load(ctx, "99")
	if err != nil {
		t.Fatal(err)
	}
	if after.Stats["fatigue"] != 99 || after.Stats["private_activity_v1_progress_6"] != 51 || after.Stats["private_activity_v1_points"] != beforePoints || after.Stats["private_activity_v1_claimed"] != 1 || after.Stats["private_activity_v1_fingerprint"] != catalog.Fingerprint || after.Stats["private_activity_v1_carry_version"] != 1 {
		t.Fatal("entry lost progress/claims or fatigue debit", after.Stats)
	}
}

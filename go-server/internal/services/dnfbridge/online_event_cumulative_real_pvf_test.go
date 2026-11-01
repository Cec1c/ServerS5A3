package dnfbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	event "longheng.io/server/internal/modules/dnf/onlineevent"
	r "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	pvf "longheng.io/server/internal/platform/pvf"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestRealPVFCumulativeAttendanceTwentyFiveQualifiedDaysAwardsExactEightItems(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set fixed real PVF path")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	sha := sha256.Sum256(raw)
	// This exercises the retained 2347 migration definition, not the current
	// 228 panel's 20-day reward table. Only these exact resource editions are allowed.
	switch hex.EncodeToString(sha[:]) {
	case "369d072b82c715aea4e02813d89ad9225686983e147bc7be0ee0cae2e296bb06",
		"94a2549598816198115340f84c0be2d31603ddeea58f70f27afe6f73b25e3df7":
	default:
		t.Fatalf("unexpected immutable PVF %x", sha)
	}
	archive, e := pvf.LoadArchive(pvf.Options{Path: path})
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	rawCatalog, e := event.LoadAttendancePVFCatalog(ctx, archive)
	if e != nil {
		t.Fatal(e)
	}
	// Bind the accepted file hash to the complete legacy reward contract.
	snapshot := rawCatalog.Snapshot()
	if !reflect.DeepEqual(snapshot.ProcessDurationsSeconds, []int64{1800, 1800, 3600, 3600}) ||
		!reflect.DeepEqual(snapshot.SumThresholds, []int64{5, 10, 15, 25}) ||
		!reflect.DeepEqual(snapshot.RewardActivityForSum, []int64{0}) {
		t.Fatalf("legacy resource contract changed: %+v", snapshot)
	}
	rewards := append(append([]event.AttendancePVFItemReward{}, snapshot.RewardItems...), snapshot.RewardItemsForSum...)
	if len(rewards) != 8 {
		t.Fatalf("legacy rewards=%+v, want eight", rewards)
	}
	for n, reward := range rewards {
		if reward.ItemID != int64(490006574+n) || reward.Count != 1 || reward.DefinitionPath == "" {
			t.Fatalf("legacy reward[%d]=%+v", n, reward)
		}
	}
	catalog, e := buildCurrentSpendTimeRuntimeCatalog(rawCatalog)
	if e != nil {
		t.Fatal(e)
	}
	if catalog.definition.QualificationStageID != "stage-4" || catalog.totalStageSeconds != 10800 || catalog.cumulative == nil {
		t.Fatalf("daily completion contract=%+v", catalog)
	}
	days := []uint32{}
	for n, s := range catalog.cumulative.Stages {
		days = append(days, s.RequiredDays)
		if s.Items[0] != (event.ItemReward{ItemID: int64(490006578 + n), Count: 1}) {
			t.Fatal("wrong real sum reward")
		}
	}
	if !reflect.DeepEqual(days, []uint32{5, 10, 15, 25}) {
		t.Fatal(days)
	}
	repos := memory.NewMemoryGroup()
	if e = repos.Account.Save(ctx, r.AccountRecord{AccountID: "candidate-account"}); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"7", "8"} {
		if e = repos.Character.Save(ctx, r.CharacterRecord{CharacterID: id, AccountID: "candidate-account", Stats: map[string]int64{}}); e != nil {
			t.Fatal(e)
		}
		if e = repos.Inventory.Save(ctx, r.InventoryRecord{CharacterID: id, Slots: map[string]r.ItemStack{}}); e != nil {
			t.Fatal(e)
		}
	}
	itemCatalog, e := newPVFDungeonDropCatalog(archive)
	if e != nil {
		t.Fatal(e)
	}
	service := &Service{pvfItemCatalog: itemCatalog}
	base := time.Date(2026, 8, 1, 7, 0, 0, 0, event.ChinaCalendar)
	sumGrants := map[int64]int64{}
	for n := 0; n < 25; n++ {
		owner, e := event.NewOwner(repos)
		if e != nil {
			t.Fatal(e)
		}
		id := fmt.Sprint(7 + n%2)
		at := base.AddDate(0, 0, n*2) // Offline gap never counts as a qualified date.
		observed, e := owner.Observe(ctx, event.ObserveCommand{AccountID: "candidate-account", CharacterID: id, Definition: catalog.definition, IntervalFrom: at, IntervalTo: at.Add(3 * time.Hour)})
		if e != nil {
			t.Fatal(e)
		}
		snapshot, items, e := service.claimCurrentSpendTimeUnlockedStages(ctx, owner, "candidate-account", id, catalog, observed.Snapshot, at.Add(3*time.Hour))
		if e != nil {
			t.Fatalf("day=%d real allocator: %v", n+1, e)
		}
		for _, i := range items {
			sumGrants[i.ItemID] += i.Delta
		}
		_, replayed, e := service.claimCurrentSpendTimeUnlockedStages(ctx, owner, "candidate-account", id, catalog, snapshot, at.Add(3*time.Hour))
		if e != nil || len(replayed) != 0 {
			t.Fatalf("day=%d repeat=%+v err=%v", n+1, replayed, e)
		}
	}
	owner, _ := event.NewOwner(repos)
	status, e := owner.CumulativeStatus(ctx, event.CumulativeStatusCommand{AccountID: "candidate-account", CharacterID: "8", Definition: *catalog.cumulative, ObservedAt: base.AddDate(0, 0, 50)})
	if e != nil || len(status.QualifiedDates) != 25 || len(status.ClaimedStageIDs) != 4 {
		t.Fatalf("persistent status=%+v err=%v", status, e)
	}
	for n := 0; n < 8; n++ {
		want := int64(25)
		if n >= 4 {
			want = 1
		}
		if sumGrants[int64(490006574+n)] != want {
			t.Fatalf("real reward%d got=%d want=%d", n, sumGrants[int64(490006574+n)], want)
		}
	}
	t.Logf("PVF=%x qualified_dates=%d claimed=%v actual_granted=%v", sha, len(status.QualifiedDates), status.ClaimedStageIDs, sumGrants)
}

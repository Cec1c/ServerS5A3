package dnfbridge

import (
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"testing"
	"time"
)

// Synthetic definitions exercise every published identity without requiring a
// private PVF. Separate opt-in archive tests verify actual resource definitions.
func TestExactHistoricalRenewalIdentityAndProtection(t *testing.T) {
	target := time.Unix(1861826400, 0)
	now := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	for _, group := range []struct {
		name  string
		count int
		rows  []struct {
			id       uint32
			path     string
			previous uint32
		}
	}{
		{"festival", 50, privateFestivalRetiredDates}, {"experience", 357, privateExperienceRetiredDates},
	} {
		if len(group.rows) != group.count {
			t.Fatalf("%s allowlist size %d", group.name, len(group.rows))
		}
		seen := map[uint32]bool{}
		for _, row := range group.rows {
			if seen[row.id] {
				t.Fatalf("duplicate %s item %d", group.name, row.id)
			}
			seen[row.id] = true
			t.Run(fmt.Sprintf("%s/%d", group.name, row.id), func(t *testing.T) {
				d := dungeonDropItemDefinition{ItemID: row.id, Kind: dungeonDropItemStackable, PVFPath: row.path, ExpirationDate: target}
				if !currentGiftBoxHistoryExpiration(d, row.previous, false).Equal(target) {
					t.Fatal("exact identity not renewed")
				}
				normalized := d
				normalized.PVFPath = strings.ToUpper(strings.ReplaceAll(row.path, "/", "\\"))
				if !currentGiftBoxHistoryExpiration(normalized, row.previous, false).Equal(target) {
					t.Fatal("normalized path rejected")
				}
				for name, change := range map[string]func(*dungeonDropItemDefinition){
					"wrong_id":        func(x *dungeonDropItemDefinition) { x.ItemID = 1 },
					"wrong_path":      func(x *dungeonDropItemDefinition) { x.PVFPath = "stackable/unrelated.stk" },
					"wrong_target":    func(x *dungeonDropItemDefinition) { x.ExpirationDate = target.Add(time.Second) },
					"daily":           func(x *dungeonDropItemDefinition) { x.DailyDeleteItem = true },
					"relative":        func(x *dungeonDropItemDefinition) { x.UsablePeriodDays = 1 },
					"relative_origin": func(x *dungeonDropItemDefinition) { x.ExpirationFromUsablePeriod = true },
				} {
					bad := d
					change(&bad)
					if !currentGiftBoxHistoryExpiration(bad, row.previous, false).IsZero() {
						t.Fatal(name, "accepted")
					}
				}
				if !currentGiftBoxHistoryExpiration(d, row.previous+1, false).IsZero() || !currentGiftBoxHistoryExpiration(d, row.previous, true).IsZero() {
					t.Fatal("independent/protected deadline changed")
				}
				old := repo.ItemStack{ItemID: int64(row.id), Count: 1, ExpireAt: time.Unix(int64(row.previous), 0)}
				renewed, changed := normalizeCurrentPVFItemStack(old, d, now)
				if !changed || currentItemListStackExpire(renewed) != 1861826400 {
					t.Fatal("instance not renewed")
				}
				if _, changed = normalizeCurrentPVFItemStack(renewed, d, now); changed {
					t.Fatal("renewal is not idempotent")
				}
			})
		}
	}
}

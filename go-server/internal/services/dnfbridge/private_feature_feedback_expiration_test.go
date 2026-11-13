package dnfbridge

import (
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

func TestRenewedLutherOldInstanceAndUnselectedItemBoundary(t *testing.T) {
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	deadline := time.Date(2028, 12, 31, 23, 59, 59, 0, currentPVFExpirationDateLocation)
	for _, id := range []int64{490008445, 490007117, 490007118} {
		d := dungeonDropItemDefinition{ItemID: uint32(id), Kind: dungeonDropItemStackable, ExpirationDate: deadline}
		old := now.Add(-time.Hour)
		v := repo.ItemStack{ItemID: id, Count: 1, ExpireAt: old, Extra: map[string]string{"expiration_source": "runtime_pvf_usable_period_grant", "usable_period_days": "7"}}
		got, _ := applyCurrentPVFItemExpirationAt(v, d, now)
		want := deadline
		if id != 490007117 {
			want = old
		}
		if !got.ExpireAt.Equal(want) {
			t.Fatalf("id=%d deadline=%v want=%v", id, got.ExpireAt, want)
		}
		again, changed := applyCurrentPVFItemExpirationAt(got, d, now)
		if changed || !again.ExpireAt.Equal(want) {
			t.Fatal("renewal not idempotent")
		}
		if id == 490007117 {
			d.UsablePeriodDays = 7
			d.ExpirationFromUsablePeriod = true
			got, _ = applyCurrentPVFItemExpirationAt(v, d, now)
			if !got.ExpireAt.Equal(old) {
				t.Fatal("old PVF authorized renewal")
			}
		}
	}
}

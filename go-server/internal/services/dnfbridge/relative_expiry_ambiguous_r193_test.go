package dnfbridge

import (
	"strconv"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestAikenR193RealMixedExpiryKeepsUnknownPolicy(t *testing.T) {
	c, err := newPVFDungeonDropCatalog(aikenR142Archive(t))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1800000000, 0)
	for _, tc := range []struct {
		id        uint32
		hasMarker bool
	}{{108551075, false}, {108551076, true}, {108020302, true}, {63430, true}, {10149649, true}} {
		t.Run(strconv.FormatUint(uint64(tc.id), 10), func(t *testing.T) {
			d, err := c.ResolveItem(tc.id)
			if err != nil {
				t.Fatal(err)
			}
			if d.UsablePeriodDays <= 0 || d.UsableExpiredItem != tc.hasMarker {
				t.Fatalf("resource mismatch %+v", d)
			}
			s := repo.ItemStack{ItemID: int64(tc.id), Count: 1, ExpireAt: now, Extra: map[string]string{"expiration_source": "runtime_pvf_usable_period_grant", "usable_period_days": strconv.FormatInt(d.UsablePeriodDays, 10), "expire_time": strconv.FormatInt(now.Unix(), 10), "expire_unix": strconv.FormatInt(now.Unix(), 10)}}
			if got := currentPVFRelativeGrantExpired(s, d, now); got == tc.hasMarker {
				t.Fatalf("delete=%v marker=%v", got, tc.hasMarker)
			}
		})
	}
}

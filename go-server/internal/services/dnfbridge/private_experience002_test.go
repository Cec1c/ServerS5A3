package dnfbridge

import (
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
)

func TestPrivateExperience002RenewalScope(t *testing.T) {
	path := os.Getenv("PRIVATE_EXPERIENCE_PVF")
	if path == "" {
		t.Skip("set PRIVATE_EXPERIENCE_PVF to a matching resource archive")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	if len(privateExperienceRetiredDates) != 357 {
		t.Fatal("renewal count")
	}
	for _, r := range privateExperienceRetiredDates {
		d, err := catalog.ResolveItem(r.id)
		if err != nil {
			t.Fatal(err)
		}
		// Relative-duration definitions retain their own earlier instance deadline.
		eligible := !d.DailyDeleteItem && d.UsablePeriodDays == 0 && !d.ExpirationFromUsablePeriod
		if privateExperienceInstanceNeedsRenewal(d, r.previous) != eligible {
			t.Fatalf("renewal %d", r.id)
		}
		if privateExperienceInstanceNeedsRenewal(d, r.previous+1) || !currentGiftBoxHistoryExpiration(d, r.previous, true).IsZero() {
			t.Fatalf("independent/protected date overwritten: %d", r.id)
		}
		bad := d
		bad.PVFPath = "wrong"
		if privateExperienceInstanceNeedsRenewal(bad, r.previous) {
			t.Fatal("path mismatch accepted")
		}
		if currentPVFExpirationUnix(d.ExpirationDate) != 1861826400 {
			t.Fatal("new PVF deadline")
		}
	}
	d, err := catalog.ResolveItem(490008916)
	if err != nil {
		t.Fatal(err)
	}
	if d.ExpirationDate.Year() != 2028 {
		t.Fatal("reported potion still expired")
	}
}

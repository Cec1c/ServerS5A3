package dnfbridge

import (
	"testing"
	"time"
)

func TestMailboxOriginalExpiryMixedDeclarations(t *testing.T) {
	t.Setenv(mailboxClaimTypeRepairEnv, "")
	t.Setenv(currentGMExpirationOverrideEnv, "")
	catalog, err := newPVFDungeonDropCatalog(adventureShopExpirationSource("[usable period]\n7\n[expiration date] `2017-11-02 06:00:00`\n"))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := catalog.ResolveItem(10304122)
	if err != nil || definition.UsablePeriodDays != 7 || definition.ExpirationDate.IsZero() {
		t.Fatalf("mixed definition: %+v err=%v", definition, err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []p157RealMailCase{
		{name: "relative_live", createdAt: now.Add(-2 * 24 * time.Hour), wantExpiry: now.Add(5 * 24 * time.Hour), wantUse: 65535, wantClaimed: true},
		{name: "relative_expired", createdAt: now.Add(-8 * 24 * time.Hour), wantExpiry: now.Add(-24 * time.Hour), wantUse: 0, wantClaimed: true},
		{name: "original_instance", createdAt: now.Add(-2 * 24 * time.Hour), explicitExpireAt: now.Add(-time.Hour), wantExpiry: now.Add(-time.Hour), wantUse: 0, wantClaimed: true},
	} {
		t.Run(tc.name, func(t *testing.T) { p157RealMailRunCase(t, catalog, "default_mixed", "gm_off", tc) })
	}
}

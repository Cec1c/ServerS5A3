package guild

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
)

const guardianCatalogFixture = "[guardian list]\n[guardian]\n1\n[ap]\n50000\n[gp]\n1000\n[/guardian]\n[guardian]\n2\n[ap]\n50000\n[gp]\n1000\n[/guardian]\n[/guardian list]"

func guardianExistingStateFixture() *GuardianState {
	return &GuardianState{EverAcquired: true, ActiveID: 2, Owned: []Guardian{
		{ID: 1, Level: 10, Skills: [5]uint8{1, 1, 1, 1, 1}, APProgress: 18},
		{ID: 2, Level: 21, Skills: [5]uint8{5, 4, 3, 2, 1}, APProgress: 83, GPProgress: 604},
	}}
}

func TestGuardianCatalogMatchedPVFAndBoundaries(t *testing.T) {
	text := guardianCatalogFixture
	if path := os.Getenv("GUILD_ESSENCE_EXTRACTED_ETC"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text = string(b)
	} else {
		t.Log("matched PVF not configured; structural catalog fixture only")
	}
	catalog, err := ParseGuardianCatalog(text)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint32{1, 2} {
		cost, err := catalog.PurchaseCost(GuardianPaid, nil, id)
		if err != nil || cost != (EssenceBalance{AP: 50000, GP: 1000}) {
			t.Fatal(id, cost, err)
		}
	}
	for name, invalid := range map[string]string{
		"empty": "", "missing_end": strings.TrimSuffix(guardianCatalogFixture, "[/guardian list]"),
		"missing_gp":   strings.Replace(guardianCatalogFixture, "[gp]\n1000\n", "", 1),
		"duplicate_ap": strings.Replace(guardianCatalogFixture, "[ap]\n50000", "[ap]\n50000\n[ap]\n50000", 1),
		"duplicate_id": strings.Replace(guardianCatalogFixture, "[guardian]\n2", "[guardian]\n1", 1),
		"unknown_id":   strings.Replace(guardianCatalogFixture, "[guardian]\n2", "[guardian]\n3", 1),
		"negative":     strings.Replace(guardianCatalogFixture, "50000", "-1", 1),
		"zero":         strings.Replace(guardianCatalogFixture, "50000", "0", 1),
		"overflow":     strings.Replace(guardianCatalogFixture, "50000", "2147483648", 1),
		"extra_token":  strings.Replace(guardianCatalogFixture, "50000", "50000 1", 1),
		"two_lists":    guardianCatalogFixture + "\n" + guardianCatalogFixture,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseGuardianCatalog(invalid); err == nil {
				t.Fatal("malformed purchase catalog accepted")
			}
		})
	}
}

func TestGuardianExplicitPoliciesDoNotMutateOrTreatActiveAsFirst(t *testing.T) {
	catalog, err := ParseGuardianCatalog(guardianCatalogFixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, policy := range []GuardianFirstPurchasePolicy{"", "automatic", GuardianFreeOnce, GuardianPaid} {
		t.Run(string(policy), func(t *testing.T) {
			cost, err := catalog.PurchaseCost(policy, nil, 1)
			if policy != GuardianFreeOnce && policy != GuardianPaid {
				if !errors.Is(err, ErrGuardianPolicy) {
					t.Fatal(err)
				}
				return
			}
			want := EssenceBalance{AP: 50000, GP: 1000}
			if policy == GuardianFreeOnce {
				want = EssenceBalance{}
			}
			if err != nil || cost != want {
				t.Fatal(cost, err)
			}
			// Another unowned guardian costs AP/GP even when ActiveID is zero.
			state := &GuardianState{EverAcquired: true, Owned: []Guardian{{ID: 1, Level: 10}}}
			for range 3 {
				cost, err = catalog.PurchaseCost(policy, state, 2)
				if err != nil || cost != (EssenceBalance{AP: 50000, GP: 1000}) || state.ActiveID != 0 || len(state.Owned) != 1 {
					t.Fatal("quote changed ownership or repeated first discount", cost, err)
				}
			}
			if _, err = catalog.PurchaseCost(policy, state, 1); !errors.Is(err, ErrGuardianAlreadyOwned) {
				t.Fatal("owned purchase must not act as selection", err)
			}
			state.Owned = nil // Retained lifetime fact, regardless of a future reset.
			cost, err = catalog.PurchaseCost(policy, state, 1)
			if err != nil || cost != (EssenceBalance{AP: 50000, GP: 1000}) {
				t.Fatal("lifetime acquisition ignored", cost, err)
			}
		})
	}
	if _, err = catalog.PurchaseCost(GuardianFreeOnce, nil, 3); !errors.Is(err, ErrGuardianCatalog) {
		t.Fatal("unknown ID got free quote", err)
	}
	var missing *GuardianCatalog
	if _, err = missing.PurchaseCost(GuardianPaid, nil, 1); !errors.Is(err, ErrGuardianCatalog) {
		t.Fatal(err)
	}
}

func TestGuardianStateValidationAndFailedSavePreserveRegistry(t *testing.T) {
	for _, mode := range []string{"missing_lifetime", "unknown_id", "duplicate_id", "missing_active", "zero_level", "large_level", "large_skill", "too_many"} {
		t.Run(mode, func(t *testing.T) {
			repos, _, now := essenceTestGroup(t)
			ctx := context.Background()
			before, _, _ := repos.Settings.Load(ctx, RegistryScope)
			reg, _ := Load(ctx, repos.Settings)
			s := guardianExistingStateFixture()
			reg.Guilds[1].Guardians = s
			switch mode {
			case "missing_lifetime":
				s.EverAcquired = false
			case "unknown_id":
				s.Owned[0].ID = 3
			case "duplicate_id":
				s.Owned[0].ID = 2
			case "missing_active":
				s.ActiveID = 3
			case "zero_level":
				s.Owned[0].Level = 0
			case "large_level":
				s.Owned[0].Level = 51
			case "large_skill":
				s.Owned[1].Skills[4] = 6
			case "too_many":
				s.Owned = append(s.Owned, s.Owned[0])
			}
			if err := Save(ctx, repos.Settings, reg, now); !errors.Is(err, ErrGuardianState) {
				t.Fatal(err)
			}
			after, _, _ := repos.Settings.Load(ctx, RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("invalid state partially saved")
			}
		})
	}
	for _, state := range []*GuardianState{nil, {}, {EverAcquired: true}, guardianExistingStateFixture()} {
		if err := state.Validate(); err != nil {
			t.Fatal("valid state rejected", err)
		}
	}
}

func TestGuardianPersistenceSurvivesConcurrentIncomeAndLateRollback(t *testing.T) {
	repos, owner, now := essenceTestGroup(t)
	ctx := context.Background()
	reg, _ := Load(ctx, repos.Settings)
	reg.Guilds[1].Guardians = guardianExistingStateFixture()
	if err := Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	before, _, _ := repos.Settings.Load(ctx, RegistryScope)
	beforeChar, _, _ := repos.Character.Load(ctx, owner.CharacterID)
	calls := 0
	_, err := GrantEssenceLogin(ctx, repos.Guild, owner, essenceTestCatalog(t), now, func() bool { calls++; return calls < 2 })
	after, _, _ := repos.Settings.Load(ctx, RegistryScope)
	afterChar, _, _ := repos.Character.Load(ctx, owner.CharacterID)
	if !errors.Is(err, ErrCharacterNotFound) || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeChar, afterChar) {
		t.Fatal("late session rollback changed guardian or income", err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for range 12 {
		wg.Go(func() {
			_, err := GrantEssenceLogin(ctx, repos.Guild, owner, essenceTestCatalog(t), now, func() bool { return true })
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := Load(ctx, repos.Settings)
	if err != nil || !reflect.DeepEqual(loaded.Guilds[1].Guardians, guardianExistingStateFixture()) || loaded.Guilds[1].Essence != (EssenceBalance{AP: 10, GP: 17}) {
		t.Fatal("concurrent income lost ownership or duplicated balance", err)
	}
}

package characterdata

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func renameFixture(t *testing.T) (repo.Group, RenameRequest) {
	t.Helper()
	g := memory.NewMemoryGroup()
	r := RenameRequest{AccountID: "a", CharacterID: "77", Slot: 3, OldName: "旧名字", NewName: "新名字", Now: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)}
	if err := g.Character.Save(context.Background(), repo.CharacterRecord{CharacterID: r.CharacterID, AccountID: r.AccountID, Slot: r.Slot, Name: r.OldName, Level: 90, Stats: map[string]int64{RenameEntitlementStat: 1, RenameMinimumLevelStat: 30, RenameCooldownSecondsStat: int64(RenameCooldown / time.Second), RenameReservationSecondsStat: int64(PreviousNameReservation / time.Second), "gold": 9876}}); err != nil {
		t.Fatal(err)
	}
	return g, r
}

func TestRenameCommitRetryAndGlobalReservation(t *testing.T) {
	g, r := renameFixture(t)
	ctx := context.Background()
	if err := Rename(ctx, g, r); err != nil {
		t.Fatal(err)
	}
	c, _, _ := g.Character.Load(ctx, r.CharacterID)
	if c.Name != r.NewName || c.Stats[RenameEntitlementStat] != 0 || c.Stats[RenameLastUnixStat] != r.Now.Unix() || c.Stats["gold"] != 9876 {
		t.Fatal(c)
	}
	r.Now = r.Now.Add(time.Hour)
	if err := Rename(ctx, g, r); err != nil {
		t.Fatal("retry", err)
	}
	after, _, _ := g.Character.Load(ctx, r.CharacterID)
	if !after.UpdatedAt.Equal(c.UpdatedAt) || after.Stats[RenameLastUnixStat] != c.Stats[RenameLastUnixStat] {
		t.Fatal("retry rewrote receipt", after)
	}
	if err := CheckNameAvailable(ctx, g.Character, g.Settings, r.OldName, r.Now); !errors.Is(err, repo.ErrCharacterNameExists) {
		t.Fatal("old name available", err)
	}
	creation := Creation{Character: repo.CharacterRecord{CharacterID: "78", AccountID: "other", Slot: 0, Name: r.OldName, Level: 1}}
	if err := NewCreator(g).Create(ctx, creation); !errors.Is(err, repo.ErrCharacterNameExists) {
		t.Fatal("create stole reserved name", err)
	}
	if _, found, _ := g.Character.Load(ctx, "78"); found {
		t.Fatal("failed creation persisted")
	}
	if err := CheckNameAvailable(ctx, g.Character, g.Settings, r.OldName, c.UpdatedAt.Add(PreviousNameReservation)); err != nil {
		t.Fatal("expired reservation", err)
	}
}

func TestRenameRejectsWithoutChangingEntitlement(t *testing.T) {
	for _, scenario := range []string{"wrong_account", "wrong_slot", "old_mismatch", "no_entitlement", "multiple_entitlements", "under_level", "cooldown", "future_clock", "duplicate", "reserved", "deleted", "same_name", "bad_name"} {
		t.Run(scenario, func(t *testing.T) {
			g, r := renameFixture(t)
			ctx := context.Background()
			c, _, _ := g.Character.Load(ctx, r.CharacterID)
			switch scenario {
			case "wrong_account":
				r.AccountID = "other"
			case "wrong_slot":
				r.Slot = 2
			case "old_mismatch":
				r.OldName = "bad"
			case "no_entitlement":
				c.Stats[RenameEntitlementStat] = 0
			case "multiple_entitlements":
				c.Stats[RenameEntitlementStat] = 2
			case "under_level":
				c.Level = 29
			case "cooldown":
				c.Stats[RenameNextAllowedUnixStat] = r.Now.Add(time.Second).Unix()
			case "future_clock":
				c.Stats[RenameNextAllowedUnixStat] = r.Now.Add(time.Hour).Unix()
			case "duplicate":
				_ = g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "88", AccountID: "other", Slot: 0, Name: r.NewName})
			case "reserved":
				if err := ReservePreviousName(ctx, g.Settings, r.NewName, r.Now, r.Now.Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
			case "deleted":
				c.Stats["delete_flag"] = 1
			case "same_name":
				r.NewName = r.OldName
			case "bad_name":
				r.NewName = "bad\x00name"
			}
			_ = g.Character.Save(ctx, c)
			if err := Rename(ctx, g, r); err == nil {
				t.Fatal("accepted invalid rename")
			}
			after, _, _ := g.Character.Load(ctx, r.CharacterID)
			if after.Name != c.Name || after.Stats[RenameEntitlementStat] != c.Stats[RenameEntitlementStat] {
				t.Fatal("failure changed character", after)
			}
		})
	}
}

type failedRenameSettings struct{ repo.SettingsRepository }

func (s failedRenameSettings) Save(ctx context.Context, r repo.SettingsRecord) error {
	if r.Scope == "character:77:rename" {
		return errors.New("receipt physical save failed")
	}
	return s.SettingsRepository.Save(ctx, r)
}

func TestRenamePhysicalSaveFailureRollsBackNameAndReservations(t *testing.T) {
	g, r := renameFixture(t)
	original := g
	g.Settings = failedRenameSettings{g.Settings}
	if err := Rename(context.Background(), g, r); err == nil {
		t.Fatal("accepted failed receipt")
	}
	c, _, _ := original.Character.Load(context.Background(), r.CharacterID)
	if c.Name != r.OldName || c.Stats[RenameEntitlementStat] != 1 || c.Stats[RenameLastUnixStat] != 0 {
		t.Fatal("character escaped rollback", c)
	}
	if err := CheckNameAvailable(context.Background(), original.Character, original.Settings, r.NewName, r.Now); err != nil {
		t.Fatal("new name falsely reserved", err)
	}
	state, _, _ := original.Settings.Load(context.Background(), repo.CharacterNameMutationScope)
	if len(state.Values) != 0 {
		t.Fatal("reservation escaped rollback", state)
	}
}

func TestConcurrentRenameAndCreateCannotClaimSameName(t *testing.T) {
	g, r := renameFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); <-start; results <- Rename(ctx, g, r) }()
	go func() {
		defer wg.Done()
		<-start
		results <- NewCreator(g).Create(ctx, Creation{Character: repo.CharacterRecord{CharacterID: "88", AccountID: "other", Slot: 0, Name: r.NewName, Level: 1}})
	}()
	close(start)
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, repo.ErrCharacterNameExists) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatal("claims", success)
	}
}

func TestNameReservationEquivalenceAndBoundedStorageKey(t *testing.T) {
	for _, pair := range [][2]string{{"Name", "NAME"}, {"Ｃａｒｄ", "card"}, {"Café", "cafe"}} {
		if NameReservationKey(pair[0]) != NameReservationKey(pair[1]) {
			t.Fatal(pair)
		}
	}
	if len(NameReservationKey("三十字节以内的名字")) > 128 {
		t.Fatal("settings key overflow")
	}
}

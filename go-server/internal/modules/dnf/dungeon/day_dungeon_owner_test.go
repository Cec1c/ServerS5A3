package dungeon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func dayEntryTestDungeon(t *testing.T, id int64) worldmap.Dungeon {
	t.Helper()
	path := fmt.Sprintf("dungeon/gcontents/171116_dayofdungeon/chn_daydungeon_%02d.dgn", id-4905)
	days := DayDungeonSchedules()[id]
	doc, err := pvf.Parse(path, fmt.Sprintf("[schedule]\n%d %d\n[/schedule]\n", days[0], days[1]))
	if err != nil {
		t.Fatal(err)
	}
	return worldmap.ParseDungeon(id, path, doc)
}

func dayEntryFixture(t *testing.T, id int64) (dnfrepo.Group, *DayDungeonEntryOwner, DayDungeonEntryCommand) {
	t.Helper()
	repos := memory.NewMemoryGroup()
	ctx := context.Background()
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "dnf:1", Metadata: map[string]string{"unrelated": "retain"}}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 12; i++ {
		key := fmt.Sprint(i)
		if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: key, AccountID: "dnf:1", Level: 90, Stats: map[string]int64{"fatigue": 100, "unrelated": 99}}); err != nil {
			t.Fatal(err)
		}
	}
	owner, err := NewDayDungeonEntryOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	return repos, owner, DayDungeonEntryCommand{AccountID: "dnf:1", CharacterID: "1", Dungeon: dayEntryTestDungeon(t, id), AccountCharacterLimit: 5, ObservedAt: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
}

func TestDayDungeonDistinctCharactersAndRepeatedEntryDurablyLimit(t *testing.T) {
	repos, owner, cmd := dayEntryFixture(t, 4906)
	for count := int64(1); count <= 2; count++ {
		result, err := owner.Consume(context.Background(), cmd)
		if err != nil || result.Status.CharacterUsed != count || result.Status.AccountCharactersUsed != 1 {
			t.Fatalf("entry %d: %+v %v", count, result, err)
		}
		owner, _ = NewDayDungeonEntryOwner(repos)
	}
	if _, err := owner.Consume(context.Background(), cmd); !errors.Is(err, ErrDailyEntryLimit) {
		t.Fatal("third character entry accepted", err)
	}
	for i := 2; i <= 5; i++ {
		cmd.CharacterID = fmt.Sprint(i)
		if _, err := owner.Consume(context.Background(), cmd); err != nil {
			t.Fatal(err)
		}
	}
	cmd.CharacterID = "6"
	if _, err := owner.Consume(context.Background(), cmd); !errors.Is(err, ErrDailyEntryLimit) {
		t.Fatal("sixth distinct character accepted", err)
	}
	// An already admitted character can enter another Saturday dungeon even
	// when all five account character slots have been used.
	cmd.CharacterID = "1"
	cmd.Dungeon = dayEntryTestDungeon(t, 4908)
	if result, err := owner.Consume(context.Background(), cmd); err != nil || result.Status.AccountCharactersUsed != 5 || result.Status.CharacterUsed != 1 {
		t.Fatalf("existing character across dungeons: %+v %v", result, err)
	}
	a, _, _ := repos.Account.Load(context.Background(), cmd.AccountID)
	c, _, _ := repos.Character.Load(context.Background(), cmd.CharacterID)
	if a.Metadata["unrelated"] != "retain" || c.Stats["fatigue"] != 100 || c.Stats["unrelated"] != 99 {
		t.Fatal("unrelated owner state changed")
	}
	cmd.ObservedAt = time.Date(2026, 10, 10, 21, 59, 59, 0, time.UTC)
	if _, err := owner.Consume(context.Background(), cmd); !errors.Is(err, ErrDailyEntryLimit) {
		t.Fatal("counter reset before06", err)
	}
	cmd.ObservedAt = cmd.ObservedAt.Add(time.Second)
	cmd.Dungeon = dayEntryTestDungeon(t, 4907)
	cmd.CharacterID = "6"
	result, err := owner.Consume(context.Background(), cmd)
	if err != nil || result.Status.CharacterUsed != 1 || result.Status.AccountCharactersUsed != 1 {
		t.Fatalf("new Sunday counters: %+v %v", result, err)
	}
}

func TestDayDungeonConcurrentAccountAndCharacterLimits(t *testing.T) {
	for _, sameCharacter := range []bool{false, true} {
		repos, owner, cmd := dayEntryFixture(t, 4908)
		var wg sync.WaitGroup
		results := make(chan error, 12)
		for i := 1; i <= 12; i++ {
			wg.Add(1)
			copyCmd := cmd
			if !sameCharacter {
				copyCmd.CharacterID = fmt.Sprint(i)
			}
			go func() { defer wg.Done(); _, err := owner.Consume(context.Background(), copyCmd); results <- err }()
		}
		wg.Wait()
		close(results)
		accepted := 0
		for err := range results {
			if err == nil {
				accepted++
			} else if !errors.Is(err, ErrDailyEntryLimit) {
				t.Fatal(err)
			}
		}
		want := 5
		if sameCharacter {
			want = 1
		}
		if accepted != want {
			t.Fatalf("accepted %d want%d", accepted, want)
		}
		account, _, _ := repos.Account.Load(context.Background(), cmd.AccountID)
		var state dayDungeonAccountState
		if err := jsonUnmarshalDayState(account, &state); err != nil || len(state.Characters) != want {
			t.Fatal("account counter did not match committed entries", err)
		}
	}
}

func jsonUnmarshalDayState(account dnfrepo.AccountRecord, state *dayDungeonAccountState) error {
	return json.Unmarshal([]byte(account.Metadata[dayDungeonAccountMetadataKey]), state)
}

type failingDayEntryTransaction struct{ dnfrepo.RentalAssetUnitOfWork }

func (u failingDayEntryTransaction) WithinRentalAssets(ctx context.Context, a, c string, apply func(dnfrepo.AccountRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error) error {
	return u.RentalAssetUnitOfWork.WithinRentalAssets(ctx, a, c, func(accounts dnfrepo.AccountRepository, characters dnfrepo.CharacterRepository, inventory dnfrepo.InventoryRepository, equipment dnfrepo.EquipmentRepository) error {
		if err := apply(accounts, characters, inventory, equipment); err != nil {
			return err
		}
		return errors.New("commit fixture failure")
	})
}

func TestDayDungeonAtomicFailureAndForgedOwnerDoNotConsume(t *testing.T) {
	repos, owner, cmd := dayEntryFixture(t, 4908)
	owner.assets = failingDayEntryTransaction{repos.RentalAssets}
	if _, err := owner.Consume(context.Background(), cmd); err == nil {
		t.Fatal("failed transaction accepted")
	}
	a, _, _ := repos.Account.Load(context.Background(), cmd.AccountID)
	c, _, _ := repos.Character.Load(context.Background(), cmd.CharacterID)
	used, err := DailyEntryUsed(c, 4908, cmd.ObservedAt)
	if err != nil || used != 0 || a.Metadata[dayDungeonAccountMetadataKey] != "" {
		t.Fatal("failed transaction committed one counter")
	}
	owner.assets = repos.RentalAssets
	cmd.AccountID = "dnf:2"
	if err := repos.Account.Save(context.Background(), dnfrepo.AccountRecord{AccountID: cmd.AccountID}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Consume(context.Background(), cmd); err == nil {
		t.Fatal("cross-account character consumed")
	}
	cmd.AccountID = "dnf:1"
	cmd.ObservedAt = cmd.ObservedAt.Add(24 * time.Hour)
	if _, err := owner.Consume(context.Background(), cmd); !errors.Is(err, ErrDayDungeonClosed) {
		t.Fatal("closed day consumed", err)
	}
}

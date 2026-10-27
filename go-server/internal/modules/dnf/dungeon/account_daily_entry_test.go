package dungeon

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func forestEntryFixture(t *testing.T) (repo.Group, *Owner, EpicEntryCommand) {
	t.Helper()
	g := memory.NewMemoryGroup()
	now := time.Date(2026, 10, 25, 5, 59, 0, 0, time.FixedZone("China", 8*3600))
	if err := g.Account.Save(nil, repo.AccountRecord{AccountID: "a", Metadata: map[string]string{"unrelated": "keep"}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"31", "32"} {
		if err := g.Character.Save(nil, repo.CharacterRecord{CharacterID: id, AccountID: "a", Level: 17, Stats: map[string]int64{"fatigue": 0}}); err != nil {
			t.Fatal(err)
		}
	}
	o, err := NewOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	return g, o, EpicEntryCommand{CharacterID: "31", AccountID: "a", DungeonID: 6062, Nonce: 1, UpdatedAt: now}
}

func TestSilentForestAccountTenAcrossCharactersReplayAndServiceDay(t *testing.T) {
	g, o, cmd := forestEntryFixture(t)
	seq := map[string]int64{}
	for i := 0; i < 10; i++ {
		cmd.CharacterID = fmt.Sprint(31 + i%2)
		cmd.ExpectedSequence = seq[cmd.CharacterID]
		cmd.Nonce = int64(i + 1)
		if _, err := o.ConsumeSilentForestEntry(nil, cmd, 10); err != nil {
			t.Fatal(i, err)
		}
		if out, err := o.ConsumeSilentForestEntry(nil, cmd, 10); err != nil || !out.Replay {
			t.Fatal("replay", out, err)
		}
		seq[cmd.CharacterID]++
	}
	a, _, _ := g.Account.Load(nil, "a")
	used, err := SilentForestEntryUsed(a, cmd.UpdatedAt)
	if err != nil || used != 10 || a.Metadata["unrelated"] != "keep" {
		t.Fatal(a, used, err)
	}
	for _, id := range []string{"31", "32"} {
		cmd.CharacterID = id
		cmd.ExpectedSequence = seq[id]
		cmd.Nonce = 99
		if _, err := o.ConsumeSilentForestEntry(nil, cmd, 10); !errors.Is(err, ErrDailyEntryLimit) {
			t.Fatal("eleventh", id, err)
		}
		c, _, _ := g.Character.Load(nil, id)
		if c.Stats["fatigue"] != 0 || c.Stats[EpicEntrySequenceKey] != seq[id] {
			t.Fatal("rejected mutation", c)
		}
	}
	cmd.UpdatedAt = cmd.UpdatedAt.Add(time.Minute)
	if _, err := o.ConsumeSilentForestEntry(nil, cmd, 10); err != nil {
		t.Fatal("06:00 reset", err)
	}
	a, _, _ = g.Account.Load(nil, "a")
	used, err = SilentForestEntryUsed(a, cmd.UpdatedAt)
	if used != 1 || err != nil {
		t.Fatal(used, err)
	}
}

type forestFailureAssets struct {
	repo.AccountCharacterAssetUnitOfWork
	fault error
}

func (u forestFailureAssets) WithinAccountCharacterLedgerAssets(ctx context.Context, a, c string, fn func(repo.AccountRepository, repo.AccountInventoryRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	return u.AccountCharacterAssetUnitOfWork.(repo.AccountCharacterLedgerAssetUnitOfWork).WithinAccountCharacterLedgerAssets(ctx, a, c, func(ar repo.AccountRepository, ai repo.AccountInventoryRepository, ch repo.CharacterRepository, in repo.InventoryRepository, eq repo.EquipmentRepository) error {
		if err := fn(ar, ai, ch, in, eq); err != nil {
			return err
		}
		return u.fault
	})
}
func TestSilentForestAccountTransactionRollbackAndIdentity(t *testing.T) {
	for _, kind := range []string{"late_failure", "identity", "validation"} {
		t.Run(kind, func(t *testing.T) {
			g, _, cmd := forestEntryFixture(t)
			fault := errors.New("late rollback")
			switch kind {
			case "late_failure":
				g.AccountAssets = forestFailureAssets{g.AccountAssets, fault}
			case "identity":
				cmd.AccountID = "other"
			case "validation":
				calls := 0
				cmd.Validate = func() error {
					calls++
					if calls == 2 {
						return fault
					}
					return nil
				}
			}
			o, _ := NewOwner(g)
			if _, err := o.ConsumeSilentForestEntry(nil, cmd, 10); err == nil {
				t.Fatal("accepted invalid admission")
			}
			a, _, _ := g.Account.Load(nil, "a")
			used, err := SilentForestEntryUsed(a, cmd.UpdatedAt)
			c, _, _ := g.Character.Load(nil, "31")
			if used != 0 || err != nil || c.Stats[EpicEntrySequenceKey] != 0 {
				t.Fatal("partial commit", a, c, err)
			}
		})
	}
}
func TestSilentForestAccountConcurrentLastEntry(t *testing.T) {
	g, o, cmd := forestEntryFixture(t)
	a, _, _ := g.Account.Load(nil, "a")
	a.Metadata[silentForestAccountEntryKey] = fmt.Sprintf(`{"day":%d,"used":9}`, premium.ServiceDayID(cmd.UpdatedAt))
	if err := g.Account.Save(nil, a); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	out := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := cmd
			c.CharacterID = fmt.Sprint(31 + i)
			c.Nonce = int64(i + 1)
			<-start
			_, err := o.ConsumeSilentForestEntry(nil, c, 10)
			out <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(out)
	success, denied := 0, 0
	for err := range out {
		if err == nil {
			success++
		} else if errors.Is(err, ErrDailyEntryLimit) {
			denied++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || denied != 1 {
		t.Fatal(success, denied)
	}
}

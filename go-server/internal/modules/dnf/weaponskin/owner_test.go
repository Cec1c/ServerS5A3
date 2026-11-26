package weaponskin

import (
	"context"
	"errors"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestDemoWardrobeApplyClearAndReopen(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	ch := dnfrepo.CharacterRecord{CharacterID: "1", AccountID: "a", Job: "0", Stats: map[string]int64{"fatigue": 20}}
	bag := dnfrepo.InventoryRecord{CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{"1:3": {ItemID: SampleID, Count: 1}}}
	if err := repos.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, bag); err != nil {
		t.Fatal(err)
	}
	command := Command{CharacterID: "1", AccountID: "a", Now: time.Unix(123, 0), Current: func() bool { return true }}
	for _, trial := range []struct {
		action   Action
		revision uint32
		want     State
		err      error
	}{
		{List, 0, State{Owned: true}, nil},
		{Apply, 0, State{Owned: true, Selected: SampleID, Revision: 1}, nil},
		{List, 0, State{Owned: true, Selected: SampleID, Revision: 1}, nil},
		{Clear, 1, State{Owned: true, Revision: 2}, nil},
		{Apply, 0, State{Owned: true, Revision: 2}, ErrRevision},
		{List, 0, State{Owned: true, Revision: 2}, nil},
	} {
		command.Action, command.Revision = trial.action, trial.revision
		got, err := Update(ctx, repos.CharacterAssets, command)
		if got != trial.want || !errors.Is(err, trial.err) {
			t.Fatalf("action=%v revision=%d state=%+v err=%v", trial.action, trial.revision, got, err)
		}
	}
	loaded, _, _ := repos.Character.Load(ctx, "1")
	back, _, _ := repos.Inventory.Load(ctx, "1")
	if loaded.Stats["fatigue"] != 20 || Selected(loaded) != 0 || back.Slots["1:3"].Count != 1 {
		t.Fatalf("角色或物品出现额外修改: %+v %+v", loaded.Stats, back.Slots)
	}
}

func TestDemoWardrobeRejectsForeignExpiredAndRevoked(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "1", AccountID: "a", Job: "0"}); err != nil {
		t.Fatal(err)
	}
	expired := dnfrepo.InventoryRecord{CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{
		"1:3": {ItemID: SampleID, Count: 1, ExpireAt: time.Unix(10, 0)},
		"0:3": {ItemID: SampleID, Count: 1},
	}}
	if err := repos.Inventory.Save(ctx, expired); err != nil {
		t.Fatal(err)
	}
	command := Command{CharacterID: "1", AccountID: "b", Action: Apply, Now: time.Unix(10, 0), Current: func() bool { return true }}
	for _, trial := range []struct {
		account string
		current bool
		err     error
	}{
		{"b", true, ErrCharacter},
		{"a", false, ErrCharacter},
		{"a", true, ErrNotOwned},
	} {
		command.AccountID = trial.account
		command.Current = func() bool { return trial.current }
		if _, err := Update(ctx, repos.CharacterAssets, command); !errors.Is(err, trial.err) {
			t.Fatalf("account=%s current=%t err=%v", trial.account, trial.current, err)
		}
	}
	got, _, _ := repos.Character.Load(ctx, "1")
	if len(got.Stats) != 0 {
		t.Fatalf("拒绝请求改写角色: %+v", got.Stats)
	}
}

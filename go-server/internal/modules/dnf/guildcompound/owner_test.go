package guildcompound

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

const ownerTestCatalog = `[compound info]
0 4 2000 1 4 1500 2 4 750 3 2 0
[/compound info]

[guild flag compound info]
0 4 2500 1 4 2000 2 4 1000 3 4 500 6 2 0
[/guid flag compound info]
`

type ownerTestSource string

func (s ownerTestSource) ReadText(string) (string, error) { return string(s), nil }

var ownerTestNow = time.Unix(1_700_000_000, 0).UTC()

type ownerTestPoolKey struct {
	kind   CompoundKind
	rarity uint32
}

type ownerTestRules struct {
	defs        map[uint32]MaterialDefinition
	pools       map[ownerTestPoolKey][]uint32
	rolls       []uint32
	rollIndex   int
	updateCalls []repo.ItemStack
	grantCalls  []uint32
	grantDelta  int64
	grantErr    error
	expiredAll  bool
	updateErr   error
}

func (r *ownerTestRules) rules(catalog CompoundCatalog) Rules {
	return Rules{
		Catalog: catalog,
		Resolve: func(itemID uint32) (MaterialDefinition, error) {
			definition, found := r.defs[itemID]
			if !found {
				return MaterialDefinition{}, errors.New("owner test item is unresolved")
			}
			return definition, nil
		},
		Candidates: func(kind CompoundKind, rarity uint32) ([]uint32, error) {
			return append([]uint32(nil), r.pools[ownerTestPoolKey{kind: kind, rarity: rarity}]...), nil
		},
		UpdateStack: func(stack repo.ItemStack, slot int16) (repo.ItemStack, error) {
			r.updateCalls = append(r.updateCalls, stack)
			if r.updateErr != nil {
				return repo.ItemStack{}, r.updateErr
			}
			stack.RawEntry = []byte{byte(stack.Count)}
			return stack, nil
		},
		Grant: func(inventory *repo.InventoryRecord, itemID uint32) (int16, error) {
			r.grantCalls = append(r.grantCalls, itemID)
			return r.grant(inventory, itemID)
		},
		Expired: func(stack repo.ItemStack, _ time.Time) bool {
			return r.expiredAll || stack.Extra["test_expired"] == "1"
		},
		Roll: func(limit uint32) uint32 {
			if r.rollIndex >= len(r.rolls) {
				return limit - 1
			}
			value := r.rolls[r.rollIndex]
			r.rollIndex++
			return value
		},
	}
}

func (r *ownerTestRules) grant(inventory *repo.InventoryRecord, itemID uint32) (int16, error) {
	if r.grantErr != nil {
		return 0, r.grantErr
	}
	definition, found := r.defs[itemID]
	if !found {
		return 0, errors.New("owner test grant item is unresolved")
	}
	first, last, ok := definition.Family.page()
	if !ok {
		return 0, errors.New("owner test grant family is invalid")
	}
	amount := int64(1) + r.grantDelta
	if definition.Family == MaterialFamilyGuardianGem {
		for slot := first; slot <= last; slot++ {
			key := list38SlotKey(slot)
			stack, occupied := inventory.Slots[key]
			if !occupied || stack.ItemID != int64(itemID) {
				continue
			}
			stack.Count += amount
			stack.RawEntry = []byte{byte(stack.Count)}
			inventory.Slots[key] = stack
			return slot, nil
		}
	}
	for slot := first; slot <= last; slot++ {
		key := list38SlotKey(slot)
		if _, occupied := inventory.Slots[key]; occupied {
			continue
		}
		inventory.Slots[key] = repo.ItemStack{
			ItemID:   int64(itemID),
			Count:    amount,
			RawEntry: []byte{byte(amount)},
		}
		return slot, nil
	}
	return 0, ErrNoSpace
}

type ownerTestFixture struct {
	ctx     context.Context
	repos   repo.Group
	owner   *Owner
	rules   *ownerTestRules
	catalog CompoundCatalog
}

func newOwnerTestFixture(t *testing.T, entries map[string]repo.ItemStack, configure ...func(*ownerTestRules)) *ownerTestFixture {
	t.Helper()
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, repo.CharacterRecord{CharacterID: "77", AccountID: "acct"}); err != nil {
		t.Fatalf("seed character: %v", err)
	}
	if entries == nil {
		entries = map[string]repo.ItemStack{}
	}
	if err := repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "77", Slots: entries}); err != nil {
		t.Fatalf("seed inventory: %v", err)
	}
	catalog, err := LoadCompoundCatalog(ownerTestSource(ownerTestCatalog))
	if err != nil {
		t.Fatalf("load compound catalog: %v", err)
	}
	rules := &ownerTestRules{
		defs: map[uint32]MaterialDefinition{
			90002:     {Family: MaterialFamilyGuardianGem, Rarity: 2},
			90003:     {Family: MaterialFamilyGuardianGem, Rarity: 3},
			90013:     {Family: MaterialFamilyGuardianGem, Rarity: 3},
			100380017: {Family: MaterialFamilyGuildMedal, Rarity: 0},
			100380018: {Family: MaterialFamilyGuildMedal, Rarity: 0},
			100380031: {Family: MaterialFamilyGuildMedal, Rarity: 1},
			100380032: {Family: MaterialFamilyGuildMedal, Rarity: 1},
		},
		pools: map[ownerTestPoolKey][]uint32{
			{kind: CompoundGem, rarity: 3}:  {90003, 90013},
			{kind: CompoundFlag, rarity: 1}: {100380031, 100380032},
		},
	}
	for _, setup := range configure {
		if setup != nil {
			setup(rules)
		}
	}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatalf("NewOwner: %v", err)
	}
	return &ownerTestFixture{ctx: ctx, repos: repos, owner: owner, rules: rules, catalog: catalog}
}

func (f *ownerTestFixture) command(accountID, characterID string, kind CompoundKind, grade, count uint32, slots ...uint32) Command {
	request := Request{Count: count, Grade: grade}
	for index := range request.MaterialRefs {
		request.MaterialRefs[index] = AbsentMaterialRef
	}
	copy(request.MaterialRefs[:], slots)
	return Command{
		AccountID:   accountID,
		CharacterID: characterID,
		Kind:        kind,
		Request:     request,
		Rules:       f.rules.rules(f.catalog),
		Now:         ownerTestNow,
	}
}

func (f *ownerTestFixture) inventory(t *testing.T) repo.InventoryRecord {
	t.Helper()
	record, found, err := f.repos.Inventory.Load(f.ctx, "77")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	return record
}

func TestOwnerFourSameIDGemAlwaysUpgrades(t *testing.T) {
	t.Run("four same id upgrades", func(t *testing.T) {
		f := newOwnerTestFixture(t, map[string]repo.ItemStack{
			"38:49": {ItemID: 90002, Count: 10, RawEntry: []byte{10}},
		})

		result, err := f.owner.Apply(f.ctx, f.command("acct", "77", CompoundGem, 2, 4, 49, 49, 49, 49))
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if !result.Success || result.ResultItem != 90003 || result.NextRarity != 3 || result.ResultSlot != 50 {
			t.Fatalf("result = %+v", result)
		}
		inventory := f.inventory(t)
		if got := inventory.Slots["38:49"]; got.Count != 6 || !reflect.DeepEqual(got.RawEntry, []byte{6}) {
			t.Fatalf("consumed material = %+v", got)
		}
		if got := inventory.Slots["38:50"]; got.ItemID != 90003 || got.Count != 1 {
			t.Fatalf("result stack = %+v", got)
		}
		if len(f.rules.updateCalls) != 1 || f.rules.updateCalls[0].Count != 6 {
			t.Fatalf("update calls = %+v", f.rules.updateCalls)
		}
	})

	t.Run("two distinct ids select a next-tier candidate", func(t *testing.T) {
		f := newOwnerTestFixture(t, map[string]repo.ItemStack{
			"38:49": {ItemID: 90002, Count: 2, RawEntry: []byte{2}},
			"38:50": {ItemID: 90002, Count: 2, RawEntry: []byte{2}},
		}, func(rules *ownerTestRules) { rules.rolls = []uint32{1} })

		result, err := f.owner.Apply(f.ctx, f.command("acct", "77", CompoundGem, 2, 2, 49, 50))
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if !result.Success || result.ResultItem != 90013 || result.NextRarity != 3 {
			t.Fatalf("result = %+v", result)
		}
		inventory := f.inventory(t)
		if inventory.Slots["38:49"].Count != 1 || inventory.Slots["38:50"].Count != 1 {
			t.Fatalf("materials were not consumed once each: %+v", inventory.Slots)
		}
		if got := inventory.Slots["38:51"]; got.ItemID != 90013 || got.Count != 1 {
			t.Fatalf("result stack = %+v", got)
		}
	})
}

func TestOwnerFourSameIDGemRejectsMissingSuccessorBeforeConsumption(t *testing.T) {
	f := newOwnerTestFixture(t, map[string]repo.ItemStack{
		"38:49": {ItemID: 90002, Count: 10},
	}, func(rules *ownerTestRules) {
		rules.pools[ownerTestPoolKey{kind: CompoundGem, rarity: 3}] = []uint32{90013}
	})

	result, err := f.owner.Apply(f.ctx, f.command("acct", "77", CompoundGem, 2, 4, 49, 49, 49, 49))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
	if !reflect.DeepEqual(result, Result{}) {
		t.Fatalf("result = %+v, want empty", result)
	}
	if got := f.inventory(t).Slots["38:49"]; got.Count != 10 {
		t.Fatalf("material count = %d, want 10", got.Count)
	}
	if len(f.rules.updateCalls) != 0 || len(f.rules.grantCalls) != 0 {
		t.Fatal("missing successor mutated assets")
	}
}

func TestOwnerRejectsMixedRarityAliasesAndMedalQuantityShortage(t *testing.T) {
	t.Run("mixed rarity", func(t *testing.T) {
		f := newOwnerTestFixture(t, map[string]repo.ItemStack{
			"38:49": {ItemID: 90002, Count: 5},
			"38:50": {ItemID: 90003, Count: 5},
		})
		if _, err := f.owner.Apply(f.ctx, f.command("acct", "77", CompoundGem, 2, 2, 49, 50)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("error = %v, want ErrInvalid", err)
		}
		inventory := f.inventory(t)
		if inventory.Slots["38:49"].Count != 5 || inventory.Slots["38:50"].Count != 5 {
			t.Fatal("mixed-rarity rejection changed materials")
		}
	})

	t.Run("cross-page alias", func(t *testing.T) {
		f := newOwnerTestFixture(t, map[string]repo.ItemStack{
			"38:0":  {ItemID: 100380017, Count: 5},
			"38:49": {ItemID: 90002, Count: 5},
		})
		if _, err := f.owner.Apply(f.ctx, f.command("acct", "77", CompoundGem, 2, 2, 0, 49)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("error = %v, want ErrInvalid", err)
		}
	})

	t.Run("medal duplicate slot needs two units", func(t *testing.T) {
		f := newOwnerTestFixture(t, map[string]repo.ItemStack{
			"38:0": {ItemID: 100380017, Count: 1},
		})
		if _, err := f.owner.Apply(f.ctx, f.command("acct", "77", CompoundFlag, 0, 2, 0, 0)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("error = %v, want ErrInvalid", err)
		}
		if got := f.inventory(t).Slots["38:0"]; got.Count != 1 {
			t.Fatalf("medal count = %d, want 1", got.Count)
		}
	})
	t.Run("malformed stacked medal", func(t *testing.T) {
		f := newOwnerTestFixture(t, map[string]repo.ItemStack{
			"38:0": {ItemID: 100380017, Count: 4},
		})
		if _, err := f.owner.Apply(f.ctx, f.command("acct", "77", CompoundFlag, 0, 4, 0, 0, 0, 0)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("error = %v, want ErrInvalid", err)
		}
		if got := f.inventory(t).Slots["38:0"].Count; got != 4 {
			t.Fatalf("malformed medal consumed: %d", got)
		}
	})
}

func TestOwnerMaterialLockAndExpiryBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*repo.ItemStack, *ownerTestRules)
		wantErr bool
	}{
		{
			name: "sort lock",
			mutate: func(stack *repo.ItemStack, _ *ownerTestRules) {
				stack.Extra = map[string]string{repo.SortLockExtraKey: "1"}
			},
			wantErr: true,
		},
		{
			name: "equipment pending unlock",
			mutate: func(stack *repo.ItemStack, _ *ownerTestRules) {
				stack.Extra = map[string]string{"equipment_lock_state": "pending_unlock"}
			},
			wantErr: true,
		},
		{
			name: "expiry equal to now",
			mutate: func(stack *repo.ItemStack, _ *ownerTestRules) {
				stack.ExpireAt = ownerTestNow
			},
			wantErr: true,
		},
		{
			name: "unlock deadline reached",
			mutate: func(stack *repo.ItemStack, _ *ownerTestRules) {
				stack.Extra = map[string]string{"equipment_lock_state": "pending_unlock", "equipment_unlock_at": strconv.FormatInt(ownerTestNow.Unix(), 10)}
			},
		},
		{
			name: "callback expiry",
			mutate: func(stack *repo.ItemStack, _ *ownerTestRules) {
				stack.ExpireAt = ownerTestNow.Add(time.Nanosecond)
				stack.Extra = map[string]string{"test_expired": "1"}
			},
			wantErr: true,
		},
		{
			name: "future expiry consumes and rebuilds raw",
			mutate: func(stack *repo.ItemStack, _ *ownerTestRules) {
				stack.ExpireAt = ownerTestNow.Add(time.Nanosecond)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stack := repo.ItemStack{ItemID: 90002, Count: 10, RawEntry: []byte{10}}
			test.mutate(&stack, nil)
			f := newOwnerTestFixture(t, map[string]repo.ItemStack{"38:49": stack})
			result, err := f.owner.Apply(f.ctx, f.command("acct", "77", CompoundGem, 2, 4, 49, 49, 49, 49))
			if test.wantErr {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("error = %v, want ErrInvalid", err)
				}
				if got := f.inventory(t).Slots["38:49"]; got.Count != 10 || !reflect.DeepEqual(got.RawEntry, []byte{10}) {
					t.Fatalf("rejected material changed: %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if !result.Success || result.ResultItem == 0 {
				t.Fatalf("result = %+v, want produced item", result)
			}
			got := f.inventory(t).Slots["38:49"]
			if got.Count != 6 || !reflect.DeepEqual(got.RawEntry, []byte{6}) {
				t.Fatalf("future-expiry material = %+v, want count 6 and rebuilt raw", got)
			}
			if len(f.rules.updateCalls) != 1 || f.rules.updateCalls[0].Count != 6 {
				t.Fatalf("update calls = %+v", f.rules.updateCalls)
			}
		})
	}
}

func TestOwnerGrantMustAddExactlyOneAndMedalResultIsCountOne(t *testing.T) {
	t.Run("gem grant adding two rolls back", func(t *testing.T) {
		f := newOwnerTestFixture(t, map[string]repo.ItemStack{
			"38:49": {ItemID: 90002, Count: 10},
		}, func(rules *ownerTestRules) {
			rules.grantDelta = 1
		})
		result, err := f.owner.Apply(f.ctx, f.command("acct", "77", CompoundGem, 2, 4, 49, 49, 49, 49))
		if !errors.Is(err, ErrInvalid) || !reflect.DeepEqual(result, Result{}) {
			t.Fatalf("result=%+v err=%v, want empty ErrInvalid", result, err)
		}
		if got := f.inventory(t).Slots["38:49"]; got.Count != 10 {
			t.Fatalf("material count = %d, want rollback to 10", got.Count)
		}
	})

	t.Run("medal result count one", func(t *testing.T) {
		f := newOwnerTestFixture(t, map[string]repo.ItemStack{
			"38:0": {ItemID: 100380017, Count: 1},
			"38:1": {ItemID: 100380018, Count: 1},
		}, func(rules *ownerTestRules) { rules.rolls = []uint32{1} })
		result, err := f.owner.Apply(f.ctx, f.command("acct", "77", CompoundFlag, 0, 2, 0, 1))
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if !result.Success || result.ResultItem != 100380032 || result.ResultSlot != 0 {
			t.Fatalf("result = %+v", result)
		}
		if got := f.inventory(t).Slots["38:0"]; got.ItemID != 100380032 || got.Count != 1 {
			t.Fatalf("medal result = %+v, want count 1", got)
		}
	})

	t.Run("medal grant count two rolls back", func(t *testing.T) {
		f := newOwnerTestFixture(t, map[string]repo.ItemStack{
			"38:0": {ItemID: 100380017, Count: 1},
			"38:1": {ItemID: 100380018, Count: 1},
		}, func(rules *ownerTestRules) {
			rules.rolls = []uint32{1}
			rules.grantDelta = 1
		})
		result, err := f.owner.Apply(f.ctx, f.command("acct", "77", CompoundFlag, 0, 2, 0, 1))
		if !errors.Is(err, ErrInvalid) || !reflect.DeepEqual(result, Result{}) {
			t.Fatalf("result=%+v err=%v, want empty ErrInvalid", result, err)
		}
		inventory := f.inventory(t)
		if inventory.Slots["38:0"].Count != 1 || inventory.Slots["38:1"].Count != 1 {
			t.Fatal("medal count violation did not roll back materials")
		}
	})
}

var errOwnerTestSave = errors.New("owner test save failure")

type ownerTestFailingSaveInventory struct {
	repo.InventoryRepository
}

func (ownerTestFailingSaveInventory) Save(context.Context, repo.InventoryRecord) error {
	return errOwnerTestSave
}

type ownerTestFailingSaveAssets struct {
	characters repo.CharacterRepository
	inventory  repo.InventoryRepository
}

func (a ownerTestFailingSaveAssets) WithinCharacterAssets(
	ctx context.Context,
	_ string,
	apply func(repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error,
) error {
	return apply(a.characters, ownerTestFailingSaveInventory{InventoryRepository: a.inventory}, nil)
}

func TestOwnerGrantNoSpaceSaveFailureOwnershipAndRollRange(t *testing.T) {
	t.Run("grant no space", func(t *testing.T) {
		f := newOwnerTestFixture(t, map[string]repo.ItemStack{
			"38:49": {ItemID: 90002, Count: 10},
		}, func(rules *ownerTestRules) {
			rules.grantErr = ErrNoSpace
		})
		result, err := f.owner.Apply(f.ctx, f.command("acct", "77", CompoundGem, 2, 4, 49, 49, 49, 49))
		if !errors.Is(err, ErrNoSpace) || !reflect.DeepEqual(result, Result{}) {
			t.Fatalf("result=%+v err=%v, want empty ErrNoSpace", result, err)
		}
		if got := f.inventory(t).Slots["38:49"]; got.Count != 10 {
			t.Fatalf("material count = %d, want rollback to 10", got.Count)
		}
	})

	t.Run("save failure", func(t *testing.T) {
		f := newOwnerTestFixture(t, map[string]repo.ItemStack{
			"38:49": {ItemID: 90002, Count: 10},
		})
		f.owner = &Owner{characterAssets: ownerTestFailingSaveAssets{
			characters: f.repos.Character,
			inventory:  f.repos.Inventory,
		}}
		result, err := f.owner.Apply(f.ctx, f.command("acct", "77", CompoundGem, 2, 4, 49, 49, 49, 49))
		if !errors.Is(err, errOwnerTestSave) || !reflect.DeepEqual(result, Result{}) {
			t.Fatalf("result=%+v err=%v, want empty save error", result, err)
		}
		if got := f.inventory(t).Slots["38:49"]; got.Count != 10 {
			t.Fatalf("material count = %d, want rollback to 10", got.Count)
		}
	})

	t.Run("character ownership", func(t *testing.T) {
		f := newOwnerTestFixture(t, map[string]repo.ItemStack{
			"38:49": {ItemID: 90002, Count: 10},
		})
		if _, err := f.owner.Apply(f.ctx, f.command("other", "77", CompoundGem, 2, 4, 49, 49, 49, 49)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("error = %v, want ErrInvalid", err)
		}
	})

	t.Run("selection roll out of range", func(t *testing.T) {
		f := newOwnerTestFixture(t, map[string]repo.ItemStack{
			"38:49": {ItemID: 90002, Count: 10},
		}, func(rules *ownerTestRules) { rules.rolls = []uint32{2} })
		result, err := f.owner.Apply(f.ctx, f.command("acct", "77", CompoundGem, 2, 2, 49, 49))
		if !errors.Is(err, ErrInvalid) || !reflect.DeepEqual(result, Result{}) {
			t.Fatalf("result=%+v err=%v, want empty ErrInvalid", result, err)
		}
		if got := f.inventory(t).Slots["38:49"]; got.Count != 10 {
			t.Fatalf("material count = %d, want 10", got.Count)
		}
	})
}

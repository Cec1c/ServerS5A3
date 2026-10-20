package inventory

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	guild "longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func cargoFixture(t *testing.T) (r.Group, *Owner, GuildCargoRequest, GuildCargoRules, time.Time) {
	t.Helper()
	repos, o, itemReq, now := mileageItemFixture(t, 10)
	ctx := context.Background()
	registry, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	g, _, _ := registry.GuildForCharacter("1")
	// A deliberately explicit fixture capacity, not a claim about live defaults.
	g.Cargo = &guild.Cargo{Capacity: 3}
	if err = guild.Save(ctx, repos.Settings, registry, now); err != nil {
		t.Fatal(err)
	}
	inv, _, _ := repos.Inventory.Load(ctx, "1")
	stack := inv.Slots["0:87"]
	stack.Extra["stack_limit"] = "1000"
	inv.Slots["0:87"] = stack
	if err = repos.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	req := GuildCargoRequest{Owner: itemReq.Owner, GuildID: g.ID, Source: GuildCargoLocation{List: 0, Slot: 87}, Destination: GuildCargoLocation{Cargo: true, Slot: 0}, SourceItemID: uint32(itemReq.Definition.ItemID), Count: 3, Revalidate: func() bool { return true }}
	rules := GuildCargoRules{MaxCapacity: 56, Validate: func(_ r.CharacterRecord, _ GuildCargoRequest, source, dest r.ItemStack) error {
		if source.ItemID != itemReq.Definition.ItemID || source.Extra["pvf_path"] != itemReq.Definition.PVFPath {
			return ErrGuildCargoRequest
		}
		return nil
	}}
	return repos, o, req, rules, now
}

func readCargo(t *testing.T, repos r.Group, id uint32) (*guild.Guild, r.InventoryRecord) {
	t.Helper()
	ctx := context.Background()
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	inv, found, err := repos.Inventory.Load(ctx, "1")
	if err != nil || !found {
		t.Fatalf("inventory: %v %v", found, err)
	}
	return reg.Guilds[id], inv
}

func TestGuildCargoDepositWithdrawPreservesInstancesAndOtherOwners(t *testing.T) {
	repos, o, req, rules, now := cargoFixture(t)
	ctx := context.Background()
	beforeChar, _, _ := repos.Character.Load(ctx, "1")
	g, before := readCargo(t, repos, req.GuildID)
	mileage := g.Mileage
	got, err := o.TransferGuildCargo(ctx, req, rules, now)
	if err != nil || got.Moved != 3 || got.Remaining != 7 {
		t.Fatalf("deposit %+v %v", got, err)
	}
	g, inv := readCargo(t, repos, req.GuildID)
	if inv.Slots["0:87"].Count != 7 || g.Cargo.Slots[0].Count != 3 {
		t.Fatal("split not durable")
	}
	if binary.LittleEndian.Uint32(g.Cargo.Slots[0].RawEntry[6:10]) != 3 || inv.Slots["0:87"].Extra["count"] != "7" {
		t.Fatal("count mirrors stale")
	}
	if g.Cargo.Slots[0].Extra["marker"] != "keep" {
		t.Fatal("instance metadata lost")
	}
	if len(g.Cargo.History) != 1 || g.Cargo.History[0].Kind != guild.CargoHistoryDeposit || g.Cargo.History[0].Item.Count != 3 || g.Cargo.History[0].CharacterName != beforeChar.Name {
		t.Fatal("deposit history does not match committed transfer")
	}
	withdraw := req
	withdraw.Source = req.Destination
	withdraw.Destination = GuildCargoLocation{List: 0, Slot: 87}
	withdraw.DestinationItemID = req.SourceItemID
	withdraw.Count = 2
	if _, err = o.TransferGuildCargo(ctx, withdraw, rules, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	g, inv = readCargo(t, repos, req.GuildID)
	if inv.Slots["0:87"].Count != 9 || g.Cargo.Slots[0].Count != 1 || g.Mileage != mileage {
		t.Fatal("withdraw changed wrong balances")
	}
	if len(g.Cargo.History) != 2 || g.Cargo.History[1].Kind != guild.CargoHistoryWithdraw || g.Cargo.History[1].Item.Count != 2 || g.Cargo.History[0].Item.Count != 3 || g.Cargo.History[0].Item.Extra["count"] != "3" {
		t.Fatal("withdrawal/history snapshot mismatch")
	}
	if !reflect.DeepEqual(before.Warehouse, inv.Warehouse) || !reflect.DeepEqual(before.Slots["0:90"], inv.Slots["0:90"]) {
		t.Fatal("unrelated items changed")
	}
	afterChar, _, _ := repos.Character.Load(ctx, "1")
	if !reflect.DeepEqual(beforeChar, afterChar) {
		t.Fatal("character changed")
	}
}

func TestGuildCargoConcurrentWholeStackTransferHasOneWinner(t *testing.T) {
	repos, o, req, rules, now := cargoFixture(t)
	req.Count = 10
	errorsCh := make(chan error, 16)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := o.TransferGuildCargo(context.Background(), req, rules, now)
			errorsCh <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errorsCh)
	success := 0
	for err := range errorsCh {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrGuildCargoStale) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("successes=%d", success)
	}
	g, inv := readCargo(t, repos, req.GuildID)
	if len(g.Cargo.Slots) != 1 || g.Cargo.Slots[0].Count != 10 || inv.Slots["0:87"].Count != 0 {
		t.Fatal("duplicated or lost assets")
	}
	if len(g.Cargo.History) != 1 || g.Cargo.History[0].Item.Count != 10 {
		t.Fatal("failed attempts wrote history")
	}
	if _, err := o.TransferGuildCargo(context.Background(), req, rules, now); !errors.Is(err, ErrGuildCargoStale) {
		t.Fatalf("replay=%v", err)
	}
}

func TestGuildCargoSwapAndEmptyMovePreserveWholeInstances(t *testing.T) {
	repos, o, req, rules, now := cargoFixture(t)
	ctx := context.Background()
	if _, err := o.TransferGuildCargo(ctx, req, rules, now); err != nil {
		t.Fatal(err)
	}
	reg, _ := guild.Load(ctx, repos.Settings)
	g := reg.Guilds[req.GuildID]
	g.Cargo.Slots[1] = r.ItemStack{ItemID: 2, Count: 1, Bind: true, RawEntry: []byte{1, 2, 3}, Extra: map[string]string{"marker": "other"}}
	if err := guild.Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	first, second := cloneStack(g.Cargo.Slots[0]), cloneStack(g.Cargo.Slots[1])
	req.Source = GuildCargoLocation{Cargo: true, Slot: 0}
	req.Destination = GuildCargoLocation{Cargo: true, Slot: 1}
	req.DestinationItemID = 2
	req.Count = 0
	if _, err := o.TransferGuildCargo(ctx, req, rules, now); err != nil {
		t.Fatal(err)
	}
	g, _ = readCargo(t, repos, req.GuildID)
	if !reflect.DeepEqual(g.Cargo.Slots[0], second) || !reflect.DeepEqual(g.Cargo.Slots[1], first) {
		t.Fatal("swap corrupted instances")
	}
	req.Source.Slot = 1
	req.Destination.Slot = 2
	req.DestinationItemID = 0
	if _, err := o.TransferGuildCargo(ctx, req, rules, now); err != nil {
		t.Fatal(err)
	}
	g, _ = readCargo(t, repos, req.GuildID)
	if _, exists := g.Cargo.Slots[1]; exists || !reflect.DeepEqual(g.Cargo.Slots[2], first) {
		t.Fatal("empty move corrupted instances")
	}
	if len(g.Cargo.History) != 1 {
		t.Fatal("internal moves invented deposit/withdraw rows")
	}
}

func TestGuildCargoRefusesInvalidStateWithoutPartialWrites(t *testing.T) {
	for _, name := range []string{"capacity_unknown", "slot_outside", "capacity_above_rule", "stale_item", "wrong_guild", "stale_account", "permission_revoked", "locked", "too_many", "zero_count", "resolver_reject", "no_resolver", "generation_changed", "account_slot", "no_character"} {
		t.Run(name, func(t *testing.T) {
			repos, o, req, rules, now := cargoFixture(t)
			ctx := context.Background()
			reg, _ := guild.Load(ctx, repos.Settings)
			g := reg.Guilds[req.GuildID]
			inv, _, _ := repos.Inventory.Load(ctx, "1")
			switch name {
			case "capacity_unknown":
				g.Cargo = nil
			case "slot_outside":
				req.Destination.Slot = 3
			case "capacity_above_rule":
				rules.MaxCapacity = 2
			case "stale_item":
				req.SourceItemID++
			case "wrong_guild":
				req.GuildID++
			case "stale_account":
				m := g.Members["1"]
				m.AccountID = "old-account"
				g.Members["1"] = m
			case "permission_revoked":
				g.GradePermissions = map[byte]uint32{1: 0}
			case "locked":
				s := inv.Slots["0:87"]
				s.Extra["equipment_lock_state"] = "1"
				inv.Slots["0:87"] = s
			case "too_many":
				req.Count = 11
			case "zero_count":
				req.Count = 0
			case "resolver_reject":
				rules.Validate = func(r.CharacterRecord, GuildCargoRequest, r.ItemStack, r.ItemStack) error {
					return errors.New("native restriction")
				}
			case "no_resolver":
				rules.Validate = nil
			case "generation_changed":
				calls := 0
				req.Revalidate = func() bool { calls++; return calls < 2 }
			case "account_slot":
				req.Source.Slot = uint16(r.CrystalWarehouseFirstSlot)
				inv.Slots[slotKey(req.Source.List, int16(req.Source.Slot))] = cloneStack(inv.Slots["0:87"])
			case "no_character":
				req.Owner.AccountID = "wrong-account"
			}
			if err := guild.Save(ctx, repos.Settings, reg, now); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(ctx, inv); err != nil {
				t.Fatal(err)
			}
			before, _ := guild.Load(ctx, repos.Settings)
			beforeInv, _, _ := repos.Inventory.Load(ctx, "1")
			got, err := o.TransferGuildCargo(ctx, req, rules, now.Add(time.Second))
			if err == nil || got != (GuildCargoResult{}) {
				t.Fatalf("result=%+v err=%v", got, err)
			}
			after, _ := guild.Load(ctx, repos.Settings)
			afterInv, _, _ := repos.Inventory.Load(ctx, "1")
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeInv, afterInv) {
				t.Fatal("rejection mutated assets")
			}
		})
	}
}

type failingCargoTx struct {
	r.GuildUnitOfWork
	inventory r.GuildInventoryUnitOfWork
	fail      error
}

func (u failingCargoTx) WithinGuildInventory(ctx context.Context, owner r.GuildOwner, apply func(r.Group) error) error {
	return u.inventory.WithinGuildInventory(ctx, owner, func(tx r.Group) error {
		if err := apply(tx); err != nil {
			return err
		}
		return u.fail
	})
}

func TestGuildCargoLateFailureRollsBackInventoryAndRegistry(t *testing.T) {
	repos, o, req, rules, now := cargoFixture(t)
	ctx := context.Background()
	boom := errors.New("late failure")
	o.guild = failingCargoTx{repos.Guild, repos.Guild.(r.GuildInventoryUnitOfWork), boom}
	before, inv := readCargo(t, repos, req.GuildID)
	got, err := o.TransferGuildCargo(ctx, req, rules, now.Add(time.Second))
	if !errors.Is(err, boom) || got != (GuildCargoResult{}) {
		t.Fatalf("%+v %v", got, err)
	}
	after, afterInv := readCargo(t, repos, req.GuildID)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(inv, afterInv) {
		t.Fatal("rollback lost assets")
	}
}

func TestGuildCargoResolverReceivesDetachedInstances(t *testing.T) {
	repos, o, req, rules, now := cargoFixture(t)
	rules.Validate = func(ch r.CharacterRecord, _ GuildCargoRequest, source, _ r.ItemStack) error {
		ch.Stats["gold"] = 0
		source.Extra["marker"] = "mutated"
		source.RawEntry[0] = 255
		return nil
	}
	if _, err := o.TransferGuildCargo(context.Background(), req, rules, now); err != nil {
		t.Fatal(err)
	}
	g, inv := readCargo(t, repos, req.GuildID)
	if g.Cargo.Slots[0].Extra["marker"] != "keep" || inv.Slots["0:87"].RawEntry[0] == 255 {
		t.Fatal("resolver altered stored instance")
	}
	ch, _, _ := repos.Character.Load(context.Background(), "1")
	if ch.Stats["gold"] != 12345 {
		t.Fatal("resolver altered character")
	}
}

func TestGuildCargoConcurrentMembersCannotOverdrawSharedInventory(t *testing.T) {
	repos, o, req, rules, now := cargoFixture(t)
	ctx := context.Background()
	req.Count = 10
	if _, err := o.TransferGuildCargo(ctx, req, rules, now); err != nil {
		t.Fatal(err)
	}
	ch := r.CharacterRecord{CharacterID: "2", AccountID: "a2", Name: "会员", Level: 90}
	if err := repos.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "2"}); err != nil {
		t.Fatal(err)
	}
	reg, _ := guild.Load(ctx, repos.Settings)
	g := reg.Guilds[req.GuildID]
	g.Members["2"] = guild.Member{CharacterID: "2", AccountID: "a2", Name: "会员", Grade: 4}
	g.GradePermissions = map[byte]uint32{4: guild.CargoPermission}
	if err := guild.Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 16)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		current := req
		id := fmt.Sprint(i%2 + 1)
		current.Owner = r.GuildOwner{CharacterID: id, AccountID: "a" + id}
		current.Source = GuildCargoLocation{Cargo: true, Slot: 0}
		current.Destination = GuildCargoLocation{List: 0, Slot: uint16(100 + i)}
		current.Count = 1
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := o.TransferGuildCargo(ctx, current, rules, now)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrGuildCargoStale) {
			t.Fatal(err)
		}
	}
	reg, _ = guild.Load(ctx, repos.Settings)
	total := reg.Guilds[req.GuildID].Cargo.Slots[0].Count
	for _, id := range []string{"1", "2"} {
		inv, _, _ := repos.Inventory.Load(ctx, id)
		for _, stack := range inv.Slots {
			if stack.ItemID == int64(req.SourceItemID) {
				total += stack.Count
			}
		}
	}
	if success != 10 || total != 10 {
		t.Fatalf("success=%d total=%d", success, total)
	}
	logs := reg.Guilds[req.GuildID].Cargo.History
	var withdrawn int64
	for _, e := range logs {
		if e.Kind == guild.CargoHistoryWithdraw {
			withdrawn += e.Item.Count
		}
	}
	if len(logs) != 11 || withdrawn != 10 {
		t.Fatalf("logs=%d withdrawn=%d", len(logs), withdrawn)
	}
}

func TestGuildCargoHistoryInvalidMetadataRollsBackTransfer(t *testing.T) {
	for _, name := range []string{"blank", "nul", "bad_time"} {
		t.Run(name, func(t *testing.T) {
			repos, o, req, rules, now := cargoFixture(t)
			ctx := context.Background()
			ch, _, _ := repos.Character.Load(ctx, "1")
			switch name {
			case "blank":
				ch.Name = " "
			case "nul":
				ch.Name = "名\x00称"
			case "bad_time":
				now = time.Time{}
			}
			if err := repos.Character.Save(ctx, ch); err != nil {
				t.Fatal(err)
			}
			before, inv := readCargo(t, repos, req.GuildID)
			got, err := o.TransferGuildCargo(ctx, req, rules, now)
			if !errors.Is(err, guild.ErrCargoHistory) || got != (GuildCargoResult{}) {
				t.Fatalf("%+v %v", got, err)
			}
			after, afterInv := readCargo(t, repos, req.GuildID)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(inv, afterInv) {
				t.Fatal("failed log committed transfer")
			}
		})
	}
}

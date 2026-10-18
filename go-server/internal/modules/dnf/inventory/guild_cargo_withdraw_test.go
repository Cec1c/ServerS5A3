package inventory

import (
	"context"
	"errors"
	"math"
	"reflect"
	"sync"
	"testing"
	"time"

	guild "longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func cargoWithdrawalFixture(t *testing.T) (r.Group, *Owner, GuildCargoWithdrawalRequest, GuildCargoRules, time.Time) {
	t.Helper()
	repos, o, deposit, rules, now := cargoFixture(t)
	deposit.Count = 10
	if _, err := o.TransferGuildCargo(context.Background(), deposit, rules, now); err != nil {
		t.Fatal(err)
	}
	rules.WithdrawalRange = func(r.CharacterRecord, r.ItemStack) (GuildCargoSlotRange, error) {
		return GuildCargoSlotRange{List: 0, First: 87, Last: 89}, nil
	}
	req := GuildCargoWithdrawalRequest{Owner: deposit.Owner, GuildID: deposit.GuildID, CargoSlot: 0,
		ItemID: deposit.SourceItemID, Count: 3, DestinationList: 0, Revalidate: deposit.Revalidate}
	return repos, o, req, rules, now.Add(time.Second)
}

func TestGuildCargoWithdrawalChoosesOneWholeDestination(t *testing.T) {
	for _, name := range []string{"lowest_merge", "merge_before_empty", "full_stack", "identity", "expiry", "locked", "malformed", "first_empty", "list1", "list7", "equipment"} {
		t.Run(name, func(t *testing.T) {
			repos, o, req, rules, now := cargoWithdrawalFixture(t)
			ctx := context.Background()
			reg, _ := guild.Load(ctx, repos.Settings)
			cargo := reg.Guilds[req.GuildID].Cargo
			inv, _, _ := repos.Inventory.Load(ctx, "1")
			source := cloneStack(cargo.Slots[0])
			source.Extra["stack_limit"] = "10"
			cargo.Slots[0] = source
			stack := cloneStack(source)
			stack.Count = 2
			inv.Slots["0:87"], inv.Slots["0:88"] = cloneStack(stack), cloneStack(stack)
			wantSlot, wantCount, wantList := uint16(87), int64(5), byte(0)
			switch name {
			case "merge_before_empty":
				delete(inv.Slots, "0:87")
				wantSlot = 88
			case "full_stack":
				stack.Count = 8 // Two spare slots cannot hold a quantity of three.
				inv.Slots["0:87"] = stack
				wantSlot = 88
			case "identity":
				stack.Bind = !source.Bind
				inv.Slots["0:87"] = stack
				wantSlot = 88
			case "expiry":
				stack.ExpireAt = now.Add(time.Hour)
				inv.Slots["0:87"] = stack
				wantSlot = 88
			case "locked":
				stack.Extra["equipment_lock_state"] = "1"
				inv.Slots["0:87"] = stack
				wantSlot = 88
			case "malformed":
				stack.Count = -1
				inv.Slots["0:87"] = stack
				wantSlot = 88
			case "first_empty":
				delete(inv.Slots, "0:87")
				delete(inv.Slots, "0:88")
				wantCount = 3
			case "list1", "list7":
				req.DestinationList = 1
				if name == "list7" {
					req.DestinationList = 7
				}
				rules.WithdrawalRange = func(r.CharacterRecord, r.ItemStack) (GuildCargoSlotRange, error) {
					return GuildCargoSlotRange{List: req.DestinationList, First: 0, Last: 2}, nil
				}
				wantSlot, wantCount, wantList = 0, 3, req.DestinationList
			case "equipment":
				cargo.Slots[0] = r.ItemStack{ItemID: int64(req.ItemID), Count: 1,
					RawEntry: append([]byte(nil), source.RawEntry...), Extra: map[string]string{"item_kind": "equipment", "pvf_path": "equipment/example.equ"}}
				delete(inv.Slots, "0:88")
				rules.Validate = func(r.CharacterRecord, GuildCargoRequest, r.ItemStack, r.ItemStack) error { return nil }
				req.Count = 1
				wantSlot, wantCount = 88, 1
			}
			if err := guild.Save(ctx, repos.Settings, reg, now); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(ctx, inv); err != nil {
				t.Fatal(err)
			}
			beforeSource := cloneStack(cargo.Slots[0])
			beforeInv := r.CloneInventory(inv)
			var validated GuildCargoRequest
			validate := rules.Validate
			rules.Validate = func(ch r.CharacterRecord, resolved GuildCargoRequest, src, dst r.ItemStack) error {
				validated = resolved
				return validate(ch, resolved, src, dst)
			}
			got, err := o.WithdrawGuildCargo(ctx, req, rules, now)
			want := GuildCargoLocation{List: wantList, Slot: wantSlot}
			if err != nil || got.Destination != want || got.Moved != req.Count || got.Remaining != uint32(beforeSource.Count)-req.Count {
				t.Fatalf("withdraw=%+v err=%v want destination=%+v", got, err, want)
			}
			key := slotKey(wantList, int16(wantSlot))
			if validated.Destination != want || validated.DestinationItemID != uint32(beforeInv.Slots[key].ItemID) {
				t.Fatalf("resolver did not receive actual locked destination: %+v", validated)
			}
			g, after := readCargo(t, repos, req.GuildID)
			if after.Slots[key].Count != wantCount || g.Cargo.Slots[0].Count != beforeSource.Count-int64(req.Count) {
				t.Fatal("wrong persisted quantity")
			}
			last := g.Cargo.History[len(g.Cargo.History)-1]
			if len(g.Cargo.History) != 2 || last.Kind != guild.CargoHistoryWithdraw || last.Item.Count != int64(req.Count) {
				t.Fatal("history does not describe one complete withdrawal")
			}
			delete(beforeInv.Slots, key)
			delete(after.Slots, key)
			if !reflect.DeepEqual(beforeInv.Slots, after.Slots) || !reflect.DeepEqual(beforeInv.Warehouse, after.Warehouse) {
				t.Fatal("allocation touched an unrelated slot")
			}
		})
	}
}

func TestGuildCargoWithdrawalRejectsWithoutChangingAssets(t *testing.T) {
	for _, name := range []string{"no_resolver", "resolver_error", "wrong_list", "unsupported_list", "backwards", "signed_overflow", "gold", "account_slot", "range_crosses_account", "single_slot_full", "split_would_fit", "source_locked", "source_stale", "too_many", "invalid_equipment_count", "permission_revoked", "wrong_owner", "late_session", "late_failure"} {
		t.Run(name, func(t *testing.T) {
			repos, o, req, rules, now := cargoWithdrawalFixture(t)
			ctx := context.Background()
			reg, _ := guild.Load(ctx, repos.Settings)
			g := reg.Guilds[req.GuildID]
			inv, _, _ := repos.Inventory.Load(ctx, "1")
			allowed := GuildCargoSlotRange{List: 0, First: 87, Last: 89}
			rules.WithdrawalRange = func(r.CharacterRecord, r.ItemStack) (GuildCargoSlotRange, error) { return allowed, nil }
			wantErr := ErrGuildCargoRequest
			boom := errors.New("injected failure")
			switch name {
			case "no_resolver":
				rules.WithdrawalRange = nil
			case "resolver_error":
				rules.WithdrawalRange = func(r.CharacterRecord, r.ItemStack) (GuildCargoSlotRange, error) { return allowed, boom }
				wantErr = boom
			case "wrong_list":
				allowed.List = 1
			case "unsupported_list":
				req.DestinationList, allowed.List = 9, 9
			case "backwards":
				allowed.First, allowed.Last = 89, 87
			case "signed_overflow":
				allowed.Last = math.MaxInt16 + 1
			case "gold":
				allowed.First, allowed.Last = 0, 0
			case "account_slot":
				allowed.First, allowed.Last = uint16(r.CrystalWarehouseFirstSlot), uint16(r.CrystalWarehouseFirstSlot)
			case "range_crosses_account":
				allowed.First, allowed.Last = uint16(r.CrystalWarehouseFirstSlot)-1, uint16(r.SoulWarehouseLastSlot)+1
			case "single_slot_full", "split_would_fit":
				stack := cloneStack(g.Cargo.Slots[0])
				stack.Extra["stack_limit"] = "10"
				g.Cargo.Slots[0] = cloneStack(stack)
				stack.Count = 8
				inv.Slots["0:87"], inv.Slots["0:88"] = cloneStack(stack), cloneStack(stack)
				allowed.Last = 87
				if name == "split_would_fit" {
					allowed.Last = 88
				}
				wantErr = ErrGuildCargoInventoryFull
			case "source_locked":
				stack := g.Cargo.Slots[0]
				stack.Extra["equipment_lock_state"] = "1"
				g.Cargo.Slots[0] = stack
				wantErr = ErrItemLocked
			case "source_stale":
				req.ItemID++
				wantErr = ErrGuildCargoStale
			case "too_many":
				req.Count = 11
			case "invalid_equipment_count":
				g.Cargo.Slots[0] = r.ItemStack{ItemID: int64(req.ItemID), Count: 10, Extra: map[string]string{"item_kind": "equipment", "pvf_path": "equipment/example.equ"}}
			case "permission_revoked":
				g.GradePermissions = map[byte]uint32{1: 0}
				wantErr = guild.ErrPermissionDenied
			case "wrong_owner":
				req.Owner.AccountID = "other"
				wantErr = r.ErrGuildTransaction
			case "late_session":
				calls := 0
				req.Revalidate = func() bool { calls++; return calls < 2 }
				wantErr = guild.ErrPermissionDenied
			case "late_failure":
				o.guild = failingCargoTx{repos.Guild, repos.Guild.(r.GuildInventoryUnitOfWork), boom}
				wantErr = boom
			}
			if err := guild.Save(ctx, repos.Settings, reg, now); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(ctx, inv); err != nil {
				t.Fatal(err)
			}
			before, beforeInv := readCargo(t, repos, req.GuildID)
			got, err := o.WithdrawGuildCargo(ctx, req, rules, now.Add(time.Second))
			if !errors.Is(err, wantErr) || got != (GuildCargoResult{}) {
				t.Fatalf("result=%+v err=%v want=%v", got, err, wantErr)
			}
			after, afterInv := readCargo(t, repos, req.GuildID)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeInv, afterInv) {
				t.Fatal("rejected withdrawal changed inventory/history")
			}
		})
	}
}

func TestGuildCargoWithdrawalRangeReceivesDetachedState(t *testing.T) {
	repos, o, req, rules, now := cargoWithdrawalFixture(t)
	rules.WithdrawalRange = func(ch r.CharacterRecord, source r.ItemStack) (GuildCargoSlotRange, error) {
		ch.Stats["gold"] = 0
		source.RawEntry[0] = 255
		source.Extra["marker"] = "mutated"
		return GuildCargoSlotRange{List: 0, First: 87, Last: 89}, nil
	}
	if _, err := o.WithdrawGuildCargo(context.Background(), req, rules, now); err != nil {
		t.Fatal(err)
	}
	g, inv := readCargo(t, repos, req.GuildID)
	ch, _, _ := repos.Character.Load(context.Background(), "1")
	if ch.Stats["gold"] != 12345 || g.Cargo.Slots[0].Extra["marker"] != "keep" || inv.Slots["0:87"].Extra["marker"] != "keep" || inv.Slots["0:87"].RawEntry[0] == 255 {
		t.Fatal("range resolver mutated authoritative state")
	}
}

func TestGuildCargoWithdrawalConcurrentAllocationUsesLockedInventory(t *testing.T) {
	repos, o, req, rules, now := cargoWithdrawalFixture(t)
	ctx := context.Background()
	reg, _ := guild.Load(ctx, repos.Settings)
	source := reg.Guilds[req.GuildID].Cargo.Slots[0]
	source.Extra["stack_limit"] = "10"
	reg.Guilds[req.GuildID].Cargo.Slots[0] = source
	if err := guild.Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	inv, _, _ := repos.Inventory.Load(ctx, "1")
	source.Count = 5
	inv.Slots["0:87"], inv.Slots["0:88"] = cloneStack(source), cloneStack(source)
	if err := repos.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	req.Count = 5
	rules.WithdrawalRange = func(r.CharacterRecord, r.ItemStack) (GuildCargoSlotRange, error) {
		return GuildCargoSlotRange{List: 0, First: 87, Last: 88}, nil
	}
	start := make(chan struct{})
	type outcome struct {
		result GuildCargoResult
		err    error
	}
	results := make(chan outcome, 8)
	var wg sync.WaitGroup
	for i := 0; i < cap(results); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got, err := o.WithdrawGuildCargo(ctx, req, rules, now)
			results <- outcome{got, err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	destinations := map[uint16]bool{}
	for got := range results {
		if got.err == nil {
			if destinations[got.result.Destination.Slot] || got.result.Moved != 5 {
				t.Fatalf("allocated same full slot: %+v", got.result)
			}
			destinations[got.result.Destination.Slot] = true
		} else if !errors.Is(got.err, ErrGuildCargoStale) {
			t.Fatal(got.err)
		}
	}
	g, inv := readCargo(t, repos, req.GuildID)
	if len(destinations) != 2 || !destinations[87] || !destinations[88] || len(g.Cargo.Slots) != 0 || inv.Slots["0:87"].Count != 10 || inv.Slots["0:88"].Count != 10 || len(g.Cargo.History) != 3 {
		t.Fatal("concurrent allocation lost or duplicated items/history")
	}
}

func TestGuildCargoWithdrawalConcurrentEmptySlotsKeepBothInstances(t *testing.T) {
	repos, o, req, rules, now := cargoWithdrawalFixture(t)
	ctx := context.Background()
	reg, _ := guild.Load(ctx, repos.Settings)
	first := r.ItemStack{ItemID: 100, Count: 1, RawEntry: []byte{1, 2, 3}, Extra: map[string]string{"item_kind": "equipment", "pvf_path": "equipment/one.equ"}}
	second := r.ItemStack{ItemID: 200, Count: 1, RawEntry: []byte{4, 5, 6}, Extra: map[string]string{"item_kind": "equipment", "pvf_path": "equipment/two.equ"}}
	reg.Guilds[req.GuildID].Cargo.Slots = map[uint16]r.ItemStack{0: first, 1: second}
	if err := guild.Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	rules.Validate = func(r.CharacterRecord, GuildCargoRequest, r.ItemStack, r.ItemStack) error { return nil }
	rules.WithdrawalRange = func(r.CharacterRecord, r.ItemStack) (GuildCargoSlotRange, error) {
		return GuildCargoSlotRange{List: 0, First: 87, Last: 88}, nil
	}
	req.Count = 1
	start := make(chan struct{})
	type outcome struct {
		result GuildCargoResult
		item   uint32
		err    error
	}
	results := make(chan outcome, 16)
	var wg sync.WaitGroup
	for i := 0; i < cap(results); i++ {
		current := req
		current.CargoSlot = uint16(i % 2)
		current.ItemID = uint32(100 * (i%2 + 1))
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got, err := o.WithdrawGuildCargo(ctx, current, rules, now)
			results <- outcome{got, current.ItemID, err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	g, inv := readCargo(t, repos, req.GuildID)
	destinations := map[uint16]bool{}
	for got := range results {
		if got.err == nil {
			want := first
			if got.item == 200 {
				want = second
			}
			if destinations[got.result.Destination.Slot] || !reflect.DeepEqual(inv.Slots[slotKey(0, int16(got.result.Destination.Slot))], want) {
				t.Fatal("one allocation replaced another instance")
			}
			destinations[got.result.Destination.Slot] = true
		} else if !errors.Is(got.err, ErrGuildCargoStale) {
			t.Fatal(got.err)
		}
	}
	if len(destinations) != 2 || !destinations[87] || !destinations[88] || len(g.Cargo.Slots) != 0 || len(g.Cargo.History) != 3 {
		t.Fatal("empty-slot allocation lost or duplicated instances/history")
	}
}

func TestGuildCargoEmptyDestinationEnforcesStackLimit(t *testing.T) {
	for _, direction := range []string{"deposit", "resolved_withdraw", "allocated_withdraw"} {
		t.Run(direction, func(t *testing.T) {
			repos, o, req, rules, now := cargoFixture(t)
			ctx := context.Background()
			if direction != "deposit" {
				req.Count = 10
				if _, err := o.TransferGuildCargo(ctx, req, rules, now); err != nil {
					t.Fatal(err)
				}
				req.Source, req.Destination = req.Destination, req.Source
			}
			reg, _ := guild.Load(ctx, repos.Settings)
			inv, _, _ := repos.Inventory.Load(ctx, "1")
			source := guildCargoStack(inv, reg.Guilds[req.GuildID].Cargo, req.Source)
			source.Extra["stack_limit"] = "2"
			putGuildCargoStack(&inv, reg.Guilds[req.GuildID].Cargo, req.Source, source)
			if err := guild.Save(ctx, repos.Settings, reg, now); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(ctx, inv); err != nil {
				t.Fatal(err)
			}
			req.Count = 3
			before, beforeInv := readCargo(t, repos, req.GuildID)
			var got GuildCargoResult
			var err error
			if direction == "allocated_withdraw" {
				rules.WithdrawalRange = func(r.CharacterRecord, r.ItemStack) (GuildCargoSlotRange, error) {
					return GuildCargoSlotRange{List: 0, First: 87, Last: 89}, nil
				}
				got, err = o.WithdrawGuildCargo(ctx, GuildCargoWithdrawalRequest{Owner: req.Owner, GuildID: req.GuildID, CargoSlot: 0, ItemID: req.SourceItemID, Count: req.Count, Revalidate: req.Revalidate}, rules, now)
			} else {
				got, err = o.TransferGuildCargo(ctx, req, rules, now)
			}
			if !errors.Is(err, ErrGuildCargoRequest) || got != (GuildCargoResult{}) {
				t.Fatalf("oversized transfer accepted: %+v %v", got, err)
			}
			after, afterInv := readCargo(t, repos, req.GuildID)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeInv, afterInv) {
				t.Fatal("oversized transfer changed assets")
			}
		})
	}
}

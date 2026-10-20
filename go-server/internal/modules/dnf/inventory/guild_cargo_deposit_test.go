package inventory

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	guild "longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func cargoDepositRequest(req GuildCargoRequest) GuildCargoDepositRequest {
	return GuildCargoDepositRequest{Owner: req.Owner, GuildID: req.GuildID, SourceList: req.Source.List,
		SourceSlot: req.Source.Slot, ItemID: req.SourceItemID, Count: req.Count, CargoSlot: req.Destination.Slot, Revalidate: req.Revalidate}
}

func TestGuildCargoDepositResolvesConcurrentDestinationIdentityUnderLock(t *testing.T) {
	repos, o, transfer, rules, now := cargoFixture(t)
	ctx := context.Background()
	inv, _, _ := repos.Inventory.Load(ctx, "1")
	stack := cloneStack(inv.Slots["0:87"])
	stack.Count = 5
	updateStackRawAmount(&stack)
	inv.Slots["0:87"], inv.Slots["0:88"] = cloneStack(stack), cloneStack(stack)
	if err := repos.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	var seen []uint32
	validate := rules.Validate
	rules.Validate = func(ch r.CharacterRecord, req GuildCargoRequest, source, destination r.ItemStack) error {
		if req.DestinationItemID != uint32(destination.ItemID) {
			return ErrGuildCargoStale
		}
		seen = append(seen, req.DestinationItemID) // Serialized by the Guild UoW.
		destination.Extra = map[string]string{"marker": "mutated"}
		return validate(ch, req, source, destination)
	}
	start := make(chan struct{})
	type outcome struct {
		result GuildCargoResult
		err    error
	}
	results := make(chan outcome, 16)
	var wg sync.WaitGroup
	for i := 0; i < cap(results); i++ {
		req := cargoDepositRequest(transfer)
		req.SourceSlot, req.Count = uint16(87+i%2), 5
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got, err := o.DepositGuildCargo(ctx, req, rules, now)
			results <- outcome{got, err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	success := 0
	for got := range results {
		if got.err == nil {
			success++
			if got.result.Destination != transfer.Destination || got.result.Moved != 5 || got.result.Remaining != 0 {
				t.Fatalf("wrong result %+v", got.result)
			}
		} else if !errors.Is(got.err, ErrGuildCargoStale) {
			t.Fatal(got.err)
		}
	}
	g, after := readCargo(t, repos, transfer.GuildID)
	if success != 2 || !reflect.DeepEqual(seen, []uint32{0, transfer.SourceItemID}) || g.Cargo.Slots[0].Count != 10 || after.Slots["0:87"].Count != 0 || after.Slots["0:88"].Count != 0 || len(g.Cargo.History) != 2 {
		t.Fatalf("deposit lost items/logs or used stale identity: success=%d seen=%v", success, seen)
	}
	if g.Cargo.Slots[0].Extra["marker"] != "keep" || g.Cargo.Slots[0].Extra["count"] != "10" {
		t.Fatal("merged metadata/count not preserved")
	}
}

func TestGuildCargoDepositRejectsOccupiedSlotConflictsAndRollsBack(t *testing.T) {
	for _, name := range []string{"other_item", "bound_mismatch", "full_stack", "locked_target", "late_failure", "permission", "wrong_source_id"} {
		t.Run(name, func(t *testing.T) {
			repos, o, transfer, rules, now := cargoFixture(t)
			ctx := context.Background()
			inv, _, _ := repos.Inventory.Load(ctx, "1")
			reg, _ := guild.Load(ctx, repos.Settings)
			g := reg.Guilds[transfer.GuildID]
			dest := cloneStack(inv.Slots["0:87"])
			dest.Count = 2
			req := cargoDepositRequest(transfer)
			boom := errors.New("late deposit failure")
			want := ErrGuildCargoRequest
			switch name {
			case "other_item":
				dest.ItemID++
			case "bound_mismatch":
				dest.Bind = !dest.Bind
			case "full_stack":
				dest.Count = 999
			case "locked_target":
				dest.Extra["equipment_lock_state"] = "1"
				want = ErrItemLocked
			case "late_failure":
				o.guild = failingCargoTx{repos.Guild, repos.Guild.(r.GuildInventoryUnitOfWork), boom}
				want = boom
			case "permission":
				g.GradePermissions = map[byte]uint32{1: 0}
				want = guild.ErrPermissionDenied
			case "wrong_source_id":
				req.ItemID++
				want = ErrGuildCargoStale
			}
			g.Cargo.Slots = map[uint16]r.ItemStack{0: dest}
			if err := guild.Save(ctx, repos.Settings, reg, now); err != nil {
				t.Fatal(err)
			}
			before, beforeInv := readCargo(t, repos, req.GuildID)
			got, err := o.DepositGuildCargo(ctx, req, rules, now.Add(time.Second))
			if !errors.Is(err, want) || got != (GuildCargoResult{}) {
				t.Fatalf("result=%+v error=%v want=%v", got, err, want)
			}
			after, afterInv := readCargo(t, repos, req.GuildID)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeInv, afterInv) {
				t.Fatal("rejection changed inventory or history")
			}
		})
	}
}

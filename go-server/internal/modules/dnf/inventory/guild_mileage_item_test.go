package inventory

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func mileageItemFixture(t *testing.T, count int64) (r.Group, *Owner, GuildMileageItemRequest, time.Time) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	repos := memory.NewMemoryGroup()
	c := r.CharacterRecord{CharacterID: "1", AccountID: "a1", Name: "会长", Level: 90, Stats: map[string]int64{"gold": 12345}}
	if err := repos.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	registry := dnfguild.NewRegistry()
	g, err := registry.Create(c, "测试公会", now)
	if err != nil {
		t.Fatal(err)
	}
	g.Mileage = 25
	if err := dnfguild.Save(ctx, repos.Settings, registry, now); err != nil {
		t.Fatal(err)
	}
	def := GuildMileageItemDefinition{ItemID: 10153530, PVFPath: "stackable/10153001/10153530.stk", Amount: 100, MinimumLevel: 1}
	raw := make([]byte, currentItemListEntrySize)
	binary.LittleEndian.PutUint32(raw[6:10], uint32(count))
	if err := repos.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "1", Slots: map[string]r.ItemStack{"0:87": {ItemID: def.ItemID, Count: count, RawEntry: raw, Extra: map[string]string{"item_kind": "stackable", "pvf_path": def.PVFPath, "stackable_type": "[waste]", "count": "2", "marker": "keep"}}, "0:90": {ItemID: 3037, Count: 99}}, Warehouse: map[string]r.ItemStack{"2:3": {ItemID: 3033, Count: 42}}}); err != nil {
		t.Fatal(err)
	}
	o, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	req := GuildMileageItemRequest{Owner: r.GuildOwner{CharacterID: "1", AccountID: "a1"}, SlotIndex: 87, Definition: def, Revalidate: func() bool { return true }}
	return repos, o, req, now
}

func TestGuildMileageItemAtomicConsumption(t *testing.T) {
	repos, o, req, now := mileageItemFixture(t, 2)
	ctx := context.Background()
	before, _, _ := repos.Character.Load(ctx, "1")
	for i := int64(1); i <= 2; i++ {
		got, err := o.UseGuildMileageItem(ctx, req, now)
		if err != nil || got.Remaining != 2-i || got.Mileage != 25+uint32(100*i) {
			t.Fatalf("result=%+v err=%v", got, err)
		}
		inv, _, _ := repos.Inventory.Load(ctx, "1")
		if i == 1 && (binary.LittleEndian.Uint32(inv.Slots["0:87"].RawEntry[6:10]) != 1 || inv.Slots["0:87"].Extra["count"] != "1" || inv.Slots["0:87"].Extra["marker"] != "keep") {
			t.Fatalf("remaining stack=%+v", inv.Slots["0:87"])
		}
		if inv.Slots["0:90"].Count != 99 || inv.Warehouse["2:3"].Count != 42 {
			t.Fatal("unrelated inventory changed")
		}
	}
	if _, err := o.UseGuildMileageItem(ctx, req, now); err == nil {
		t.Fatal("empty slot reused")
	}
	registry, _ := dnfguild.Load(ctx, repos.Settings)
	g, m, _ := registry.GuildForCharacter("1")
	after, _, _ := repos.Character.Load(ctx, "1")
	if g.Mileage != 225 || m.Mileage != 0 || m.ContributionPoints != 0 || g.Experience != 0 || !reflect.DeepEqual(before, after) {
		t.Fatal("reward altered unrelated asset or points")
	}
	if len(g.MileageHistory) != 2 {
		t.Fatalf("history=%+v", g.MileageHistory)
	}
	for _, event := range g.MileageHistory {
		if event.Kind != 8 || event.Amount != 100 || event.CharacterID != "1" || event.CharacterName != "会长" || !event.At.Equal(now) {
			t.Fatalf("history event=%+v", event)
		}
	}
}

func TestGuildMileageItemConcurrentConservation(t *testing.T) {
	repos, o, req, now := mileageItemFixture(t, 7)
	var applied atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := o.UseGuildMileageItem(context.Background(), req, now); err == nil {
				applied.Add(1)
			}
		}()
	}
	wg.Wait()
	registry, _ := dnfguild.Load(context.Background(), repos.Settings)
	g, _, _ := registry.GuildForCharacter("1")
	if applied.Load() != 7 || g.Mileage != 725 || len(g.MileageHistory) != 7 {
		t.Fatalf("uses=%d mileage=%d", applied.Load(), g.Mileage)
	}
}

func TestGuildMileageHistoryKeepsRecentCommittedEventsAfterReload(t *testing.T) {
	repos, owner, req, now := mileageItemFixture(t, 52)
	ctx := context.Background()
	for i := 0; i < 52; i++ {
		character, _, _ := repos.Character.Load(ctx, "1")
		character.Name = "当前名" + strconv.Itoa(i)
		if err := repos.Character.Save(ctx, character); err != nil {
			t.Fatal(err)
		}
		// A fresh owner each time must use persisted history, not a session cache.
		owner, _ = NewOwner(repos)
		if _, err := owner.UseGuildMileageItem(ctx, req, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	r, err := dnfguild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	g, _, _ := r.GuildForCharacter("1")
	if g.Mileage != 5225 || len(g.MileageHistory) != 50 || g.MileageHistory[0].CharacterName != "当前名2" || g.MileageHistory[49].CharacterName != "当前名51" {
		t.Fatalf("history/balance=%+v", g)
	}
	before, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
	if _, err := owner.UseGuildMileageItem(ctx, req, now); err == nil {
		t.Fatal("empty slot replay accepted")
	}
	after, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed use appended a history event")
	}
}

func TestGuildMileageHistoryUnrepresentableEventDoesNotDebit(t *testing.T) {
	for _, mode := range []string{"amount", "time", "name"} {
		t.Run(mode, func(t *testing.T) {
			repos, owner, req, now := mileageItemFixture(t, 1)
			ctx := context.Background()
			switch mode {
			case "amount":
				req.Definition.Amount = math.MaxInt32 + 1
			case "time":
				now = time.Unix(math.MaxInt32+1, 0)
			case "name":
				c, _, _ := repos.Character.Load(ctx, "1")
				c.Name = "bad\x00name"
				if err := repos.Character.Save(ctx, c); err != nil {
					t.Fatal(err)
				}
			}
			beforeInv, _, _ := repos.Inventory.Load(ctx, "1")
			beforeReg, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if _, err := owner.UseGuildMileageItem(ctx, req, now); !errors.Is(err, dnfguild.ErrMileageHistory) {
				t.Fatalf("err=%v", err)
			}
			afterInv, _, _ := repos.Inventory.Load(ctx, "1")
			afterReg, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if !reflect.DeepEqual(beforeInv, afterInv) || !reflect.DeepEqual(beforeReg, afterReg) {
				t.Fatal("unrepresentable event changed assets")
			}
		})
	}
}

func TestGuildMileageItemRejectsWithoutMutation(t *testing.T) {
	for _, kind := range []string{"wrong_account", "not_member", "member_account", "overflow", "item_swapped", "expired", "low_level", "bad_path", "oversized_count", "invalid_definition", "missing_revalidation", "stale_before", "stale_after"} {
		t.Run(kind, func(t *testing.T) {
			repos, o, req, now := mileageItemFixture(t, 2)
			ctx := context.Background()
			inv, _, _ := repos.Inventory.Load(ctx, "1")
			registry, _ := dnfguild.Load(ctx, repos.Settings)
			g, m, _ := registry.GuildForCharacter("1")
			c, _, _ := repos.Character.Load(ctx, "1")
			stack := inv.Slots["0:87"]
			switch kind {
			case "wrong_account":
				req.Owner.AccountID = "other"
			case "not_member":
				delete(registry.Guilds, g.ID)
			case "member_account":
				m.AccountID = "other"
				g.Members["1"] = m
			case "overflow":
				g.Mileage = ^uint32(0) - 99
			case "item_swapped":
				stack.ItemID = 3037
			case "expired":
				stack.ExpireAt = now
			case "low_level":
				c.Level = 0
			case "bad_path":
				stack.Extra["pvf_path"] = "stackable/other.stk"
			case "oversized_count":
				stack.Count = math.MaxInt32 + 1
			case "invalid_definition":
				req.Definition.Amount = 0
			case "missing_revalidation":
				req.Revalidate = nil
			case "stale_before":
				req.Revalidate = func() bool { return false }
			case "stale_after":
				calls := 0
				req.Revalidate = func() bool { calls++; return calls == 1 }
			}
			inv.Slots["0:87"] = stack
			if err := repos.Inventory.Save(ctx, inv); err != nil {
				t.Fatal(err)
			}
			if err := repos.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			if err := dnfguild.Save(ctx, repos.Settings, registry, now); err != nil {
				t.Fatal(err)
			}
			settings, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if _, err := o.UseGuildMileageItem(ctx, req, now); err == nil {
				t.Fatal("invalid use accepted")
			}
			gotInv, _, _ := repos.Inventory.Load(ctx, "1")
			gotSettings, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if !reflect.DeepEqual(inv, gotInv) || !reflect.DeepEqual(settings, gotSettings) {
				t.Fatal("rejected use changed assets")
			}
		})
	}
}

func TestGuildMileageItemCountUsesInventoryProjection(t *testing.T) {
	for _, count := range []int64{65536, 65537, math.MaxInt32} {
		t.Run(strconv.FormatInt(count, 10), func(t *testing.T) {
			repos, o, req, now := mileageItemFixture(t, count)
			got, err := o.UseGuildMileageItem(context.Background(), req, now)
			if err != nil || got.Remaining != count-1 || got.Mileage != 125 {
				t.Fatalf("result=%+v err=%v", got, err)
			}
			inv, _, _ := repos.Inventory.Load(context.Background(), "1")
			stack := inv.Slots["0:87"]
			if stack.Count != count-1 || binary.LittleEndian.Uint32(stack.RawEntry[6:10]) != uint32(count-1) || stack.Extra["count"] != strconv.FormatInt(count-1, 10) {
				t.Fatalf("count projection=%+v", stack)
			}
		})
	}
}

type mileageFailingUow struct {
	r.GuildUnitOfWork
	failSettings bool
}

func (u mileageFailingUow) WithinGuildInventory(ctx context.Context, owner r.GuildOwner, apply func(r.Group) error) error {
	return u.GuildUnitOfWork.(r.GuildInventoryUnitOfWork).WithinGuildInventory(ctx, owner, func(tx r.Group) error {
		if u.failSettings {
			tx.Settings = mileageFailSettings{tx.Settings}
		} else {
			tx.Inventory = mileageFailInventory{tx.Inventory}
		}
		return apply(tx)
	})
}

type mileageFailSettings struct{ r.SettingsRepository }

func (mileageFailSettings) Save(context.Context, r.SettingsRecord) error {
	return errors.New("injected settings save")
}

type mileageFailInventory struct{ r.InventoryRepository }

func (mileageFailInventory) Save(context.Context, r.InventoryRecord) error {
	return errors.New("injected inventory save")
}

func TestGuildMileageItemWriteFailureRollsBack(t *testing.T) {
	for _, settings := range []bool{false, true} {
		repos, o, req, now := mileageItemFixture(t, 2)
		ctx := context.Background()
		beforeInv, _, _ := repos.Inventory.Load(ctx, "1")
		beforeSettings, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
		o.guild = mileageFailingUow{repos.Guild, settings}
		if _, err := o.UseGuildMileageItem(ctx, req, now); err == nil {
			t.Fatal("write failure accepted")
		}
		inv, _, _ := repos.Inventory.Load(ctx, "1")
		reg, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
		if !reflect.DeepEqual(inv, beforeInv) || !reflect.DeepEqual(reg, beforeSettings) {
			t.Fatal("write failure lost assets")
		}
	}
}

func TestGuildMileageItemCannotUseGenericWastePath(t *testing.T) {
	for _, mode := range []string{"metadata_only", "pvf", "future_item"} {
		t.Run(mode, func(t *testing.T) {
			repos, o, req, _ := mileageItemFixture(t, 2)
			var resolver alignedcmd.StackableUseProvenanceResolver
			if mode != "metadata_only" {
				resolver = func(id int64) (alignedcmd.StackableUseProvenanceResolution, error) {
					return alignedcmd.StackableUseProvenanceResolution{Found: true, ItemID: id, PVFPath: req.Definition.PVFPath, StackableType: "[waste]", ActionType: "[add guildmileage]"}, nil
				}
			}
			id := int32(req.Definition.ItemID)
			if mode == "future_item" {
				id = 777
				inv, _, _ := repos.Inventory.Load(context.Background(), "1")
				stack := inv.Slots["0:87"]
				stack.ItemID = int64(id)
				inv.Slots["0:87"] = stack
				repos.Inventory.Save(context.Background(), inv)
			}
			_, err := o.UseStackable(context.Background(), Command{SelectedCharacterID: 1, AccountID: "a1", SourceListType: 0, SourceSlotIndex: 87, SourceInstanceValue: 42, ItemTemplateID: id, StackableUseProvenanceResolver: resolver}, nil)
			inv, _, _ := repos.Inventory.Load(context.Background(), "1")
			if err == nil || inv.Slots["0:87"].Count != 2 {
				t.Fatal("generic path consumed guild mileage item")
			}
		})
	}
}

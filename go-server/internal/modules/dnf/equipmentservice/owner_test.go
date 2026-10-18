package equipmentservice

import (
	"bytes"
	"context"
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/itemplacement"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"reflect"
	"sync"
	"testing"
	"time"
)

type testCatalog map[uint32]Definition

func (c testCatalog) Resolve(id uint32) (Definition, error) {
	d, ok := c[id]
	if !ok {
		return d, ErrRules
	}
	return d, nil
}
func fixture(t *testing.T) (r.Group, *Owner, testCatalog, TransferConfig, ResealConfig, Command) {
	t.Helper()
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	now := time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC)
	if e := g.Account.Save(ctx, r.AccountRecord{AccountID: "a", Metadata: map[string]string{"account_cargo_level": "8", r.AccountEpicPieceMetadataKey(10300000): "1000", "other": "keep"}}); e != nil {
		t.Fatal(e)
	}
	for _, v := range []r.CharacterRecord{{CharacterID: "19", AccountID: "a", Slot: 0, Name: "来源"}, {CharacterID: "42", AccountID: "a", Slot: 3, Name: "接收"}, {CharacterID: "99", AccountID: "other", Slot: 0}} {
		if e := g.Character.Save(ctx, v); e != nil {
			t.Fatal(e)
		}
	}
	raw := bytes.Repeat([]byte{0x19}, 119)
	binary.LittleEndian.PutUint16(raw, 9)
	binary.LittleEndian.PutUint32(raw[2:], 1001)
	raw[10] = 0x1b
	raw[13] = 0
	i := r.InventoryRecord{CharacterID: "19", Slots: map[string]r.ItemStack{"0:9": {ItemID: 1001, Count: 1, RawEntry: raw, Extra: map[string]string{"item_kind": "equipment", "pvf_path": "equipment/test.equ", "reinforce": "12", "card": "123"}}, "0:65": {ItemID: 2000, Count: 30}, "0:66": {ItemID: 3000, Count: 2}, "0:121": {ItemID: 3167, Count: 2000}}}
	if e := g.Inventory.Save(ctx, i); e != nil {
		t.Fatal(e)
	}
	if e := g.AccountInventory.Save(ctx, r.AccountInventoryRecord{AccountID: "a", Slots: map[string]r.ItemStack{"0:363": {ItemID: 10099774, Count: 2000}, "0:365": {ItemID: 10158124, Count: 2000}}}); e != nil {
		t.Fatal(e)
	}
	c := testCatalog{1001: {ItemID: 1001, PVFPath: "equipment/test.equ", Equipment: true, AttachType: "[sealing]", EquipmentType: "[weapon]", MinimumLevel: 85, Rarity: 3, Policy: itemplacement.SlotPolicy{Start: 9, End: 64, AllowQuickSlots: true}}, 2000: {ItemID: 2000, PVFPath: "stackable/cash/candle_goldwax.stk", Policy: itemplacement.SlotPolicy{Start: 65, End: 120, AllowQuickSlots: true}}, 2001: {ItemID: 2001, PVFPath: "stackable/cash/candle_goldwax_oneshot.stk", Policy: itemplacement.SlotPolicy{Start: 65, End: 120}}, 3000: {ItemID: 3000, CargoTransportStone: true, CargoTransportStoneType: 8, Policy: itemplacement.SlotPolicy{Start: 65, End: 120}}, 3167: {ItemID: 3167, Policy: itemplacement.SlotPolicy{Start: 121, End: 176}}}
	rule := TransferRule{GradeWeights: []RangeWeight{{1, 90, 5}}, RarityWeights: map[int64]int64{3: 2, 4: 9}, EquipmentTypes: map[string]bool{"weapon": true}, SoulItemIDs: map[int64]uint32{3: 10099774, 4: 10158124}, SoulWeights: map[int64][]int64{3: {10, 10, 10, 10}, 4: {2, 3, 3, 3}}, ElementItemID: 3167, ElementWeights: map[int64][]int64{3: {2, 2, 2, 2}, 4: {8, 10, 10, 15}}, EpicPieceWeight: []int64{2, 3, 3, 3}, ExceptGrowth: true, ExceptPeriod: true}
	transfer := TransferConfig{Rules: map[int64]TransferRule{7: rule, 8: rule}}
	seal := ResealConfig{GradeWeights: []RangeWeight{{1, 90, 4}}, RarityWeights: map[int64]float64{3: 2}, ExtendedWeights: map[int64]float64{3: 1}, SoulItemIDs: map[int64]uint32{3: 10099774}, SoulCostWeights: map[int64]float64{3: 10}, ResealConstants: map[int64]float64{3: 1.1}, ResealCountLimits: []RangeWeight{{1, 85, 7}, {86, 90, 4}}}
	o, e := NewOwner(g)
	if e != nil {
		t.Fatal(e)
	}
	return g, o, c, transfer, seal, Command{AccountID: "a", CharacterID: "19", RecipientID: "42", TargetSlot: 9, MaterialSlot: 66, Mode: 2, Now: now}
}
func TestNPCTransferNonEpicDoesNotRequireFragmentsAndPreservesInstance(t *testing.T) {
	g, o, c, rules, _, cmd := fixture(t)
	ctx := context.Background()
	before, _, _ := g.Inventory.Load(ctx, "19")
	result, e := o.Transfer(ctx, c, rules, cmd)
	if e != nil {
		t.Fatal(e)
	}
	if result.RecipientID != "42" || result.EpicPieceID != 0 {
		t.Fatal(result)
	}
	i, _, _ := g.Inventory.Load(ctx, "19")
	if _, ok := i.Slots["0:9"]; ok || i.Slots["0:121"].Count != 1980 {
		t.Fatal(i)
	}
	a, _, _ := g.AccountInventory.Load(ctx, "a")
	if a.Slots["0:363"].Count != 1900 {
		t.Fatal(a)
	}
	m, _, _ := g.Mailbox.Load(ctx, "42")
	if len(m.Mails) != 1 {
		t.Fatal(m)
	}
	for _, mail := range m.Mails {
		if len(mail.Attachments) != 1 {
			t.Fatal(mail)
		}
		got := mail.Attachments[0]
		want := before.Slots["0:9"]
		if got.ItemID != want.ItemID || got.Count != 1 || !bytes.Equal(got.RawEntry, want.RawEntry) || !reflect.DeepEqual(got.Extra, want.Extra) {
			t.Fatalf("跨界改变原装备：%+v", got)
		}
	}
	if _, e = o.Transfer(ctx, c, rules, cmd); e == nil {
		t.Fatal("重复跨界成功")
	}
	after, _, _ := g.AccountInventory.Load(ctx, "a")
	if !reflect.DeepEqual(a, after) {
		t.Fatal("重复请求重复扣材")
	}
}
func TestNPCTransferEpicDebitsCurrentPieceKeyOnly(t *testing.T) {
	g, o, c, rules, _, cmd := fixture(t)
	d := c[1001]
	d.Rarity = 4
	d.EpicPieceID = 10300000
	c[1001] = d
	result, e := o.Transfer(context.Background(), c, rules, cmd)
	if e != nil {
		t.Fatal(e)
	}
	if result.EpicBalance != 910 {
		t.Fatal(result)
	}
	a, _, _ := g.Account.Load(context.Background(), "a")
	if a.Metadata[r.AccountEpicPieceMetadataKey(10300000)] != "910" || a.Metadata["other"] != "keep" {
		t.Fatal(a.Metadata)
	}
}
func TestStoneTransferUsesAccountCargoAndConsumesOneStone(t *testing.T) {
	g, o, c, rules, _, cmd := fixture(t)
	cmd.Mode = 0
	cmd.RecipientID = ""
	ctx := context.Background()
	before, _, _ := g.Inventory.Load(ctx, "19")
	x, e := o.Transfer(ctx, c, rules, cmd)
	if e != nil {
		t.Fatal(e)
	}
	if x.CargoSlot != 0 || x.MaterialRemaining != 1 {
		t.Fatal(x)
	}
	i, _, _ := g.Inventory.Load(ctx, "19")
	a, _, _ := g.AccountInventory.Load(ctx, "a")
	if i.Slots["0:66"].Count != 1 || i.Slots["0:9"].ItemID != 0 || !reflect.DeepEqual(a.Slots["12:0"], before.Slots["0:9"]) {
		t.Fatal("跨界石未原样入账号金库")
	}
	if m, _, _ := g.Mailbox.Load(ctx, "42"); len(m.Mails) != 0 {
		t.Fatal("跨界石错误邮寄")
	}
}
func TestEquipmentServicesRejectionsLeaveEveryAssetUntouched(t *testing.T) {
	for _, name := range []string{"foreign", "same_recipient", "locked", "expired", "closed_slot", "stacked", "material_short", "excluded", "cargo_inactive", "cargo_full", "wrong_stone_page", "epic_short"} {
		t.Run(name, func(t *testing.T) {
			g, o, c, rules, _, cmd := fixture(t)
			ctx := context.Background()
			i, _, _ := g.Inventory.Load(ctx, "19")
			a, _, _ := g.AccountInventory.Load(ctx, "a")
			account, _, _ := g.Account.Load(ctx, "a")
			switch name {
			case "foreign":
				cmd.RecipientID = "99"
			case "same_recipient":
				cmd.RecipientID = "19"
			case "locked":
				s := i.Slots["0:9"]
				s.Extra["equipment_lock_state"] = "active"
				i.Slots["0:9"] = s
			case "expired":
				s := i.Slots["0:9"]
				s.ExpireAt = cmd.Now.Add(-time.Second)
				i.Slots["0:9"] = s
			case "closed_slot":
				i.Slots["0:60"] = i.Slots["0:9"]
				delete(i.Slots, "0:9")
				cmd.TargetSlot = 60
			case "stacked":
				s := i.Slots["0:9"]
				s.Count = 2
				i.Slots["0:9"] = s
			case "material_short":
				delete(a.Slots, "0:363")
			case "excluded":
				v := rules.Rules[7]
				v.ExcludeItemIDs = map[uint32]bool{1001: true}
				rules.Rules[7] = v
			case "cargo_inactive":
				cmd.Mode = 0
				cmd.RecipientID = ""
				account.Metadata["account_cargo_level"] = "0"
			case "cargo_full":
				cmd.Mode = 0
				cmd.RecipientID = ""
				account.Metadata["account_cargo_level"] = "1"
				a.Slots["12:0"] = r.ItemStack{ItemID: 1001, Count: 1}
			case "wrong_stone_page":
				cmd.Mode = 0
				cmd.RecipientID = ""
				i.Slots["0:122"] = i.Slots["0:66"]
				delete(i.Slots, "0:66")
				cmd.MaterialSlot = 122
			case "epic_short":
				d := c[1001]
				d.Rarity = 4
				d.EpicPieceID = 10300000
				c[1001] = d
				account.Metadata[r.AccountEpicPieceMetadataKey(10300000)] = "0"
			}
			g.Inventory.Save(ctx, i)
			g.AccountInventory.Save(ctx, a)
			g.Account.Save(ctx, account)
			if _, e := o.Transfer(ctx, c, rules, cmd); e == nil {
				t.Fatal("应拒绝")
			}
			ii, _, _ := g.Inventory.Load(ctx, "19")
			aa, _, _ := g.AccountInventory.Load(ctx, "a")
			ac, _, _ := g.Account.Load(ctx, "a")
			if !reflect.DeepEqual(i, ii) || !reflect.DeepEqual(a, aa) || !reflect.DeepEqual(account, ac) {
				t.Fatal("拒绝后资产发生变化")
			}
			if m, _, _ := g.Mailbox.Load(ctx, "42"); len(m.Mails) != 0 {
				t.Fatal("拒绝仍生成邮件")
			}
		})
	}
}
func TestResealNPCAndWaxUseDifferentMaterialOwners(t *testing.T) {
	for _, mode := range []byte{0, 1} {
		t.Run(string(rune('0'+mode)), func(t *testing.T) {
			g, o, c, _, rules, cmd := fixture(t)
			cmd.Mode = mode
			cmd.RecipientID = ""
			cmd.MaterialSlot = 65
			cmd.TargetItemID = 1001
			ctx := context.Background()
			before, _, _ := g.Inventory.Load(ctx, "19")
			target := before.Slots["0:9"]
			x, e := o.Reseal(ctx, c, rules, cmd)
			if e != nil {
				t.Fatal(e)
			}
			i, _, _ := g.Inventory.Load(ctx, "19")
			a, _, _ := g.AccountInventory.Load(ctx, "a")
			s := i.Slots["0:9"]
			if SealFlag(s) != 1 || ResealCount(s) != 1 || s.RawEntry[10]&31 != target.RawEntry[10]&31 || s.Extra["card"] != "123" || s.Extra["reinforce"] != "12" {
				t.Fatal("封装破坏实例或次数")
			}
			want := append([]byte(nil), target.RawEntry...)
			want[10] = (want[10] & 31) | 32
			want[13] = 1
			if !bytes.Equal(want, s.RawEntry) {
				t.Fatal("封装改变了其它原始行字段")
			}
			if mode == 0 {
				if i.Slots["0:65"].Count != 22 || a.Slots["0:363"].Count != 2000 || x.MaterialRemaining != 22 {
					t.Fatal(x)
				}
			} else {
				if i.Slots["0:65"].Count != 30 || a.Slots["0:363"].Count != 1920 || x.MaterialSlot != -1 {
					t.Fatal(x)
				}
			}
			if _, e = o.Reseal(ctx, c, rules, cmd); e == nil {
				t.Fatal("已封装装备再次扣费")
			}
		})
	}
}
func TestOneShotWaxAtLimitPreservesCounterAndOrdinaryWaxRollsBack(t *testing.T) {
	for _, one := range []bool{false, true} {
		t.Run(map[bool]string{false: "普通蜜蜡", true: "一次性蜜蜡"}[one], func(t *testing.T) {
			g, o, c, _, rules, cmd := fixture(t)
			cmd.Mode = 0
			cmd.RecipientID = ""
			cmd.MaterialSlot = 65
			ctx := context.Background()
			i, _, _ := g.Inventory.Load(ctx, "19")
			s := i.Slots["0:9"]
			s.RawEntry[10] = (s.RawEntry[10] & 31) | (7 << 5)
			i.Slots["0:9"] = s
			if one {
				i.Slots["0:65"] = r.ItemStack{ItemID: 2001, Count: 2}
			}
			g.Inventory.Save(ctx, i)
			_, e := o.Reseal(ctx, c, rules, cmd)
			after, _, _ := g.Inventory.Load(ctx, "19")
			if one {
				if e != nil || ResealCount(after.Slots["0:9"]) != 7 || after.Slots["0:65"].Count != 1 {
					t.Fatal(e, after)
				}
			} else if e == nil || !reflect.DeepEqual(i, after) {
				t.Fatal("次数上限没有回滚")
			}
		})
	}
}
func TestConcurrentDuplicateNPCTransferCommitsOnlyOnce(t *testing.T) {
	g, o, c, rules, _, cmd := fixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := o.Transfer(context.Background(), c, rules, cmd); results <- e }()
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		}
	}
	m, _, _ := g.Mailbox.Load(context.Background(), "42")
	if success != 1 || len(m.Mails) != 1 {
		t.Fatal(success, m)
	}
}

func TestEquipmentServiceReadsPersistedExpandedEquipmentCapacity(t *testing.T) {
	g, o, c, rules, _, cmd := fixture(t)
	ctx := context.Background()
	i, _, _ := g.Inventory.Load(ctx, "19")
	i.Slots["0:60"] = i.Slots["0:9"]
	delete(i.Slots, "0:9")
	g.Inventory.Save(ctx, i)
	cmd.TargetSlot = 60
	if e := g.Settings.Save(ctx, r.SettingsRecord{Scope: r.CharacterContainerStateScope("19"), Values: map[string]string{"main_list_param16": "24", "avatar_list_param16": "0", "personal_cargo_list_param16": "8"}}); e != nil {
		t.Fatal(e)
	}
	if _, e := o.Transfer(ctx, c, rules, cmd); e != nil {
		t.Fatal("开放的扩容格被误拒绝", e)
	}
}

func TestNPCChronicleTransferUsesFlatConfiguredCostAndRollsBackShortages(t *testing.T) {
	for _, name := range []string{"success", "split_exact", "fragments_short", "elements_short", "cost_missing"} {
		t.Run(name, func(t *testing.T) {
			g, o, c, rules, _, cmd := fixture(t)
			ctx := context.Background()
			d := c[1001]
			d.Rarity = 5
			c[1001] = d
			c[3311] = Definition{ItemID: 3311, Policy: itemplacement.SlotPolicy{Start: 121, End: 176}}
			rule := rules.Rules[7]
			rule.RarityWeights[5] = 8
			rule.SoulItemIDs[5] = 3311
			rule.SoulWeights[5] = []int64{-1, -1, -1, -1}
			rule.ElementWeights[5] = []int64{8, 8, 8, 8}
			rule.ChronicleCost = 250
			if name == "cost_missing" {
				rule.ChronicleCost = 0
			}
			rules.Rules[7] = rule
			i, _, _ := g.Inventory.Load(ctx, "19")
			i.Slots["0:122"] = r.ItemStack{ItemID: 3311, Count: 500}
			switch name {
			case "split_exact":
				i.Slots["0:122"] = r.ItemStack{ItemID: 3311, Count: 120}
				i.Slots["0:123"] = r.ItemStack{ItemID: 3311, Count: 130}
				i.Slots["0:121"] = r.ItemStack{ItemID: 3167, Count: 320}
			case "fragments_short":
				i.Slots["0:122"] = r.ItemStack{ItemID: 3311, Count: 249}
			case "elements_short":
				i.Slots["0:121"] = r.ItemStack{ItemID: 3167, Count: 319}
			}
			if e := g.Inventory.Save(ctx, i); e != nil {
				t.Fatal(e)
			}
			account, _, _ := g.Account.Load(ctx, "a")
			shared, _, _ := g.AccountInventory.Load(ctx, "a")
			result, err := o.Transfer(ctx, c, rules, cmd)
			after, _, _ := g.Inventory.Load(ctx, "19")
			gotAccount, _, _ := g.Account.Load(ctx, "a")
			gotShared, _, _ := g.AccountInventory.Load(ctx, "a")
			mail, _, _ := g.Mailbox.Load(ctx, "42")
			if name != "success" && name != "split_exact" {
				if err == nil || !reflect.DeepEqual(i, after) || !reflect.DeepEqual(account, gotAccount) || !reflect.DeepEqual(shared, gotShared) || len(mail.Mails) != 0 {
					t.Fatal("材料不足或规则缺失没有完整回滚", err)
				}
				return
			}
			if err != nil || result.EpicPieceID != 0 || len(mail.Mails) != 1 {
				t.Fatal(result, err, mail)
			}
			if !reflect.DeepEqual(account, gotAccount) || !reflect.DeepEqual(shared.Slots, gotShared.Slots) {
				t.Fatal("异界跨界错误扣除了灵魂或史诗碎片")
			}
			if name == "split_exact" {
				for _, key := range []string{"0:121", "0:122", "0:123"} {
					if _, exists := after.Slots[key]; exists {
						t.Fatal("耗尽材料未删除", key)
					}
				}
			} else if after.Slots["0:122"].Count != 250 || after.Slots["0:121"].Count != 1680 {
				t.Fatal("异界费用应为固定250碎片及320结晶", after)
			}
			for _, m := range mail.Mails {
				if !bytes.Equal(m.Attachments[0].RawEntry, i.Slots["0:9"].RawEntry) || !reflect.DeepEqual(m.Attachments[0].Extra, i.Slots["0:9"].Extra) {
					t.Fatal("异界装备实例被改变")
				}
			}
			if _, err = o.Transfer(ctx, c, rules, cmd); err == nil {
				t.Fatal("重复跨界成功")
			}
			again, _, _ := g.Inventory.Load(ctx, "19")
			if !reflect.DeepEqual(after, again) {
				t.Fatal("重复请求重复扣材")
			}
		})
	}
}

func TestNPCEpicTransferCostsAreAtomicAndOnlyHighLevelRequiresPieces(t *testing.T) {
	for _, name := range []string{"success", "level65", "pieces_short", "elements_short", "particular_short"} {
		t.Run(name, func(t *testing.T) {
			g, o, c, rules, _, cmd := fixture(t)
			ctx := context.Background()
			d := c[1001]
			d.Rarity = 4
			d.MinimumLevel = 90
			d.EpicPieceID = 10300000
			if name == "level65" {
				d.MinimumLevel = 65
				d.EpicPieceID = 0
			}
			c[1001] = d
			c[5000] = Definition{ItemID: 5000, Policy: itemplacement.SlotPolicy{Start: 121, End: 176}}
			rule := rules.Rules[7]
			rule.ParticularCosts = []ParticularCost{{Rarity: 4, Level: 90, ItemID: 5000, Count: 15}}
			rules.Rules[7] = rule
			i, _, _ := g.Inventory.Load(ctx, "19")
			i.Slots["0:123"] = r.ItemStack{ItemID: 5000, Count: 20}
			account, _, _ := g.Account.Load(ctx, "a")
			if name == "pieces_short" {
				account.Metadata[r.AccountEpicPieceMetadataKey(10300000)] = "89"
			}
			if name == "elements_short" {
				i.Slots["0:121"] = r.ItemStack{ItemID: 3167, Count: 359}
			}
			if name == "particular_short" {
				i.Slots["0:123"] = r.ItemStack{ItemID: 5000, Count: 14}
			}
			if e := g.Inventory.Save(ctx, i); e != nil {
				t.Fatal(e)
			}
			if e := g.Account.Save(ctx, account); e != nil {
				t.Fatal(e)
			}
			shared, _, _ := g.AccountInventory.Load(ctx, "a")
			result, err := o.Transfer(ctx, c, rules, cmd)
			after, _, _ := g.Inventory.Load(ctx, "19")
			gotAccount, _, _ := g.Account.Load(ctx, "a")
			gotShared, _, _ := g.AccountInventory.Load(ctx, "a")
			mail, _, _ := g.Mailbox.Load(ctx, "42")
			if name != "success" && name != "level65" {
				if err == nil || !reflect.DeepEqual(i, after) || !reflect.DeepEqual(account, gotAccount) || !reflect.DeepEqual(shared, gotShared) || len(mail.Mails) != 0 {
					t.Fatal("史诗费用不足未完整回滚", err)
				}
				return
			}
			if err != nil || len(mail.Mails) != 1 || gotShared.Slots["0:365"].Count != 1910 || after.Slots["0:121"].Count != 1640 {
				t.Fatal("史诗灵魂或结晶扣除不正确", result, err)
			}
			if name == "level65" {
				if result.EpicPieceID != 0 || !reflect.DeepEqual(account, gotAccount) || after.Slots["0:123"].Count != 20 {
					t.Fatal("65级史诗不应扣碎片或90级专用材料")
				}
			} else if result.EpicBalance != 910 || gotAccount.Metadata[r.AccountEpicPieceMetadataKey(10300000)] != "910" || after.Slots["0:123"].Count != 5 {
				t.Fatal("史诗碎片或专用材料未正确扣除")
			}
		})
	}
}

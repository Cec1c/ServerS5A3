package equipmentservice

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"longheng.io/server/internal/modules/dnf/itemtrade"
	r "longheng.io/server/internal/modules/dnf/repository"
	"math"
	"reflect"
	"strconv"
	"testing"
)

func TestEquipmentFollowupResealReleasesBindingAndPreservesInstance(t *testing.T) {
	for _, kind := range []string{"普通蜜蜡", "一次性蜜蜡", "NPC灵魂"} {
		t.Run(kind, func(t *testing.T) {
			g, o, c, _, rules, cmd := fixture(t)
			ctx := context.Background()
			cmd.RecipientID = ""
			cmd.MaterialSlot = 65
			cmd.Mode = 0
			i, _, _ := g.Inventory.Load(ctx, "19")
			target := i.Slots["0:9"]
			target.Bind = true
			binary.LittleEndian.PutUint32(target.RawEntry[6:], 462346698)
			binary.LittleEndian.PutUint16(target.RawEntry[11:], 13)
			target.Extra["quality_seed"] = "462346698"
			target.Extra["durability"] = "13"
			target.Extra["max_durability"] = "40"
			target.Extra["avatar_socket_data"] = "0100020089130000030090130000"
			target.Extra["enchant_card"] = "777"
			target.Extra["forge"] = "8"
			target.RawEntry[60] = 2
			binary.LittleEndian.PutUint32(target.RawEntry[61:], 5001)
			binary.LittleEndian.PutUint32(target.RawEntry[65:], 5002)
			target.Extra["equipment_emblem_data"] = "02891300008a130000"
			target.Extra["equipment_socket_data"] = "02891300008a130000"
			target.Extra["seal_removed_by_first_equip"] = "1"
			target.Extra["trade_locked_by_first_equip"] = "1"
			i.Slots["0:9"] = target
			if kind == "一次性蜜蜡" {
				i.Slots["0:65"] = r.ItemStack{ItemID: 2001, Count: 2}
			}
			if kind == "NPC灵魂" {
				cmd.Mode = 1
			}
			if e := g.Inventory.Save(ctx, i); e != nil {
				t.Fatal(e)
			}
			if e := g.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "42", Slots: map[string]r.ItemStack{}}); e != nil {
				t.Fatal(e)
			}
			if _, e := o.Reseal(ctx, c, rules, cmd); e != nil {
				t.Fatal(e)
			}
			after, _, _ := g.Inventory.Load(ctx, "19")
			sealed := after.Slots["0:9"]
			if sealed.Bind || SealFlag(sealed) != 1 {
				t.Fatal("封装后仍为角色绑定，无法交易", sealed.Bind)
			}
			want := append([]byte(nil), target.RawEntry...)
			if kind != "一次性蜜蜡" {
				want[10] = (want[10] & 31) | 32
			}
			want[13] = 1
			if !bytes.Equal(want, sealed.RawEntry) {
				t.Fatal("封装改变了品级、耐久、附魔或其它原始实例字节")
			}
			for _, k := range []string{"quality_seed", "durability", "max_durability", "reinforce", "card", "enchant_card", "forge", "avatar_socket_data", "equipment_emblem_data", "equipment_socket_data"} {
				if sealed.Extra[k] != target.Extra[k] {
					t.Fatalf("封装改变属性%s", k)
				}
			}
			trade, e := itemtrade.NewOwner(g)
			if e != nil {
				t.Fatal(e)
			}
			result, e := trade.Exchange(ctx, itemtrade.Participant{CharacterID: "19", Offers: []itemtrade.Offer{{TradeSlot: 0, SourceList: 0, SourceSlot: 9, Count: 1, ExpectedItem: 1001}}}, itemtrade.Participant{CharacterID: "42"})
			if e != nil || len(result.Received["42"]) != 1 {
				t.Fatal("真实交易owner拒绝再封装装备", e)
			}
			got := result.Received["42"][0].Stack
			if got.Bind || got.Extra["avatar_socket_data"] != target.Extra["avatar_socket_data"] || got.Extra["enchant_card"] != "777" {
				t.Fatal("交易后丢失封装或附魔徽章")
			}
		})
	}
}

func TestEquipmentFollowupResealRejectsCounterMismatchAndExtraOnlyPeriod(t *testing.T) {
	for _, kind := range []string{"次数不一致", "次数损坏", "仅扩展字段期限"} {
		t.Run(kind, func(t *testing.T) {
			g, o, c, _, rules, cmd := fixture(t)
			ctx := context.Background()
			cmd.Mode = 0
			cmd.MaterialSlot = 65
			cmd.RecipientID = ""
			i, _, _ := g.Inventory.Load(ctx, "19")
			target := i.Slots["0:9"]
			switch kind {
			case "次数不一致":
				target.Extra["reseal_count"] = "7"
			case "次数损坏":
				target.Extra["reseal_count"] = "bad"
			default:
				target.Extra["expire_unix"] = "2000000000"
			}
			i.Slots["0:9"] = target
			if e := g.Inventory.Save(ctx, i); e != nil {
				t.Fatal(e)
			}
			shared, _, _ := g.AccountInventory.Load(ctx, "a")
			if _, e := o.Reseal(ctx, c, rules, cmd); e == nil {
				t.Fatal("放行不完整或矛盾的实例证据")
			}
			after, _, _ := g.Inventory.Load(ctx, "19")
			a, _, _ := g.AccountInventory.Load(ctx, "a")
			if !reflect.DeepEqual(after, i) || !reflect.DeepEqual(a, shared) {
				t.Fatal("拒绝后扣除了材料")
			}
		})
	}
}

func TestEquipmentFollowupTransferRejectsMissingCrystalRow(t *testing.T) {
	g, o, c, rules, _, cmd := fixture(t)
	ctx := context.Background()
	rule := rules.Rules[7]
	delete(rule.ElementWeights, 3)
	rules.Rules[7] = rule
	before, _, _ := g.Inventory.Load(ctx, "19")
	shared, _, _ := g.AccountInventory.Load(ctx, "a")
	if _, e := o.Transfer(ctx, c, rules, cmd); e == nil {
		t.Fatal("结晶费用行缺失时仍跨界，少扣了材料")
	}
	after, _, _ := g.Inventory.Load(ctx, "19")
	a, _, _ := g.AccountInventory.Load(ctx, "a")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(shared, a) {
		t.Fatal("规则缺失后未回滚")
	}
}

func TestEquipmentFollowupTransferIgnoresResealCount(t *testing.T) {
	for _, mode := range []byte{0, 2} {
		for _, count := range []byte{0, 7} {
			t.Run(fmt.Sprintf("模式%d_已封装%d次", mode, count), func(t *testing.T) {
				g, o, c, rules, _, cmd := fixture(t)
				cmd.Mode = mode
				ctx := context.Background()
				i, _, _ := g.Inventory.Load(ctx, "19")
				target := i.Slots["0:9"]
				target.RawEntry[10] = (target.RawEntry[10] & 31) | (count << 5)
				target.Extra["reseal_count"] = strconv.Itoa(int(count))
				i.Slots["0:9"] = target
				if err := g.Inventory.Save(ctx, i); err != nil {
					t.Fatal(err)
				}
				if _, err := o.Transfer(ctx, c, rules, cmd); err != nil {
					t.Fatal("跨界不应检查剩余封装次数", err)
				}
				var raw []byte
				if mode == 0 {
					a, _, _ := g.AccountInventory.Load(ctx, "a")
					raw = a.Slots["12:0"].RawEntry
				} else {
					m, _, _ := g.Mailbox.Load(ctx, "42")
					for _, mail := range m.Mails {
						raw = mail.Attachments[0].RawEntry
					}
				}
				if !bytes.Equal(raw, target.RawEntry) {
					t.Fatal("跨界改变封装次数或其它装备实例")
				}
			})
		}
	}
}

func TestEquipmentFollowupResealRejectsNonFiniteAndOverflowPrices(t *testing.T) {
	for _, weight := range []float64{math.NaN(), math.Inf(1), math.MaxFloat64} {
		g, o, c, _, rules, cmd := fixture(t)
		cmd.Mode = 0
		cmd.MaterialSlot = 65
		cmd.RecipientID = ""
		ctx := context.Background()
		rules.RarityWeights[3] = weight
		before, _, _ := g.Inventory.Load(ctx, "19")
		if _, err := o.Reseal(ctx, c, rules, cmd); err == nil {
			t.Fatal("非法费用被转成整数继续扣除")
		}
		after, _, _ := g.Inventory.Load(ctx, "19")
		if !reflect.DeepEqual(before, after) {
			t.Fatal("非法费用修改资产")
		}
	}
}

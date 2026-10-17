package inventory

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"reflect"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func wornEnchantFixture(t *testing.T, size int) (dnfrepo.Group, Command) {
	t.Helper()
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	saveEnchantFixture(t, ctx, g, 2, 0, 123)
	raw := make([]byte, size)
	if size == currentItemListEntrySize {
		binary.LittleEndian.PutUint32(raw[2:6], 700)
		binary.LittleEndian.PutUint32(raw[6:10], 999999998)
	} else {
		binary.LittleEndian.PutUint32(raw[12:16], 999999998)
	}
	if err := g.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "77", Entries: map[string]dnfrepo.EquipmentEntry{
		"14": {SlotIndex: 14, ItemID: 700, RawEntry: raw, Extra: map[string]string{"equipment_type": "[coat]", "quality_seed": "999999998", "keep": "不变"}},
	}}); err != nil {
		t.Fatal(err)
	}
	cmd := newEnchantTestCommand(12, 14)
	cmd.TargetListType = listTypeEquipment
	return g, cmd
}

func TestWornEquipmentEnchantPersistsWithoutTouchingBackpackTarget(t *testing.T) {
	for _, test := range []struct {
		size   int
		legacy bool
	}{{43, false}, {currentItemListEntrySize, false}, {43, true}} {
		size := test.size
		t.Run(strconv.Itoa(size)+strconv.FormatBool(test.legacy), func(t *testing.T) {
			g, cmd := wornEnchantFixture(t, size)
			ctx := context.Background()
			key := "14"
			if test.legacy {
				eq, _, _ := g.Equipment.Load(ctx, "77")
				e := eq.Entries["14"]
				delete(eq.Entries, "14")
				e.SlotIndex = 13
				e.Extra["source"] = "pvf_create_equipment_list"
				eq.Entries["13"] = e
				key = "13"
				if err := g.Equipment.Save(ctx, eq); err != nil {
					t.Fatal(err)
				}
			}
			before, _, _ := g.Inventory.Load(ctx, "77")
			o, _ := NewOwner(g)
			result, err := o.Enchant(ctx, cmd, coatEnchantResolver(9001, nil))
			if err != nil || !result.Success || !result.WornEquipmentTarget || result.BeadRemainingCount != 1 {
				t.Fatalf("穿戴附魔未提交：%+v %v", result, err)
			}
			after, _, _ := g.Inventory.Load(ctx, "77")
			if !reflect.DeepEqual(before.Slots["0:30"], after.Slots["0:30"]) || after.Slots["0:12"].Count != 1 {
				t.Fatal("背包目标被误改或扣材错误")
			}
			eq, _, _ := g.Equipment.Load(ctx, "77")
			encoded, _ := json.Marshal(eq)
			var cold dnfrepo.EquipmentRecord
			if err := json.Unmarshal(encoded, &cold); err != nil {
				t.Fatal(err)
			}
			e := cold.Entries[key]
			offset := 0x10
			if size == currentItemListEntrySize {
				offset = 0x0e
			}
			if e.Extra["enchant_card_id"] != "9001" || e.Extra["quality_seed"] != "999999998" || e.Extra["keep"] != "不变" || binary.LittleEndian.Uint32(e.RawEntry[offset:offset+4]) != 9001 || len(e.RawEntry) != size {
				t.Fatal("附魔、品质或旧行布局未正确持久化")
			}
			response := ownerAppliedEnchantResult(ctx, g.Settings, 272, cmd, result)
			if !reflect.DeepEqual(response.WornEquipmentSlotRefreshes, []int16{14}) || len(response.AuthoritativeListRefreshes) != 0 || len(response.UpperResponses) != 2 || !reflect.DeepEqual(response.UpperResponses[1].Body, []byte{1, 3, 14, 0}) {
				t.Fatal("缺少穿戴权威刷新或附魔结果槽位不符")
			}
		})
	}
}

func TestWornEquipmentEnchantRejectsInvalidTargetsAndRollsBack(t *testing.T) {
	for _, name := range []string{"保存失败", "部位不匹配", "锁定", "装备缺失"} {
		t.Run(name, func(t *testing.T) {
			g, cmd := wornEnchantFixture(t, 43)
			ctx := context.Background()
			resolver := coatEnchantResolver(9001, nil)
			eq, _, _ := g.Equipment.Load(ctx, "77")
			switch name {
			case "保存失败":
				g.CharacterItems = forgingFailingEquipmentSaveUOW{base: g.CharacterItems}
			case "部位不匹配":
				resolver = staticEnchantResolver(alignedcmd.EnchantBeadResolution{CardItemID: 9001, TargetKind: "equipment", AllowedEquipmentTypes: []string{"[ring]"}, TargetEquipmentType: "[coat]"})
			case "锁定":
				e := eq.Entries["14"]
				e.Extra["equipment_lock_state"] = "locked"
				eq.Entries["14"] = e
				if err := g.Equipment.Save(ctx, eq); err != nil {
					t.Fatal(err)
				}
			case "装备缺失":
				cmd.TargetSlotIndex = 15
			}
			before, _, _ := g.Inventory.Load(ctx, "77")
			eqBefore, _, _ := g.Equipment.Load(ctx, "77")
			o, _ := NewOwner(g)
			result, err := o.Enchant(ctx, cmd, resolver)
			if result.Success || (name == "保存失败" && err == nil) {
				t.Fatalf("应当拒绝：%+v %v", result, err)
			}
			after, _, _ := g.Inventory.Load(ctx, "77")
			eqAfter, _, _ := g.Equipment.Load(ctx, "77")
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(eqBefore, eqAfter) {
				t.Fatal("拒绝或存储失败仍留下部分写入")
			}
		})
	}
}

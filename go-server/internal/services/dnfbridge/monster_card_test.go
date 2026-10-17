package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"reflect"
	"testing"

	inventory "longheng.io/server/internal/modules/dnf/inventory"
	pvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestCurrentMonsterCardCommitsBeforeAckThenAuthoritativeRows(t *testing.T) {
	for _, opcode := range []uint16{603, 414} {
		t.Run(fmt.Sprint(opcode), func(t *testing.T) {
			ctx := context.Background()
			r := memory.NewMemoryGroup()
			if e := r.Character.Save(ctx, repo.CharacterRecord{CharacterID: "77", AccountID: "acc", Stats: map[string]int64{"gold": 0}}); e != nil {
				t.Fatal(e)
			}
			s := repo.ItemStack{ItemID: 100, Count: 13, Extra: map[string]string{"item_kind": "stackable", "pvf_path": "stackable/monstercard/100.stk"}}
			repo.SetMonsterCardUpgradeCount(&s, 0)
			if e := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{"0:241": s, "0:68": {ItemID: 900, Count: 1}}}); e != nil {
				t.Fatal(e)
			}
			source := dungeonDropCatalogTestSource{"monster/monster.lst": "", "equipment/equipment.lst": "", "stackable/stackable.lst": "100 `monstercard/100.stk`\n110 `monstercard/110.stk`\n120 `monstercard/120.stk`\n130 `monstercard/130.stk`\n900 `binder.stk`\n", "stackable/binder.stk": "[stackable type] `[expert town potion]`\n[monstercard bind] 2\n", "etc/monstercardcontents.etc": "[upgrade infos]\n[upgrade info] 0 100\n[upgrade info] 1 80\n[upgrade info] 2 60\n[upgrade info] 3 40\n[/upgrade infos]\n[bind result infos]\n[bind result info] 0 1 5 100 1\n[/bind result info]\n[bind result info] 1 3 4 110 1\n[/bind result info]\n[bind result info] 2 12 3 120 1\n[/bind result info]\n[bind result info] 3 0 2 130 1\n[/bind result info]\n[/bind result infos]"}
			for quality := 0; quality < 4; quality++ {
				source[fmt.Sprintf("stackable/monstercard/%d.stk", 100+quality*10)] = fmt.Sprintf("[stackable type] `[material expert job]`\n[item category] `monster card`\n[rarity] %d\n[enchant table]\n[enchant index] 0\n[light attack] 3\n[/enchant index]\n[enchant index] 1\n[light attack] 4\n[/enchant index]\n[/enchant table]", quality)
			}
			catalog, e := newPVFDungeonDropCatalog(source)
			if e != nil {
				t.Fatal(e)
			}
			service := &Service{options: options{accountID: "acc", gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain}, pvfItemCatalog: catalog, repositoryProvider: func() (repo.Group, bool) { return r, true }}
			conn := &bufferConn{}
			session := &gameSession{conn: conn, connID: "card-test", selectedCharacterID: 77}
			body := []byte{0, 241, 0, 1, 0, 241, 0}
			if opcode == 414 {
				body = []byte{68, 0, 2, 241, 0, 241, 0}
			}
			handled, e := service.handleCurrentMonsterCardCommand(session, opcode, body)
			if !handled || e != nil {
				t.Fatal(handled, e)
			}
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if ack.Header.Classification != 1 || ack.Header.MsgID != opcode || len(ack.Body) < 2 || ack.Body[0] != 1 {
				t.Fatal(ack)
			}
			rows, rest := splitGameServerUpperPacket(t, rest)
			if rows.Header.Classification != 0 || rows.Header.MsgID != 14 || rows.Body[0] != 0 {
				t.Fatal(rows)
			}
			if opcode == 603 {
				list, tail := splitGameServerUpperPacket(t, rest)
				if list.Header.Classification != 0 || list.Header.MsgID != 13 || list.Body[0] != 0 || len(tail) != 0 {
					t.Fatal(list, len(tail))
				}
			} else if len(rest) != 0 {
				t.Fatal("合成后存在多余回包")
			}
			inv, _, e := r.Inventory.Load(ctx, "77")
			if e != nil {
				t.Fatal(e)
			}
			total := int64(0)
			for _, stack := range inv.Slots {
				if stack.ItemID != 900 {
					total += stack.Count
				}
			}
			if total != 12 {
				t.Fatal("扣材或产出不正确", inv)
			}
			if opcode == 603 {
				if inv.Slots["0:241"].Count != 11 {
					t.Fatal(inv)
				}
				for _, stack := range inv.Slots {
					if stack.ItemID == 100 && stack.Count == 1 {
						level, ok := repo.MonsterCardUpgradeCount(stack)
						if !ok || level != 1 {
							t.Fatal(stack)
						}
					}
				}
			} else if _, ok := inv.Slots["0:68"]; ok {
				t.Fatal("合成器没有扣除")
			}
			char, _, e := r.Character.Load(ctx, "77")
			if e != nil || char.Stats["gold"] != 0 {
				t.Fatal(char, e)
			}
			conn.write.Reset()
			service.options.accountID = "other"
			before := repo.CloneInventory(inv)
			_, e = service.handleCurrentMonsterCardCommand(session, opcode, body)
			if e != nil {
				t.Fatal(e)
			}
			ack, rest = splitGameServerUpperPacket(t, conn.write.Bytes())
			if !reflect.DeepEqual(ack.Body, []byte{0, 19}) || len(rest) != 0 {
				t.Fatal(ack, len(rest))
			}
			after, _, _ := r.Inventory.Load(ctx, "77")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("错误账号操作资产")
			}
		})
	}
}

func TestCurrentMonsterCardNativeResultLayouts(t *testing.T) {
	r := inventory.MonsterCardResult{Applied: true, Upgraded: true, ItemID: 3624, Level: 1, ResultSlot: 234}
	b := upperSuccessBody(buildCurrentMonsterCardUpgradeResult(r))
	if !reflect.DeepEqual(b, []byte{1, 1, 0x28, 0x0e, 0, 0, 1}) {
		t.Fatal(b)
	}
	r.Upgraded = false
	b = upperSuccessBody(buildCurrentMonsterCardUpgradeResult(r))
	if b[1] != 0 || len(b) != 7 {
		t.Fatal(b)
	}
	for n := 2; n <= 5; n++ {
		c := inventory.MonsterCardCommand{BinderSlot: 68}
		for i := 0; i < n; i++ {
			c.InputSlots = append(c.InputSlots, 242)
		}
		b = upperSuccessBody(buildCurrentMonsterCardCompoundResult(c, r))
		if len(b) != 16+2*n || b[0] != 1 || binary.LittleEndian.Uint16(b[1:3]) != 68 || int(b[3]) != n {
			t.Fatal(b)
		}
		offset := 4
		for i := 0; i < n; i++ {
			if binary.LittleEndian.Uint16(b[offset:offset+2]) != 242 {
				t.Fatal(b)
			}
			offset += 2
		}
		if b[offset] != 1 || binary.LittleEndian.Uint16(b[offset+1:offset+3]) != 234 || binary.LittleEndian.Uint32(b[offset+3:offset+7]) != 3624 || binary.LittleEndian.Uint32(b[offset+7:offset+11]) != 1 || b[offset+11] != 0 {
			t.Fatal(b)
		}
	}
	stack := repo.ItemStack{ItemID: 3624, Count: 1}
	repo.SetMonsterCardUpgradeCount(&stack, 1)
	r.Inventory = repo.InventoryRecord{Slots: map[string]repo.ItemStack{"0:234": stack}}
	r.ChangedSlots = []int16{233, 234}
	rows := currentMonsterCardChangedEntries(r)
	if binary.LittleEndian.Uint32(rows[0].data[2:6]) != ^uint32(0) || binary.LittleEndian.Uint16(rows[0].data[:2]) != 233 || rows[1].data[18] != 1 {
		t.Fatal(rows)
	}
}
func TestCurrentMonsterCardStagesIgnoreAttributeIntegers(t *testing.T) {
	doc, e := pvf.Parse("card.stk", "[enchant table]\n[enchant index] 0\n[light attack] 3\n[/enchant index]\n[enchant index] 1\n[light attack] 4\n[/enchant index]\n[enchant index] 2\n[light attack] 5\n[/enchant index]\n[/enchant table]")
	if e != nil {
		t.Fatal(e)
	}
	if got := currentMonsterCardStages(doc); !reflect.DeepEqual(got, []byte{0, 1, 2}) {
		t.Fatal(got)
	}
}
func TestCurrentMonsterCardRealPVF(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("设置真实PVF路径后核对卡片配置")
	}
	archive, e := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if e != nil {
		t.Fatal(e)
	}
	catalog, e := newPVFDungeonDropCatalog(archive)
	if e != nil {
		t.Fatal(e)
	}
	doc, e := parseDungeonCardPVFDocument(catalog.source, "etc/monstercardcontents.etc")
	if e != nil {
		t.Fatal(e)
	}
	rules, e := parseCurrentMonsterCardRules(doc)
	if e != nil {
		t.Fatal(e)
	}
	for rarity, want := range []int{100, 80, 60, 40} {
		if rules.UpgradePercent[rarity] != want || rules.MaximumInputs[rarity] != 5-rarity || len(rules.Pools[rarity]) == 0 {
			t.Fatal(rules)
		}
	}
	if rules.UpgradePercent[6] != 40 {
		t.Fatalf("传说升级概率：%v", rules.UpgradePercent)
	}
	if _, ok := rules.Pools[6]; ok {
		t.Fatal("当前用户要求传说不参与合成")
	}
	legend, e := resolveCurrentMonsterCard(catalog, 10159184)
	if e != nil || !legend.IsCard || legend.Rarity != 6 || !reflect.DeepEqual(legend.Stages, []byte{0, 1, 2}) {
		t.Fatal(legend, e)
	}
	t.Logf("制造者卢克(光)10159184，真实品质6，级数%v，升级概率%d%%，不参与合成", legend.Stages, rules.UpgradePercent[6])
	binder, e := resolveCurrentMonsterCard(catalog, 2682575)
	if e != nil || !binder.IsBinder || binder.IsCard {
		t.Fatal(binder, e)
	}
	for _, id := range []int64{3606, 3607, 3616, 3624, 3798, 10159164} {
		def, e := resolveCurrentMonsterCard(catalog, id)
		if e != nil || !def.IsCard || len(def.Stages) == 0 || def.Policy.Start != 233 {
			t.Fatal(def, e)
		}
		t.Logf("卡片%d 品质%d 实际级数%v", id, def.Rarity, def.Stages)
	}
	// 逐个核对真实候选池，配置里不存在的卡或品质不符不能发给玩家。
	for rarity, pool := range rules.Pools {
		for _, entry := range pool {
			def, e := resolveCurrentMonsterCard(catalog, entry.ItemID)
			if e != nil || !def.IsCard || def.Rarity != rarity || len(def.Stages) == 0 || def.Stages[0] != 0 {
				t.Fatalf("品质%d 奖励%d：%+v %v", rarity, entry.ItemID, def, e)
			}
		}
	}
}

func TestCurrentMonsterCardEnchantStageCompatibility(t *testing.T) {
	for _, nested := range []bool{false, true} {
		for _, direct := range []bool{false, true} {
			t.Run(fmt.Sprintf("嵌套表=%t直接卡片=%t", nested, direct), func(t *testing.T) {
				catalog, source := newEnchantBeadTestCatalog(t)
				table := "[enchant table]\n0 1 2\n[/enchant table]\n"
				if nested {
					table = "[enchant table]\n[enchant index] 0\n[physical attack] 75\n[/enchant index]\n[enchant index] 1\n[physical attack] 100\n[/enchant index]\n[enchant index] 2\n[physical attack] 125\n[/enchant index]\n[/enchant table]\n"
				}
				source["stackable/card/fire_card.stk"] = "[name] `测试卡片`\n[string data] `icon.img` `[coat]` `[weapon]`\n" + table
				resolution, err := resolveCurrentEnchantBeadMetadata(catalog, source, 8801, 700)
				if direct {
					resolution, err = resolveCurrentEnchantCardMetadata(catalog, source, 9001, 700)
				}
				if err != nil || !reflect.DeepEqual(resolution.UpgradeCounts, []int64{0, 1, 2}) {
					t.Fatal("卡片附魔级数混入属性值或旧平铺表丢失", resolution, err)
				}
			})
		}
	}
}

package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfsvc "longheng.io/server/internal/modules/dnf/equipmentservice"
	r "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func equipmentFollowupSetExistingEmblems(target *r.ItemStack) {
	vector := make([]byte, currentEquipmentEmblemDataBytes)
	vector[0] = 2
	binary.LittleEndian.PutUint32(vector[1:], 5001)
	binary.LittleEndian.PutUint32(vector[5:], 5002)
	copy(target.RawEntry[currentEquipmentVectorOffset:], vector)
	currentSetHexExtra(target.Extra, "equipment_emblem_data", vector)
	currentSetHexExtra(target.Extra, "equipment_socket_data", vector)
}

func TestEquipmentFollowupNativeWaxErrorsAndSilentNPCValidation(t *testing.T) {
	items, err := newPVFDungeonDropCatalog(npcEquipmentSource())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		code byte
	}{{"材料不足", 0x16}, {"剩余次数为0", 0x0d}, {"已经封装", 0x12}, {"目标锁定", 0xd6}} {
		for _, mode := range []uint16{0, 1} {
			t.Run(fmt.Sprintf("%s_模式%d", tc.name, mode), func(t *testing.T) {
				s, session, conn, g := npcEquipmentFixture(t, items, 3000, 6200, 6100)
				ctx := context.Background()
				i, _, _ := g.Inventory.Load(ctx, "19")
				a, _, _ := g.AccountInventory.Load(ctx, "a")
				target := i.Slots["0:9"]
				switch tc.name {
				case "材料不足":
					wax := i.Slots["0:65"]
					wax.Count = 1
					i.Slots["0:65"] = wax
					a.Slots = map[string]r.ItemStack{}
				case "剩余次数为0":
					target.RawEntry[10] = (target.RawEntry[10] & 31) | 0xe0
					target.Extra["reseal_count"] = "7"
					target.Extra["ext_data0"] = strconv.Itoa(int(target.RawEntry[10]))
				case "已经封装":
					target.RawEntry[13] = 1
					target.Extra["seal_flag"] = "1"
				case "目标锁定":
					target.Extra["equipment_lock_state"] = "1"
				}
				i.Slots["0:9"] = target
				if err := g.Inventory.Save(ctx, i); err != nil {
					t.Fatal(err)
				}
				if err := g.AccountInventory.Save(ctx, a); err != nil {
					t.Fatal(err)
				}
				i, _, _ = g.Inventory.Load(ctx, "19")
				a, _, _ = g.AccountInventory.Load(ctx, "a")
				if err := s.handleCurrentResetItemAttr(session, npcEquipmentBody81(9, 3000, 65, mode)); err != nil {
					t.Fatal(err)
				}
				if mode == 1 {
					if conn.write.Len() != 0 {
						t.Fatal("NPC校验失败不应发错误包")
					}
				} else {
					ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
					if len(rest) != 0 || ack.Header.MsgID != 81 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{0, tc.code}) {
						t.Fatalf("原生蜜蜡提示不符：body=%x want=00%02x", ack.Body, tc.code)
					}
				}
				after, _, _ := g.Inventory.Load(ctx, "19")
				shared, _, _ := g.AccountInventory.Load(ctx, "a")
				if !reflect.DeepEqual(i, after) || !reflect.DeepEqual(a, shared) {
					t.Fatal("校验拒绝仍扣材或改装备")
				}
			})
		}
	}
	for _, kind := range []string{"材料不足", "接收自己", "来源不存在"} {
		t.Run("NPC跨界_"+kind, func(t *testing.T) {
			s, session, conn, g := npcEquipmentFixture(t, items, 3000, 6200, 6100)
			ctx := context.Background()
			i, _, _ := g.Inventory.Load(ctx, "19")
			body := npcEquipmentBody557(2)
			switch kind {
			case "材料不足":
				delete(i.Slots, "0:121")
			case "接收自己":
				binary.LittleEndian.PutUint32(body[5:], 0)
			case "来源不存在":
				delete(i.Slots, "0:9")
			}
			if err := g.Inventory.Save(ctx, i); err != nil {
				t.Fatal(err)
			}
			shared, _, _ := g.AccountInventory.Load(ctx, "a")
			if err := s.handleCurrentEquipmentTransfer(session, body); err != nil {
				t.Fatal(err)
			}
			if conn.write.Len() != 0 {
				t.Fatal("NPC跨界拒绝不应额外发错误包")
			}
			after, _, _ := g.Inventory.Load(ctx, "19")
			a, _, _ := g.AccountInventory.Load(ctx, "a")
			m, _, _ := g.Mailbox.Load(ctx, "42")
			if !reflect.DeepEqual(i, after) || !reflect.DeepEqual(shared, a) || len(m.Mails) != 0 {
				t.Fatal("NPC跨界拒绝修改资产")
			}
		})
	}
}

func TestEquipmentFollowupTransferAndMailClaimKeepInstance(t *testing.T) {
	items, err := newPVFDungeonDropCatalog(npcEquipmentSource())
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []byte{0, 2} {
		for _, dur := range []uint16{0, 13} {
			for _, rawOnly := range []bool{false, true} {
				t.Run(fmt.Sprintf("模式%d_耐久%d_旧格式%t", mode, dur, rawOnly), func(t *testing.T) {
					s, session, conn, g := npcEquipmentFixture(t, items, 3000, 6200, 6100)
					ctx := context.Background()
					i, _, _ := g.Inventory.Load(ctx, "19")
					target := i.Slots["0:9"]
					binary.LittleEndian.PutUint32(target.RawEntry[6:], 462346698)
					binary.LittleEndian.PutUint16(target.RawEntry[11:], dur)
					target.RawEntry[10] = 0xeb
					binary.LittleEndian.PutUint32(target.RawEntry[14:], 777)
					target.Extra["quality_seed"] = "462346698"
					target.Extra["durability"] = fmt.Sprint(dur)
					target.Extra["max_durability"] = "40"
					target.Extra["ext_data0"] = "235"
					target.Extra["reseal_count"] = "7"
					target.Extra["value_a"] = "777"
					target.Extra["enchant_card"] = "777"
					target.Extra["forge"] = "8"
					target.Extra["reinforce"] = "11"
					target.Extra["avatar_socket_data"] = "0100020089130000030090130000"
					equipmentFollowupSetExistingEmblems(&target)
					if rawOnly {
						for _, key := range []string{"quality_seed", "durability", "max_durability", "ext_data0", "reseal_count"} {
							delete(target.Extra, key)
						}
					}
					i.Slots["0:9"] = target
					if err := g.Inventory.Save(ctx, i); err != nil {
						t.Fatal(err)
					}
					if err := s.handleCurrentEquipmentTransfer(session, npcEquipmentBody557(mode)); err != nil {
						t.Fatal(err)
					}
					ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
					if ack.Header.MsgID != 557 || ack.Body[0] != 1 {
						t.Fatal("跨界未成功", ack)
					}
					var got r.ItemStack
					slot := int16(0)
					if mode == 0 {
						a, _, _ := g.AccountInventory.Load(ctx, "a")
						got = a.Slots["12:0"]
					} else {
						if err := g.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "42", Slots: map[string]r.ItemStack{}}); err != nil {
							t.Fatal(err)
						}
						m, _, _ := g.Mailbox.Load(ctx, "42")
						if len(m.Mails) != 1 {
							t.Fatal("未收到跨界邮件")
						}
						var mailID uint32
						for id := range m.Mails {
							n, err := strconv.ParseUint(id, 10, 32)
							if err != nil {
								t.Fatal(err)
							}
							mailID = uint32(n)
						}
						if _, err := s.reconcileCurrentMailAttachmentExpirations(ctx, 42); err != nil {
							t.Fatal(err)
						}
						// 第二次打开邮箱必须保持实例证据，不能修成新品级或满耐久。
						again, _, _ := g.Mailbox.Load(ctx, "42")
						if _, err := s.reconcileCurrentMailAttachmentExpirations(ctx, 42); err != nil {
							t.Fatal(err)
						}
						same, _, _ := g.Mailbox.Load(ctx, "42")
						if !reflect.DeepEqual(again, same) {
							t.Fatal("重复打开邮箱改变装备")
						}
						receiver := &gameSession{conn: &bufferConn{}, selectedCharacterID: 42}
						s.bindGameSessionCharacter(receiver, 42)
						handled, err := s.handleAlignedGameCommand(receiver, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketMailboxExtractItem), mailIDListBody(mailID))
						if err != nil || !handled {
							t.Fatal("领取跨界邮件失败", handled, err)
						}
						inv, _, _ := g.Inventory.Load(ctx, "42")
						slot = 9
						got = inv.Slots["0:9"]
						if got.Extra["quality_seed"] != "462346698" || got.Extra["durability"] != fmt.Sprint(dur) {
							t.Fatal("领取后重置品级或当前耐久", got.Extra)
						}
					}
					if got.ItemID != 3000 || len(got.RawEntry) != 119 {
						t.Fatal("跨界装备未落袋", got)
					}
					want := append([]byte(nil), target.RawEntry...)
					binary.LittleEndian.PutUint16(want, uint16(slot))
					if !bytes.Equal(got.RawEntry, want) {
						t.Fatalf("跨界/领取改变原始装备：got=%x want=%x", got.RawEntry, want)
					}
					for _, key := range []string{"value_a", "enchant_card", "forge", "reinforce", "avatar_socket_data", "equipment_emblem_data", "equipment_socket_data"} {
						if got.Extra[key] != target.Extra[key] {
							t.Fatal("丢失原有实例属性", key)
						}
					}
					row := currentItemListEntryFromStack(0, slot, got)
					assertCurrentEquipmentVectorEquals(t, target.RawEntry, row.data[:])
					if binary.LittleEndian.Uint32(row.data[6:]) != 462346698 || binary.LittleEndian.Uint16(row.data[11:]) != dur || row.data[10] != 0xeb {
						t.Fatal("刷新/重登投影改变品级、耐久或次数")
					}
				})
			}
		}
	}
}

func TestEquipmentFollowupNumericTablesRejectBrokenPrices(t *testing.T) {
	for _, kind := range []string{"封装等级半行", "封装等级重复", "封装品质漏值", "封装字符串", "跨界结晶半行", "跨界品质重复", "跨界类型重复", "跨界名单损坏", "跨界额外费用无效"} {
		t.Run(kind, func(t *testing.T) {
			source := npcEquipmentSource()
			path := dnfsvc.ResealConfigPath
			switch kind {
			case "封装等级半行":
				source[path] = strings.Replace(source[path], "1 90 4", "1 90", 1)
			case "封装等级重复":
				source[path] += "[grade]\n1 90 4\n"
			case "封装品质漏值":
				source[path] = strings.Replace(source[path], "1 2 1 2 8 8 6", "1 2 1 2 8 8", 1)
			case "封装字符串":
				source[path] = strings.Replace(source[path], "1 2 1 2 8 8 6", "1 2 `bad` 2 8 8 6", 1)
			case "跨界结晶半行":
				path = dnfsvc.TransferConfigPath
				source[path] = strings.Replace(source[path], "3167 3 2 2 2 2", "3167 3 2 2 2", 1)
			case "跨界品质重复":
				path = dnfsvc.TransferConfigPath
				source[path] = strings.Replace(source[path], "3 2\n", "3 2 3 2\n", 1)
			case "跨界类型重复":
				path = dnfsvc.TransferConfigPath
				source[path] += source[path]
			case "跨界名单损坏":
				path = dnfsvc.TransferConfigPath
				source[path] = strings.Replace(source[path], "[/stone type]", "[cargo transport stone include index]\n`bad`\n[/stone type]", 1)
			case "跨界额外费用无效":
				path = dnfsvc.TransferConfigPath
				source[path] = strings.Replace(source[path], "[/stone type]", "[cargo transport stone particular cost]\n4 90 10158964 -1\n[/stone type]", 1)
			}
			var err error
			if path == dnfsvc.ResealConfigPath {
				_, err = dnfsvc.LoadResealConfig(source)
			} else {
				_, err = dnfsvc.LoadTransferConfig(source)
			}
			if err == nil {
				t.Fatal("损坏费用表仍被加载")
			}
		})
	}
}

func TestRealPVFEquipmentFollowupNativeWaxAndInstance(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("设置真实PVF后核对黄金蜜蜡和粉装实例")
	}
	source, err := platformpvf.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dnfsvc.LoadTransferConfig(source); err != nil {
		t.Fatal(err)
	}
	if _, err := dnfsvc.LoadResealConfig(source); err != nil {
		t.Fatal(err)
	}
	def, err := (currentNPCEquipmentCatalog{items}).Resolve(28309)
	if err != nil {
		t.Fatal(err)
	}
	if def.Rarity != 3 {
		t.Fatal("真实粉装品质不符", def)
	}
	for _, mode := range []uint16{0, 1} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			s, session, conn, g := npcEquipmentFixture(t, items, 28309, 14, 0)
			ctx := context.Background()
			i, _, _ := g.Inventory.Load(ctx, "19")
			target := i.Slots["0:9"]
			target.Bind = true
			binary.LittleEndian.PutUint32(target.RawEntry[6:], 462346698)
			binary.LittleEndian.PutUint16(target.RawEntry[11:], 13)
			target.Extra["quality_seed"] = "462346698"
			target.Extra["durability"] = "13"
			target.Extra["enchant_card"] = "777"
			target.Extra["avatar_socket_data"] = "0100020089130000"
			equipmentFollowupSetExistingEmblems(&target)
			i.Slots["0:9"] = target
			if err := g.Inventory.Save(ctx, i); err != nil {
				t.Fatal(err)
			}
			if err := s.handleCurrentResetItemAttr(session, npcEquipmentBody81(9, 28309, 65, mode)); err != nil {
				t.Fatal(err)
			}
			assertNPCSealPackets(t, conn.write.Bytes(), mode == 1)
			after, _, _ := g.Inventory.Load(ctx, "19")
			got := after.Slots["0:9"]
			want := append([]byte(nil), target.RawEntry...)
			want[10] = (want[10] & 31) | 32
			want[13] = 1
			if got.Bind || !bytes.Equal(got.RawEntry, want) || got.Extra["enchant_card"] != "777" || got.Extra["avatar_socket_data"] != target.Extra["avatar_socket_data"] {
				t.Fatal("真实粉装再封装丢失实例或仍绑定")
			}
			row := currentItemListEntryFromStack(0, 9, got)
			assertCurrentEquipmentVectorEquals(t, target.RawEntry, row.data[:])
		})
	}
}

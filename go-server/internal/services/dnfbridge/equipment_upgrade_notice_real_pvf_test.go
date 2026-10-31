package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/inventory"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 实际PVF决定装备、材料、播报门槛和惩罚；只固定掷点和费用，避免随机回归。
// 领域事务产生播报事实后走真实桥接发包，观察者不能收到ACK、钱包或背包行。
func TestRealPVFEquipmentUpgradeNoticeChannelRules(t *testing.T) {
	path := realForgingPVFPath(t)
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"reinforce", "amplify"} {
		for _, succeeded := range []bool{true, false} {
			name := mode + "_failure"
			if succeeded {
				name = mode + "_success"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				s, source, _, self, peer := newCurrentSocialPeerTest(t)
				s.initialEquipmentArchive = archive
				repos, _ := s.repositoryGroup()
				resolve := s.alignedUpgradePolicyResolver()
				first, err := resolve(mode, 28309, 0)
				if err != nil || first.NoticeLevel < 1 || first.NoticeLevel > 20 {
					t.Fatalf("真实播报门槛=%+v err=%v", first, err)
				}
				old := first.NoticeLevel
				if succeeded {
					old--
				}
				policy, err := resolve(mode, 28309, old)
				if err != nil {
					t.Fatal(err)
				}
				target := realForgingWeaponStack(28309, 45)
				target.RawEntry[0x0a] = byte(old)
				target.RawEntry[0x47], target.RawEntry[0x48], target.RawEntry[0x4b], target.RawEntry[0x4e] = 1, 2, 9, 10
				target.RawEntry[0x51], target.RawEntry[0x52] = 3, 0xff
				if mode == "amplify" {
					target.Extra["amplify_type"], target.Extra["amplify_value"] = "3", "5"
				}
				char, _, _ := repos.Character.Load(ctx, "1")
				char.Stats = map[string]int64{"gold": 1000000}
				if err := repos.Character.Save(ctx, char); err != nil {
					t.Fatal(err)
				}
				materialSlot := uint16(121)
				bag := dnfrepo.InventoryRecord{CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{"0:9": target, "0:121": {ItemID: int64(policy.MaterialItemID), Count: 10000}}}
				if mode == "reinforce" {
					if policy.MaterialItemID != 3037 {
						t.Fatalf("夹具材料身份变化：%d", policy.MaterialItemID)
					}
					materialSlot = 358
					delete(bag.Slots, "0:121")
					if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: source.accountID, Slots: map[string]dnfrepo.ItemStack{dnfrepo.AccountSharedInventorySlotKey(358): {ItemID: 3037, Count: 10000}}}); err != nil {
						t.Fatal(err)
					}
				}
				if err := repos.Inventory.Save(ctx, bag); err != nil {
					t.Fatal(err)
				}
				body := make([]byte, 16)
				if mode == "amplify" {
					binary.LittleEndian.PutUint16(body, 1)
				}
				binary.LittleEndian.PutUint16(body[2:], 9)
				binary.LittleEndian.PutUint32(body[4:], 28309)
				binary.LittleEndian.PutUint16(body[8:], materialSlot)
				binary.LittleEndian.PutUint16(body[10:], 65535)
				result, err := inventory.NewHandler().Handle(ctx, alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketUpgradeItem), Body: body, AccountID: source.accountID, SelectedCharacterID: 1, Repositories: repos,
					UpgradePolicyResolver: func(m string, id int64, level int) (alignedcmd.UpgradePolicyResolution, error) {
						p, e := resolve(m, id, level)
						p.SuccessWeight = 0
						if succeeded {
							p.SuccessWeight = 100000
						}
						return p, e
					}, UpgradeCostResolver: func(string, int64, int, bool, uint16) (alignedcmd.UpgradeCostResolution, error) {
						return alignedcmd.UpgradeCostResolution{Resolved: true, GoldCost: 100}, nil
					},
				})
				if err != nil || result.EquipmentUpgradeNotice == nil {
					t.Fatalf("未产生已提交播报 reason=%s err=%v", result.Reason, err)
				}
				fact := result.EquipmentUpgradeNotice
				if fact.Succeeded != succeeded || int(fact.Level) != first.NoticeLevel || len(fact.Body) != 15 || fact.Body[14] != 0xff {
					t.Fatalf("播报事实=%+v", fact)
				}
				if err := s.sendAlignedUpperResponses(source, result); err != nil {
					t.Fatal(err)
				}
				ack, _ := splitGameServerUpperPacket(t, self.write.Bytes())
				if ack.Header.MsgID != uint16(dnfenum.CmdPacketUpgradeItem) || ack.Header.Classification != 1 || ack.Body[0] != 1 {
					t.Fatalf("事务应答缺失 header=%+v body=%x", ack.Header, ack.Body)
				}
				packet, rest := splitGameServerUpperPacket(t, peer.write.Bytes())
				if packet.Header.MsgID != 86 || packet.Header.Classification != 0 || !bytes.Equal(packet.Body, fact.Body) || len(rest) != 0 {
					t.Fatal("观察者播报缺失或泄露资产包")
				}
				stored, _, _ := repos.Character.Load(ctx, "1")
				if stored.Stats["gold"] != 999900 {
					t.Fatal("播报影响资产扣费")
				}
				t.Logf("实际PVF mode=%s notice=%d success=%t body=%x", mode, first.NoticeLevel, succeeded, fact.Body)
			})
		}
	}
}

func TestRealPVFEquipmentForgingNoticeTransactionAndChannel(t *testing.T) {
	path := realForgingPVFPath(t)
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	table, err := loadCurrentForgingTable(archive)
	if err != nil {
		t.Fatal(err)
	}
	resolve := currentForgingResolver(catalog, archive, table)
	for _, succeeded := range []bool{true, false} {
		name := "failure"
		if succeeded {
			name = "success"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s, source, _, _, peer := newCurrentSocialPeerTest(t)
			repos, _ := s.repositoryGroup()
			target := realForgingWeaponStack(realForgingWeaponItemID, 48)
			target.RawEntry[0x57] = 5
			if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{"0:9": target, "0:121": realForgingStack(realForgingBreathItemID, 10000, 0, nil)}}); err != nil {
				t.Fatal(err)
			}
			result, err := inventory.NewHandler().Handle(ctx, alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketUpgradeItemSeparate), AccountID: source.accountID, SelectedCharacterID: 1, Repositories: repos,
				Forging: &alignedcmd.ForgingRequest{List: 0, Slot: 9, ItemID: realForgingWeaponItemID, MaterialSlot: 121},
				ForgingResolver: func(material, item int64) (alignedcmd.ForgingResolution, error) {
					r, e := resolve(material, item)
					r.Levels = append([]alignedcmd.ForgingLevelRow(nil), r.Levels...)
					for i := range r.Levels {
						r.Levels[i].SuccessWeight = 0
						if succeeded {
							r.Levels[i].SuccessWeight = 10000
						}
					}
					return r, e
				},
			})
			if err != nil || result.EquipmentUpgradeNotice == nil || result.Forging == nil || result.Forging.Rejected {
				t.Fatalf("锻造提交=%+v err=%v", result, err)
			}
			if err := s.sendAlignedUpperResponses(source, result); err != nil {
				t.Fatal(err)
			}
			packet, rest := splitGameServerUpperPacket(t, peer.write.Bytes())
			wantLevel := byte(5)
			if succeeded {
				wantLevel = 6
			}
			if packet.Header.MsgID != 86 || len(packet.Body) != 11 || packet.Body[0] != 14 || packet.Body[9] != wantLevel || len(rest) != 0 {
				t.Fatal("锻造观察者投影不符")
			}
			stored, _, _ := repos.Inventory.Load(ctx, "1")
			if stored.Slots["0:9"].RawEntry[0x57] != wantLevel || stored.Slots["0:121"].Count >= 10000 {
				t.Fatal("播报与提交资产不一致")
			}
		})
	}
}

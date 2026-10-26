package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/premium"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func premiumPackageFixture(t *testing.T, mixed bool) (*Service, *gameSession, *pvfDungeonDropCatalog, r.Group) {
	t.Helper()
	normal := ""
	if mixed {
		normal = " 600 1"
	}
	source := dungeonDropCatalogTestSource{
		"monster/monster.lst": "", "equipment/equipment.lst": "",
		"stackable/stackable.lst":              "500 `cash/contracts.stk`\n501 `cash/contracts_select.stk`\n502 `cash/contracts_package.stk`\n30 `cash/overequip.stk`\n43 `cash/overskill.stk`\n600 `material/normal.stk`\n",
		"stackable/cash/contracts.stk":         "[stackable type]\n`[booster]` 0\n[stack limit]\n100\n[booster info]\n[etc]\n1 30 10000 2\n[/etc]\n[/booster info]\n",
		"stackable/cash/contracts_select.stk":  "[stackable type]\n`[booster selection]` 0\n[stack limit]\n100\n[booster selection num]\n1\n[booster select category]\n0 0\n[etc]\n43 3\n[/etc]\n[/booster select category]\n",
		"stackable/cash/contracts_package.stk": "[stackable type]\n`[usable cera package]`\n[stack limit]\n100\n[package data]\n30 2 43 3" + normal + "\n[/package data]\n",
		"stackable/cash/overequip.stk":         "[stackable type]\n`[waste]`\n[stack limit]\n100\n",
		"stackable/cash/overskill.stk":         "[stackable type]\n`[waste]`\n[stack limit]\n100\n",
		"stackable/material/normal.stk":        "[stackable type]\n`[material]`\n[stack limit]\n1\n",
	}
	c, e := newPVFDungeonDropCatalog(source)
	if e != nil {
		t.Fatal(e)
	}
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	slots := map[string]r.ItemStack{}
	for slot := 3; slot < 354; slot++ {
		slots[fmt.Sprintf("0:%d", slot)] = r.ItemStack{ItemID: 999, Count: 1}
	}
	for _, e := range []error{g.Account.Save(ctx, r.AccountRecord{AccountID: "account-1"}), g.Character.Save(ctx, r.CharacterRecord{CharacterID: "19", AccountID: "account-1"}), g.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "19", Slots: slots})} {
		if e != nil {
			t.Fatal(e)
		}
	}
	s := currentBoosterTestService(c, g)
	s.premiumCatalog = &currentPremiumCatalog{contractsByItem: map[int64]currentPremiumContractInfo{30: {ItemID: 30, PremiumType: premium.TypeOverEquip, DurationSeconds: 86400}, 43: {ItemID: 43, PremiumType: premium.TypeOverSkill, DurationSeconds: 3 * 86400}}}
	return s, &gameSession{selectedCharacterID: 19, conn: &bufferConn{}}, c, g
}

func premiumPackageSource(t *testing.T, g r.Group, id int64) {
	t.Helper()
	ctx := context.Background()
	inv, _, _ := g.Inventory.Load(ctx, "19")
	inv.Slots["0:75"] = r.ItemStack{ItemID: id, Count: 2}
	if e := g.Inventory.Save(ctx, inv); e != nil {
		t.Fatal(e)
	}
}

func TestPremiumPackageBoosterAndSelectionActivateWithFullBag(t *testing.T) {
	for _, selected := range []bool{false, true} {
		t.Run(fmt.Sprint(selected), func(t *testing.T) {
			s, session, c, g := premiumPackageFixture(t, false)
			id := uint32(500)
			kind := currentBoosterRequestRandom
			wantType := premium.TypeOverEquip
			seconds := int64(2 * 86400)
			req := currentBoosterOpenRequest{SourceSlot: 75}
			if selected {
				id = 501
				kind = currentBoosterRequestSelection
				req.Kind = kind
				req.SelectedItemID = 43
				req.Selections = []currentBoosterSelectionRequest{{ItemID: 43}}
				wantType = premium.TypeOverSkill
				seconds = 9 * 86400
			}
			premiumPackageSource(t, g, int64(id))
			def, e := resolveCurrentBoosterDefinition(c, id, kind)
			if e != nil {
				t.Fatal(e)
			}
			before := time.Now().Unix()
			got, e := s.commitCurrentBooster(context.Background(), session, c, def, req)
			if e != nil {
				t.Fatal(e)
			}
			account, _, _ := g.Account.Load(context.Background(), "account-1")
			expiry := premium.ExpireAt(account, wantType)
			if len(got.ContractActivations) != 1 || expiry < before+seconds || expiry > time.Now().Unix()+seconds {
				t.Fatal("契约时长错误", got, account)
			}
			inv, _, _ := g.Inventory.Load(context.Background(), "19")
			if inv.Slots["0:75"].Count != 1 || len(inv.Slots) != 351 {
				t.Fatal("礼包未扣或契约占格", inv.Slots["0:75"], len(inv.Slots))
			}
			for _, v := range inv.Slots {
				if v.ItemID == 30 || v.ItemID == 43 {
					t.Fatal("契约进背包")
				}
			}
		})
	}
}

func TestRealPVFPremiumPackagesAndMailActivateWithoutSlots(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("未指定真实PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	contracts, err := buildCurrentPremiumCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint32{490700040, 490700043, 490700081, 2682804, 2682805} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			s, session, _, g := premiumPackageFixture(t, false)
			s.pvfItemCatalog, s.initialEquipmentArchive, s.premiumCatalog = catalog, archive, contracts
			premiumPackageSource(t, g, int64(id))
			definition, err := resolveCurrentBoosterDefinition(catalog, id, currentBoosterRequestRandom)
			if err != nil {
				t.Fatal(err)
			}
			result, err := s.commitCurrentBooster(context.Background(), session, catalog, definition, currentBoosterOpenRequest{SourceSlot: 75})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.ContractActivations) == 0 {
				t.Fatal("真实契约礼包未直接激活", result)
			}
			account, _, _ := g.Account.Load(context.Background(), "account-1")
			for _, activation := range result.ContractActivations {
				info, found := contracts.contractsByItem[activation.ItemID]
				if !found || activation.PremiumType != info.PremiumType || premium.ExpireAt(account, info.PremiumType) < activation.ExpireAt {
					t.Fatal("真实PVF映射未落账", activation)
				}
			}
			inventory, _, _ := g.Inventory.Load(context.Background(), "19")
			if len(inventory.Slots) != 351 || inventory.Slots["0:75"].Count != 1 {
				t.Fatal("真实礼包契约占用空格")
			}
			box, _, _ := g.Mailbox.Load(context.Background(), "19")
			if len(box.Mails) != 0 {
				t.Fatal("真实契约进邮件")
			}
			t.Logf("真实礼包=%d 名称=%s 路径=%s 激活=%+v", id, definition.Source.Name, definition.Source.PVFPath, result.ContractActivations)
		})
	}
	for _, id := range []int64{30, 43, 490700031} {
		t.Run(fmt.Sprintf("邮件%d", id), func(t *testing.T) {
			s, session, _, g := premiumPackageFixture(t, false)
			s.pvfItemCatalog, s.initialEquipmentArchive, s.premiumCatalog = catalog, archive, contracts
			_, err := r.AppendSystemMail(context.Background(), g.Mailbox, r.SystemMailDelivery{RecipientCharacterID: "19", CreatedAt: time.Now(), Source: "cera_package_main_inventory_full", Attachments: []r.MailAttachment{{ItemID: id, Count: 2}}})
			if err != nil {
				t.Fatal(err)
			}
			body := make([]byte, 8)
			binary.LittleEndian.PutUint32(body, 1)
			binary.LittleEndian.PutUint32(body[4:], 1)
			if err := s.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketMailboxExtractItem), body); err != nil {
				t.Fatal(err)
			}
			data := session.conn.(*bufferConn).write.Bytes()
			notice := false
			for len(data) > 0 {
				packet, rest := splitGameServerUpperPacket(t, data)
				data = rest
				if packet.Header.MsgID == 95 && packet.Body[0] != 1 {
					t.Fatal("满包契约领取失败", packet)
				}
				if packet.Header.Classification == 0 && packet.Header.MsgID == currentPremiumActivatedMsgID {
					notice = true
				}
			}
			account, _, _ := g.Account.Load(context.Background(), "account-1")
			info, found := contracts.contractsByItem[id]
			if !found || !notice || premium.ExpireAt(account, info.PremiumType) < time.Now().Unix()+2*info.DurationSeconds-2 {
				t.Fatal("真实契约领取后未即时激活", id, notice, account)
			}
			inv, _, _ := g.Inventory.Load(context.Background(), "19")
			if len(inv.Slots) != 351 {
				t.Fatal("邮件契约占用空格")
			}
		})
	}
}

func TestPremiumPackageCeraContractsNeverOverflowToMail(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		t.Run(fmt.Sprint(mixed), func(t *testing.T) {
			s, session, c, g := premiumPackageFixture(t, mixed)
			premiumPackageSource(t, g, 502)
			def, e := resolveCurrentCeraPackageDefinition(c, 502)
			if e != nil {
				t.Fatal(e)
			}
			before := time.Now().Unix()
			got, e := s.commitCurrentCeraPackage(context.Background(), session, c, def, currentCeraPackageOpenRequest{SourceSlot: 75})
			if e != nil {
				t.Fatal(e)
			}
			a, _, _ := g.Account.Load(context.Background(), "account-1")
			if len(got.ContractActivations) != 2 || premium.ExpireAt(a, premium.TypeOverEquip) < before+2*86400 || premium.ExpireAt(a, premium.TypeOverSkill) < before+9*86400 {
				t.Fatal(got, a)
			}
			box, _, _ := g.Mailbox.Load(context.Background(), "19")
			if mixed {
				if len(box.Mails) != 1 || got.OverflowItems != 1 {
					t.Fatal("普通奖励应留邮件", got, box)
				}
				for _, m := range box.Mails {
					if len(m.Attachments) != 1 || m.Attachments[0].ItemID != 600 {
						t.Fatal("契约被转入邮件", m)
					}
				}
			} else if len(box.Mails) != 0 || got.OverflowMailID != "" {
				t.Fatal("纯契约生成了满包邮件", got, box)
			}
			inv, _, _ := g.Inventory.Load(context.Background(), "19")
			if inv.Slots["0:75"].Count != 1 || len(inv.Slots) != 351 {
				t.Fatal("契约占格或礼包扣除错误")
			}
		})
	}
}

func TestPremiumPackageActivationFailureDoesNotConsumeBox(t *testing.T) {
	for _, cera := range []bool{false, true} {
		t.Run(fmt.Sprint(cera), func(t *testing.T) {
			s, session, c, g := premiumPackageFixture(t, false)
			id := uint32(500)
			if cera {
				id = 502
			}
			premiumPackageSource(t, g, int64(id))
			contract := s.premiumCatalog.contractsByItem[30]
			contract.DurationSeconds = math.MaxInt64
			s.premiumCatalog.contractsByItem[30] = contract
			before, _, _ := g.Inventory.Load(context.Background(), "19")
			var e error
			if cera {
				def, err := resolveCurrentCeraPackageDefinition(c, id)
				if err != nil {
					t.Fatal(err)
				}
				_, e = s.commitCurrentCeraPackage(context.Background(), session, c, def, currentCeraPackageOpenRequest{SourceSlot: 75})
			} else {
				def, err := resolveCurrentBoosterDefinition(c, id, currentBoosterRequestRandom)
				if err != nil {
					t.Fatal(err)
				}
				_, e = s.commitCurrentBooster(context.Background(), session, c, def, currentBoosterOpenRequest{SourceSlot: 75})
			}
			if e == nil {
				t.Fatal("超出时长边界仍提交")
			}
			after, _, _ := g.Inventory.Load(context.Background(), "19")
			a, _, _ := g.Account.Load(context.Background(), "account-1")
			if !reflect.DeepEqual(before, after) || premium.ExpireAt(a, premium.TypeOverEquip) != 0 || premium.ExpireAt(a, premium.TypeOverSkill) != 0 {
				t.Fatal("失败吞礼包或留下部分时长")
			}
		})
	}
}

func TestPremiumPackageHandlersNotifyActivationWithoutContractInventoryRows(t *testing.T) {
	for _, cera := range []bool{false, true} {
		t.Run(fmt.Sprint(cera), func(t *testing.T) {
			s, session, _, g := premiumPackageFixture(t, false)
			id := int64(500)
			opcode := uint16(dnfenum.CmdPacketUseBoosterItem)
			body := []byte{75, 0}
			want := 1
			if cera {
				id = 502
				opcode = uint16(dnfenum.CmdPacketOpenCerapackage)
				body = []byte{75, 0, 0}
				want = 2
			}
			premiumPackageSource(t, g, id)
			if e := s.handleGameCommand(session, byte(dnfenum.GameCmdCommand), opcode, body); e != nil {
				t.Fatal(e)
			}
			data := session.conn.(*bufferConn).write.Bytes()
			notices := 0
			first := true
			for len(data) > 0 {
				packet, rest := splitGameServerUpperPacket(t, data)
				data = rest
				if first {
					if packet.Header.MsgID != opcode || packet.Body[0] != 1 {
						t.Fatal("缺少成功确认", packet)
					}
					first = false
				}
				if packet.Header.Classification == 0 && packet.Header.MsgID == currentPremiumActivatedMsgID {
					notices++
					if len(packet.Body) != 11 || binary.LittleEndian.Uint16(packet.Body) != 2 {
						t.Fatal(packet)
					}
				}
			}
			if notices != want {
				t.Fatalf("激活通知=%d 期望=%d", notices, want)
			}
		})
	}
}

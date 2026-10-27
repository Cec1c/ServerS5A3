package dnfbridge

import (
	"bytes"
	"context"

	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/cerashop"
	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestPrivateWalletActualBoxesRightClickAndReplay(t *testing.T) {
	_, catalog := privateRaidTokenCatalog(t)
	for n, amount := range []int64{2000, 4000, 5000, 10000} {
		box := uint32(990027101 + n)
		if n == 3 {
			box = 490006075
		}
		t.Run(fmt.Sprint(box), func(t *testing.T) {
			s, session, conn, g := epicGiftFixture(t, catalog, box)
			ctx := context.Background()
			a, _, _ := g.Account.Load(ctx, "account-1")
			cerashop.SetHappyTokenCeraBalance(&a, 17)
			cerashop.SetTokenCeraBalance(&a, 19)
			cerashop.SetBalance(&a, 23)
			if e := g.Account.Save(ctx, a); e != nil {
				t.Fatal(e)
			}
			// Fill every ordinary bag slot; wallet rewards need no material slot.
			inv, _, _ := g.Inventory.Load(ctx, "19")
			for slot := int16(0); slot <= 300; slot++ {
				if slot != 75 {
					inv.Slots[currentDungeonPickupMainSlotKey(slot)] = repo.ItemStack{ItemID: 10096296, Count: 1}
				}
			}
			if e := g.Inventory.Save(ctx, inv); e != nil {
				t.Fatal(e)
			}
			for attempt := 0; attempt < 2; attempt++ {
				conn.write.Reset()
				if e := s.handleCurrentBoosterItem(session, []byte{75, 0}); e != nil {
					t.Fatal(e)
				}
				data := conn.write.Bytes()
				updates := 0
				acks := 0
				for len(data) > 0 {
					p, rest := splitGameServerUpperPacket(t, data)
					data = rest
					if p.Header.MsgID == 160 {
						acks++
						if len(p.Body) == 0 || (p.Body[0] == 1) != (attempt == 0) {
							t.Fatalf("wrong open ACK: %x", p.Body)
						}
					}
					if p.Header.MsgID == currentCeraShopUpdateMsgID {
						updates++
						if p.Header.Classification != 0 || !bytes.Equal(p.Body, buildCurrentCeraShopWalletBody(23, 19, 17+amount)) {
							t.Fatalf("wrong wallet notification: %x", p.Body)
						}
					}
				}
				if acks != 1 || (attempt == 0 && updates != 1) || (attempt == 1 && updates != 0) {
					t.Fatal("missing or duplicate wallet refresh", attempt, acks, updates)
				}
				got, _, _ := g.Account.Load(ctx, "account-1")
				w := cerashop.WalletBalance(got)
				if w.HappyTokenCera != 17+amount || w.TokenCera != 19 || w.Cera != 23 || got.Metadata["other"] != "keep" {
					t.Fatal(w)
				}
				after, _, _ := g.Inventory.Load(ctx, "19")
				if _, exists := after.Slots["0:75"]; exists {
					t.Fatal("source box not consumed")
				}
				for _, stack := range after.Slots {
					if cerashop.IsVoucher(stack.ItemID) {
						t.Fatal("currency stored as material")
					}
				}
			}
		})
	}
}

func privateWalletMixedCatalog(t *testing.T, loot bool) *pvfDungeonDropCatalog {
	t.Helper()
	source := dungeonDropCatalogTestSource{
		"monster/monster.lst": "", "equipment/equipment.lst": "",
		"stackable/stackable.lst": "900 `wallet.stk`\n2681762 `normal.stk`\n2681917 `happy.stk`\n600 `loot.stk`\n",
		"stackable/normal.stk":    "[stackable type]\n`[material]` 4\n[stack limit]\n999999\n",
		"stackable/happy.stk":     "[stackable type]\n`[material]` 4\n[stack limit]\n999999\n",
		"stackable/loot.stk":      "[stackable type]\n`[material]` 0\n[stack limit]\n999\n",
	}
	reward := "[etc]\n1 2681762 10000 2000\n[/etc]\n[etc]\n1 2681917 10000 4000\n[/etc]\n"
	if loot {
		reward += "[etc]\n1 600 10000 3\n[/etc]\n"
	}
	source["stackable/wallet.stk"] = "[stackable type]\n`[booster]` 0\n[stack limit]\n999\n[booster info]\n" + reward + "[/booster info]\n"
	c, e := newPVFDungeonDropCatalog(source)
	if e != nil {
		t.Fatal(e)
	}
	return c
}

func TestPrivateWalletBoosterAtomicFailureAndConcurrentOpen(t *testing.T) {
	for _, mode := range []string{"success", "normal_overflow", "happy_overflow", "full_bag", "concurrent", "lottery"} {
		t.Run(mode, func(t *testing.T) {
			c := privateWalletMixedCatalog(t, mode != "concurrent")
			s, session, _, g := epicGiftFixture(t, c, 900)
			ctx := context.Background()
			a, _, _ := g.Account.Load(ctx, "account-1")
			if mode == "normal_overflow" {
				cerashop.SetTokenCeraBalance(&a, cerashop.MaxWalletBalance-1999)
			}
			if mode == "happy_overflow" {
				cerashop.SetHappyTokenCeraBalance(&a, cerashop.MaxWalletBalance-3999)
			}
			if e := g.Account.Save(ctx, a); e != nil {
				t.Fatal(e)
			}
			inv, _, _ := g.Inventory.Load(ctx, "19")
			if mode == "full_bag" {
				for slot := int16(0); slot <= 300; slot++ {
					if slot != 75 {
						inv.Slots[currentDungeonPickupMainSlotKey(slot)] = repo.ItemStack{ItemID: 601, Count: 999}
					}
				}
				if e := g.Inventory.Save(ctx, inv); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "concurrent" {
				inv.Slots["0:50"] = repo.ItemStack{ItemID: 601, Count: 1}
				if e := g.Inventory.Save(ctx, inv); e != nil {
					t.Fatal(e)
				}
			}
			req := currentBoosterOpenRequest{SourceSlot: 75}
			def, e := s.prepareCurrentBooster(ctx, session, c, req)
			if e != nil {
				t.Fatal(e)
			}
			if mode == "lottery" {
				req.AllowLotteryGold = true
			}
			if mode == "concurrent" {
				var wg sync.WaitGroup
				var successes atomic.Int32
				for n := 0; n < 24; n++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						if _, e := s.commitCurrentBooster(ctx, session, c, def, req); e == nil {
							successes.Add(1)
						} else if !errors.Is(e, errCurrentBoosterSourceMissing) {
							t.Error(e)
						}
					}()
				}
				wg.Wait()
				if successes.Load() != 1 {
					t.Fatal("duplicate open", successes.Load())
				}
			} else {
				got, e := s.commitCurrentBooster(ctx, session, c, def, req)
				if mode == "success" {
					if e != nil || !got.WalletChanged {
						t.Fatal(got, e)
					}
				} else if e == nil {
					t.Fatal("expected rollback", mode)
				}
			}
			a1, _, _ := g.Account.Load(ctx, "account-1")
			i1, _, _ := g.Inventory.Load(ctx, "19")
			if mode == "success" || mode == "concurrent" {
				w := cerashop.WalletBalance(a1)
				if w.TokenCera != 2000 || w.HappyTokenCera != 4000 {
					t.Fatal(w)
				}
				var loot int64
				for _, row := range i1.Slots {
					if cerashop.IsVoucher(row.ItemID) || row.ItemID == 900 {
						t.Fatal("unexpected inventory", row)
					}
					if row.ItemID == 600 {
						loot += row.Count
					}
				}
				if mode == "success" && loot != 3 {
					t.Fatal("mixed loot lost", loot)
				}
			} else if !reflect.DeepEqual(a, a1) || !reflect.DeepEqual(inv, i1) {
				t.Fatal("wallet or box changed on failure", mode)
			}
		})
	}
}

func TestPrivateWalletRaidMixedOverflowAndConcurrentReceipt(t *testing.T) {
	for _, mode := range []string{"success", "overflow", "missing_combined_transaction"} {
		t.Run(mode, func(t *testing.T) {
			c := privateWalletMixedCatalog(t, true)
			_, _, _, g := epicGiftFixture(t, c, 900)
			ctx := context.Background()
			plan, e := resolveCurrentAntonConfiguredReward(c, raid.AntonRewardChoice{Type: "squad_item", Weight: 1, ContainerID: 900, Flags: 1}, func(int64) (int64, error) { return 0, nil }, time.Now())
			if e != nil {
				t.Fatal(e)
			}
			// Ensure the synthetic PVF retains all guaranteed rows, not one weighted choice.
			if len(plan.Rewards) != 3 {
				t.Fatal("expected two currencies and loot", plan.Rewards)
			}
			inv, _, _ := g.Inventory.Load(ctx, "19")
			for slot := int16(0); slot <= 300; slot++ {
				inv.Slots[currentDungeonPickupMainSlotKey(slot)] = repo.ItemStack{ItemID: 601, Count: 999}
			}
			if e = g.Inventory.Save(ctx, inv); e != nil {
				t.Fatal(e)
			}
			before, _, _ := g.Account.Load(ctx, "account-1")
			if mode == "overflow" {
				cerashop.SetHappyTokenCeraBalance(&before, cerashop.MaxWalletBalance)
				if e = g.Account.Save(ctx, before); e != nil {
					t.Fatal(e)
				}
			}
			char0, _, _ := g.Character.Load(ctx, "19")
			if mode == "missing_combined_transaction" {
				g.AccountAssets = privateWalletBaseOnly{g.AccountAssets}
			}
			cmd := raid.RewardCommand{AccountID: "account-1", CharacterID: "19", InstanceID: "00112233445566778899aabbccddeeff", Mode: 1, Phase: 1, Kind: 3, MinimumClears: 1, OwnedClears: 1, Fingerprint: plan.Fingerprint, CompletedAt: time.Now()}
			var wg sync.WaitGroup
			var credits atomic.Int32
			for n := 0; n < 20; n++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					r, e := grantCurrentAntonRewardPlan(ctx, g, c, plan, cmd)
					if mode == "success" {
						if e != nil {
							t.Error(e)
						}
						if r.WalletChanged {
							credits.Add(1)
						}
					} else if e == nil {
						t.Error("expected failed grant")
					}
				}()
			}
			wg.Wait()
			after, _, _ := g.Account.Load(ctx, "account-1")
			char1, _, _ := g.Character.Load(ctx, "19")
			mail, _, _ := g.Mailbox.Load(ctx, "19")
			i1, _, _ := g.Inventory.Load(ctx, "19")
			if !reflect.DeepEqual(inv, i1) {
				t.Fatal("full bag changed")
			}
			if mode == "success" {
				w := cerashop.WalletBalance(after)
				if credits.Load() != 1 || w.TokenCera != 2000 || w.HappyTokenCera != 4000 || len(mail.Mails) != 1 || !privateWalletMailLoot(mail) {
					t.Fatal("mixed raid credit/mail/replay failed", w, credits.Load(), mail)
				}
			} else if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(char0, char1) || len(mail.Mails) != 0 {
				t.Fatal("failed raid changed wallet, receipt or mail")
			}
		})
	}
}

type privateWalletBaseOnly struct {
	repo.AccountCharacterAssetUnitOfWork
}

func privateWalletMailLoot(mail repo.MailboxRecord) bool {
	for _, m := range mail.Mails {
		return len(m.Attachments) == 1 && m.Attachments[0].ItemID == 600 && m.Attachments[0].Count == 3
	}
	return false
}

func TestPrivateWalletRaidFlowRefreshesPersistedBalance(t *testing.T) {
	archive, catalog := privateRaidTokenCatalog(t)
	f, session, _ := firstAntonFlowFixture(t)
	f.service.initialEquipmentArchive = archive
	f.service.pvfItemCatalog = catalog
	flow := f.state.Battle.RewardFlow[0]
	for i := range flow.Rewards.Choices {
		if flow.Rewards.Choices[i].Type == "squad_item" {
			flow.Rewards.Choices[i].ContainerID = 990027101
			flow.Rewards.Choices[i].Flags = 1
		}
	}
	for _, owner := range flow.Recipients {
		if e := f.repos.Account.Save(context.Background(), repo.AccountRecord{AccountID: owner.AccountID}); e != nil {
			t.Fatal(e)
		}
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) > 0 {
		t.Fatal(errs)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(20*time.Second), zeroAntonRoll); len(errs) > 0 {
		t.Fatal(errs)
	}
	state := cloneRuntimeRaidState(*f.state)
	if e := f.service.sendCurrentAntonTerminalResult(session, state); e != nil {
		t.Fatal(e)
	}
	f.conn.write.Reset()
	if e := f.service.sendCurrentAntonRewardFlowProjection(session, state); e != nil {
		t.Fatal(e)
	}
	data := f.conn.write.Bytes()
	updates := 0
	for len(data) > 0 {
		p, rest := splitGameServerUpperPacket(t, data)
		data = rest
		if p.Header.MsgID == currentCeraShopUpdateMsgID {
			updates++
			if !bytes.Equal(p.Body, buildCurrentCeraShopWalletBody(0, 0, 2000)) {
				t.Fatalf("wrong raid wallet refresh %x", p.Body)
			}
		}
	}
	if updates != 1 {
		t.Fatal("missing raid wallet refresh", updates)
	}
	f.conn.write.Reset()
	if e := f.service.sendCurrentAntonRewardFlowProjection(session, state); e != nil {
		t.Fatal(e)
	}
	if f.conn.write.Len() != 0 {
		t.Fatal("duplicate projection emitted packets")
	}
	a, _, _ := f.repos.Account.Load(context.Background(), "account-1")
	if cerashop.WalletBalance(a).HappyTokenCera != 2000 {
		t.Fatal("projection changed wallet")
	}
}

package dnfbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func offerAndConfirmOnlineCrystal(t *testing.T, f *onlineCrystalTradeFixture, slot int16, beforeReady func()) {
	t.Helper()
	request := buildOnlineItemTradeMoveRequest(repo.MainInventoryListType, slot, 0, 1000, currentItemTradeListType, 0)
	handled, err := f.service.handleAlignedGameCommand(f.offerer, byte(dnfenum.GameCmdCommand), currentItemTradeMoveMsg, request)
	if !handled || err != nil {
		t.Fatalf("offer handled=%v err=%v", handled, err)
	}
	if len(f.service.currentOnlineItemTrade(1).offers[1]) != 1 {
		t.Fatal("offer was not admitted")
	}
	if beforeReady != nil {
		beforeReady()
	}
	f.offerConn.write.Reset()
	f.peerConn.write.Reset()
	for _, state := range []byte{currentItemTradeAddRequest, currentItemTradeReady} {
		for _, session := range []*gameSession{f.offerer, f.peer} {
			handled, err = f.service.handleAlignedGameCommand(session, byte(dnfenum.GameCmdCommand), currentItemTradeReadyMsg, []byte{state})
			if !handled || err != nil {
				t.Fatalf("ready handled=%v err=%v", handled, err)
			}
		}
	}
	if f.service.currentOnlineItemTrade(1) != nil || f.service.currentOnlineItemTrade(5) != nil {
		t.Fatal("terminal trade retained live state")
	}
}

func TestOnlineAccountCrystalCommitsToWarehouseOnBothReady(t *testing.T) {
	f := newOnlineCrystalTradeFixture(t, nil)
	offerAndConfirmOnlineCrystal(t, f, 358, nil)
	ctx := context.Background()
	for account, want := range map[string]int64{"a": 4000, "b": 1000} {
		r, found, err := f.repositories.AccountInventory.Load(ctx, account)
		if err != nil || !found || r.Slots["0:358"].Count != want {
			t.Fatalf("account%s=%+v/%v/%v want%d", account, r, found, err, want)
		}
	}
	for _, id := range []string{"1", "5"} {
		r, _, _ := f.repositories.Inventory.Load(ctx, id)
		if _, bad := r.Slots["0:358"]; bad {
			t.Fatal("character duplicate", id)
		}
	}
	assertCrystalTradeFinishBody(t, f.offerConn.write.Bytes(), []byte{0, 0})
	assertCrystalTradeFinishBody(t, f.peerConn.write.Bytes(), []byte{1, 0, 3, 0, 0x66, 1})
	// A late confirmation cannot replay the cleared exchange.
	_, err := f.service.handleAlignedGameCommand(f.peer, byte(dnfenum.GameCmdCommand), currentItemTradeReadyMsg, []byte{currentItemTradeReady})
	if err != nil {
		t.Fatal(err)
	}
	a, _, _ := f.repositories.AccountInventory.Load(ctx, "a")
	if a.Slots["0:358"].Count != 4000 {
		t.Fatal("late ready charged twice", a)
	}
}

func assertCrystalTradeFinishBody(t *testing.T, wire, expected []byte) {
	t.Helper()
	count := 0
	for len(wire) > 0 {
		packet, rest := splitGameServerUpperPacket(t, wire)
		wire = rest
		if packet.Header.MsgID != currentItemTradeFinishMsg || packet.Header.Classification != 0 {
			continue
		}
		count++
		if !bytes.Equal(packet.Body, expected) {
			t.Fatalf("crystal finish got%x want%x", packet.Body, expected)
		}
	}
	if count != 1 {
		t.Fatalf("crystal finish frames=%d", count)
	}
}

func TestOnlineAccountCrystalRevalidatesOwnersAndBalanceAtCommit(t *testing.T) {
	for _, scenario := range []string{"sender_token_changed", "receiver_token_changed", "receiver_token_lost", "character_owner_changed", "balance_spent_after_offer", "source_bound_after_offer"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			f := newOnlineCrystalTradeFixture(t, nil)
			var before repo.AccountInventoryRecord
			offerAndConfirmOnlineCrystal(t, f, 358, func() {
				switch scenario {
				case "sender_token_changed":
					f.offerer.accountID = "other"
				case "receiver_token_changed":
					f.peer.accountID = "other"
				case "receiver_token_lost":
					f.peer.accountFromToken = false
				case "character_owner_changed":
					c, _, _ := f.repositories.Character.Load(ctx, "1")
					c.AccountID = "other"
					if err := f.repositories.Character.Save(ctx, c); err != nil {
						t.Fatal(err)
					}
				case "balance_spent_after_offer", "source_bound_after_offer":
					a, _, _ := f.repositories.AccountInventory.Load(ctx, "a")
					stack := a.Slots["0:358"]
					if scenario == "balance_spent_after_offer" {
						stack.Count = 999
					} else {
						stack.Bind = true
					}
					a.Slots["0:358"] = stack
					if err := f.repositories.AccountInventory.Save(ctx, a); err != nil {
						t.Fatal(err)
					}
				}
				before, _, _ = f.repositories.AccountInventory.Load(ctx, "a")
			})
			after, _, _ := f.repositories.AccountInventory.Load(ctx, "a")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected commit changed source", before, after)
			}
			b, found, _ := f.repositories.AccountInventory.Load(ctx, "b")
			if found && len(b.Slots) > 0 {
				t.Fatal("rejected commit credited recipient", b)
			}
		})
	}
}

func TestOnlineAccountCrystalTradeRealPVFSixFreeItems(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires pinned current PVF")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the original fixture and the approved PVF paired with v133/v134.
	// Each archive still has to satisfy all six actual item contracts below.
	switch fmt.Sprintf("%x", sha256.Sum256(data)) {
	case "ad7d05c70421278f7385729bcba3e81e37d99d0f3df85a87a9f0dc19c92504cb",
		"a56cacf96087ba34ca5299e04ab65f8268f694b214b97dec9879fdebf46f5290":
	default:
		t.Fatal("PVF hash mismatch")
	}
	archive, err := platformpvf.OpenBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	for index, item := range []int64{3033, 3034, 3035, 3036, 3037, 3262} {
		t.Run(strconv.FormatInt(item, 10), func(t *testing.T) {
			slot := int16(354 + index)
			definition, err := catalog.ResolveItem(uint32(item))
			if err != nil {
				t.Fatal(err)
			}
			if !currentDungeonDiscardTradeable(definition) || definition.StackLimit != 0 {
				t.Fatal("actual PVF cube contract", definition)
			}
			f := newOnlineCrystalTradeFixture(t, nil)
			f.service.pvfItemCatalog = catalog
			saveOnlineCrystalTradeAccountSlots(t, f.repositories, map[string]repo.ItemStack{repo.AccountSharedInventorySlotKey(slot): {ItemID: item, Count: 5000}})
			offerAndConfirmOnlineCrystal(t, f, slot, nil)
			for account, want := range map[string]int64{"a": 4000, "b": 1000} {
				r, _, err := f.repositories.AccountInventory.Load(context.Background(), account)
				if err != nil || r.Slots[repo.AccountSharedInventorySlotKey(slot)].ItemID != item || r.Slots[repo.AccountSharedInventorySlotKey(slot)].Count != want {
					t.Fatal("real PVF transfer", account, r, err)
				}
			}
		})
	}
}

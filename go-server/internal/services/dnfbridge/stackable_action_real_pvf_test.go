package dnfbridge

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"testing"

	dnffatigue "longheng.io/server/internal/modules/dnf/fatigue"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestFatiguePotionNativeActionRealPVF(t *testing.T) {
	if os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE") == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the matching PVF")
	}
	for _, mode := range []string{"success_and_daily_limit", "full", "dungeon", "foreign_action"} {
		t.Run(mode, func(t *testing.T) {
			s, session, repos := guildMileageFixture(t)
			ctx := context.Background()
			catalog, err := newPVFDungeonDropCatalog(s.initialEquipmentArchive)
			if err != nil {
				t.Fatal(err)
			}
			s.pvfItemCatalog = catalog
			// Actual 30-point potion, one use per service day.
			const itemID = 490010384
			item, err := catalog.ResolveItem(itemID)
			if err != nil {
				t.Fatal(err)
			}
			character, _, err := repos.Character.Load(ctx, "29")
			if err != nil {
				t.Fatal(err)
			}
			account := dnfrepo.AccountRecord{AccountID: character.AccountID}
			if err := repos.Account.Save(ctx, account); err != nil {
				t.Fatal(err)
			}
			character.Level = 90
			dnffatigue.Refresh(&character, account, s.gameplayNow())
			character.Stats["fatigue"] = 0
			if mode == "full" {
				character.Stats["fatigue"] = character.Stats["fatigue_limit"]
			}
			if err := repos.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			inv := dnfrepo.InventoryRecord{CharacterID: "29", Slots: map[string]dnfrepo.ItemStack{
				"0:87": {ItemID: itemID, Count: 2, Extra: map[string]string{"item_kind": "stackable", "pvf_path": item.PVFPath}},
			}}
			if err := repos.Inventory.Save(ctx, inv); err != nil {
				t.Fatal(err)
			}
			if mode == "dungeon" {
				session.dungeon.runtime = &runtimeDungeonState{}
			}
			if mode == "foreign_action" {
				for _, action := range []uint32{145, 162, 164, 169, 179} {
					handled, err := s.handleCurrentFatiguePotionUse(session, testQuestClearCouponRequestBody(87, action))
					if handled || err != nil {
						t.Fatalf("action=%d handled=%v err=%v", action, handled, err)
					}
				}
				if session.conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("foreign action emitted potion ACK")
				}
			} else {
				handled, err := s.handleCurrentFatiguePotionUse(session, testQuestClearCouponRequestBody(87, 58))
				if !handled || err != nil {
					t.Fatalf("handled=%v err=%v", handled, err)
				}
				packet, rest := splitCurrentGameServerUpperPacketAuto(t, session.conn.(*bufferConn).write.Bytes())
				if packet.Header.MsgID != 515 || packet.Header.Classification != 1 {
					t.Fatalf("header=%+v", packet.Header)
				}
				if mode == "success_and_daily_limit" {
					if !bytes.Equal(packet.Body, []byte{1, 87, 0, 0, 58, 0, 0, 0}) {
						t.Fatalf("ACK=%x", packet.Body)
					}
					var ops []uint16
					for len(rest) > 0 {
						p, next := splitCurrentGameServerUpperPacketAuto(t, rest)
						ops, rest = append(ops, p.Header.MsgID), next
					}
					if !reflect.DeepEqual(ops, []uint16{14, currentFatigueMsgID}) {
						t.Fatalf("refresh order=%v", ops)
					}
					inv, _, _ = repos.Inventory.Load(ctx, "29")
					character, _, _ = repos.Character.Load(ctx, "29")
					if inv.Slots["0:87"].Count != 1 || character.Stats["fatigue"] != 30 || character.Stats["fatigue_potion_used_490010384"] != 1 {
						t.Fatalf("bag=%+v stats=%v", inv, character.Stats)
					}
					// The next request must reject without another local debit.
					session.conn.(*bufferConn).write.Reset()
					handled, err = s.handleCurrentFatiguePotionUse(session, testQuestClearCouponRequestBody(87, 58))
					if !handled || err != nil {
						t.Fatalf("repeat handled=%v err=%v", handled, err)
					}
					packet, rest = splitCurrentGameServerUpperPacketAuto(t, session.conn.(*bufferConn).write.Bytes())
				}
				if !bytes.Equal(packet.Body, []byte{0, 1, 87, 0, 0, 58}) || len(rest) != 0 {
					t.Fatalf("failure=%x trailing=%x", packet.Body, rest)
				}
			}
			afterInv, _, _ := repos.Inventory.Load(ctx, "29")
			afterCharacter, _, _ := repos.Character.Load(ctx, "29")
			if !reflect.DeepEqual(inv, afterInv) || !reflect.DeepEqual(character, afterCharacter) {
				t.Fatal("rejected request changed assets")
			}
		})
	}
}

func TestQuestCouponNativeActionDefinitionsRealPVF(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the matching PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id     uint32
		action string
	}{{10164630, "[gm quest clear]"}, {10301410, "[lv clear]"}} {
		item, err := catalog.ResolveItem(tc.id)
		if err != nil || item.Kind != dungeonDropItemStackable || item.ActionType != tc.action {
			t.Fatalf("item %d=%+v err=%v", tc.id, item, err)
		}
	}
}

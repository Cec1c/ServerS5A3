package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func adventureShopExpirationSource(period string) initialEquipmentMemSource {
	system := "[capsule need minimum exp]\n1\n[capsule max exp]\n10\n[capsule max count]\n10\n[capsule limit level min]\n1\n[capsule limit level max]\n90\n[capsule give exp]\n1\n"
	for i := 0; i < 3; i++ {
		system += fmt.Sprintf("[shop]\n[index]\n%d\n[max shop point]\n100\n[adventurer shop purchase info]\n10304122 1 2 3 0\n[/shop]\n", i)
	}
	return initialEquipmentMemSource{
		adventuregroup.CharacterManagementPath: "[point bonus]\n1 90 10\n[/point bonus]\n[manage level point]\n10 40 100\n[/manage level point]\n[manage level max]\n3\n[exp bonus]\n[/exp bonus]\n[gold bonus]\n[/gold bonus]\n[manage option]\n1 5 2 9 3 15\n[/manage option]\n",
		adventuregroup.AdventureSystem2018Path: system,
		adventuregroup.ExpeditionSystemPath:    "[area]\n[index]\n1\n[required level]\n1\n[reward info]\n6 1\n[/area]\n[attribute data]\n1 `a` `g` 0\n[group]\n`g` `a`\n[character attribute]\n0 0 0 `a` `a` `a`\n[attribute achieve rate]\n1\n[party member rate]\n1 1 1 1 1\n[extra rate]\n1\n",
		"monster/monster.lst":                  "",
		"equipment/equipment.lst":              "",
		"stackable/stackable.lst":              "10304122 `potion.stk`\n",
		"stackable/potion.stk":                 "[name]\n`Potion`\n[stackable type]\n`[waste]` 0\n[stack limit]\n1\n" + period,
	}
}

func newAdventureShopExpirationFixture(t *testing.T, source dnfpvf.Source) (*Service, *gameSession, dnfrepo.Group, *fakeCurrentDungeonDeathTimerQueue, adventuregroup.ShopCategory, adventuregroup.ShopProduct) {
	t.Helper()
	tables, err := adventuregroup.LoadComplete(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	var category adventuregroup.ShopCategory
	var product adventuregroup.ShopProduct
	for _, c := range tables.Runtime().ShopCategories {
		for _, p := range c.Products {
			if p.ItemID == 10304122 {
				category, product = c, p
				break
			}
		}
		if product.ItemID != 0 {
			break
		}
	}
	if product.ItemID == 0 {
		t.Fatal("potion missing from adventure shop")
	}
	q := newFakeCurrentDungeonDeathTimerQueue()
	q.now = time.Now().UTC().Truncate(time.Second)
	g := memory.NewMemoryGroup()
	runtime := adventuregroup.RuntimeState{Version: 1, Month: q.now.In(time.FixedZone("CST", 8*3600)).Format("2006-01")}
	runtime.ShopPoints[category.Index] = category.MaxPoint
	encoded, err := runtime.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := g.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "shop-owner", Metadata: map[string]string{adventuregroup.RuntimeStateMetadataKey: encoded, "keep": "yes"}}); err != nil {
		t.Fatal(err)
	}
	if err := g.Character.Save(ctx, dnfrepo.CharacterRecord{AccountID: "shop-owner", CharacterID: "31", Name: "Buyer", Level: 90, Job: "0"}); err != nil {
		t.Fatal(err)
	}
	if err := g.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "31", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	s := &Service{options: options{currentClientVersion: "official-24.9"}, adventureGroupTable: tables, pvfItemCatalog: catalog, gameplayTimers: q, repositoryProvider: func() (dnfrepo.Group, bool) { return g, true }}
	session := &gameSession{accountID: "shop-owner", accountFromToken: true, loginPacketSeen: true, selectedCharacterID: 31, conn: &bufferConn{}}
	return s, session, g, q, category, product
}

func buyAdventureExpirationPotion(t *testing.T, s *Service, session *gameSession, category byte, itemID uint32, success bool) {
	t.Helper()
	body := make([]byte, 5)
	body[0] = category
	binary.LittleEndian.PutUint32(body[1:], itemID)
	conn := &bufferConn{}
	session.conn = conn
	if err := s.handleCurrentAdventureShopPurchase(session, body); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
	if ack.Header.MsgID != currentAdventureShopPurchaseMsgID || (ack.Body[0] == 1) != success {
		t.Fatalf("ack=%x want success=%v", ack.Body, success)
	}
}

func verifyAdventurePotionExpiration(t *testing.T, source dnfpvf.Source) {
	t.Helper()
	s, session, g, q, category, product := newAdventureShopExpirationFixture(t, source)
	definition, err := s.pvfItemCatalog.ResolveItem(product.ItemID)
	if err != nil || definition.UsablePeriodDays != 7 {
		t.Fatalf("potion definition=%+v err=%v", definition, err)
	}
	buyAdventureExpirationPotion(t, s, session, category.Index, product.ItemID, true)
	ctx := context.Background()
	inv, _, err := g.Inventory.Load(ctx, "31")
	if err != nil || len(inv.Slots) != 1 {
		t.Fatalf("inventory=%+v err=%v", inv, err)
	}
	want := q.now.Add(7 * 24 * time.Hour)
	_, rest := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
	update, _ := splitGameServerUpperPacket(t, rest)
	if update.Header.MsgID != 14 || len(update.Body) != 3+currentItemListEntryWireSize ||
		binary.LittleEndian.Uint32(update.Body[3+0x38:]) != uint32(want.Unix()) ||
		binary.LittleEndian.Uint16(update.Body[3+0x0b:]) == 0 {
		t.Fatal("purchase inventory refresh omitted the live expiry")
	}
	for _, stack := range inv.Slots {
		if !stack.ExpireAt.Equal(want) || currentItemListStackExpire(stack) != uint32(want.Unix()) || len(stack.RawEntry) != currentItemListEntryWireSize || binary.LittleEndian.Uint32(stack.RawEntry[0x38:]) != uint32(want.Unix()) || binary.LittleEndian.Uint16(stack.RawEntry[0x0b:]) == 0 {
			t.Fatalf("new purchase has invalid expiry: expire=%s metadata=%v raw=%x want=%s", stack.ExpireAt, stack.Extra, stack.RawEntry, want)
		}
		// Re-login projects the durable deadline; it must not grant seven more days.
		later, _ := normalizeCurrentPVFItemStack(stack, definition, q.now.Add(24*time.Hour))
		if !later.ExpireAt.Equal(want) || currentItemListStackExpire(later) != uint32(want.Unix()) {
			t.Fatal("relogin extended purchase")
		}
	}
	account, _, _ := g.Account.Load(ctx, "shop-owner")
	state, err := adventuregroup.ParseRuntimeState(account, s.adventureGroupTable.Runtime(), q.now)
	if err != nil || state.ShopPoints[category.Index] != category.MaxPoint-product.Cost || state.PurchaseCount(category.Index, product.ItemID) != 1 || account.Metadata["keep"] != "yes" {
		t.Fatalf("purchase accounting=%+v err=%v", state, err)
	}
	// A later purchase gets its own date and cannot reset the earlier instance.
	q.now = q.now.Add(time.Hour)
	buyAdventureExpirationPotion(t, s, session, category.Index, product.ItemID, true)
	after, _, _ := g.Inventory.Load(ctx, "31")
	if len(after.Slots) != 2 {
		t.Fatal("distinct purchases merged")
	}
	for key, previous := range inv.Slots {
		if !reflect.DeepEqual(after.Slots[key], previous) {
			t.Fatal("later purchase rewrote older instance")
		}
	}
}

func TestAdventureShopPurchaseSetsUsablePeriodOnce(t *testing.T) {
	verifyAdventurePotionExpiration(t, adventureShopExpirationSource("[usable period]\n7\n"))
}

func TestRealPVFAdventureShopPotionExpiration(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires matching runtime PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	verifyAdventurePotionExpiration(t, archive)
}

func TestAdventureShopUnrepresentablePeriodDoesNotSpend(t *testing.T) {
	s, session, g, _, category, product := newAdventureShopExpirationFixture(t, adventureShopExpirationSource("[usable period]\n9999999999\n"))
	before, _, _ := g.Account.Load(context.Background(), "shop-owner")
	buyAdventureExpirationPotion(t, s, session, category.Index, product.ItemID, false)
	after, _, _ := g.Account.Load(context.Background(), "shop-owner")
	inv, _, _ := g.Inventory.Load(context.Background(), "31")
	if !reflect.DeepEqual(before, after) || len(inv.Slots) != 0 {
		t.Fatal("invalid period charged account or granted item")
	}
}

func TestAdventureShopPermanentItemRemainsPermanent(t *testing.T) {
	s, session, g, _, category, product := newAdventureShopExpirationFixture(t, adventureShopExpirationSource(""))
	buyAdventureExpirationPotion(t, s, session, category.Index, product.ItemID, true)
	inv, _, _ := g.Inventory.Load(context.Background(), "31")
	for _, stack := range inv.Slots {
		if !stack.ExpireAt.IsZero() || !bytes.Equal(stack.RawEntry[0x38:0x3c], make([]byte, 4)) {
			t.Fatal("permanent item gained expiry")
		}
	}
}

func TestAdventureShopExpirationFailuresDoNotCharge(t *testing.T) {
	for _, kind := range []string{"insufficient_points", "full_inventory", "foreign_owner", "late_transaction_failure"} {
		t.Run(kind, func(t *testing.T) {
			s, session, g, q, category, product := newAdventureShopExpirationFixture(t, adventureShopExpirationSource("[usable period]\n7\n"))
			ctx := context.Background()
			switch kind {
			case "insufficient_points":
				account, _, _ := g.Account.Load(ctx, "shop-owner")
				runtime, err := adventuregroup.ParseRuntimeState(account, s.adventureGroupTable.Runtime(), q.now)
				if err != nil {
					t.Fatal(err)
				}
				runtime.ShopPoints[category.Index] = 0
				account.Metadata[adventuregroup.RuntimeStateMetadataKey], err = runtime.Marshal()
				if err != nil {
					t.Fatal(err)
				}
				if err := g.Account.Save(ctx, account); err != nil {
					t.Fatal(err)
				}
			case "full_inventory":
				inv, _, _ := g.Inventory.Load(ctx, "31")
				inv.Slots = make(map[string]dnfrepo.ItemStack)
				for slot := 0; slot <= 120; slot++ {
					inv.Slots[fmt.Sprintf("0:%d", slot)] = dnfrepo.ItemStack{ItemID: int64(product.ItemID), Count: 1}
				}
				if err := g.Inventory.Save(ctx, inv); err != nil {
					t.Fatal(err)
				}
			case "foreign_owner":
				session.accountID = "another-owner"
			case "late_transaction_failure":
				failing := g
				failing.CeraShopAssets = epicFailCampaignAssets{g.CeraShopAssets}
				s.repositoryProvider = func() (dnfrepo.Group, bool) { return failing, true }
			}
			accountBefore, _, _ := g.Account.Load(ctx, "shop-owner")
			invBefore, _, _ := g.Inventory.Load(ctx, "31")
			buyAdventureExpirationPotion(t, s, session, category.Index, product.ItemID, false)
			accountAfter, _, _ := g.Account.Load(ctx, "shop-owner")
			invAfter, _, _ := g.Inventory.Load(ctx, "31")
			if !reflect.DeepEqual(accountBefore, accountAfter) || !reflect.DeepEqual(invBefore, invAfter) {
				t.Fatal("failed purchase changed points, counters or inventory")
			}
		})
	}
}

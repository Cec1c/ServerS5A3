package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"sort"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 现场报障（操作者原话）："商城购买装扮放在购物车无法批量购买，提示为物品栏空间不足"。
//
// 本文件证明两件事：
//  1. 装扮（avatar）**不占主背包**：主背包装备页 9..64 全满时，购物车里 4 件装扮
//     仍然全部落进装扮栏 list 1，并回成功 ACK。判定真源必须是运行时 PVF
//     （`currentPVFItemTargetContainer` / `[equipment type]` 的 avatar 家族），
//     而不是商品所在的目录段（`[avatar]` / `[item]` / `[package]`）——旧代码按
//     `product.Section == "avatar"` 分流，于是任何从非 `[avatar]` 段卖出的装扮
//     都会掉进主背包装备页，主背包满时报的正是 op64 失败码 4（客户端显示"物品栏
//     空间不足"）。
//  2. 用装扮兑换券（payment_mode=1）**整批购买**装扮：每件装扮消耗一张自己的
//     兑换券，券不足时整单原子失败（不扣券、不发装扮）。
//
// op64 的**任何**失败都回同一个 4 字节码失败体（`buildCurrentCeraShopPurchaseFailureBody`，
// 错误码 4），客户端把它渲染成"物品栏空间不足"，所以这条报障的提示语本身不含
// 诊断信息，只能靠"哪种形状在服务端被拒"来定位。

// shopCatalogWithAvatarOutsideAvatarSection 让同一件装扮（item 9001，`[hat avatar]`）
// 同时出现在 `[avatar]` 段（commodity 100070）和普通 `[item]` 段（commodity 109900），
// 并提供一个普通装备（item 9004，`[coat]`，commodity 109901）作为反例。
func shopCatalogWithAvatarOutsideAvatarSection(t *testing.T) *pvfCeraShopCatalog {
	t.Helper()
	source := bridgePVFSource{
		"etc/cerashop.etc": "[item]\n100050 37 3 0 0 80 `test potion` 0 0\n" +
			"109900 9001 1 0 0 30 `mall avatar sold from the item section` 0 0\n" +
			"109901 9004 1 0 0 30 `mall coat sold from the item section` 0 0\n[/item]\n" +
			"[premium]\n[/premium]\n[creature]\n[/creature]\n[coin]\n[/coin]\n[material]\n[/material]\n[recoveryitem]\n[/recoveryitem]\n" +
			"[package]\n[/package]\n[visual]\n[/visual]\n" +
			"[avatar]\n100070 9001 1 0 0 30\n[/avatar]\n",
		"monster/monster.lst":     "",
		"stackable/stackable.lst": "37 `test_potion.stk`\n",
		"equipment/equipment.lst": "9001 `avatar/test_avatar.equ`\n9004 `test_coat.equ`\n",
		"stackable/test_potion.stk": "[stackable type]\n`[waste]`\n[stack limit]\n1000\n" +
			"[expiration date]\n`2028-08-16 06:00:00`\n",
		"equipment/avatar/test_avatar.equ": "[equipment type]\n`[hat avatar]`\n[grade]\n3\n" +
			"[avatar type select]\n7 0 0 30 0 0 0 30 0 0 30 0 0 0 0 0 0 30 0 0 0\n",
		"equipment/test_coat.equ": "[equipment type]\n`[coat]`\n[grade]\n3\n[minimum level]\n1\n[rarity]\n3\n",
	}
	catalog, err := newPVFCeraShopCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

// shopCartBody builds one op64 cart: 4-byte header (count at +2), then 15-byte rows
// whose commodity sits at +3 and whose per-row avatar attribute sits at +1.
func shopCartBody(paymentMode byte, attribute byte, commodityIDs ...uint32) []byte {
	body := make([]byte, currentCeraShopRequestHeaderSize+len(commodityIDs)*currentCeraShopRequestItemStride)
	body[2] = byte(len(commodityIDs))
	for index, commodityID := range commodityIDs {
		offset := currentCeraShopRequestHeaderSize + index*currentCeraShopRequestItemStride
		if index == 0 {
			body[4] = paymentMode
		}
		body[offset+1] = attribute
		binary.LittleEndian.PutUint32(body[offset+3:offset+7], commodityID)
	}
	return body
}

func shopTestService(repositories dnfrepo.Group, catalog *pvfCeraShopCatalog) *Service {
	return &Service{
		options: options{
			accountID:          "account-1",
			gameUpperHeader:    gameUpperHeaderChannel13,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		ceraShopCatalog:    catalog,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
}

func seedShopCharacter(t *testing.T, ctx context.Context, repositories dnfrepo.Group, characterID string, cera int64, slots map[string]dnfrepo.ItemStack) {
	t.Helper()
	seedCeraShopAccountCera(t, ctx, repositories, "account-1", cera)
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: characterID,
		AccountID:   "account-1",
		Stats:       map[string]int64{"cera": cera},
	}); err != nil {
		t.Fatal(err)
	}
	if slots == nil {
		slots = map[string]dnfrepo.ItemStack{}
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: characterID, Slots: slots}); err != nil {
		t.Fatal(err)
	}
}

// fullMainEquipmentSlots 占满主背包装备页 9..64（`dungeonDropItemDefinition` 给
// equipment 的分段），也就是"主背包放不下"的最小现场。
func fullMainEquipmentSlots() map[string]dnfrepo.ItemStack {
	slots := make(map[string]dnfrepo.ItemStack)
	for slot := currentPVFMainEquipmentSlotStart; slot <= currentPVFMainEquipmentSlotEnd; slot++ {
		slots["0:"+strconv.Itoa(int(slot))] = dnfrepo.ItemStack{ItemID: 9004, Count: 1}
	}
	return slots
}

func shopAvatarRowCount(inventory dnfrepo.InventoryRecord) int {
	rows := 0
	for key, stack := range inventory.Slots {
		if len(key) > 0 && key[0] == '1' && stack.ItemID == 9001 {
			rows++
		}
	}
	return rows
}

func TestCurrentCeraShopBulkAvatarCartLandsInAvatarContainerWithFullMainBag(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name       string
		commodity  uint32
		section    string
		wantAvatar int
	}{
		{name: "avatar section rows", commodity: 100070, section: "avatar", wantAvatar: 4},
		{name: "item section avatar rows", commodity: 109900, section: "item", wantAvatar: 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog := shopCatalogWithAvatarOutsideAvatarSection(t)
			product, found := catalog.Product(test.commodity)
			if !found || product.Section != test.section {
				t.Fatalf("fixture commodity=%d product=%+v found=%t", test.commodity, product, found)
			}
			repositories := dnfrepomemory.NewMemoryGroup()
			seedShopCharacter(t, ctx, repositories, "77", 4000, fullMainEquipmentSlots())
			connection := &bufferConn{}
			service := shopTestService(repositories, catalog)
			session := &gameSession{conn: connection, connID: "cera-avatar-cart", selectedCharacterID: 77}

			cart := []uint32{test.commodity, test.commodity, test.commodity, test.commodity}
			if err := service.handleCurrentCeraShopPurchase(session, shopCartBody(currentCeraShopPaymentModeCera, 0xff, cart...)); err != nil {
				t.Fatalf("handle error=%v", err)
			}
			ack, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
			if len(ack.Body) == 0 || ack.Body[0] != 1 {
				t.Fatalf("bulk avatar cart must succeed, ack=%x", ack.Body)
			}
			// 4 件装扮 = 4 条 list-1 增量行（class1/op14 原始 0x77 行）；
			// 主背包(list 0)不得因为装扮而出现任何更新。
			avatarEntries := 0
			for len(rest) > 0 {
				update, trailing := splitGameServerUpperPacket(t, rest)
				rest = trailing
				if update.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) {
					continue
				}
				switch update.Body[0] {
				case 1:
					avatarEntries += int(binary.LittleEndian.Uint16(update.Body[1:3]))
				case 0:
					t.Fatalf("avatar cart wrote a main-list update: %x", update.Body)
				}
			}
			if avatarEntries != test.wantAvatar {
				t.Fatalf("avatar list entries=%d, want %d", avatarEntries, test.wantAvatar)
			}

			inventory, found, err := repositories.Inventory.Load(ctx, "77")
			if err != nil || !found {
				t.Fatalf("load inventory found=%t err=%v", found, err)
			}
			if got := shopAvatarRowCount(inventory); got != test.wantAvatar {
				t.Fatalf("persisted avatar rows=%d, want %d (slots=%d)", got, test.wantAvatar, len(inventory.Slots))
			}
			for slot := currentPVFMainEquipmentSlotStart; slot <= currentPVFMainEquipmentSlotEnd; slot++ {
				stack := inventory.Slots["0:"+strconv.Itoa(int(slot))]
				if stack.ItemID != 9004 {
					t.Fatalf("main equipment slot %d changed to item=%d", slot, stack.ItemID)
				}
			}
			if got := len(inventory.Slots); got != int(currentPVFMainEquipmentSlotEnd-currentPVFMainEquipmentSlotStart+1)+test.wantAvatar {
				t.Fatalf("inventory slots=%d, want the untouched full page plus %d avatars", got, test.wantAvatar)
			}
		})
	}
}

func TestCurrentCeraShopNonAvatarEquipmentStillUsesTheMainBag(t *testing.T) {
	ctx := context.Background()
	catalog := shopCatalogWithAvatarOutsideAvatarSection(t)
	repositories := dnfrepomemory.NewMemoryGroup()
	seedShopCharacter(t, ctx, repositories, "78", 4000, fullMainEquipmentSlots())
	connection := &bufferConn{}
	service := shopTestService(repositories, catalog)
	session := &gameSession{conn: connection, connID: "cera-coat-cart", selectedCharacterID: 78}

	if err := service.handleCurrentCeraShopPurchase(session, shopCartBody(currentCeraShopPaymentModeCera, 0xff, 109901)); err != nil {
		t.Fatalf("handle error=%v", err)
	}
	ack, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || len(ack.Body) == 0 || ack.Body[0] != 0 || ack.Body[1] != 4 {
		t.Fatalf("a full main bag must still fail closed for a non-avatar equipment product: ack=%x trailing=%d", ack.Body, len(trailing))
	}
	inventory, _, _ := repositories.Inventory.Load(ctx, "78")
	if got := shopAvatarRowCount(inventory); got != 0 {
		t.Fatalf("failed coat purchase created %d avatar rows", got)
	}
	account, _, _ := repositories.Account.Load(ctx, "account-1")
	if got := currentAccountCera(account); got != 4000 {
		t.Fatalf("failed purchase charged cera: %d", got)
	}
}

func TestCurrentCeraShopBulkAvatarCartWithCouponsConsumesOneCouponPerAvatar(t *testing.T) {
	ctx := context.Background()
	catalog := mustCurrentCeraShopTestCatalog(t)
	repositories := dnfrepomemory.NewMemoryGroup()
	seedShopCharacter(t, ctx, repositories, "31", 40, map[string]dnfrepo.ItemStack{
		"0:67": {ItemID: 2681594, Count: 8, Bind: true},
	})
	connection := &bufferConn{}
	service := shopTestService(repositories, catalog)
	session := &gameSession{conn: connection, connID: "cera-coupon-cart", selectedCharacterID: 31}

	// Commodity 100071 = item 9001 with duration index 3; its `[grade] 3` row maps to
	// coupon item 2681594 (`currentCeraShopAvatarCouponItemIDs[3][3]`).
	cart := []uint32{100071, 100071, 100071}
	if err := service.handleCurrentCeraShopPurchase(session, shopCartBody(currentCeraShopPaymentModeAvatarCoupon, 0, cart...)); err != nil {
		t.Fatalf("handle error=%v", err)
	}
	ack, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(ack.Body) == 0 || ack.Body[0] != 1 {
		t.Fatalf("bulk coupon cart must succeed, ack=%x", ack.Body)
	}
	inventory, found, err := repositories.Inventory.Load(ctx, "31")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if got := shopAvatarRowCount(inventory); got != 3 {
		t.Fatalf("avatar rows=%d, want 3", got)
	}
	if coupon := inventory.Slots["0:67"]; coupon.ItemID != 2681594 || coupon.Count != 5 {
		t.Fatalf("coupon row after 3 avatar coupons=%+v, want item 2681594 count 5", coupon)
	}
	account, _, _ := repositories.Account.Load(ctx, "account-1")
	if got := currentAccountCera(account); got != 40 {
		t.Fatalf("avatar coupon checkout deducted cera: %d", got)
	}
}

func TestCurrentCeraShopBulkAvatarCartWithTooFewCouponsFailsAtomically(t *testing.T) {
	ctx := context.Background()
	catalog := mustCurrentCeraShopTestCatalog(t)
	repositories := dnfrepomemory.NewMemoryGroup()
	seedShopCharacter(t, ctx, repositories, "32", 40, map[string]dnfrepo.ItemStack{
		"0:67": {ItemID: 2681594, Count: 1, Bind: true},
	})
	connection := &bufferConn{}
	service := shopTestService(repositories, catalog)
	session := &gameSession{conn: connection, connID: "cera-coupon-short", selectedCharacterID: 32}

	if err := service.handleCurrentCeraShopPurchase(session, shopCartBody(currentCeraShopPaymentModeAvatarCoupon, 0, 100071, 100071)); err != nil {
		t.Fatalf("handle error=%v", err)
	}
	ack, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || len(ack.Body) == 0 || ack.Body[0] != 0 {
		t.Fatalf("a cart that needs more coupons than the player owns must fail: ack=%x trailing=%d", ack.Body, len(trailing))
	}
	inventory, _, _ := repositories.Inventory.Load(ctx, "32")
	if got := shopAvatarRowCount(inventory); got != 0 {
		t.Fatalf("failed coupon checkout granted %d avatars", got)
	}
	if coupon := inventory.Slots["0:67"]; coupon.ItemID != 2681594 || coupon.Count != 1 {
		t.Fatalf("failed coupon checkout consumed the coupon: %+v", coupon)
	}
}

// TestRealPVFMallAvatarProductsResolveAndStayInTheAvatarSection 是现场目录的只读取证：
// 运行时 etc/cerashop.etc 的每一件装扮商品都必须能被 resolvePurchase 解析（否则
// 玩家在购物车里点购买只会拿到失败码 4 = "物品栏空间不足"），并记录有多少商品
// 是从非 `[avatar]` 段卖出的装扮——那正是旧分流逻辑会放进主背包的形状。
func TestRealPVFMallAvatarProductsResolveAndStayInTheAvatarSection(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to audit the live cera shop catalog")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFCeraShopCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]uint32, 0, len(catalog.products))
	for id := range catalog.products {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	avatarProducts := 0
	avatarOutsideAvatarSection := 0
	avatarResolveFailures := 0
	for _, id := range ids {
		product := catalog.products[id]
		definition, resolveErr := catalog.items.ResolveItem(product.ItemID)
		if resolveErr != nil || !currentPVFItemTargetContainer(definition).Avatar {
			continue
		}
		avatarProducts++
		if product.Section != "avatar" {
			avatarOutsideAvatarSection++
			t.Logf("avatar product outside [avatar]: commodity=%d item=%d section=%s type=%s",
				id, product.ItemID, product.Section, definition.EquipmentType)
		}
		if _, purchaseErr := catalog.resolvePurchase(product); purchaseErr != nil {
			avatarResolveFailures++
			t.Logf("avatar product that cannot be bought: commodity=%d item=%d err=%v", id, product.ItemID, purchaseErr)
		}
	}
	t.Logf("cerashop products=%d avatar_products=%d avatar_outside_avatar_section=%d resolve_failures=%d",
		len(ids), avatarProducts, avatarOutsideAvatarSection, avatarResolveFailures)
	if avatarProducts < 1000 {
		t.Fatalf("live cerashop catalog exposes only %d avatar products; the catalog parse is wrong", avatarProducts)
	}
	if avatarResolveFailures != 0 {
		t.Fatalf("%d live avatar products cannot be resolved at checkout; every one of them reports the generic failure code 4", avatarResolveFailures)
	}
}

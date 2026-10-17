package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"strconv"
	"testing"
	"time"

	dnfcerashop "longheng.io/server/internal/modules/dnf/cerashop"
	"longheng.io/server/internal/modules/dnf/itemplacement"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func seedCargoUpgrade90(t *testing.T, catalog *pvfCeraShopCatalog, personal uint16, account int64, cera int64) (*Service, dnfrepo.Group, *gameSession, *bufferConn) {
	t.Helper()
	ctx := context.Background()
	r := dnfrepomemory.NewMemoryGroup()
	seedCeraShopAccountCera(t, ctx, r, "upgrade90", cera)
	a, _, _ := r.Account.Load(ctx, "upgrade90")
	a.Metadata["account_cargo_level"] = strconv.FormatInt(account, 10)
	a.Metadata["account_cargo_created"] = "true"
	for _, err := range []error{
		r.Account.Save(ctx, a),
		r.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "22", AccountID: "upgrade90", Stats: map[string]int64{"gold": 99999}}),
		r.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "22", Slots: map[string]dnfrepo.ItemStack{}, Warehouse: map[string]dnfrepo.ItemStack{"2:0": {ItemID: 37, Count: 5}}}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	settings := newCharacterContainerStateSettings("22", time.Now().UTC())
	settings.Values["personal_cargo_list_param16"] = strconv.Itoa(int(personal))
	if err := r.Settings.Save(ctx, settings); err != nil {
		t.Fatal(err)
	}
	conn := &bufferConn{}
	s := &Service{options: options{accountID: "upgrade90", gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain}, ceraShopCatalog: catalog, repositoryProvider: func() (dnfrepo.Group, bool) { return r, true }}
	return s, r, &gameSession{selectedCharacterID: 22, conn: conn}, conn
}

func TestCurrentCeraShopAccountCargoRefreshBeforePurchaseAck(t *testing.T) {
	s, r, session, conn := seedCargoUpgrade90(t, mustCurrentCeraShopTestCatalog(t), 8, 1, 5000)
	if err := s.handleCurrentCeraShopPurchase(session, currentCeraShopTestRequestBody(100083)); err != nil {
		t.Fatal(err)
	}
	upgrade, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if upgrade.Header.MsgID != 306 || upgrade.Header.Classification != 1 || !bytes.Equal(upgrade.Body, []byte{1}) {
		t.Fatalf("升级通知=%+v", upgrade)
	}
	list, rest := splitGameServerUpperPacketWithHeader(t, rest, dnfproto.GameServerUpperHeaderSize16)
	if list.Header.MsgID != 13 || list.Body[0] != 12 || binary.LittleEndian.Uint16(list.Body[1:3]) != 8 {
		t.Fatalf("购买成功前的容量刷新=%+v", list)
	}
	ack, rest := splitGameServerUpperPacket(t, rest)
	if ack.Header.MsgID != 64 || !bytes.Equal(ack.Body, buildCurrentCeraShopPurchaseSuccessBodyWithCount(100083, 1)) {
		t.Fatalf("购买结果=%+v", ack)
	}
	_, rest = splitGameServerUpperPacket(t, rest)
	if len(rest) != 0 || loadCeraShopAccountCera(t, context.Background(), r, "upgrade90") != 3000 {
		t.Fatal("余额或重复刷新错误")
	}
}

func TestCurrentCeraShopPersonalCargoCapacityNotificationDoesNotLoadItemContainer(t *testing.T) {
	// 金库从未打开也不需要取物品列表或具备容器仓储；换角色后的首次购买
	// 使用同一容量通知。容量边界与有物品的情况均不发送op13重建列表。
	for _, capacity := range []uint16{24, 216, 264} {
		t.Run(strconv.Itoa(int(capacity)), func(t *testing.T) {
			conn := &bufferConn{}
			s := &Service{options: options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
				repositoryProvider: func() (dnfrepo.Group, bool) {
					t.Fatal("容量通知不能读取或重建物品容器")
					return dnfrepo.Group{}, false
				}}
			if err := s.sendCurrentCeraShopCargoRefresh(context.Background(), &gameSession{conn: conn}, currentCeraShopCommitResult{PersonalCargoSlotCount: capacity}); err != nil {
				t.Fatal(err)
			}
			packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if packet.Header.Classification != 0 || packet.Header.MsgID != 66 || len(packet.Body) != 4 || binary.LittleEndian.Uint16(packet.Body) != 1 || binary.LittleEndian.Uint16(packet.Body[2:]) != capacity || len(rest) != 0 {
				t.Fatalf("容量通知=%+v trailing=%x", packet, rest)
			}
		})
	}
}

func TestCurrentCeraShopCargoUpgradeRejectsStalePlanWithoutCharging(t *testing.T) {
	ctx := context.Background()
	catalog := mustCurrentCeraShopTestCatalog(t)
	_, r, _, _ := seedCargoUpgrade90(t, catalog, 24, 1, 1000)
	purchases := []currentCeraShopResolvedPurchase{{Product: catalog.products[100080], PersonalCargoUpgradeTarget: 24}}
	if err := resolveCurrentCeraShopCargoPurchases(ctx, r, catalog, "upgrade90", "22", purchases); err != nil {
		t.Fatal(err)
	}
	// 计划24→40形成后，另一个合法操作已把容量升至40；提交时不得再次按旧计划扣款。
	settings := newCharacterContainerStateSettings("22", time.Now().UTC())
	settings.Values["personal_cargo_list_param16"] = "40"
	if err := r.Settings.Save(ctx, settings); err != nil {
		t.Fatal(err)
	}
	owner, err := dnfcerashop.NewOwner(r)
	if err != nil {
		t.Fatal(err)
	}
	_, err = owner.Checkout(ctx, dnfcerashop.CheckoutCommand{
		AccountID: "upgrade90", CharacterID: "22", SettingsScope: dnfrepo.CharacterContainerStateScope("22"), Cost: 60,
		Project: func(assets *dnfcerashop.CheckoutAssets) (dnfcerashop.CheckoutChanges, error) {
			_, _, _, err := currentCeraShopPrepareContainerUpgrades(assets.Settings, assets.SettingsFound, "22", purchases, time.Now().UTC())
			return dnfcerashop.CheckoutChanges{}, err
		},
	})
	if err == nil {
		t.Fatal("陈旧计划被接受")
	}
	if loadCeraShopAccountCera(t, ctx, r, "upgrade90") != 1000 {
		t.Fatal("陈旧计划失败后仍扣款")
	}
	state, _, _ := dnfrepo.LoadCharacterContainerState(ctx, r.Settings, "22")
	if state.PersonalCargoSlotCount != 40 {
		t.Fatal("陈旧计划覆盖了新容量")
	}
}

func TestRealPVFCargoUpgrade90AllTiersAndPrices(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("需要指定实际PVF")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFCeraShopCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	t.Run("个人8到264及旧商品顺延计价", func(t *testing.T) {
		s, r, session, _ := seedCargoUpgrade90(t, catalog, 8, 120, 1000000)
		ids := []uint32{100063, 100064, 100065, 100066, 100067, 100068, 100069, 101062, 101063, 103662, 103663, 103664, 104889, 104890, 104891, 104892}
		remaining := int64(1000000)
		for index, commodity := range ids {
			request, _ := parseCurrentCeraShopPurchaseRequest(currentCeraShopTestRequestBody(100063))
			result, err := s.commitCurrentCeraShopPurchase(ctx, session, catalog, request)
			if err != nil {
				t.Fatalf("目标%d: %v", 24+index*16, err)
			}
			product := catalog.products[commodity]
			remaining -= int64(product.CeraPrice)
			if result.PersonalCargoSlotCount != uint16(24+index*16) || result.CeraAfter != remaining || result.Products[0].CommodityID != commodity {
				t.Fatalf("目标档位/计价错误: %+v 预期商品=%d 余额=%d", result, commodity, remaining)
			}
		}
		state, _, err := dnfrepo.LoadCharacterContainerState(ctx, r.Settings, "22")
		if err != nil || state.PersonalCargoSlotCount != 264 || !itemplacement.SlotOpen(2, 263, &state) || itemplacement.SlotOpen(2, 264, &state) {
			t.Fatalf("满级容量边界错误: %+v %v", state, err)
		}
		request, _ := parseCurrentCeraShopPurchaseRequest(currentCeraShopTestRequestBody(104892))
		if _, err := s.commitCurrentCeraShopPurchase(ctx, session, catalog, request); err == nil {
			t.Fatal("满级购买未拒绝")
		}
		if loadCeraShopAccountCera(t, ctx, r, "upgrade90") != remaining {
			t.Fatal("满级拒绝扣款")
		}
		inventory, _, _ := r.Inventory.Load(ctx, "22")
		if len(inventory.Slots) != 0 || inventory.Warehouse["2:0"].Count != 5 {
			t.Fatal("升级污染库存")
		}
	})
	t.Run("账号120到240及旧商品按当前价格计价", func(t *testing.T) {
		s, r, session, _ := seedCargoUpgrade90(t, catalog, 264, 120, 1000000)
		remaining := int64(1000000)
		for commodity := uint32(104893); commodity <= 104907; commodity++ {
			request, _ := parseCurrentCeraShopPurchaseRequest(currentCeraShopTestRequestBody(103671))
			result, err := s.commitCurrentCeraShopPurchase(ctx, session, catalog, request)
			if err != nil {
				t.Fatalf("商品%d: %v", commodity, err)
			}
			remaining -= int64(catalog.products[commodity].CeraPrice)
			if result.AccountCargoSelectionKey != uint16(128+(commodity-104893)*8) || result.CeraAfter != remaining || len(result.Updates) != 0 {
				t.Fatalf("账号升级结果=%+v", result)
			}
		}
		request, _ := parseCurrentCeraShopPurchaseRequest(currentCeraShopTestRequestBody(104907))
		if _, err := s.commitCurrentCeraShopPurchase(ctx, session, catalog, request); err == nil {
			t.Fatal("账号满级仍扣款")
		}
		if loadCeraShopAccountCera(t, ctx, r, "upgrade90") != remaining {
			t.Fatal("账号满级拒绝改变余额")
		}
	})
	t.Run("下一档余额不足不改容量", func(t *testing.T) {
		s, r, session, _ := seedCargoUpgrade90(t, catalog, 24, 120, 60)
		request, _ := parseCurrentCeraShopPurchaseRequest(currentCeraShopTestRequestBody(100063))
		if _, err := s.commitCurrentCeraShopPurchase(ctx, session, catalog, request); err == nil {
			t.Fatal("以旧档60点券购买了新档120点券")
		}
		state, _, _ := dnfrepo.LoadCharacterContainerState(ctx, r.Settings, "22")
		if state.PersonalCargoSlotCount != 24 || loadCeraShopAccountCera(t, ctx, r, "upgrade90") != 60 {
			t.Fatal("拒绝后容量或余额变化")
		}
	})
}

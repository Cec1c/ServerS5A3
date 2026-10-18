package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/itemtrade"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 本文件覆盖账号仓库小晶块（3037 在 0:358）走在线交易挂单的桥接边界：official-24.9
// 真实令牌会话、角色账号归属匹配、角色背包为空时仍只从账号仓库读；挂单与取消都不动
// 资产。完整事务提交（双方就绪后的 op18 原子交换）由领域与父代理侧验证覆盖，这里不重放。
const (
	onlineCrystalTradeItemID      = 3037
	onlineCrystalTradeSoulItemID  = 10158124
	onlineCrystalTradeAccount     = "a"
	onlineCrystalTradePeerAccount = "b"
	onlineCrystalTradeCount       = 5000
	onlineCrystalTradeMovedCount  = 1000
	onlineCrystalTradeSlot        = dnfrepo.CrystalWarehouseFirstSlot + 4
	onlineCrystalTradeSoulSlot    = dnfrepo.SoulWarehouseFirstSlot + 5
)

// onlineCrystalTradeFixture 复用同目录 item_trade_online_test.go 的既有夹具形态
// （内存仓储 + 双方 bufferConn + 同图在线表 + beginOnlineItemTrade），只补账号仓库
// 挂单所需的角色/账号归属、official-24.9 与真实令牌登录标志。
type onlineCrystalTradeFixture struct {
	service      *Service
	repositories dnfrepo.Group
	offerer      *gameSession
	peer         *gameSession
	offerConn    *bufferConn
	peerConn     *bufferConn
}

func newOnlineCrystalTradeFixture(t *testing.T, mutate func(*onlineCrystalTradeFixture)) *onlineCrystalTradeFixture {
	t.Helper()
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "1", AccountID: onlineCrystalTradeAccount}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "5", AccountID: onlineCrystalTradePeerAccount}); err != nil {
		t.Fatal(err)
	}
	// 角色背包刻意留空：0:358 只能来自账号仓库，不能退回角色本地副本或共享账号兜底。
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	saveOnlineCrystalTradeAccountSlots(t, repositories, map[string]dnfrepo.ItemStack{
		dnfrepo.AccountSharedInventorySlotKey(onlineCrystalTradeSlot): {ItemID: onlineCrystalTradeItemID, Count: onlineCrystalTradeCount},
	})
	catalog, err := newPVFDungeonDropCatalog(bridgePVFSource{
		"monster/monster.lst":     "",
		"stackable/stackable.lst": "3037 `material/cubepiece_clear.stk`\n10158124 `material/soul_epic.stk`\n",
		"equipment/equipment.lst": "",
		// 真实 90 PVF 的六种小晶块没有 `[stack limit]`（round9 固定证据），夹具保持同形，
		// 不引入会改变落位/堆叠判定的假限量；只保留 PVF 归属格与 `[attach type]` 交易资格。
		"stackable/material/cubepiece_clear.stk": "[stackable type] `[material]`\n[attach type] `[free]`\n",
		"stackable/material/soul_epic.stk":       "[stackable type] `[material]`\n[attach type] `[free]`\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	channel := channelcatalog.Channel{ID: 42, Name: "ch.42"}
	fixture := &onlineCrystalTradeFixture{
		repositories: repositories,
		offerConn:    &bufferConn{},
		peerConn:     &bufferConn{},
	}
	fixture.offerer = &gameSession{
		conn: fixture.offerConn, channel: channel, selectedCharacterID: 1,
		accountID: onlineCrystalTradeAccount, loginPacketSeen: true, accountFromToken: true,
	}
	fixture.peer = &gameSession{
		conn: fixture.peerConn, channel: channel, selectedCharacterID: 5,
		accountID: onlineCrystalTradePeerAccount, loginPacketSeen: true, accountFromToken: true,
	}
	fixture.service = &Service{
		pvfItemCatalog:     catalog,
		options:            options{gameUpperBodyCodec: gameUpperBodyCodecPlain, currentClientVersion: "official-24.9"},
		onlinePlayers:      newOnlinePlayerManager(),
		repositoryProvider: func() (dnfrepo.Group, bool) { return fixture.repositories, true },
	}
	if mutate != nil {
		mutate(fixture)
	}
	// begin 在夹具调整之后：它会把双方当前 session.accountID 冻结进 trade.accounts。
	fixture.service.bindGameSessionCharacter(fixture.offerer, fixture.offerer.selectedCharacterID)
	fixture.service.bindGameSessionCharacter(fixture.peer, fixture.peer.selectedCharacterID)
	fixture.service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 1, TownID: 38, AreaID: 0, Session: fixture.offerer})
	fixture.service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 5, TownID: 38, AreaID: 0, Session: fixture.peer})
	fixture.service.beginOnlineItemTrade(fixture.offerer, fixture.peer)
	return fixture
}

// saveOnlineCrystalTradeAccountSlots 整体替换 offerer 账号仓库内容，供拒绝用例把
// 道具挪到灵魂格/错误晶块格或下调余额。
func saveOnlineCrystalTradeAccountSlots(t *testing.T, repositories dnfrepo.Group, slots map[string]dnfrepo.ItemStack) {
	t.Helper()
	if err := repositories.AccountInventory.Save(context.Background(), dnfrepo.AccountInventoryRecord{
		AccountID: onlineCrystalTradeAccount,
		Slots:     slots,
	}); err != nil {
		t.Fatal(err)
	}
}

// crystalTradeWithoutWarehouseUnit 复现"仓储只提供角色交易 UoW、没有账号仓库交易扩展"
// 的旧实现：账号仓库晶块挂单必须被拒，不能退回共享账号兜底。
type crystalTradeWithoutWarehouseUnit struct {
	inner dnfrepo.CharacterTradeUnitOfWork
}

func (u crystalTradeWithoutWarehouseUnit) WithinCharacterTrade(ctx context.Context, first, second string, apply func(dnfrepo.CharacterRepository, dnfrepo.InventoryRepository) error) error {
	return u.inner.WithinCharacterTrade(ctx, first, second, apply)
}

func TestOnlineItemTradeOffersAccountWarehouseCrystalWithoutChargingAssets(t *testing.T) {
	ctx := context.Background()
	fixture := newOnlineCrystalTradeFixture(t, nil)
	if slot, ok := itemtrade.TradableCrystalSlot(onlineCrystalTradeItemID); !ok || slot != onlineCrystalTradeSlot {
		t.Fatalf("fixture item %d maps to slot %d/%t; PVF home is %d", onlineCrystalTradeItemID, slot, ok, onlineCrystalTradeSlot)
	}

	request := buildOnlineItemTradeMoveRequest(dnfrepo.MainInventoryListType, onlineCrystalTradeSlot, 0, onlineCrystalTradeMovedCount, currentItemTradeListType, 0)
	handled, err := fixture.service.handleAlignedGameCommand(fixture.offerer, byte(dnfenum.GameCmdCommand), currentItemTradeMoveMsg, request)
	if err != nil || !handled {
		t.Fatalf("warehouse crystal offer handled=%t err=%v", handled, err)
	}

	// 1=成功；来源 list0/0:358、数量1000；目标交易 list4，服务端分配的可见行是 3。
	wantACK := []byte{1, 0, 0x66, 0x01, 0xe8, 0x03, 0x00, 0x00, 4, 3, 0}
	ack, rest := splitGameServerUpperPacket(t, fixture.offerConn.write.Bytes())
	if ack.Header.Classification != dnfproto.DefaultChannelClassification || ack.Header.MsgID != currentItemTradeMoveMsg ||
		!bytes.Equal(ack.Body, wantACK) || len(rest) != 0 {
		t.Fatalf("offer ack class=%d msg=%d body=% X trailing=%d", ack.Header.Classification, ack.Header.MsgID, ack.Body, len(rest))
	}
	change, rest := splitGameServerUpperPacket(t, fixture.peerConn.write.Bytes())
	if change.Header.Classification != 0 || change.Header.MsgID != currentItemTradeChangeMsg || len(change.Body) != currentItemListEntryWireSize ||
		binary.LittleEndian.Uint16(change.Body[0:2]) != currentItemTradeOfferFirst ||
		binary.LittleEndian.Uint32(change.Body[2:6]) != onlineCrystalTradeItemID ||
		binary.LittleEndian.Uint32(change.Body[6:10]) != onlineCrystalTradeMovedCount || len(rest) != 0 {
		t.Fatalf("peer crystal row class=%d msg=%d body=% X trailing=%d", change.Header.Classification, change.Header.MsgID, change.Body, len(rest))
	}

	// 挂单只登记，不扣资产：账号仓库仍是 5000，角色背包仍是空。
	account, found, err := fixture.repositories.AccountInventory.Load(ctx, onlineCrystalTradeAccount)
	if err != nil || !found || len(account.Slots) != 1 ||
		account.Slots[dnfrepo.AccountSharedInventorySlotKey(onlineCrystalTradeSlot)].ItemID != onlineCrystalTradeItemID ||
		account.Slots[dnfrepo.AccountSharedInventorySlotKey(onlineCrystalTradeSlot)].Count != onlineCrystalTradeCount {
		t.Fatalf("account warehouse while offer pending = %+v found=%t err=%v", account.Slots, found, err)
	}
	bag, found, err := fixture.repositories.Inventory.Load(ctx, "1")
	if err != nil || !found || len(bag.Slots) != 0 {
		t.Fatalf("character bag while offer pending = %+v found=%t err=%v", bag.Slots, found, err)
	}

	fixture.offerConn.write.Reset()
	fixture.peerConn.write.Reset()
	fixture.service.cancelOnlineItemTrade(fixture.service.currentOnlineItemTrade(1), "test_cancel")
	// 取消回执按各自持有的挂单渲染：发起方带回 3/list0/0:358 一行，对端没有挂单所以是 0 行。
	offererCancel, rest := splitGameServerUpperPacket(t, fixture.offerConn.write.Bytes())
	if offererCancel.Header.Classification != 0 || offererCancel.Header.MsgID != currentItemTradeCancelMsg ||
		!bytes.Equal(offererCancel.Body, []byte{1, 0, 3, 0, 0, 0x66, 0x01}) || len(rest) != 0 {
		t.Fatalf("offerer cancel class=%d msg=%d body=% X trailing=%d", offererCancel.Header.Classification, offererCancel.Header.MsgID, offererCancel.Body, len(rest))
	}
	peerCancel, rest := splitGameServerUpperPacket(t, fixture.peerConn.write.Bytes())
	if peerCancel.Header.Classification != 0 || peerCancel.Header.MsgID != currentItemTradeCancelMsg ||
		!bytes.Equal(peerCancel.Body, []byte{0, 0}) || len(rest) != 0 {
		t.Fatalf("peer cancel class=%d msg=%d body=% X trailing=%d", peerCancel.Header.Classification, peerCancel.Header.MsgID, peerCancel.Body, len(rest))
	}
	if fixture.service.currentOnlineItemTrade(1) != nil || fixture.service.currentOnlineItemTrade(5) != nil {
		t.Fatal("cancelled warehouse crystal offer remained registered")
	}
	afterCancel, found, err := fixture.repositories.AccountInventory.Load(ctx, onlineCrystalTradeAccount)
	if err != nil || !found || len(afterCancel.Slots) != 1 ||
		afterCancel.Slots[dnfrepo.AccountSharedInventorySlotKey(onlineCrystalTradeSlot)].ItemID != onlineCrystalTradeItemID ||
		afterCancel.Slots[dnfrepo.AccountSharedInventorySlotKey(onlineCrystalTradeSlot)].Count != onlineCrystalTradeCount {
		t.Fatalf("account warehouse after cancel = %+v found=%t err=%v", afterCancel.Slots, found, err)
	}
}

func TestOnlineItemTradeRejectsAccountWarehouseCrystalWithoutPeerProjection(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name       string
		sourceSlot int16
		moveCount  int32
		mutate     func(*testing.T, *onlineCrystalTradeFixture)
	}{
		{
			name:   "session_account_not_from_token",
			mutate: func(_ *testing.T, fixture *onlineCrystalTradeFixture) { fixture.offerer.accountFromToken = false },
		},
		{
			name:   "session_account_missing",
			mutate: func(_ *testing.T, fixture *onlineCrystalTradeFixture) { fixture.offerer.accountID = "" },
		},
		{
			name: "character_bound_to_another_account",
			mutate: func(t *testing.T, fixture *onlineCrystalTradeFixture) {
				if err := fixture.repositories.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "1", AccountID: "c"}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:       "soul_warehouse_slot_is_not_a_tradable_crystal",
			sourceSlot: onlineCrystalTradeSoulSlot,
			mutate: func(t *testing.T, fixture *onlineCrystalTradeFixture) {
				saveOnlineCrystalTradeAccountSlots(t, fixture.repositories, map[string]dnfrepo.ItemStack{
					dnfrepo.AccountSharedInventorySlotKey(onlineCrystalTradeSoulSlot): {ItemID: onlineCrystalTradeSoulItemID, Count: onlineCrystalTradeCount},
				})
			},
		},
		{
			name:       "crystal_stored_outside_its_warehouse_slot",
			sourceSlot: dnfrepo.CrystalWarehouseFirstSlot,
			mutate: func(t *testing.T, fixture *onlineCrystalTradeFixture) {
				saveOnlineCrystalTradeAccountSlots(t, fixture.repositories, map[string]dnfrepo.ItemStack{
					dnfrepo.AccountSharedInventorySlotKey(dnfrepo.CrystalWarehouseFirstSlot): {ItemID: onlineCrystalTradeItemID, Count: onlineCrystalTradeCount},
				})
			},
		},
		{
			name: "account_balance_below_pending_count",
			mutate: func(t *testing.T, fixture *onlineCrystalTradeFixture) {
				saveOnlineCrystalTradeAccountSlots(t, fixture.repositories, map[string]dnfrepo.ItemStack{
					dnfrepo.AccountSharedInventorySlotKey(onlineCrystalTradeSlot): {ItemID: onlineCrystalTradeItemID, Count: onlineCrystalTradeMovedCount - 1},
				})
			},
		},
		{
			name: "warehouse_trade_unit_of_work_missing",
			mutate: func(_ *testing.T, fixture *onlineCrystalTradeFixture) {
				fixture.repositories.CharacterTrade = crystalTradeWithoutWarehouseUnit{inner: fixture.repositories.CharacterTrade}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newOnlineCrystalTradeFixture(t, func(fixture *onlineCrystalTradeFixture) {
				if test.mutate != nil {
					test.mutate(t, fixture)
				}
			})
			sourceSlot := test.sourceSlot
			if sourceSlot == 0 {
				sourceSlot = onlineCrystalTradeSlot
			}
			moveCount := test.moveCount
			if moveCount == 0 {
				moveCount = onlineCrystalTradeMovedCount
			}
			before, found, err := fixture.repositories.AccountInventory.Load(ctx, onlineCrystalTradeAccount)
			if err != nil || !found {
				t.Fatalf("account warehouse pre-image found=%t err=%v", found, err)
			}
			request := buildOnlineItemTradeMoveRequest(dnfrepo.MainInventoryListType, sourceSlot, 0, moveCount, currentItemTradeListType, 0)
			handled, err := fixture.service.handleAlignedGameCommand(fixture.offerer, byte(dnfenum.GameCmdCommand), currentItemTradeMoveMsg, request)
			if err != nil || !handled {
				t.Fatalf("rejected warehouse crystal offer handled=%t err=%v", handled, err)
			}
			assertOnlineItemTradeOfferRejected(t, fixture)
			after, found, err := fixture.repositories.AccountInventory.Load(ctx, onlineCrystalTradeAccount)
			if err != nil || !found || !reflect.DeepEqual(before.Slots, after.Slots) {
				t.Fatalf("rejected warehouse crystal offer changed account = %+v found=%t err=%v", after.Slots, found, err)
			}
		})
	}
}

// assertOnlineItemTradeOfferRejected 断言被拒的挂单只回失败 ACK，不产生任何对端投影，
// 也没有在交易状态里留下挂单。
func assertOnlineItemTradeOfferRejected(t *testing.T, fixture *onlineCrystalTradeFixture) {
	t.Helper()
	ack, rest := splitGameServerUpperPacket(t, fixture.offerConn.write.Bytes())
	if ack.Header.Classification != dnfproto.DefaultChannelClassification || ack.Header.MsgID != currentItemTradeMoveMsg ||
		!bytes.Equal(ack.Body, []byte{0, 1}) || len(rest) != 0 {
		t.Fatalf("reject ack class=%d msg=%d body=% X trailing=%d", ack.Header.Classification, ack.Header.MsgID, ack.Body, len(rest))
	}
	if projected := fixture.peerConn.write.Bytes(); len(projected) != 0 {
		t.Fatalf("rejected warehouse crystal offer projected to peer: % X", projected)
	}
	trade := fixture.service.currentOnlineItemTrade(1)
	if trade == nil || len(trade.offers[1]) != 0 {
		t.Fatalf("rejected warehouse crystal offer staged trade state: %+v", trade)
	}
}

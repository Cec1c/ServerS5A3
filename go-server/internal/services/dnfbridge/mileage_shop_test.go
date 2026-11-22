package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/mileage"
	"longheng.io/server/internal/modules/dnf/pvf"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"reflect"
	"strconv"
	"testing"
)

func mileageBridgeFixture(t *testing.T, catalog *pvfCeraShopCatalog) (*Service, *gameSession, r.Group) {
	t.Helper()
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	a := r.AccountRecord{AccountID: "account-1", Metadata: map[string]string{"account_cera": "9999", "account_token_cera": "8888"}}
	if err := mileage.AccrueSpend(&a, 1000000, 20000); err != nil {
		t.Fatal(err)
	}
	if err := g.Account.Save(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := g.Character.Save(ctx, r.CharacterRecord{AccountID: a.AccountID, CharacterID: "19", Level: 90, Stats: map[string]int64{}}); err != nil {
		t.Fatal(err)
	}
	if err := g.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "19", Slots: map[string]r.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	p := &gameSession{conn: &bufferConn{}, connID: "mileage-test", accountID: a.AccountID, selectedCharacterID: 19, characterGeneration: 1, loginPacketSeen: true, townSceneReadyCharacterID: 19}
	s := &Service{options: options{accountID: a.AccountID, currentClientVersion: "official-24.9", gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain, mileageShopEnabled: true}, ceraShopCatalog: catalog, gameSessions: map[uint16]*gameSession{19: p}, repositoryProvider: func() (r.Group, bool) { return g, true }}
	return s, p, g
}
func mileageRequest(id uint32) []byte { b := currentCeraShopTestRequestBody(id); b[0] = 1; return b }
func mileageBalance(t *testing.T, g r.Group) mileage.State {
	t.Helper()
	a, _, e := g.Account.Load(context.Background(), "account-1")
	if e != nil {
		t.Fatal(e)
	}
	s, e := mileage.Load(a)
	if e != nil {
		t.Fatal(e)
	}
	return s
}

func TestMileageNativePurchaseReplayLimitsAndFullBag(t *testing.T) {
	c := mustCurrentCeraShopTestCatalog(t)
	c.source.(bridgePVFSource)["etc/mileageshop_chn.etc"] = "[item]\n1001 37 3 0 5 0 `one` 0 0\n1002 37 1 0 10 0 `limited` 101 0\n[/item]\n[purchasing limit]\n1002 1 0 0 0 0\n[/purchasing limit]\n[limit mileage by cash]\n20000\n[/limit mileage by cash]"
	s, p, g := mileageBridgeFixture(t, c)
	seq := p.guild.purchaseSequence.observe(100)
	body := mileageRequest(1001)
	if err := s.handleCurrentCeraShopPurchase(p, body, &seq); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, p.conn.(*bufferConn).write.Bytes())
	if ack.Header.MsgID != 64 {
		t.Fatalf("purchase ACK opcode=%d", ack.Header.MsgID)
	}
	itemUpdate, rest := splitGameServerUpperPacket(t, rest)
	if itemUpdate.Header.MsgID != 14 {
		t.Fatalf("purchase item refresh opcode=%d", itemUpdate.Header.MsgID)
	}
	statePacket, trailing := splitGameServerUpperPacket(t, rest)
	if len(trailing) != 0 || statePacket.Header.Classification != 0 || statePacket.Header.MsgID != currentMileageStateMsgID {
		t.Fatalf("mileage state header=%+v trailing=%d", statePacket.Header, len(trailing))
	}
	if want := buildCurrentMileageStateBody(mileageBalance(t, g)); !bytes.Equal(statePacket.Body, want) {
		t.Fatalf("mileage state body=%x want=%x", statePacket.Body, want)
	}
	p.conn.(*bufferConn).write.Reset()
	if b := mileageBalance(t, g).Balance; b != 19995 {
		t.Fatal("purchase not settled", b)
	}
	inv, _, _ := g.Inventory.Load(context.Background(), "19")
	var total int64
	for _, item := range inv.Slots {
		if item.ItemID == 37 {
			total += item.Count
		}
	}
	if total != 3 {
		t.Fatal("wrong grant", inv)
	}
	before := mileageBalance(t, g)
	if err := s.handleCurrentCeraShopPurchase(p, body, &seq); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, mileageBalance(t, g)) {
		t.Fatal("replay charged points")
	}
	seq = p.guild.purchaseSequence.observe(101)
	if err := s.handleCurrentCeraShopPurchase(p, mileageRequest(1002), &seq); err != nil {
		t.Fatal(err)
	}
	before = mileageBalance(t, g)
	seq = p.guild.purchaseSequence.observe(102)
	s.handleCurrentCeraShopPurchase(p, mileageRequest(1002), &seq)
	if !reflect.DeepEqual(before, mileageBalance(t, g)) {
		t.Fatal("account limit bypassed")
	}
	inv, _, _ = g.Inventory.Load(context.Background(), "19")
	inv.Slots = map[string]r.ItemStack{}
	for i := 0; i < 1000; i++ {
		inv.Slots["0:"+strconv.Itoa(i)] = r.ItemStack{ItemID: 38, Count: 1}
	}
	g.Inventory.Save(context.Background(), inv)
	seq = p.guild.purchaseSequence.observe(103)
	s.handleCurrentCeraShopPurchase(p, body, &seq)
	if !reflect.DeepEqual(before, mileageBalance(t, g)) {
		t.Fatal("full bag lost points")
	}
	a, _, _ := g.Account.Load(context.Background(), "account-1")
	if a.Metadata["account_cera"] != "9999" || a.Metadata["account_token_cera"] != "8888" {
		t.Fatal("mileage touched wallets")
	}
	if err := mileage.Save(&a, mileage.State{Version: 1, Balance: 1}); err != nil {
		t.Fatal(err)
	}
	g.Account.Save(context.Background(), a)
	before = mileageBalance(t, g)
	seq = p.guild.purchaseSequence.observe(104)
	s.handleCurrentCeraShopPurchase(p, body, &seq)
	if !reflect.DeepEqual(before, mileageBalance(t, g)) {
		t.Fatal("insufficient balance charged")
	}
	stateBody := buildCurrentMileageStateBody(mileage.State{Balance: 123, CashEarned: 9})
	if len(stateBody) != 9 || stateBody[0] != 0 || binary.LittleEndian.Uint32(stateBody[1:5]) != 123 || binary.LittleEndian.Uint32(stateBody[5:]) != 9 {
		t.Fatal(stateBody)
	}
}

func TestMileageNativeStateRefreshesVisibleBalance(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	a := r.AccountRecord{AccountID: "account-1", Metadata: map[string]string{}}
	if err := mileage.Save(&a, mileage.State{
		Version:     1,
		Balance:     907,
		CashEarned:  906,
		Day:         "2026-11-18",
		DailyEarned: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := g.Account.Save(ctx, a); err != nil {
		t.Fatal(err)
	}
	conn := &bufferConn{}
	p := &gameSession{
		conn: conn, connID: "mileage-native-state", accountID: a.AccountID,
		selectedCharacterID: 19, characterGeneration: 1, loginPacketSeen: true,
	}
	s := &Service{
		options: options{
			accountID: a.AccountID, currentClientVersion: "official-24.9",
			gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain,
			mileageShopEnabled: true,
		},
		gameSessions:       map[uint16]*gameSession{19: p},
		repositoryProvider: func() (r.Group, bool) { return g, true },
	}
	if err := s.sendCurrentMileageState(p); err != nil {
		t.Fatal(err)
	}
	statePacket, trailing := splitGameServerUpperPacket(t, conn.write.Bytes())
	if len(trailing) != 0 || statePacket.Header.Classification != 0 || statePacket.Header.MsgID != 427 {
		t.Fatalf("native mileage snapshot header=%+v trailing=%d", statePacket.Header, len(trailing))
	}
	if len(statePacket.Body) != 9 || statePacket.Body[0] != 0 ||
		binary.LittleEndian.Uint32(statePacket.Body[1:5]) != 907 ||
		binary.LittleEndian.Uint32(statePacket.Body[5:9]) != 906 {
		t.Fatalf("native mileage snapshot body=%x", statePacket.Body)
	}
}

func TestMileageNativeRejectsMalformedAndCouponRequests(t *testing.T) {
	valid := mileageRequest(1001)
	for _, offset := range []int{0, 1, 2, 3, 4, 11, 12, 13} {
		b := append([]byte(nil), valid...)
		b[offset] = 2
		if _, err := parseCurrentMileagePurchase(b); err == nil {
			t.Fatal("accepted malformed", offset)
		}
	}
	if _, err := parseCurrentMileagePurchase(append(valid, 0)); err == nil {
		t.Fatal("accepted trailing bytes")
	}
}

func TestRealPVFMileageCatalogAndEveryRemainingProduct(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("explicit real PVF required")
	}
	a, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	c, err := newPVFCeraShopCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	m, err := parseCurrentMileageCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	list, err := parseDungeonCardPVFDocument(a, worldmap.DefaultDungeonList)
	if err != nil {
		t.Fatal(err)
	}
	var bloodyLane worldmap.Dungeon
	for _, ref := range pvf.ParseList(list) {
		if ref.ID != 7155 {
			continue
		}
		name := "dungeon/" + ref.Path
		doc, err := parseDungeonCardPVFDocument(a, name)
		if err != nil {
			t.Fatal(err)
		}
		bloodyLane = worldmap.ParseDungeon(ref.ID, name, doc)
		break
	}
	if !currentDungeonIsRecommendedClear(&runtimeDungeonState{
		Dungeon: bloodyLane, Character: r.CharacterRecord{Level: 90}, entryCharacterLevel: 90, ordinaryFinalRoomClearAccepted: true,
	}) {
		t.Fatal("level 90 Bloody Lane clear must qualify for mileage")
	}
	t.Run("token-letter-pack", func(t *testing.T) {
		const id = 102930
		product := c.products[id]
		if product.Count != 100 || product.CeraPrice == 0 {
			t.Fatal(product)
		}
		s, player, repos := mileageBridgeFixture(t, c)
		account := r.AccountRecord{AccountID: "account-1", Metadata: map[string]string{"account_cera": "0", "account_token_cera": strconv.FormatUint(uint64(product.CeraPrice), 10)}}
		if err := repos.Account.Save(context.Background(), account); err != nil {
			t.Fatal(err)
		}
		seq := player.guild.purchaseSequence.observe(123)
		if err := s.handleCurrentCeraShopPurchase(player, currentCeraShopTestRequestBody(id), &seq); err != nil {
			t.Fatal(err)
		}
		state := mileageBalance(t, repos)
		want := int64(product.CeraPrice) * 3 / 100
		if state.Balance != want || state.CashEarned != want {
			t.Fatalf("product=%+v expected=%d state=%+v", product, want, state)
		}
		packets := player.conn.(*bufferConn).write.Bytes()
		for len(packets) > 0 {
			packet, rest := splitGameServerUpperPacket(t, packets)
			if packet.Header.Classification == 0 && packet.Header.MsgID == currentMileageStateMsgID && bytes.Equal(packet.Body, buildCurrentMileageStateBody(state)) {
				return
			}
			packets = rest
		}
		t.Fatal("native mileage snapshot missing after token purchase")
	})
	if len(m.Products) != 12 || m.CashLimit != 20000 {
		t.Fatal(m)
	}
	if _, found := m.Products[1000064]; found {
		t.Fatal("removed sun credential remains")
	}
	for id, product := range m.Products {
		t.Run(strconv.Itoa(int(id)), func(t *testing.T) {
			s, p, g := mileageBridgeFixture(t, c)
			seq := p.guild.purchaseSequence.observe(100)
			if err := s.handleCurrentCeraShopPurchase(p, mileageRequest(id), &seq); err != nil {
				t.Fatal(err)
			}
			if got := mileageBalance(t, g).Balance; got != 20000-int64(product.Rule.Price) {
				t.Fatalf("commodity%d not settled, balance%d", id, got)
			}
			inv, _, _ := g.Inventory.Load(context.Background(), "19")
			var count int64
			for _, item := range inv.Slots {
				if item.ItemID == int64(product.Item) {
					count += item.Count
				}
			}
			if count != int64(product.Rule.Count) {
				t.Fatal("wrong item/count", count, product)
			}
		})
	}
}

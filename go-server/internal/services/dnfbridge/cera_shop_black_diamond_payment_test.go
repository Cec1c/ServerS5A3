package dnfbridge

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	dnfcerashop "longheng.io/server/internal/modules/dnf/cerashop"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const (
	blackDiamondPaymentCommodityID uint32 = 900129
	blackDiamondPaymentPrice       uint32 = 3000
	blackDiamondPaymentAccountID          = "black-diamond-payment-account"
	blackDiamondPaymentCharacterID uint16 = 19
)

type blackDiamondPaymentFixture struct {
	service      *Service
	repositories dnfrepo.Group
	connection   *bufferConn
	session      *gameSession
}

func newBlackDiamondPaymentFixture(t *testing.T, cera, tokenCera int64, itemID uint32, tokenOnly bool) *blackDiamondPaymentFixture {
	t.Helper()
	catalog := mustCurrentCeraShopTestCatalog(t)
	catalog.products[blackDiamondPaymentCommodityID] = currentCeraShopProduct{
		CommodityID: blackDiamondPaymentCommodityID,
		ItemID:      itemID,
		Count:       1,
		CeraPrice:   blackDiamondPaymentPrice,
		Section:     "regular package",
	}
	definitionPath := "cash/test_potion.stk"
	if itemID == currentBlackDiamondGiftPackageItemID {
		definitionPath = "cash/490709931.stk"
	}
	catalog.items.itemCache[itemID] = dungeonDropItemDefinition{
		ItemID:        itemID,
		Kind:          dungeonDropItemStackable,
		PVFPath:       definitionPath,
		StackableType: "[usable cera package]",
		StackLimit:    1000,
		SlotStart:     9,
		SlotEnd:       56,
		TokenCeraOnly: tokenOnly,
	}

	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	account := dnfrepo.AccountRecord{AccountID: blackDiamondPaymentAccountID, Metadata: map[string]string{}}
	dnfcerashop.SetBalance(&account, cera)
	dnfcerashop.SetTokenCeraBalance(&account, tokenCera)
	dnfcerashop.SetHappyTokenCeraBalance(&account, 0)
	if err := repositories.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   blackDiamondPaymentAccountID,
		Stats:       map[string]int64{"cera": cera, "gold": 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots:       map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}

	connection := &bufferConn{}
	service := &Service{
		options: options{
			accountID:          blackDiamondPaymentAccountID,
			gameUpperHeader:    gameUpperHeaderChannel13,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		ceraShopCatalog: catalog,
		// Supplying the catalog prevents an unrelated runtime-archive load in the
		// normal-item checkout path.
		premiumCatalog: mustCurrentCeraShopPremiumCatalog(),
		repositoryProvider: func() (dnfrepo.Group, bool) {
			return repositories, true
		},
	}
	session := &gameSession{
		conn:                connection,
		connID:              t.Name(),
		accountID:           blackDiamondPaymentAccountID,
		selectedCharacterID: blackDiamondPaymentCharacterID,
		characterGeneration: 1,
	}
	service.mu.Lock()
	service.gameSessions = map[uint16]*gameSession{blackDiamondPaymentCharacterID: session}
	service.mu.Unlock()
	return &blackDiamondPaymentFixture{service: service, repositories: repositories, connection: connection, session: session}
}

func (f *blackDiamondPaymentFixture) checkout(t *testing.T) {
	t.Helper()
	if err := f.service.handleCurrentCeraShopPurchase(
		f.session,
		currentCeraShopTestRequestBody(blackDiamondPaymentCommodityID),
	); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, f.connection.write.Bytes())
	if ack.Header.Classification != dnfproto.DefaultChannelClassification ||
		ack.Header.MsgID != uint16(dnfenum.CmdPacketBuyCerashopItem) ||
		!bytes.Equal(ack.Body, buildCurrentCeraShopPurchaseSuccessBodyWithCount(blackDiamondPaymentCommodityID, 1)) {
		t.Fatalf("purchase ACK header=%+v body=%x", ack.Header, ack.Body)
	}
}

func (f *blackDiamondPaymentFixture) wallet(t *testing.T) dnfcerashop.Wallet {
	t.Helper()
	account, found, err := f.repositories.Account.Load(context.Background(), blackDiamondPaymentAccountID)
	if err != nil || !found {
		t.Fatalf("load account found=%t err=%v", found, err)
	}
	return dnfcerashop.WalletBalance(account)
}

func (f *blackDiamondPaymentFixture) hasDeliveredPackage(t *testing.T, itemID uint32) bool {
	t.Helper()
	inventory, found, err := f.repositories.Inventory.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	for _, stack := range inventory.Slots {
		if stack.ItemID == int64(itemID) && stack.Count == 1 {
			return true
		}
	}
	return false
}

func TestCurrentCeraShopBlackDiamondGiftAcceptsCeraAndTokenCera(t *testing.T) {
	tests := []struct {
		name       string
		cera       int64
		tokenCera  int64
		wantWallet dnfcerashop.Wallet
	}{
		{name: "cera only", cera: int64(blackDiamondPaymentPrice), wantWallet: dnfcerashop.Wallet{}},
		{name: "token Cera only", tokenCera: int64(blackDiamondPaymentPrice), wantWallet: dnfcerashop.Wallet{}},
		{name: "mixed uses token before Cera", cera: 500, tokenCera: 2500, wantWallet: dnfcerashop.Wallet{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newBlackDiamondPaymentFixture(t, test.cera, test.tokenCera, currentBlackDiamondGiftPackageItemID, true)
			f.checkout(t)
			if got := f.wallet(t); got != test.wantWallet {
				t.Fatalf("wallet after purchase=%+v want=%+v", got, test.wantWallet)
			}
			if !f.hasDeliveredPackage(t, currentBlackDiamondGiftPackageItemID) {
				t.Fatalf("item %d was not delivered", currentBlackDiamondGiftPackageItemID)
			}
		})
	}
}

func TestCurrentCeraShopOtherTokenOnlyItemStillRejectsCeraFallback(t *testing.T) {
	const ordinaryItemID uint32 = 37
	f := newBlackDiamondPaymentFixture(t, int64(blackDiamondPaymentPrice), 0, ordinaryItemID, true)
	before := f.wallet(t)
	if err := f.service.handleCurrentCeraShopPurchase(
		f.session,
		currentCeraShopTestRequestBody(blackDiamondPaymentCommodityID),
	); err != nil {
		t.Fatal(err)
	}
	ack, trailing := splitGameServerUpperPacket(t, f.connection.write.Bytes())
	if len(trailing) != 0 ||
		ack.Header.Classification != dnfproto.DefaultChannelClassification ||
		ack.Header.MsgID != uint16(dnfenum.CmdPacketBuyCerashopItem) ||
		!bytes.Equal(ack.Body, buildCurrentCeraShopPurchaseFailureBody()) {
		t.Fatalf("failure ACK header=%+v body=%x trailing=%d", ack.Header, ack.Body, len(trailing))
	}
	if got := f.wallet(t); got != before {
		t.Fatalf("rejected purchase changed wallet=%+v before=%+v", got, before)
	}
	if f.hasDeliveredPackage(t, ordinaryItemID) {
		t.Fatalf("rejected token-only item %d was delivered", ordinaryItemID)
	}
}

func TestRealPVFBlackDiamondGiftMapsToCheckoutWalletException(t *testing.T) {
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_BLACK_DIAMOND_PVF"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_BLACK_DIAMOND_PVF to Script.pvf")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatalf("load Script.pvf: %v", err)
	}
	catalog, err := newPVFCeraShopCatalog(archive)
	if err != nil {
		t.Fatalf("load Cera shop catalog: %v", err)
	}
	product, found := catalog.Product(blackDiamondPaymentCommodityID)
	if !found || product.ItemID != currentBlackDiamondGiftPackageItemID ||
		product.Count != 1 || product.CeraPrice != blackDiamondPaymentPrice || product.Section != "regular package" {
		t.Fatalf("commodity %d=%+v found=%t", blackDiamondPaymentCommodityID, product, found)
	}
	definition, err := catalog.items.ResolveItem(product.ItemID)
	if err != nil {
		t.Fatalf("resolve item %d: %v", product.ItemID, err)
	}
	if !definition.TokenCeraOnly || definition.CeraOnly {
		t.Fatalf("item %d payment markers: CeraOnly=%t TokenCeraOnly=%t", product.ItemID, definition.CeraOnly, definition.TokenCeraOnly)
	}
	ceraOnlyCost, tokenOnlyCost := currentCeraShopRestrictedWalletCosts(product.ItemID, definition, product.CeraPrice)
	if ceraOnlyCost != 0 || tokenOnlyCost != 0 {
		t.Fatalf("black diamond gift restrictions: cera=%d token=%d", ceraOnlyCost, tokenOnlyCost)
	}
}

package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	"testing"
)

const (
	blackDiamondShopCommodityID uint32 = 900129
	blackDiamondShopPrice              = 3000
)

func blackDiamondShopCartBody(commodityID uint32) []byte {
	body := make([]byte, currentCeraShopRequestHeaderSize+currentCeraShopRequestItemStride)
	body[2] = 1
	body[4] = currentCeraShopPaymentModeCera
	body[5] = 0xff
	binary.LittleEndian.PutUint32(body[7:11], commodityID)
	return body
}

func (f *blackDiamondShopFixture) purchase(t *testing.T) {
	t.Helper()
	f.connection.write.Reset()
	if err := f.service.handleCurrentCeraShopPurchase(
		f.session,
		blackDiamondShopCartBody(blackDiamondShopCommodityID),
	); err != nil {
		t.Fatalf("purchase commodity %d: %v", blackDiamondShopCommodityID, err)
	}
	ack, _ := splitGameServerUpperPacket(t, f.connection.write.Bytes())
	if ack.Header.Classification != dnfproto.DefaultChannelClassification ||
		ack.Header.MsgID != uint16(dnfenum.CmdPacketBuyCerashopItem) ||
		!bytes.Equal(ack.Body, buildCurrentCeraShopPurchaseSuccessBodyWithCount(blackDiamondShopCommodityID, 1)) {
		t.Fatalf("purchase ACK header=%+v body=%x", ack.Header, ack.Body)
	}
}

func TestRealBlackDiamondShelfTokenCheckout(t *testing.T) {
	f := newBlackDiamondShopFixture(t, 3500, 10000, 9000)
	product, ok := f.shop.Product(blackDiamondShopCommodityID)
	if !ok || product.ItemID != blackDiamondShopPackageID || product.CeraPrice != blackDiamondShopPrice || product.Section != "regular package" {
		t.Fatalf("unexpected shelf row: %+v", product)
	}
	f.purchase(t)
	wallet := f.wallet(t)
	if wallet.TokenCera != 500 || wallet.Cera != 10000 || f.gold(t) != 9000 || f.characterCera(t) != 10000 {
		t.Fatalf("unexpected payment: %+v", wallet)
	}
	_, pack := blackDiamondShopMainStack(t, f.inventory(t), blackDiamondShopPackageID)
	if pack.Count != 1 || f.premiumExpiry(t) != 0 {
		t.Fatal("expected one unopened package")
	}
}

func TestRealBlackDiamondShopInsufficientTokenCeraNeverFallsBackToCeraOrDelivers(t *testing.T) {
	const pointBalance int64 = 987654321
	const goldBalance int64 = 123456789
	const shortTokenBalance int64 = blackDiamondShopPrice - 1
	f := newBlackDiamondShopFixture(t, shortTokenBalance, pointBalance, goldBalance)

	f.connection.write.Reset()
	if err := f.service.handleCurrentCeraShopPurchase(
		f.session,
		blackDiamondShopCartBody(blackDiamondShopCommodityID),
	); err != nil {
		t.Fatalf("insufficient-token purchase should be answered, not fail the connection: %v", err)
	}
	ack, trailing := splitGameServerUpperPacket(t, f.connection.write.Bytes())
	if len(trailing) != 0 ||
		ack.Header.Classification != dnfproto.DefaultChannelClassification ||
		ack.Header.MsgID != uint16(dnfenum.CmdPacketBuyCerashopItem) ||
		!bytes.Equal(ack.Body, buildCurrentCeraShopPurchaseFailureBody()) {
		t.Fatalf("failure ACK header=%+v body=%x trailing=%d", ack.Header, ack.Body, len(trailing))
	}
	wallet := f.wallet(t)
	if wallet.TokenCera != shortTokenBalance || wallet.Cera != pointBalance || wallet.HappyTokenCera != 0 ||
		f.gold(t) != goldBalance || f.characterCera(t) != pointBalance {
		t.Fatalf("failed purchase mutated wallet=%+v gold=%d legacy_cera=%d", wallet, f.gold(t), f.characterCera(t))
	}
	if inventory := f.inventory(t); len(inventory.Slots) != 0 {
		t.Fatalf("failed purchase delivered inventory rows: %+v", inventory.Slots)
	}
	if f.premiumExpiry(t) != 0 {
		t.Fatal("failed purchase activated black diamond")
	}
}

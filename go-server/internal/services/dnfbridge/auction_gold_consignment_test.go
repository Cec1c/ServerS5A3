package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	auction "longheng.io/server/internal/modules/dnf/auction"
	shop "longheng.io/server/internal/modules/dnf/cerashop"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
)

func goldBridgeFixture(t *testing.T) (repo.Group, *Service, *gameSession, *bufferConn) {
	t.Helper()
	r := memory.NewMemoryGroup()
	ctx := context.Background()
	for _, id := range []string{"10", "11"} {
		a := repo.AccountRecord{AccountID: "a-" + id}
		shop.SetBalance(&a, 1000)
		if e := r.Account.Save(ctx, a); e != nil {
			t.Fatal(e)
		}
		if e := r.Character.Save(ctx, repo.CharacterRecord{CharacterID: id, AccountID: a.AccountID, Level: 90, Name: id, Stats: map[string]int64{"gold": 10000000}}); e != nil {
			t.Fatal(e)
		}
		r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: id, Slots: map[string]repo.ItemStack{}})
	}
	s := &Service{options: options{gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain}, repositoryProvider: func() (repo.Group, bool) { return r, true }}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, selectedCharacterID: 10, accountID: "a-10"}
	return r, s, session, conn
}
func goldRegisterBody() []byte {
	b := make([]byte, 28)
	b[0] = 1
	binary.LittleEndian.PutUint32(b[4:8], 2681725)
	binary.LittleEndian.PutUint32(b[8:12], 1)
	binary.LittleEndian.PutUint32(b[12:16], 0xffffffff)
	binary.LittleEndian.PutUint32(b[16:20], 111)
	binary.LittleEndian.PutUint32(b[24:28], 24)
	return b
}
func TestCurrentAuctionGoldNativeRegisterSearchBuyAndRouteIsolation(t *testing.T) {
	r, s, seller, conn := goldBridgeFixture(t)
	if e := s.handleCurrentAuctionCommand(seller, 183, goldRegisterBody()); e != nil {
		t.Fatal(e)
	}
	p, rest := splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), 16)
	if p.Header.MsgID != 183 || !bytes.Equal(p.Body, []byte{1, 1}) {
		t.Fatal(p)
	}
	p, rest = splitGameServerUpperPacketWithHeader(t, rest, 16)
	if p.Header.MsgID != 14 || len(rest) != 0 {
		t.Fatal(p)
	}
	owner, e := auction.NewGoldConsignOwner(r)
	if e != nil {
		t.Fatal(e)
	}
	rows, e := owner.Search(context.Background(), nil)
	if e != nil || len(rows) != 1 {
		t.Fatal(rows, e)
	}
	category := make([]byte, 12)
	category[0] = 1
	binary.LittleEndian.PutUint16(category[5:7], 40119)
	conn.write.Reset()
	if e = s.handleCurrentAuctionCommand(seller, 187, category); e != nil {
		t.Fatal(e)
	}
	p, rest = splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), 16)
	if len(rest) != 0 || p.Header.MsgID != 186 || len(p.Body) != 8+0xa8 || p.Body[1] != 1 || binary.LittleEndian.Uint32(p.Body[8+9:8+13]) != 0xffffffff {
		t.Fatalf("寄售分类列表=%x", p.Body)
	}
	conn.write.Reset()
	ordinary := make([]byte, 20)
	binary.LittleEndian.PutUint16(ordinary[5:7], 40119)
	ordinary[0] = 0
	if e = s.handleCurrentAuctionCommand(seller, 187, ordinary); e != nil {
		t.Fatal(e)
	}
	p, _ = splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), 16)
	if len(p.Body) > 1 && p.Body[0] == 1 && p.Body[1] == 1 {
		t.Fatal("寄售单串入普通拍卖")
	}
	buyerConn := &bufferConn{}
	buyer := &gameSession{conn: buyerConn, selectedCharacterID: 11, accountID: "a-11"}
	buy := make([]byte, 17)
	buy[0] = 1
	binary.LittleEndian.PutUint32(buy[1:5], 111)
	binary.LittleEndian.PutUint64(buy[5:13], rows[0].ID)
	binary.LittleEndian.PutUint32(buy[13:17], 2681725)
	if e = s.handleCurrentAuctionCommand(buyer, 185, buy); e != nil {
		t.Fatal(e)
	}
	p, rest = splitGameServerUpperPacketWithHeader(t, buyerConn.write.Bytes(), 16)
	if !bytes.Equal(p.Body, []byte{1, 1}) {
		t.Fatal(p)
	}
	p, rest = splitGameServerUpperPacketWithHeader(t, rest, 16)
	if p.Header.MsgID != 53 || len(rest) != 0 || binary.LittleEndian.Uint32(p.Body[1:5]) != 889 {
		t.Fatal(p)
	}
	if len(mustGoldMailbox(t, r, "10").Mails) != 1 || len(mustGoldMailbox(t, r, "11").Mails) != 1 {
		t.Fatal("成交邮件缺失")
	}
	conn.write.Reset()
	if e = s.handleCurrentAuctionCommand(seller, 182, []byte{1, 0x7d, 0xeb, 0x28, 0, 0}); e != nil {
		t.Fatal(e)
	}
	p, _ = splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), 16)
	if p.Body[0] != 1 || p.Body[1] != 1 || binary.LittleEndian.Uint32(p.Body[2:6]) != 111 || binary.LittleEndian.Uint32(p.Body[6:10]) != 1 {
		t.Fatal(p)
	}
}
func mustGoldMailbox(t *testing.T, r repo.Group, id string) repo.MailboxRecord {
	t.Helper()
	m, ok, e := r.Mailbox.Load(context.Background(), id)
	if e != nil || !ok {
		t.Fatal(e, ok)
	}
	return m
}
func TestCurrentAuctionGoldRejectsWrongBodiesAndRoutesFailures(t *testing.T) {
	_, s, session, c := goldBridgeFixture(t)
	for _, tc := range []struct {
		op   uint16
		body []byte
	}{{183, []byte{1}}, {184, []byte{1}}, {185, []byte{1}}, {182, []byte{1}}, {187, append([]byte{1}, make([]byte, 19)...)}, {188, []byte{1, 0}}} {
		c.write.Reset()
		if e := s.handleCurrentAuctionCommand(session, tc.op, tc.body); e != nil {
			t.Fatal(e)
		}
		p, rest := splitGameServerUpperPacketWithHeader(t, c.write.Bytes(), 16)
		if len(rest) != 0 || len(p.Body) != 3 || p.Body[0] != 0 || p.Body[2] != 1 {
			t.Fatal(tc.op, p)
		}
	}
}
func TestCurrentAuctionGoldClockRestoresOfflineExpiryAndIgnoresHistory(t *testing.T) {
	r, s, _, _ := goldBridgeFixture(t)
	o, _ := auction.NewGoldConsignOwner(r)
	ctx := context.Background()
	if _, e := o.Register(ctx, auction.GoldConsignRegisterCommand{ListingID: 1, SellerID: "10", AccountID: "a-10", GoldItemID: 2681725, PriceCera: 111, DurationHour: 24}); e != nil {
		t.Fatal(e)
	}
	inv, _, _ := r.Inventory.Load(ctx, auction.EscrowCharacterID)
	stack := inv.Slots["goldconsign:listing:1"]
	stack.Extra["created_at"] = "1700000000"
	stack.Extra["expires_at"] = "1700000001"
	inv.Slots["goldconsign:listing:1"] = stack
	r.Inventory.Save(ctx, inv)
	for range 2 {
		pending, e := s.maintainCurrentAuctionLocked(ctx)
		if e != nil || pending {
			t.Fatal(pending, e)
		}
	}
	if len(mustGoldMailbox(t, r, "10").Mails) != 1 {
		t.Fatal("离线到期退款失败")
	}
	inv, _, _ = r.Inventory.Load(ctx, auction.EscrowCharacterID)
	inv.Slots = map[string]repo.ItemStack{}
	inv.Slots["auction:history:99"] = repo.ItemStack{ItemID: 1, Count: 1}
	r.Inventory.Save(ctx, inv)
	if pending, e := s.maintainCurrentAuctionLocked(ctx); e != nil || pending {
		t.Fatal("成交历史触发空轮询", pending, e)
	}
}

package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	auction "longheng.io/server/internal/modules/dnf/auction"
)

func TestPrivateGoldConsignFloorAtNativeCommandBoundary(t *testing.T) {
	for _, price := range []uint32{999, 1000, 1001} {
		r, s, session, conn := goldBridgeFixture(t)
		body := goldRegisterBody()
		binary.LittleEndian.PutUint32(body[16:20], price)
		if err := s.handleCurrentAuctionCommand(session, 183, body); err != nil {
			t.Fatal(err)
		}
		p, rest := splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), 16)
		if p.Header.MsgID != 183 {
			t.Fatal(p)
		}
		owner, err := auction.NewGoldConsignOwner(r)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := owner.Search(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		character, _, err := r.Character.Load(context.Background(), "10")
		if err != nil {
			t.Fatal(err)
		}
		if price < 1000 {
			if !bytes.Equal(p.Body, []byte{0, currentAuctionErrorPriceInvalid, 1}) || len(rest) != 0 || len(rows) != 0 || character.Stats["gold"] != 10000000 {
				t.Fatalf("under-floor request wrote listing/assets or wrong failure: price=%d packet=%x rows=%v gold=%d", price, p.Body, rows, character.Stats["gold"])
			}
		} else if !bytes.Equal(p.Body, []byte{1, 1}) || len(rows) != 1 || rows[0].PriceCera != int64(price) || character.Stats["gold"] != 9000000 {
			t.Fatalf("valid floor rejected/mispriced: price=%d packet=%x rows=%v gold=%d", price, p.Body, rows, character.Stats["gold"])
		}
	}
}

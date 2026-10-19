package dnfbridge

import (
	"encoding/binary"
	"testing"
)

// The client marks the container it lists from in op183 body[1]: 0 for the
// character's own list 0, and 36 for the account-scoped crystal/soul warehouse,
// whose entries are still wire slots 354..365. Container 36 is only meaningful
// inside that range - anywhere else names a container this server cannot debit
// and has to stay refused, so a malformed body cannot reach the owner with a
// character slot it would then read from the wrong table.
//
// Live capture this pins: 无色小晶块 at the account key 0:358 arrived as
// pay=0 container=36 slot=358 item=3037 count=2 start=-1 instant=20 unit=20
// duration=24, and was refused at decode before this change.
func TestCurrentAuctionRegisterDecodesAccountWarehouseContainer(t *testing.T) {
	body := make([]byte, currentAuctionRegisterRequestSize)
	body[0] = currentAuctionPayTypeGold
	body[1] = currentAuctionAccountContainerByte
	binary.LittleEndian.PutUint16(body[2:4], 358)
	binary.LittleEndian.PutUint32(body[4:8], 3037)
	binary.LittleEndian.PutUint32(body[8:12], 2)
	binary.LittleEndian.PutUint32(body[12:16], 0xffffffff)
	binary.LittleEndian.PutUint32(body[16:20], 20)
	binary.LittleEndian.PutUint32(body[20:24], 20)
	binary.LittleEndian.PutUint32(body[24:28], 24)

	request, err := decodeCurrentAuctionRegisterRequest(body)
	if err != nil {
		t.Fatalf("account warehouse registration refused: %v", err)
	}
	if !request.AccountOwned || request.ListType != currentExeInventoryMainListType ||
		request.Slot != 358 || request.ItemID != 3037 || request.Count != 2 {
		t.Fatalf("decoded request=%+v", request)
	}

	// The same container byte on a character-bag slot is not the warehouse.
	binary.LittleEndian.PutUint16(body[2:4], 9)
	if _, err := decodeCurrentAuctionRegisterRequest(body); err == nil {
		t.Fatal("container 36 outside the warehouse slot range was accepted")
	}

	// Container 0 keeps the unchanged character path.
	body[1] = currentExeInventoryMainListType
	character, err := decodeCurrentAuctionRegisterRequest(body)
	if err != nil || character.AccountOwned {
		t.Fatalf("character registration=%+v err=%v", character, err)
	}
}

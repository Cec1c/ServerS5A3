package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	dnfauction "longheng.io/server/internal/modules/dnf/auction"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func enableAuctionMutationExperiment(t *testing.T, enabled bool) {
	t.Helper()
	if enabled {
		t.Setenv("DNFBRIDGE_EXPERIMENTAL_PEAK", "on")
	} else {
		t.Setenv("DNFBRIDGE_EXPERIMENTAL_PEAK", "off")
	}
}

func startAuctionMutationGate(t *testing.T, s *Service, session *gameSession, opcode uint16, body []byte, writeErr error) (*auctionReadWriteGate, <-chan error) {
	t.Helper()
	gate := &auctionReadWriteGate{
		bufferConn: &bufferConn{},
		entered:    make(chan struct{}),
		release:    make(chan struct{}),
		writeErr:   writeErr,
	}
	session.conn = gate
	done := make(chan error, 1)
	go func() { done <- s.handleCurrentAuctionCommand(session, opcode, body) }()
	t.Cleanup(gate.Release)
	select {
	case <-gate.entered:
	case err := <-done:
		t.Fatalf("mutation ended before its first socket write: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("mutation never reached its first socket write")
	}
	return gate, done
}

func requireAuctionMarketLockReleased(t *testing.T, s *Service) {
	t.Helper()
	if !s.auctionMu.TryLock() {
		t.Fatal("mutation still holds auctionMu while its requester write is blocked")
	}
	s.auctionMu.Unlock()
}

func auctionCancelBody(listingID uint64) []byte {
	body := make([]byte, 9)
	body[0] = currentAuctionPayTypeGold
	binary.LittleEndian.PutUint64(body[1:], listingID)
	return body
}

func auctionBuyoutBody(listingID uint64, itemID int64, money int64) []byte {
	body := make([]byte, 30)
	body[0] = currentAuctionPayTypeGold
	binary.LittleEndian.PutUint32(body[1:5], uint32(int32(money)))
	binary.LittleEndian.PutUint64(body[5:13], listingID)
	binary.LittleEndian.PutUint32(body[26:30], uint32(itemID))
	return body
}

func auctionSearchItemsBody(itemID uint32) []byte {
	body := make([]byte, 14)
	body[0] = currentAuctionPayTypeGold
	body[7] = 1
	binary.LittleEndian.PutUint32(body[10:14], itemID)
	return body
}

func TestAuctionMutationFailedResponseReleasesMarketLockWhenExperimentOn(t *testing.T) {
	enableAuctionMutationExperiment(t, true)
	repositories, s, seller, _ := auctionReadFixture(t)
	gate, request := startAuctionMutationGate(
		t, s, seller, uint16(dnfenum.CmdPacketAuctionRegistCancel), auctionCancelBody(999), nil,
	)

	requireAuctionMarketLockReleased(t, s)
	peerConnection := &bufferConn{}
	peer := &gameSession{conn: peerConnection, selectedCharacterID: 11, accountID: "a-11"}
	requireAuctionReadIndependentWork(t, s, gate, request, func() error {
		return s.handleCurrentAuctionCommand(peer, uint16(dnfenum.CmdPacketAuctionMyBiddingInfo), []byte{currentAuctionPayTypeGold})
	})
	if peerConnection.write.Len() == 0 {
		t.Fatal("peer request produced no response while the failed mutation write was blocked")
	}
	if gate.write.Len() != 0 {
		t.Fatal("failed mutation wrote before its socket gate was released")
	}

	gate.Release()
	if err := <-request; err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacketWithHeader(t, gate.write.Bytes(), 16)
	if len(rest) != 0 || packet.Header.MsgID != uint16(dnfenum.CmdPacketAuctionRegistCancel) ||
		len(packet.Body) != 3 || packet.Body[0] != 0 || packet.Body[1] != currentAuctionErrorNoListing ||
		packet.Body[2] != currentAuctionPayTypeGold {
		t.Fatalf("failed mutation response header=%+v body=%x rest=%x", packet.Header, packet.Body, rest)
	}
	owner, err := dnfauction.NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.LoadListing(context.Background(), 900); err != nil {
		t.Fatalf("failed mutation changed the existing listing: %v", err)
	}
}

func TestAuctionMutationSuccessReleasesMarketLockWithSearchReplayFrozen(t *testing.T) {
	enableAuctionMutationExperiment(t, true)
	repositories, s, _, _ := auctionReadFixture(t)
	s.auctionItemEligibility = func(itemID uint32) bool { return itemID == 6001 || itemID == 6002 }
	listingID := seedAuctionMutationBiddingListing(t, repositories, 6002)

	bidderConnection := &bufferConn{}
	bidder := &gameSession{conn: bidderConnection, selectedCharacterID: 11, accountID: "a-11"}
	search := auctionSearchItemsBody(6002)
	if err := s.handleCurrentAuctionCommand(bidder, uint16(dnfenum.CmdPacketAuctionSearchByItemkey), search); err != nil {
		t.Fatal(err)
	}
	bidderConnection.write.Reset()

	bid := auctionBuyoutBody(listingID, 6002, 100)
	gate, request := startAuctionMutationGate(t, s, bidder, uint16(dnfenum.CmdPacketAuctionBidding), bid, nil)
	requireAuctionMarketLockReleased(t, s)

	ctx := context.Background()
	listing, err := loadAuctionMutationListing(repositories, listingID)
	if err != nil {
		t.Fatalf("load committed bid: %v", err)
	}
	if listing.BidderID != "11" || listing.BidTotal != 100 {
		t.Fatalf("bid was not committed before response write: %+v", listing)
	}
	bidderRecord, found, err := repositories.Character.Load(ctx, "11")
	if err != nil || !found || bidderRecord.Stats["gold"] != 10_000_000-100 {
		t.Fatalf("bidder wallet before response write found=%t err=%v stats=%+v", found, err, bidderRecord.Stats)
	}

	peerConnection := &bufferConn{}
	peer := &gameSession{conn: peerConnection, selectedCharacterID: 10, accountID: "a-10"}
	requireAuctionReadIndependentWork(t, s, gate, request, func() error {
		return s.handleCurrentAuctionCommand(peer, uint16(dnfenum.CmdPacketAuctionMyBiddingInfo), []byte{currentAuctionPayTypeGold})
	})
	if peerConnection.write.Len() == 0 {
		t.Fatal("peer request produced no response while the successful mutation write was blocked")
	}
	if gate.write.Len() != 0 {
		t.Fatal("successful mutation wrote before its socket gate was released")
	}

	gate.Release()
	if err := <-request; err != nil {
		t.Fatal(err)
	}
	opcodes := splitAuctionMutationOpcodes(t, gate.write.Bytes())
	if want := []uint16{
		uint16(dnfenum.CmdPacketAuctionBidding),
		uint16(dnfenum.CmdPacketWalkoutPartyMember),
		uint16(dnfenum.CmdPacketAuctionSearchByItemkey),
	}; !reflect.DeepEqual(opcodes, want) {
		t.Fatalf("mutation frame order=%v want %v", opcodes, want)
	}
}

func TestAuctionMutationOffKeepsRequesterWriteUnderMarketLock(t *testing.T) {
	enableAuctionMutationExperiment(t, false)
	_, s, seller, _ := auctionReadFixture(t)
	gate, request := startAuctionMutationGate(
		t, s, seller, uint16(dnfenum.CmdPacketAuctionRegistCancel), auctionCancelBody(900), nil,
	)

	if s.auctionMu.TryLock() {
		s.auctionMu.Unlock()
		t.Fatal("experiment off let the blocked requester write escape auctionMu")
	}
	gate.Release()
	if err := <-request; err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacketWithHeader(t, gate.write.Bytes(), 16)
	if len(rest) != 0 || packet.Header.MsgID != uint16(dnfenum.CmdPacketAuctionRegistCancel) ||
		!bytes.Equal(packet.Body, []byte{1, currentAuctionPayTypeGold}) {
		t.Fatalf("experiment-off cancel response header=%+v body=%x rest=%x", packet.Header, packet.Body, rest)
	}
}

func TestAuctionMutationBidBytesAndAssetsMatchExperimentOffAndOn(t *testing.T) {
	type capture struct {
		frames []byte
		assets auctionMutationTestAssets
	}
	run := func(enabled bool) capture {
		enableAuctionMutationExperiment(t, enabled)
		repositories, s, _, _ := auctionReadFixture(t)
		s.auctionItemEligibility = func(itemID uint32) bool { return itemID == 6001 || itemID == 6002 }
		listingID := seedAuctionMutationBiddingListing(t, repositories, 6002)

		bidderConnection := &bufferConn{}
		bidder := &gameSession{conn: bidderConnection, selectedCharacterID: 11, accountID: "a-11"}
		search := auctionSearchItemsBody(6002)
		if err := s.handleCurrentAuctionCommand(bidder, uint16(dnfenum.CmdPacketAuctionSearchByItemkey), search); err != nil {
			t.Fatal(err)
		}
		bidderConnection.write.Reset()
		if err := s.handleCurrentAuctionCommand(
			bidder, uint16(dnfenum.CmdPacketAuctionBidding), auctionBuyoutBody(listingID, 6002, 100),
		); err != nil {
			t.Fatal(err)
		}
		return capture{
			frames: append([]byte(nil), bidderConnection.write.Bytes()...),
			assets: snapshotAuctionMutationTestAssets(t, repositories),
		}
	}

	off := run(false)
	on := run(true)
	if !bytes.Equal(off.frames, on.frames) {
		t.Fatalf("experiment changed mutation frame bytes:\noff=%x\non =%x", off.frames, on.frames)
	}
	if opcodes := splitAuctionMutationOpcodes(t, off.frames); !reflect.DeepEqual(opcodes, []uint16{
		uint16(dnfenum.CmdPacketAuctionBidding),
		uint16(dnfenum.CmdPacketWalkoutPartyMember),
		uint16(dnfenum.CmdPacketAuctionSearchByItemkey),
	}) {
		t.Fatalf("mutation frame order=%v", opcodes)
	}
	if !reflect.DeepEqual(off.assets, on.assets) {
		t.Fatalf("experiment changed final assets:\noff=%+v\non =%+v", off.assets, on.assets)
	}
}

func TestAuctionMutationFailedResponseKeepsCommittedAssetsAndDoesNotReexecute(t *testing.T) {
	enableAuctionMutationExperiment(t, true)
	repositories, s, _, _ := auctionReadFixture(t)
	buyer := &gameSession{conn: &bufferConn{}, selectedCharacterID: 11, accountID: "a-11"}
	buy := auctionBuyoutBody(900, 6001, 150)
	writeFailure := errors.New("injected blocked auction mutation write failure")
	gate, request := startAuctionMutationGate(t, s, buyer, uint16(dnfenum.CmdPacketAuctionBidding), buy, writeFailure)
	requireAuctionMarketLockReleased(t, s)

	ctx := context.Background()
	owner, err := dnfauction.NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.LoadListing(ctx, 900); !errors.Is(err, dnfauction.ErrListingMissing) {
		t.Fatalf("committed buyout still has a listing: %v", err)
	}
	buyerRecord, found, err := repositories.Character.Load(ctx, "11")
	if err != nil || !found || buyerRecord.Stats["gold"] != 10_000_000-150 {
		t.Fatalf("committed buyout missing before write failure found=%t err=%v stats=%+v", found, err, buyerRecord.Stats)
	}
	committed := snapshotAuctionMutationTestAssets(t, repositories)

	gate.Release()
	if err := <-request; err == nil {
		t.Fatal("injected socket write failure was swallowed")
	}

	buyer.conn = &bufferConn{}
	if err := s.handleCurrentAuctionCommand(buyer, uint16(dnfenum.CmdPacketAuctionBidding), buy); err != nil {
		t.Fatal(err)
	}
	failure, rest := splitGameServerUpperPacketWithHeader(t, buyer.conn.(*bufferConn).write.Bytes(), 16)
	if len(rest) != 0 || failure.Header.MsgID != uint16(dnfenum.CmdPacketAuctionBidding) ||
		len(failure.Body) == 0 || failure.Body[0] != 0 {
		t.Fatalf("replayed buyout response header=%+v body=%x rest=%x", failure.Header, failure.Body, rest)
	}
	if after := snapshotAuctionMutationTestAssets(t, repositories); !reflect.DeepEqual(committed, after) {
		t.Fatalf("retry reexecuted or rolled back committed assets:\ncommitted=%+v\nafter=%+v", committed, after)
	}
}

func seedAuctionMutationBiddingListing(t *testing.T, repositories dnfrepo.Group, itemID int64) uint64 {
	t.Helper()
	ctx := context.Background()
	inventory, found, err := repositories.Inventory.Load(ctx, "10")
	if err != nil || !found {
		t.Fatalf("load seller inventory found=%t err=%v", found, err)
	}
	if inventory.Slots == nil {
		inventory.Slots = make(map[string]dnfrepo.ItemStack)
	}
	inventory.Slots["0:10"] = dnfrepo.ItemStack{
		ItemID: itemID, Count: 2, RawEntry: make([]byte, currentItemListEntryWireSize),
	}
	if err := repositories.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}
	owner, err := dnfauction.NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	const listingID uint64 = 902
	if _, err := owner.Register(ctx, dnfauction.RegisterCommand{
		ListingID: listingID, SellerID: "10", AccountID: "a-10", SellerName: "10",
		ListType: 0, SourceSlot: 10, ExpectedID: itemID, Count: 2,
		UnitPrice: 75, StartPrice: 30, BuyoutPrice: 75,
		Deposit: dnfauction.ListingDepositGold, FeePercent: 5,
		Duration: time.Hour, ListingLimit: 5,
	}); err != nil {
		t.Fatal(err)
	}
	return listingID
}

func loadAuctionMutationListing(repositories dnfrepo.Group, listingID uint64) (dnfauction.Listing, error) {
	owner, err := dnfauction.NewOwner(repositories)
	if err != nil {
		return dnfauction.Listing{}, err
	}
	return owner.LoadListing(context.Background(), listingID)
}

func splitAuctionMutationOpcodes(t *testing.T, data []byte) []uint16 {
	t.Helper()
	var opcodes []uint16
	for len(data) != 0 {
		packet, rest := splitGameServerUpperPacketWithHeader(t, data, 16)
		opcodes = append(opcodes, packet.Header.MsgID)
		data = rest
	}
	return opcodes
}

type auctionMutationTestStack struct {
	ItemID int64
	Count  int64
}

type auctionMutationTestMail struct {
	Source      string
	Title       string
	Gold        int64
	Attachments []auctionMutationTestStack
}

type auctionMutationTestListing struct {
	SellerID    string
	ItemID      int64
	Count       int64
	UnitPrice   int64
	StartPrice  int64
	BuyoutPrice int64
	BidderID    string
	BidTotal    int64
}

type auctionMutationTestAssets struct {
	Characters  map[string]int64
	Inventories map[string]map[string]auctionMutationTestStack
	Mailboxes   map[string][]auctionMutationTestMail
	Listings    map[uint64]auctionMutationTestListing
}

func snapshotAuctionMutationTestAssets(t *testing.T, repositories dnfrepo.Group) auctionMutationTestAssets {
	t.Helper()
	ctx := context.Background()
	characterIDs := []string{"10", "11", dnfauction.EscrowCharacterID}
	snapshot := auctionMutationTestAssets{
		Characters:  make(map[string]int64, len(characterIDs)),
		Inventories: make(map[string]map[string]auctionMutationTestStack, len(characterIDs)),
		Mailboxes:   make(map[string][]auctionMutationTestMail, len(characterIDs)),
		Listings:    make(map[uint64]auctionMutationTestListing),
	}
	for _, characterID := range characterIDs {
		character, found, err := repositories.Character.Load(ctx, characterID)
		if err != nil || !found {
			t.Fatalf("load character %s found=%t err=%v", characterID, found, err)
		}
		snapshot.Characters[characterID] = character.Stats["gold"]

		inventory, found, err := repositories.Inventory.Load(ctx, characterID)
		if err != nil || !found {
			t.Fatalf("load inventory %s found=%t err=%v", characterID, found, err)
		}
		slots := make(map[string]auctionMutationTestStack, len(inventory.Slots))
		for key, stack := range inventory.Slots {
			slots[key] = auctionMutationTestStack{ItemID: stack.ItemID, Count: stack.Count}
		}
		snapshot.Inventories[characterID] = slots

		mailbox, found, err := repositories.Mailbox.Load(ctx, characterID)
		if err != nil {
			t.Fatalf("load mailbox %s err=%v", characterID, err)
		}
		if !found {
			snapshot.Mailboxes[characterID] = nil
			continue
		}
		mailIDs := make([]string, 0, len(mailbox.Mails))
		for mailID := range mailbox.Mails {
			mailIDs = append(mailIDs, mailID)
		}
		sort.Strings(mailIDs)
		for _, mailID := range mailIDs {
			mail := mailbox.Mails[mailID]
			entry := auctionMutationTestMail{
				Source: mail.Metadata["source"], Title: mail.Title, Gold: mail.Gold,
			}
			for _, attachment := range mail.Attachments {
				entry.Attachments = append(entry.Attachments, auctionMutationTestStack{
					ItemID: attachment.ItemID, Count: attachment.Count,
				})
			}
			snapshot.Mailboxes[characterID] = append(snapshot.Mailboxes[characterID], entry)
		}
	}

	owner, err := dnfauction.NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	listings, err := owner.Search(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, listing := range listings {
		snapshot.Listings[listing.ID] = auctionMutationTestListing{
			SellerID: listing.SellerID, ItemID: listing.Item.ItemID, Count: listing.Item.Count,
			UnitPrice: listing.UnitPrice, StartPrice: listing.StartPrice, BuyoutPrice: listing.BuyoutPrice,
			BidderID: listing.BidderID, BidTotal: listing.BidTotal,
		}
	}
	return snapshot
}

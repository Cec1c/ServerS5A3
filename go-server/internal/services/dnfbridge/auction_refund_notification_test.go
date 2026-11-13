package dnfbridge

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	dnfauction "longheng.io/server/internal/modules/dnf/auction"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/platform/eventloop"
)

const auctionRefundGoldTestPayoutKey = "auction:payout:5:refund:77:40"

type auctionRefundGoldFixture struct {
	ctx      context.Context
	service  *Service
	session  *gameSession
	buffer   *bufferConn
	repos    dnfrepo.Group
	owner    *dnfauction.Owner
	payout   dnfauction.Payout
	bidderID uint16
}

func newAuctionRefundGoldFixture(t *testing.T) *auctionRefundGoldFixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repositories := dnfrepomemory.NewMemoryGroup()
	now := time.Now().UTC()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "77", AccountID: "auction-refund-account", Name: "Bidder",
		Stats: map[string]int64{"gold": 10}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: dnfauction.EscrowCharacterID, AccountID: dnfauction.EscrowAccountID,
		Name: "Auction Escrow", Slot: -1, Stats: map[string]int64{"gold": 40},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: dnfauction.EscrowCharacterID,
		Slots: map[string]dnfrepo.ItemStack{
			auctionRefundGoldTestPayoutKey: {
				ItemID: 6001, Count: 40,
				Extra: map[string]string{
					"seller_id": "77", "listing_id": "5", "sold_count": "0", "refund": "1",
				},
			},
		},
		UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	owner, err := dnfauction.NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	buffer := &bufferConn{}
	session := &gameSession{
		conn: buffer, ctx: ctx, connID: "auction-refund-gold",
		channel: channelcatalog.Channel{ID: 19},
	}
	service := &Service{
		options: options{
			gameUpperHeader:    gameUpperHeaderChannel13,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		gameSessions:       make(map[uint16]*gameSession),
	}
	if err := service.startGameSessionEvents(ctx, session); err != nil {
		t.Fatal(err)
	}
	service.bindGameSessionCharacter(session, 77)
	buffer.write.Reset()
	return &auctionRefundGoldFixture{
		ctx: ctx, service: service, session: session, buffer: buffer,
		repos: repositories, owner: owner, bidderID: 77,
		payout: dnfauction.Payout{
			ID: auctionRefundGoldTestPayoutKey, ListingID: 5, SellerID: "77",
			ItemID: 6001, Gold: 40, Refund: true,
		},
	}
}

func (f *auctionRefundGoldFixture) blockSessionOwner(t *testing.T) (func(), <-chan error) {
	t.Helper()
	started := make(chan struct{})
	releaseCh := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCh) }) }
	done := make(chan error, 1)
	go func() {
		done <- f.service.callGameSession(f.ctx, f.session, "test-auction-refund-blocked-owner", func() error {
			close(started)
			<-releaseCh
			return nil
		})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		release()
		t.Fatal("auction refund session owner did not block")
	}
	return release, done
}

func (f *auctionRefundGoldFixture) settleWithin(t *testing.T) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		// Production enters the payout under auctionMu. The refresh must only
		// submit to the destination loop while that lock is held.
		f.service.auctionMu.Lock()
		err := f.service.settleCurrentAuctionPayoutLocked(f.ctx, f.owner, f.payout)
		f.service.auctionMu.Unlock()
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		t.Fatal("auction refund payout waited for the destination session")
		return nil
	}
}

func (f *auctionRefundGoldFixture) drainSessionEvents(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	if err := f.service.callGameSession(ctx, f.session, "test-auction-refund-queue-barrier", func() error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *auctionRefundGoldFixture) bidderGold(t *testing.T) int64 {
	t.Helper()
	character, found, err := f.repos.Character.Load(f.ctx, "77")
	if err != nil || !found {
		t.Fatalf("load bidder found=%v err=%v", found, err)
	}
	return character.Stats["gold"]
}

func (f *auctionRefundGoldFixture) setBidderGold(t *testing.T, gold int64) {
	t.Helper()
	character, found, err := f.repos.Character.Load(f.ctx, "77")
	if err != nil || !found {
		t.Fatalf("load bidder found=%v err=%v", found, err)
	}
	character.Stats["gold"] = gold
	if err := f.repos.Character.Save(f.ctx, character); err != nil {
		t.Fatal(err)
	}
}

func TestAuctionRefundGoldRefreshQueuesBehindBlockedEventLoopWithLatestGold(t *testing.T) {
	fixture := newAuctionRefundGoldFixture(t)
	defer stopTestGameSessionEvents(t, fixture.service, fixture.session)
	release, ownerDone := fixture.blockSessionOwner(t)
	defer release()

	if err := fixture.settleWithin(t); err != nil {
		t.Fatal(err)
	}
	if got := fixture.buffer.write.Len(); got != 0 {
		t.Fatalf("refund wrote %d bytes while the session owner was blocked", got)
	}
	if got := fixture.bidderGold(t); got != 50 {
		t.Fatalf("committed refund gold=%d want 50", got)
	}
	// A later committed change must be the number the client receives; the
	// refund transaction's goldAfter is already stale by the time the loop runs.
	fixture.setBidderGold(t, 91)

	release()
	if err := <-ownerDone; err != nil {
		t.Fatal(err)
	}
	fixture.drainSessionEvents(t)
	packets := splitAllCurrentGameServerUpperPackets(t, fixture.buffer.write.Bytes())
	if len(packets) != 1 {
		t.Fatalf("packet count=%d want one gold refresh", len(packets))
	}
	if packets[0].Header.MsgID != 14 || packets[0].Header.Classification != 0 {
		t.Fatalf("gold refresh header=%+v", packets[0].Header)
	}
	listType, slot, itemID, gold := decodeSingleItemUpdateRow(t, packets[0].Body)
	if listType != 0 || slot != 0 || itemID != 0 || gold != 91 {
		t.Fatalf("gold refresh list=%d slot=%d item=%d gold=%d want 0/0/0/91",
			listType, slot, itemID, gold)
	}
}

func TestAuctionRefundGoldRefreshDropsEventAfterSessionRebind(t *testing.T) {
	fixture := newAuctionRefundGoldFixture(t)
	defer stopTestGameSessionEvents(t, fixture.service, fixture.session)
	release, ownerDone := fixture.blockSessionOwner(t)
	defer release()

	if err := fixture.settleWithin(t); err != nil {
		t.Fatal(err)
	}
	replacementBuffer := &bufferConn{}
	replacement := &gameSession{
		conn: replacementBuffer, ctx: fixture.ctx, connID: "auction-refund-gold-replacement",
		channel: channelcatalog.Channel{ID: 19},
	}
	fixture.service.bindGameSessionCharacter(replacement, fixture.bidderID)
	fixture.buffer.write.Reset()

	release()
	if err := <-ownerDone; err != nil {
		t.Fatal(err)
	}
	fixture.drainSessionEvents(t)
	if fixture.buffer.write.Len() != 0 {
		t.Fatalf("old session wrote %d bytes after rebind", fixture.buffer.write.Len())
	}
	if replacementBuffer.write.Len() != 0 {
		t.Fatalf("replacement session received stale refund gold=%d bytes", replacementBuffer.write.Len())
	}
	if got := fixture.bidderGold(t); got != 50 {
		t.Fatalf("committed refund gold=%d want 50", got)
	}
}

func TestAuctionRefundGoldRefreshFullQueueKeepsCommittedRefund(t *testing.T) {
	fixture := newAuctionRefundGoldFixture(t)
	defer stopTestGameSessionEvents(t, fixture.service, fixture.session)
	release, ownerDone := fixture.blockSessionOwner(t)
	defer release()

	identity, ok := fixture.service.onlineGameSessionCharacterSnapshot(fixture.bidderID)
	if !ok {
		t.Fatal("fixture bidder is not online")
	}
	full := false
	for attempt := 0; attempt < gameSessionEventQueueSize+8; attempt++ {
		err := fixture.service.tryPostGameSessionCharacterEvent(
			identity.session, "test-fill-auction-refund-queue", identity.character, identity.generation,
			func() error { return nil },
		)
		if errors.Is(err, eventloop.ErrQueueFull) {
			full = true
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !full {
		t.Fatal("session event queue did not fill")
	}

	if err := fixture.settleWithin(t); err != nil {
		t.Fatalf("full queue changed the committed refund result: %v", err)
	}
	if got := fixture.bidderGold(t); got != 50 {
		t.Fatalf("committed refund gold=%d want 50", got)
	}
	if got := fixture.buffer.write.Len(); got != 0 {
		t.Fatalf("full queue wrote %d bytes", got)
	}

	release()
	if err := <-ownerDone; err != nil {
		t.Fatal(err)
	}
	fixture.drainSessionEvents(t)
	if got := fixture.buffer.write.Len(); got != 0 {
		t.Fatalf("dropped refresh wrote %d bytes after the queue drained", got)
	}
}

package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	auction "longheng.io/server/internal/modules/dnf/auction"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// These tests use only pre-existing command entry points and fixtures so the
// exact same test file also compiles against the pre-fix implementation.
type auctionReadWriteGate struct {
	*bufferConn
	entered, release  chan struct{}
	once, releaseOnce sync.Once
	writeErr          error
}

func (g *auctionReadWriteGate) Write(b []byte) (int, error) {
	g.once.Do(func() { close(g.entered); <-g.release })
	if g.writeErr != nil {
		return 0, g.writeErr
	}
	return g.bufferConn.Write(b)
}

func (g *auctionReadWriteGate) Release() { g.releaseOnce.Do(func() { close(g.release) }) }

type auctionReadCase struct {
	name        string
	opcode      uint16
	body        []byte
	wantSuccess bool
}

type auctionReadBlockingInventory struct {
	repo.InventoryRepository
	entered, release chan struct{}
	once             sync.Once
}

func (r *auctionReadBlockingInventory) Load(ctx context.Context, id string) (repo.InventoryRecord, bool, error) {
	if id == auction.EscrowCharacterID {
		r.once.Do(func() { close(r.entered); <-r.release })
	}
	return r.InventoryRepository.Load(ctx, id)
}

func TestAuctionReadPreparationRetainsMarketLock(t *testing.T) {
	for _, mode := range []byte{0, 1} {
		t.Run(fmt.Sprintf("mode%d", mode), func(t *testing.T) {
			r, s, p, _ := auctionReadFixture(t)
			blocked := &auctionReadBlockingInventory{InventoryRepository: r.Inventory, entered: make(chan struct{}), release: make(chan struct{})}
			r.Inventory = blocked
			s.repositoryProvider = func() (repo.Group, bool) { return r, true }
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(blocked.release) }) }
			t.Cleanup(release)
			q := auctionReadCases()[int(mode)*10]
			done := make(chan error, 1)
			go func() { done <- s.handleCurrentAuctionCommand(p, q.opcode, q.body) }()
			select {
			case <-blocked.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("query did not reach repository")
			}
			unlocked := s.auctionMu.TryLock()
			if unlocked {
				s.auctionMu.Unlock()
			}
			release()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if unlocked {
				t.Fatal("query preparation/maintenance escaped auctionMu")
			}
		})
	}
}

func auctionReadCases() []auctionReadCase {
	var cases []auctionReadCase
	for _, mode := range []byte{0, 1} {
		item := uint32(6001)
		if mode == 1 {
			item = 2681725
		}
		search := make([]byte, 14)
		search[0], search[7] = mode, 1
		binary.LittleEndian.PutUint32(search[10:], item)
		category := make([]byte, 20)
		average := make([]byte, 8)
		if mode == 1 {
			category = make([]byte, 12)
			average = make([]byte, 6)
		}
		category[0], average[0] = mode, mode
		binary.LittleEndian.PutUint32(average[1:5], item)
		for _, q := range []auctionReadCase{
			{"item", 186, search, true}, {"category", 187, category, true},
			{"average", 182, average, true}, {"owned", 188, []byte{mode}, true},
			{"bidding", 189, []byte{mode}, true},
		} {
			q.name = fmt.Sprintf("mode%d_%s", mode, q.name)
			cases = append(cases, q)
			bad := q
			bad.name += "_malformed"
			bad.body, bad.wantSuccess = []byte{mode, 0xff}, false
			cases = append(cases, bad)
		}
	}
	return cases
}

func auctionReadFixture(t *testing.T) (repo.Group, *Service, *gameSession, *bufferConn) {
	t.Helper()
	r, s, p, conn := goldBridgeFixture(t)
	s.initialEquipmentLoadErr = errors.New("auction read test has no PVF dependency")
	s.auctionItemEligibility = func(id uint32) bool { return id == 6001 }
	ctx := context.Background()
	if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "10", Slots: map[string]repo.ItemStack{"0:9": {ItemID: 6001, Count: 2}}}); err != nil {
		t.Fatal(err)
	}
	o, err := auction.NewOwner(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = o.Register(ctx, auction.RegisterCommand{ListingID: 900, SellerID: "10", AccountID: "a-10", SellerName: "10", ListType: 0, SourceSlot: 9, ExpectedID: 6001, Count: 2, UnitPrice: 75, StartPrice: -1, Duration: time.Hour, ListingLimit: 5, Deposit: 10000, FeePercent: 5}); err != nil {
		t.Fatal(err)
	}
	gold, err := auction.NewGoldConsignOwner(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = gold.Register(ctx, auction.GoldConsignRegisterCommand{ListingID: 901, SellerID: "10", AccountID: "a-10", GoldItemID: 2681725, PriceCera: 1110, DurationHour: 24}); err != nil {
		t.Fatal(err)
	}
	return r, s, p, conn
}

func startAuctionReadGate(t *testing.T, s *Service, p *gameSession, q auctionReadCase, writeErr error) (*auctionReadWriteGate, <-chan error) {
	t.Helper()
	gate := &auctionReadWriteGate{bufferConn: &bufferConn{}, entered: make(chan struct{}), release: make(chan struct{}), writeErr: writeErr}
	p.conn = gate
	done := make(chan error, 1)
	go func() { done <- s.handleCurrentAuctionCommand(p, q.opcode, q.body) }()
	t.Cleanup(gate.Release)
	select {
	case <-gate.entered:
	case err := <-done:
		t.Fatalf("request ended before socket write: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("request never reached the socket write")
	}
	return gate, done
}

func requireAuctionReadIndependentWork(t *testing.T, s *Service, gate *auctionReadWriteGate, request <-chan error, work func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- work() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		gate.Release()
		<-request
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("blocked work did not retire after releasing write")
		}
		t.Fatal("another auction operation waited for the query requester's socket")
	}
}

func TestAuctionReadSlowRequesterDoesNotHoldMarketLock(t *testing.T) {
	for _, q := range auctionReadCases() {
		t.Run(q.name, func(t *testing.T) {
			_, s, p, _ := auctionReadFixture(t)
			gate, request := startAuctionReadGate(t, s, p, q, nil)
			peerConn := &bufferConn{}
			peer := &gameSession{conn: peerConn, selectedCharacterID: 11, accountID: "a-11"}
			requireAuctionReadIndependentWork(t, s, gate, request, func() error {
				if err := s.handleCurrentAuctionCommand(peer, 189, []byte{0}); err != nil {
					return err
				}
				s.auctionMu.Lock()
				defer s.auctionMu.Unlock()
				_, err := s.maintainCurrentAuctionSafelyLocked(context.Background())
				return err
			})
			if peerConn.write.Len() == 0 {
				t.Fatal("healthy peer received no response")
			}
			if gate.write.Len() != 0 {
				t.Fatal("held socket gate wrote early")
			}
			gate.Release()
			if err := <-request; err != nil {
				t.Fatal(err)
			}
			packet, rest := splitGameServerUpperPacketWithHeader(t, gate.write.Bytes(), 16)
			wantOpcode := q.opcode
			if wantOpcode == 187 {
				wantOpcode = 186
			}
			if packet.Header.MsgID != wantOpcode || packet.Header.Classification != 1 || len(rest) != 0 || len(packet.Body) < 2 {
				t.Fatalf("wrong query frame: %+v %x rest=%d", packet.Header, packet.Body, len(rest))
			}
			want := byte(0)
			if q.wantSuccess {
				want = 1
			}
			if packet.Body[0] != want {
				t.Fatalf("success byte=%d want %d body=%x", packet.Body[0], want, packet.Body)
			}
			if q.wantSuccess && packet.Body[1] != q.body[0] {
				t.Fatalf("pay route changed: %x", packet.Body)
			}
		})
	}
}

// Compare command dispatch with the original internal response entry points.
// The baseline and fixed binaries run this same comparison, including failures.
func TestAuctionReadDispatchKeepsNativeResponseBytes(t *testing.T) {
	for _, q := range auctionReadCases() {
		t.Run(q.name, func(t *testing.T) {
			_, s, p, conn := auctionReadFixture(t)
			if err := s.handleCurrentAuctionCommand(p, q.opcode, q.body); err != nil {
				t.Fatal(err)
			}
			got := append([]byte(nil), conn.write.Bytes()...)
			_, old, actor, oldConn := auctionReadFixture(t)
			var err error
			if q.body[0] == 1 {
				err = old.handleCurrentGoldConsignLocked(actor, q.opcode, q.body)
			} else {
				switch q.opcode {
				case 182:
					err = old.sendCurrentAuctionAveragePriceLocked(actor, q.body)
				case 186:
					err = old.searchCurrentAuctionItemsLocked(actor, q.body)
				case 187:
					err = old.searchCurrentAuctionCategoryLocked(actor, q.body)
				case 188:
					err = old.sendCurrentAuctionOwnedItemsLocked(actor, q.body)
				case 189:
					err = old.sendCurrentAuctionBiddingItemsLocked(actor, q.body)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			// Listing lifetime is rounded to hours; the two fresh fixtures use
			// the same full durations and all other projected fields are exact.
			if !bytes.Equal(got, oldConn.write.Bytes()) {
				t.Fatalf("dispatch changed native bytes: got=%x direct=%x", got, oldConn.write.Bytes())
			}
		})
	}
}

// A query can recover expired assets before returning its list. Failed response
// delivery must not undo that commit or make a retry deliver the assets twice.
func TestAuctionReadFailedResponseKeepsExpiryRecoveryExactlyOnce(t *testing.T) {
	for _, mode := range []byte{0, 1} {
		t.Run(fmt.Sprintf("mode%d", mode), func(t *testing.T) {
			r, s, p, _ := auctionReadFixture(t)
			ctx := context.Background()
			inv, found, err := r.Inventory.Load(ctx, auction.EscrowCharacterID)
			if err != nil || !found {
				t.Fatal(found, err)
			}
			prefix := "auction:listing:"
			if mode == 1 {
				prefix = "goldconsign:listing:"
			}
			changed := 0
			for key, stack := range inv.Slots {
				if strings.HasPrefix(key, prefix) {
					stack.Extra["created_at"] = strconv.FormatInt(time.Now().Add(-25*time.Hour).Unix(), 10)
					stack.Extra["expires_at"] = strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)
					inv.Slots[key] = stack
					changed++
				}
			}
			if changed != 1 {
				t.Fatalf("expiry fixture changed %d rows", changed)
			}
			if err := r.Inventory.Save(ctx, inv); err != nil {
				t.Fatal(err)
			}
			fault := errors.New("injected auction query socket failure")
			gate, request := startAuctionReadGate(t, s, p, auctionReadCase{opcode: 188, body: []byte{mode}}, fault)
			before, found, err := r.Mailbox.Load(ctx, "10")
			if err != nil || !found || len(before.Mails) != 1 {
				t.Fatalf("expiry not committed before response: %v %v %+v", found, err, before)
			}
			wallet, _, err := r.Character.Load(ctx, "10")
			if err != nil {
				t.Fatal(err)
			}
			requireAuctionReadIndependentWork(t, s, gate, request, func() error {
				s.auctionMu.Lock()
				defer s.auctionMu.Unlock()
				_, err := s.maintainCurrentAuctionSafelyLocked(ctx)
				return err
			})
			gate.Release()
			if err := <-request; !errors.Is(err, fault) {
				t.Fatalf("socket error swallowed: %v", err)
			}
			// A replacement request must observe the committed result even when
			// the failed socket cannot be reused after its first write error.
			for attempt := 0; attempt < 2; attempt++ {
				peer := &gameSession{conn: &bufferConn{}, selectedCharacterID: 10, accountID: "a-10"}
				if err := s.handleCurrentAuctionCommand(peer, 188, []byte{mode}); err != nil {
					t.Fatal(err)
				}
			}
			after, _, err := r.Mailbox.Load(ctx, "10")
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("expiry replay changed mail: %v before=%+v after=%+v", err, before, after)
			}
			final, _, err := r.Character.Load(ctx, "10")
			if err != nil || final.Stats["gold"] != wallet.Stats["gold"] {
				t.Fatal("response failure/retry changed wallet", err)
			}
		})
	}
}

func TestAuctionReadFrozenResultSurvivesConcurrentCancel(t *testing.T) {
	r, s, p, _ := auctionReadFixture(t)
	q := auctionReadCases()[0]
	gate, request := startAuctionReadGate(t, s, p, q, nil)
	cancelBody := make([]byte, 9)
	binary.LittleEndian.PutUint64(cancelBody[1:], 900)
	requireAuctionReadIndependentWork(t, s, gate, request, func() error {
		actor := &gameSession{conn: &bufferConn{}, selectedCharacterID: 10, accountID: "a-10"}
		return s.handleCurrentAuctionCommand(actor, 184, cancelBody)
	})
	gate.Release()
	if err := <-request; err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacketWithHeader(t, gate.write.Bytes(), 16)
	if len(rest) != 0 || len(packet.Body) != 8+0xa8 || binary.LittleEndian.Uint64(packet.Body[8:16]) != 900 {
		t.Fatalf("prepared list changed during another transaction: %x", packet.Body)
	}
	conn := &bufferConn{}
	reader := &gameSession{conn: conn, selectedCharacterID: 11, accountID: "a-11"}
	if err := s.handleCurrentAuctionCommand(reader, q.opcode, q.body); err != nil {
		t.Fatal(err)
	}
	packet, _ = splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), 16)
	if len(packet.Body) != 8 {
		t.Fatalf("next read did not observe committed cancellation: %x", packet.Body)
	}
	before, found, err := r.Mailbox.Load(context.Background(), "10")
	if err != nil || !found || len(before.Mails) != 1 {
		t.Fatalf("cancel return not delivered once: %v %v %+v", found, err, before)
	}
	for i := 0; i < 2; i++ {
		actor := &gameSession{conn: &bufferConn{}, selectedCharacterID: 10, accountID: "a-10"}
		if err := s.handleCurrentAuctionCommand(actor, 184, cancelBody); err != nil {
			t.Fatal(err)
		}
	}
	after, _, err := r.Mailbox.Load(context.Background(), "10")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("duplicate cancel redelivered assets", err)
	}
}

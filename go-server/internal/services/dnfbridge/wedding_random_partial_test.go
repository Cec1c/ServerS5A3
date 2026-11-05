package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func addWeddingRandomGuest(t *testing.T, f *weddingChurchLoadingFixture, id uint16, channel int) *gameSession {
	t.Helper()
	p := &gameSession{conn: &bufferConn{}, connID: fmt.Sprintf("random-guest-%d", id), accountID: fmt.Sprintf("random:%d", id), selectedCharacterID: id, characterGeneration: uint64(id), channel: f.first.channel, residentChannel: f.first.residentChannel, townSceneReadyCharacterID: id,
		townPositionSnapshot: currentTownPositionSnapshot{CharacterID: id, TownID: 39, AreaID: 0, PositionValid: true}}
	p.channel.ID = channel
	p.residentChannel = p.channel
	p.connectionTownActorOwnerChannel, p.townActorOwnerChannel = byte(channel), byte(channel)
	f.s.gameSessions[id] = p
	ctx, key := context.Background(), fmt.Sprint(id)
	if e := f.repos.Character.Save(ctx, r.CharacterRecord{CharacterID: key, AccountID: p.accountID, Name: "guest" + key, Job: "6", Level: 90, Stats: map[string]int64{"gold": 20000, "town_id": 39, "area_id": 0, "area_state": 3}}); e != nil {
		t.Fatal(e)
	}
	if e := f.repos.Inventory.Save(ctx, r.InventoryRecord{CharacterID: key, Slots: map[string]r.ItemStack{}}); e != nil {
		t.Fatal(e)
	}
	if e := f.repos.Equipment.Save(ctx, r.EquipmentRecord{CharacterID: key, Entries: map[string]r.EquipmentEntry{}}); e != nil {
		t.Fatal(e)
	}
	return p
}

func TestWeddingGuestRandomUsesAvailablePeopleAndActualQuota(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		quota, available, sent int
	}{
		{"one-person-for-thirty-letters", 30, 1, 1}, {"no-eligible-guest", 30, 0, 0},
		{"several-below-quota", 30, 3, 3}, {"random-subset-stays-within-quota", 2, 4, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWeddingChurchLoadingFixture(t)
			s, ctx := f.s, context.Background()
			s.wedding.guestRules = &wedding.GuestRules{MinimumGold: 10000, MaximumGold: 400000000, Charge: 50, Capacity: map[byte]int{0: tc.quota}}
			owner, _ := s.currentWeddingOwner()
			a, _ := s.boundGameSessionCharacterSnapshot(f.first)
			b, _ := s.boundGameSessionCharacterSnapshot(f.second)
			if _, e := owner.EnsureGuestWedding(ctx, s.weddingPairRequest(a, b), s.wedding.guestRules); e != nil {
				t.Fatal(e)
			}
			guests := map[string]*gameSession{}
			for i := 0; i < tc.available; i++ {
				id := uint16(31 + i)
				guests[fmt.Sprint(id)] = addWeddingRandomGuest(t, f, id, f.first.channel.ID)
			}
			// Other-channel, unselected and revoked transports must not fill the pool.
			addWeddingRandomGuest(t, f, 50, f.first.channel.ID+1)
			addWeddingRandomGuest(t, f, 51, f.first.channel.ID).selectedCharacterID = 0
			addWeddingRandomGuest(t, f, 52, f.first.channel.ID).loginRevoked.Store(true)
			q := make([]byte, 21)
			q[13] = 1
			if e := s.handleCurrentWeddingSendLetter(f.first, q); e != nil {
				t.Fatal(e)
			}
			ack := guildReply(t, f.first, 1038)
			code := int32(0)
			if tc.sent == 0 {
				code = -7
			}
			ackLength := 5
			if code == 0 {
				ackLength = 7
			}
			if len(ack) != ackLength || int32(binary.LittleEndian.Uint32(ack[1:])) != code || code == 0 && int(binary.LittleEndian.Uint16(ack[5:])) != tc.quota-tc.sent {
				t.Fatalf("partial random result %x", ack)
			}
			ledger, found, e := owner.ReadGuestLedger(ctx, []string{"29", "30"}, f.at)
			if e != nil || !found || len(ledger.Invitations) != tc.sent || ledger.RemainingFor("29") != tc.quota-tc.sent || ledger.RemainingFor("30") != tc.quota {
				t.Fatalf("actual debit/shared targets %+v/%v", ledger, e)
			}
			for id, p := range guests {
				ops, bodies := cargoReplies(t, p)
				_, invited := ledger.Invitations[id]
				if invited && (len(ops) != 1 || ops[0] != 1005 || binary.LittleEndian.Uint32(bodies[0]) != ledger.Key) {
					t.Fatalf("guest%s did not receive its invitation: %v", id, ops)
				}
				if !invited && len(ops) != 0 {
					t.Fatal("unselected guest received an invitation")
				}
			}
			for _, id := range []uint16{50, 51, 52} {
				if ops, _ := cargoReplies(t, s.gameSessions[id]); len(ops) != 0 {
					t.Fatal("ineligible transport received a letter")
				}
			}
			// The spouse can use its own quota, but cannot invite the same guest twice.
			for _, p := range []*gameSession{f.first, f.second} {
				ops, _ := cargoReplies(t, p)
				for _, op := range ops {
					if op == 1004 {
						t.Fatal("dispatch replayed reservation and cleared congratulations; ACK/targets suffice")
					}
				}
			}
			if e := s.handleCurrentWeddingSendLetter(f.second, q); e != nil {
				t.Fatal(e)
			}
			secondAck := guildReply(t, f.second, 1038)
			secondSent := min(tc.quota, tc.available-tc.sent)
			secondCode := int32(0)
			if secondSent == 0 {
				secondCode = -7
			}
			secondLength := 5
			if secondCode == 0 {
				secondLength = 7
			}
			if len(secondAck) != secondLength || int32(binary.LittleEndian.Uint32(secondAck[1:])) != secondCode || secondCode == 0 && int(binary.LittleEndian.Uint16(secondAck[5:])) != tc.quota-secondSent {
				t.Fatalf("spouse quota %x", secondAck)
			}
			after, _, e := owner.ReadGuestLedger(ctx, []string{"29", "30"}, f.at)
			if e != nil || len(after.Invitations) != tc.sent+secondSent || after.RemainingFor("29") != ledger.RemainingFor("29") {
				t.Fatalf("shared duplicate or spouse charged initiator %+v/%v", after, e)
			}
			// Everyone is now invited. Repeating cannot consume unused letters.
			for _, p := range []*gameSession{f.first, f.second} {
				cargoReplies(t, p)
			}
			if e := s.handleCurrentWeddingSendLetter(f.first, q); e != nil {
				t.Fatal(e)
			}
			ops, bodies := cargoReplies(t, f.first)
			want := int32(-7)
			if after.RemainingFor("29") == 0 {
				want = -4
			}
			if len(ops) != 1 || ops[0] != 1038 || int32(binary.LittleEndian.Uint32(bodies[0][1:])) != want {
				t.Fatalf("repeat added non-native notice or sent duplicates: %v %x", ops, bodies)
			}
			repeated, _, _ := owner.ReadGuestLedger(ctx, []string{"29", "30"}, f.at)
			if !reflect.DeepEqual(after, repeated) {
				t.Fatal("empty repeat changed invitation ledger")
			}
			if tc.quota == 30 && tc.available == 1 {
				newGuest := addWeddingRandomGuest(t, f, 60, f.first.channel.ID)
				if e := s.handleCurrentWeddingSendLetter(f.first, q); e != nil {
					t.Fatal(e)
				}
				ack = guildReply(t, f.first, 1038)
				if binary.LittleEndian.Uint32(ack[1:]) != 0 || binary.LittleEndian.Uint16(ack[5:]) != 28 {
					t.Fatalf("new arrival could not use remaining letters %x", ack)
				}
				if ops, _ := cargoReplies(t, newGuest); len(ops) != 1 || ops[0] != 1005 {
					t.Fatal("new arrival did not receive invitation")
				}
			}
		})
	}
}

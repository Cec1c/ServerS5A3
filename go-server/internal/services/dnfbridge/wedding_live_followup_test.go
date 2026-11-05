package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func TestWeddingRingUpgradeRestoresBothBenefitsAfterBagProjection(t *testing.T) {
	f := newWeddingChurchLoadingFixture(t)
	s := f.s
	ctx := context.Background()
	s.wedding.rules.Rings = append(s.wedding.rules.Rings, wedding.Ring{Stage: 1, TicketID: 10002761, TicketCount: 1})
	for _, id := range []string{"29", "30"} {
		c, _, _ := f.repos.Character.Load(ctx, id)
		c.Stats["wedding_v1_phase"] = int64(wedding.Married)
		c.Stats["wedding_v1_ring"] = 0
		c.Stats["wedding_v1_relation_token"] = 777
		c.Stats["wedding_v1_married_at"] = f.now.Unix() - 100
		if e := r.SaveCharacterFields(ctx, f.repos.Character, c, r.CharacterFieldWedding); e != nil {
			t.Fatal(e)
		}
	}
	if e := f.repos.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "29", Slots: map[string]r.ItemStack{"0:3": {ItemID: 10002761, Count: 1}}}); e != nil {
		t.Fatal(e)
	}
	q := make([]byte, 18)
	binary.LittleEndian.PutUint32(q[13:], 3)
	q[17] = 1
	if e := s.handleCurrentWeddingRing(f.first, q); e != nil {
		t.Fatal(e)
	}
	for _, p := range []*gameSession{f.first, f.second} {
		lastBag, lastInfo, lastRoom := -1, -1, -1
		acks := 0
		notices := 0
		for index, frame := range weddingChurchLoadingFrames(t, p) {
			switch frame.op {
			case 14:
				lastBag = index
			case 1000:
				lastInfo = index
				if frame.body[14] != 1 {
					t.Fatal("final ring stage not projected")
				}
			case 1008:
				lastRoom = index
			case 1036:
				acks++
				if p != f.first || len(frame.body) != 6 || frame.body[0] != 1 || binary.LittleEndian.Uint32(frame.body[1:]) != 0 || frame.body[5] != 1 {
					t.Fatalf("upgrade ACK %x", frame.body)
				}
			case 1001:
				notices++
				if p == f.first || frame.class != 0 || len(frame.body) != 1 || frame.body[0] != 1 {
					t.Fatal("duplicate or malformed ring success notice")
				}
			}
		}
		if lastInfo <= lastBag || lastRoom <= lastInfo {
			t.Fatalf("benefits were not restored after last bag update: bag=%d info=%d room=%d", lastBag, lastInfo, lastRoom)
		}
		if p == f.first && acks != 1 {
			t.Fatal("missing exactly one initiator success ACK")
		}
		if p == f.second && (acks != 0 || notices != 1) {
			t.Fatal("spouse did not receive exactly one native upgrade result")
		}
	}
	// An identical repeat cannot spend another voucher or upgrade twice.
	if e := s.handleCurrentWeddingRing(f.first, q); e != nil {
		t.Fatal(e)
	}
	ack := guildReply(t, f.first, 1036)
	if len(ack) != 6 || int32(binary.LittleEndian.Uint32(ack[1:])) == 0 {
		t.Fatalf("repeat ACK %x", ack)
	}
	bag, _, _ := f.repos.Inventory.Load(ctx, "29")
	assertCurrentNPCShopInventoryAmount(t, bag, 10002761, 0)
}

func TestWeddingGuildInvitationPrimesSilentRealMemberBeforeRoster(t *testing.T) {
	for _, offline := range []bool{false, true} {
		f, repos := guildMemberProfileFixture(t)
		s := f.service
		ctx := context.Background()
		// The shared profile fixture replaces Character with a fake store.
		// Guild read views now own their character snapshot; bind both readers
		// to one memory group instead of leaving its internal store empty.
		coherent := memory.NewMemoryGroup()
		for _, id := range []string{"29", "17"} {
			c, found, err := repos.Character.Load(ctx, id)
			if err != nil || !found {
				t.Fatalf("load guild test character %s: found=%t err=%v", id, found, err)
			}
			if err := coherent.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
		}
		registry, found, err := repos.Settings.Load(ctx, r.GuildRegistryScope)
		if err != nil || !found {
			t.Fatalf("load guild test registry: found=%t err=%v", found, err)
		}
		if err := coherent.Settings.Save(ctx, registry); err != nil {
			t.Fatal(err)
		}
		repos.Character, repos.Settings, repos.Guild = coherent.Character, coherent.Settings, coherent.Guild
		s.repositoryProvider = func() (r.Group, bool) { return repos, true }
		viewer, _, _ := repos.Character.Load(ctx, "29")
		viewer.Stats["wedding_v1_phase"] = int64(wedding.Reserved)
		viewer.Stats["wedding_v1_partner"] = 30
		viewer.Stats["wedding_v1_ceremony_at"] = s.gameplayNow().Unix() + 300
		viewer.Stats["wedding_v1_ceremony_channel"] = int64(currentTownPresenceChannelID(f.target))
		viewer.Stats["wedding_v1_ceremony_area"] = 8
		if e := r.SaveCharacterFields(ctx, repos.Character, viewer, r.CharacterFieldWedding); e != nil {
			t.Fatal(e)
		}
		if offline {
			delete(s.gameSessions, 17)
		}
		before, _, _ := repos.Character.Load(ctx, "17")
		if e := s.handleCurrentGuildMemberListForOpcode(f.target, uint16(dnfenum.CmdPacketGuildAllMemberList)); e != nil {
			t.Fatal(e)
		}
		prime, roster := -1, -1
		for wire, index := f.targetConn.write.Bytes(), 0; len(wire) > 0; index++ {
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
			wire = rest
			if packet.Header.Classification == 0 && packet.Header.MsgID == 2 {
				if len(packet.Body) == 0 || packet.Body[0] != 8 {
					t.Fatal("guild list opened an inspection or spawned a visible actor")
				}
				prime = index
			}
			if packet.Header.MsgID == uint16(dnfenum.CmdPacketGuildAllMemberList) {
				roster = index
			}
		}
		if roster < 0 {
			t.Fatal("missing real guild roster")
		}
		if !offline && (prime < 0 || roster <= prime) {
			bound, valid := s.boundGameSessionCharacterSnapshot(f.target)
			display, displayErr := s.readCurrentGuildDisplay(ctx, bound, false, true)
			primeErr := s.primeCurrentWeddingGuildRosterForBound(ctx, bound, display)
			t.Fatalf("member metadata decoded before real silent profile: prime=%d roster=%d bound=%t read=%v prime=%v guild=%+v", prime, roster, valid, displayErr, primeErr, display)
		}
		if offline && prime >= 0 {
			t.Fatal("offline member manufactured as an online actor")
		}
		after, _, _ := repos.Character.Load(ctx, "17")
		if after.AccountID != before.AccountID || after.Name != before.Name || after.Stats["wedding_v1_phase"] != before.Stats["wedding_v1_phase"] {
			t.Fatal("guild display changed member state")
		}
	}
}

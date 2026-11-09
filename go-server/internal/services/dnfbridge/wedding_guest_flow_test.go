package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/mail"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func TestWeddingGuestRandomInviteGiftReplayAndRealChurchLoading(t *testing.T) {
	f := newWeddingChurchLoadingFixture(t)
	s := f.s
	s.wedding.guestRules = &wedding.GuestRules{MinimumGold: 10000, MaximumGold: 400000000, Charge: 50, Capacity: map[byte]int{0: 1}, Gifts: []wedding.GuestGiftTier{{Maximum: 400000000, ByKind: map[byte][]r.MailAttachment{0: {{ItemID: 10002623, Count: 2}}}}}}
	ctx := context.Background()
	// Force a token above u16: the invitation key is not a character ID.
	index, _ := json.Marshal(wedding.GuestIndex{Version: 1, Next: 65535, Runs: map[uint32]wedding.GuestRunReference{}})
	if err := f.repos.Settings.Save(ctx, r.SettingsRecord{Scope: r.WeddingGuestIndexScope, Values: map[string]string{"index": string(index)}}); err != nil {
		t.Fatal(err)
	}
	guest := &gameSession{conn: &bufferConn{}, connID: "wedding-guest-31", accountID: "wedding:31", selectedCharacterID: 31, characterGeneration: 31, channel: f.first.channel, residentChannel: f.first.channel, townSceneReadyCharacterID: 31, townPositionSnapshot: currentTownPositionSnapshot{CharacterID: 31, TownID: 39, AreaID: 0, PositionValid: true}}
	guest.connectionTownActorOwnerChannel, guest.townActorOwnerChannel = byte(guest.channel.ID), byte(guest.channel.ID)
	s.gameSessions[31] = guest
	if err := f.repos.Character.Save(ctx, r.CharacterRecord{CharacterID: "31", AccountID: guest.accountID, Name: "guest31", Job: "6", Level: 90, Stats: map[string]int64{"gold": 20000, "town_id": 39, "area_id": 0, "area_state": 3}}); err != nil {
		t.Fatal(err)
	}
	if err := f.repos.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "31", Slots: map[string]r.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	if err := f.repos.Equipment.Save(ctx, r.EquipmentRecord{CharacterID: "31", Entries: map[string]r.EquipmentEntry{}}); err != nil {
		t.Fatal(err)
	}
	if err := f.repos.Skill.Save(ctx, r.SkillRecord{CharacterID: "31", Skills: map[int64]r.SkillState{46: {Level: 1, Enabled: true}}, Points: r.SkillPointState{TotalSP: 20, RemainingSP: 20, SyncedLevel: 90}}); err != nil {
		t.Fatal(err)
	}
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 31, AccountID: guest.accountID, TownID: 39, AreaID: 0, Session: guest})
	request := make([]byte, 21)
	request[13] = 1
	if err := s.handleCurrentWeddingSendLetter(f.first, request); err != nil {
		t.Fatal(err)
	}
	ack := guildReply(t, f.first, 1038)
	if len(ack) != 7 || int32(binary.LittleEndian.Uint32(ack[1:])) != 0 || binary.LittleEndian.Uint16(ack[5:]) != 0 {
		t.Fatalf("invite ACK %x", ack)
	}
	// This fixture starts with an old reservation and no guest ledger. Both
	// owners must receive their new opaque token before native 1004 uses it.
	for _, host := range []*gameSession{f.first, f.second} {
		info, profile, ceremony := -1, -1, -1
		for wire, index := host.conn.(*bufferConn).write.Bytes(), 0; len(wire) > 0; index++ {
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
			wire = rest
			switch packet.Header.MsgID {
			case 1000:
				if info < 0 {
					info = index
					if binary.LittleEndian.Uint32(packet.Body) != 65536 {
						t.Fatal("legacy owner info kept the old relationship token")
					}
				}
			case 1035:
				if profile < 0 {
					profile = index
					if packet.Body[0] != 1 || binary.LittleEndian.Uint32(packet.Body[5:]) != 65536 {
						t.Fatal("legacy owner profile did not match the new token")
					}
				}
			case 1004:
				if ceremony < 0 {
					ceremony = index
					if binary.LittleEndian.Uint32(packet.Body) != 65536 {
						t.Fatal("legacy ceremony projection did not match the new token")
					}
				}
			}
		}
		if info < 0 || profile <= info || ceremony <= profile {
			t.Fatalf("legacy migration projection order info=%d profile=%d ceremony=%d", info, profile, ceremony)
		}
	}
	letter := guildReply(t, guest, 1005)
	if len(letter) != 82 || binary.LittleEndian.Uint32(letter) != 65536 || letter[77] != 0 {
		t.Fatalf("real invite letter %x", letter)
	}
	key := binary.LittleEndian.Uint32(letter)
	owner, _ := s.currentWeddingOwner()
	ledger, found, err := owner.ReadGuestLedger(ctx, []string{"29", "30"}, f.at)
	if err != nil || !found || ledger.RemainingFor("29") != 0 || ledger.RemainingFor("30") != 1 {
		t.Fatalf("independent quotas %+v/%v", ledger, err)
	}
	f.second.conn.(*bufferConn).write.Reset()
	if err := s.handleCurrentWeddingSendLetter(f.second, request); err != nil {
		t.Fatal(err)
	}
	ops, bodies := cargoReplies(t, f.second)
	if len(ops) != 1 || ops[0] != 1038 || int32(binary.LittleEndian.Uint32(bodies[0][1:])) != -7 {
		t.Fatalf("insufficient random send must return only its native result: %v %x", ops, bodies)
	}
	gift := make([]byte, 12)
	binary.LittleEndian.PutUint32(gift, key)
	binary.LittleEndian.PutUint32(gift[4:], 10000)
	binary.LittleEndian.PutUint32(gift[8:], 4)
	gift = append(gift, []byte("love")...)
	if err := s.handleCurrentWeddingMoneyGift(guest, gift); err != nil {
		t.Fatal(err)
	}
	for _, recipient := range []*gameSession{f.first, f.second, guest} {
		for wire := recipient.conn.(*bufferConn).write.Bytes(); len(wire) > 0; {
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
			wire = rest
			if packet.Header.MsgID == mail.MailboxAlarmNotificationMessageID && packet.Header.Classification == 0 {
				t.Fatal("gift escrow announced mail before settlement")
			}
		}
	}
	if got := guildReply(t, guest, 1034); len(got) != 9 || int32(binary.LittleEndian.Uint32(got[1:])) != 0 || binary.LittleEndian.Uint32(got[5:]) != key {
		t.Fatalf("gift ACK %x", got)
	}
	if err := s.handleCurrentWeddingMoneyGift(guest, gift); err != nil {
		t.Fatal(err)
	}
	if got := guildReply(t, guest, 1034); len(got) != 5 || int32(binary.LittleEndian.Uint32(got[1:])) != -3 {
		t.Fatalf("gift replay ACK %x", got)
	}
	c, _, _ := f.repos.Character.Load(ctx, "31")
	if c.Stats["gold"] != 10000 {
		t.Fatalf("double gift debit %d", c.Stats["gold"])
	}
	if err := s.sendInitialCurrentWeddingGuestLetters(guest); err != nil {
		t.Fatal(err)
	}
	if got := guildReply(t, guest, 1005); got[77] != 1 {
		t.Fatal("persistent paid invitation was not restored")
	}
	entry := make([]byte, 22)
	binary.LittleEndian.PutUint32(entry[13:], key)
	binary.LittleEndian.PutUint32(entry[17:], 8)
	entry[21] = 1
	if err := s.handleCurrentWeddingGuestCeremonyEntry(guest, entry); err != nil {
		t.Fatal(err)
	}
	if got := guildReply(t, guest, 1039); int32(binary.LittleEndian.Uint32(got[1:])) != 0 {
		t.Fatalf("real guest entry %x", got)
	}
	landed, _, _ := f.repos.Character.Load(ctx, "31")
	if landed.Stats["pos_x"] != 323 || landed.Stats["pos_y"] != 332 {
		t.Fatalf("guest did not use the 86/PVF movable rectangle: %v", landed.Stats)
	}
	guest.conn.(*bufferConn).write.Reset()
	guestIdentity, _ := s.boundGameSessionCharacterSnapshot(guest)
	if err := s.runCurrentWeddingCeremonyRecovery(guestIdentity, nil); err != nil {
		t.Fatal(err)
	}
	if guest.finishLoadingGateSnapshot() != (finishLoadingGates{}) {
		t.Fatal("ordinary town arrival manufactured dungeon loading gates")
	}
	view, handled, err := s.currentWeddingGuestLoadingProof(ctx, guest)
	if err != nil || !handled || view.Key != key {
		t.Fatalf("guest invitation proof %+v/%t/%v", view, handled, err)
	}
	comment := make([]byte, 8)
	binary.LittleEndian.PutUint32(comment, key)
	binary.LittleEndian.PutUint32(comment[4:], 4)
	comment = append(comment, []byte("wish")...)
	if err := s.handleCurrentWeddingUpdateGuestComment(guest, comment); err != nil {
		t.Fatal(err)
	}
	if got := guildReply(t, guest, 1040); len(got) != 5 || got[0] != 1 || int32(binary.LittleEndian.Uint32(got[1:])) != 0 {
		t.Fatalf("invited scene guest comment ACK %x", got)
	}
	// Use the real op24 entry and its queued scene recovery callback for all
	// three clients. No invented C2S37 or prefilled finish gates may be needed.
	for _, host := range []*gameSession{f.first, f.second} {
		entry := make([]byte, 22)
		binary.LittleEndian.PutUint32(entry[13:], key)
		binary.LittleEndian.PutUint32(entry[17:], 8)
		if err := s.handleCurrentWeddingCeremonyEntry(host, entry); err != nil {
			t.Fatal(err)
		}
		identity, _ := s.boundGameSessionCharacterSnapshot(host)
		if err := s.runCurrentWeddingCeremonyRecovery(identity, nil); err != nil {
			t.Fatal(err)
		}
		if host.finishLoadingGateSnapshot() != (finishLoadingGates{}) {
			t.Fatal("host town path requires fake dungeon loading")
		}
	}
	for _, p := range []*gameSession{f.first, f.second, guest} {
		weddingChurchLoadingFrames(t, p)
	}
	ceremonyKey, _ := r.WeddingPairScope([]string{"29", "30"})
	ceremony := s.wedding.ceremonies[ceremonyKey]
	ceremony.timer.Stop()
	f.now = time.Unix(f.at, 0)
	s.runCurrentWeddingCeremony(ceremonyKey, ceremony)
	for _, p := range []*gameSession{f.first, f.second, guest} {
		beginCount := 0
		for _, frame := range weddingChurchLoadingFrames(t, p) {
			if frame.class == 0 && frame.op == 1007 {
				beginCount++
			}
		}
		if beginCount != 1 {
			t.Fatalf("character %d has %d movie starts", p.selectedCharacterID, beginCount)
		}
	}
	if err := s.runCurrentWeddingCeremonyRecovery(guestIdentity, nil); err != nil {
		t.Fatal(err)
	}
	for _, frame := range weddingChurchLoadingFrames(t, guest) {
		if frame.op == 999 || frame.op == 1007 {
			t.Fatal("same guest scene replayed wedding visuals/movie")
		}
	}
	ceremony.timer.Stop()
	f.now = time.Unix(f.at+60, 0)
	s.runCurrentWeddingCeremony(ceremonyKey, ceremony)
	for _, p := range []*gameSession{f.first, f.second} {
		c, _, _ := f.repos.Character.Load(ctx, strconv.Itoa(int(p.selectedCharacterID)))
		state, err := wedding.Read(c)
		if err != nil || state.Phase != wedding.Married {
			t.Fatalf("real town ceremony did not finish: %+v/%v", state, err)
		}
		bag, _, _ := f.repos.Inventory.Load(ctx, c.CharacterID)
		assertCurrentNPCShopInventoryAmount(t, bag, 10002629, 1)
	}
}

package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/buffswap"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestPeerInspectionBuffButtonTargetAndOwnership(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "upper"
		if legacy {
			name = "native_legacy"
		}
		t.Run(name, func(t *testing.T) { testPeerInspectionBuffButtonTargetAndOwnership(t, legacy) })
	}
}

func peerInspectionDispatchForTest(legacy bool) func(*Service, *gameSession, uint16, byte, []byte) (bool, error) {
	if legacy {
		return func(s *Service, session *gameSession, opcode uint16, class byte, body []byte) (bool, error) {
			// The shipped sender writes cmd=1, a 13-byte legacy header and a u16
			// target. Exercise parsing, authentication and the production registry.
			return true, s.handleLegacyGamePacket(session, buildLegacyGamePacketForBridgeTest(class, opcode, 1, body))
		}
	}
	return currentGameplayModules.DispatchUpper
}

func testPeerInspectionBuffButtonTargetAndOwnership(t *testing.T, legacy bool) {
	t.Setenv(buffSwapReapplyEnv, "on")
	t.Setenv(peerUserInfoIdentityCarrierEnv, "off")
	r := newSkillSwitchInventoryRepos(t)
	ctx := context.Background()
	if err := r.Character.Save(ctx, repo.CharacterRecord{CharacterID: "5", AccountID: "other", Name: "peer", Level: 90}); err != nil {
		t.Fatal(err)
	}
	if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "5", Slots: map[string]repo.ItemStack{"0:55": {ItemID: 100350891, Count: 1}, "0:56": {ItemID: 987654321, Count: 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: "5"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Skill.Save(ctx, repo.SkillRecord{CharacterID: "5", Skills: map[int64]repo.SkillState{38: {Level: 10}, 99: {Level: 9}}}); err != nil {
		t.Fatal(err)
	}
	if err := buffswap.Apply(ctx, r, "5", "other", buffswap.Command{Skill: 38, Slot: 23, List: 0, Index: 55}); err != nil {
		t.Fatal(err)
	}
	s, viewer, conn, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryShapeGuardTier, r)
	s.options.currentClientVersion = "official-24.9"
	s.adventureGroupTable = loadAdventureGroupTestTables(t)
	s.onlinePlayers = newOnlinePlayerManager()
	channel := channelcatalog.Channel{ID: 42}
	viewer.channel = channel
	target := &gameSession{conn: &bufferConn{}, channel: channel, accountID: "other", selectedCharacterID: 5, townActorOwnerChannel: 42}
	s.bindGameSessionCharacter(viewer, 1)
	s.bindGameSessionCharacter(target, 5)
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 1, TownID: 1, AreaID: 2, Session: viewer})
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 5, TownID: 1, AreaID: 2, Session: target})
	before, _, _ := r.Settings.Load(ctx, buffswap.Scope("5"))
	dispatch := peerInspectionDispatchForTest(legacy)
	handled, err := dispatch(s, viewer, currentPeerBuffInspectCommand, 1, []byte{5, 0})
	if err != nil || !handled {
		t.Fatal(handled, err)
	}
	packets := drainUpperPackets(t, conn)
	if len(packets) != 1 || packets[0].msgID != 1497 {
		t.Fatalf("packets=%+v", packets)
	}
	b := packets[0].body
	if len(b) != 129 || !bytes.Equal(b[:10], []byte{1, 5, 0, 38, 0, 1, 23, 0, 55, 0}) || binary.LittleEndian.Uint32(b[12:16]) != 100350891 {
		t.Fatalf("target registration=%x", b)
	}
	if bytes.Contains(b, []byte{0xb1, 0x68, 0xde, 0x3a}) {
		t.Fatal("unregistered bag leaked")
	}
	after, _, _ := r.Settings.Load(ctx, buffswap.Scope("5"))
	if after.Values["state"] != before.Values["state"] {
		t.Fatal("inspection changed registration")
	}
	for _, body := range [][]byte{nil, {5}, {5, 0, 0}, {1, 0}, {99, 0}} {
		conn.write.Reset()
		if _, err := dispatch(s, viewer, 1497, 1, body); err != nil {
			t.Fatal(err)
		}
		if conn.write.Len() != 0 {
			t.Fatalf("invalid request replied: %x", body)
		}
	}
	conn.write.Reset()
	if _, err := dispatch(s, viewer, 1497, 0, []byte{5, 0}); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 {
		t.Fatal("wrong class accepted")
	}
	target.channel.ID = 43
	if _, err := dispatch(s, viewer, 1497, 1, []byte{5, 0}); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 {
		t.Fatal("cross-channel target accepted")
	}
}

func TestPeerInspectionTitleButtonEmptyFilledAndScope(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "upper"
		if legacy {
			name = "native_legacy"
		}
		t.Run(name, func(t *testing.T) { testPeerInspectionTitleButtonEmptyFilledAndScope(t, legacy) })
	}
}

func testPeerInspectionTitleButtonEmptyFilledAndScope(t *testing.T, legacy bool) {
	for _, populated := range []bool{false, true} {
		f := newTownPartyActorHPFixture(t, false)
		s, viewer, conn := f.service, f.target, f.targetConn
		s.options.currentClientVersion = "official-24.9"
		repos, _ := s.repositoryGroup()
		inv := repo.InventoryRecord{CharacterID: "17", Slots: map[string]repo.ItemStack{}}
		if populated {
			inv.Slots["100:1007"] = repo.ItemStack{ItemID: 26649, Count: 1}
			inv.Slots["0:12"] = repo.ItemStack{ItemID: 987654321, Count: 1}
		}
		if err := repos.Inventory.Save(context.Background(), inv); err != nil {
			t.Fatal(err)
		}
		before, _, err := repos.Inventory.Load(context.Background(), "17")
		if err != nil {
			t.Fatal(err)
		}
		dispatch := peerInspectionDispatchForTest(legacy)
		f.reset()
		if handled, err := dispatch(s, viewer, 423, 1, []byte{17, 0}); err != nil || !handled {
			t.Fatal(handled, err)
		}
		packets := drainUpperPackets(t, conn)
		if len(packets) != 5 {
			t.Fatalf("pages=%d", len(packets))
		}
		for i, p := range packets {
			b := p.body
			if p.msgID != 358 || len(b) < 11 || b[0] != 1 || binary.LittleEndian.Uint16(b[1:]) != 17 || int(binary.LittleEndian.Uint32(b[3:])) != i {
				t.Fatalf("page %d=%x", i, b)
			}
			n := binary.LittleEndian.Uint32(b[7:])
			if populated && i == 1 {
				if n != 1 || len(b) != 37 || binary.LittleEndian.Uint32(b[13:]) != 26649 {
					t.Fatalf("target title=%x", b)
				}
			} else if n != 0 || len(b) != 11 {
				t.Fatalf("empty page=%x", b)
			}
		}
		after, _, _ := repos.Inventory.Load(context.Background(), "17")
		if !reflect.DeepEqual(after, before) {
			t.Fatal("inspection mutated inventory")
		}
		for _, body := range [][]byte{nil, {17}, {17, 0, 0}, {29, 0}, {99, 0}} {
			conn.write.Reset()
			if _, err := dispatch(s, viewer, 423, 1, body); err != nil {
				t.Fatal(err)
			}
			if conn.write.Len() != 0 {
				t.Fatalf("invalid title request replied: %x", body)
			}
		}
		if _, err := dispatch(s, viewer, 423, 0, []byte{17, 0}); err != nil {
			t.Fatal(err)
		}
		if conn.write.Len() != 0 {
			t.Fatal("wrong title class accepted")
		}
		info := f.actorInfo
		info.AreaID++
		s.onlinePlayers.EnterArea(&info)
		conn.write.Reset()
		if _, err := dispatch(s, viewer, 423, 1, []byte{17, 0}); err != nil {
			t.Fatal(err)
		}
		if conn.write.Len() != 0 {
			t.Fatal("other area accepted")
		}
	}
}

// Produced by verify_requests.py from the shipped cumulative client, with
// only final transport length/checksum/sequence adapted. Both requests have
// a 13-byte header and exactly two body bytes; they are not upper packets.
func TestPeerInspectionNativeLegacyTitleFrame(t *testing.T) {
	f := newTownPartyActorHPFixture(t, false)
	raw, err := hex.DecodeString("01a7010f0000000000000001001100")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.handleLegacyGamePacket(f.target, raw); err != nil {
		t.Fatal(err)
	}
	packets := drainUpperPackets(t, f.targetConn)
	if len(packets) != 5 {
		t.Fatalf("native legacy title request did not open the book: pages=%d", len(packets))
	}
	for category, p := range packets {
		if p.msgID != 358 || len(p.body) != 11 || p.body[0] != 1 ||
			binary.LittleEndian.Uint16(p.body[1:]) != 17 ||
			binary.LittleEndian.Uint32(p.body[3:]) != uint32(category) {
			t.Fatalf("native legacy title page %d: %x", category, p.body)
		}
	}
}

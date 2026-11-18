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

func TestPeerInspectionBuffCommand1497CyclesStablePagesAndResets(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "on")
	t.Setenv(peerUserInfoIdentityCarrierEnv, "off")
	repos := newSkillSwitchInventoryRepos(t)
	ctx := context.Background()
	for _, character := range []repo.CharacterRecord{
		{CharacterID: "5", AccountID: "other5", Name: "buff-five", Level: 90},
		{CharacterID: "6", AccountID: "dnf:10001", Name: "viewer-six", Level: 90},
		{CharacterID: "7", AccountID: "other7", Name: "buff-seven", Level: 90},
	} {
		if err := repos.Character.Save(ctx, character); err != nil {
			t.Fatal(err)
		}
		if err := repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: character.CharacterID, Slots: map[string]repo.ItemStack{}}); err != nil {
			t.Fatal(err)
		}
		if err := repos.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: character.CharacterID}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "5", Slots: map[string]repo.ItemStack{
		"0:54": {ItemID: 101040493, Count: 1},
		"0:55": {ItemID: 101040494, Count: 1},
		"0:56": {ItemID: 101040495, Count: 1},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Skill.Save(ctx, repo.SkillRecord{CharacterID: "5", Skills: map[int64]repo.SkillState{
		38: {Level: 10}, 39: {Level: 11}, 40: {Level: 12},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "7", Slots: map[string]repo.ItemStack{
		"0:57": {ItemID: 101040496, Count: 1},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Skill.Save(ctx, repo.SkillRecord{CharacterID: "7", Skills: map[int64]repo.SkillState{41: {Level: 13}}}); err != nil {
		t.Fatal(err)
	}
	for _, command := range []struct {
		group byte
		skill uint16
		index uint16
	}{
		{group: 0, skill: 38, index: 54},
		{group: 1, skill: 39, index: 55},
	} {
		if err := buffswap.ApplyCurrentGroup(ctx, repos, "5", "other5", command.group, buffswap.Command{Skill: command.skill, Slot: 12, List: 0, Index: command.index}); err != nil {
			t.Fatal(err)
		}
	}
	if err := buffswap.ApplyCurrent(ctx, repos, "5", "other5", buffswap.Command{Skill: 40, Slot: 12, List: 0, Index: 56}); err != nil {
		t.Fatal(err)
	}
	if err := buffswap.ApplyCurrent(ctx, repos, "5", "other5", buffswap.Command{Skill: 39, Slot: buffswap.SlotCount, List: 44, Index: 0xffff}); err != nil {
		t.Fatal(err)
	}
	if err := buffswap.ApplyCurrent(ctx, repos, "7", "other7", buffswap.Command{Skill: 41, Slot: 12, List: 0, Index: 57}); err != nil {
		t.Fatal(err)
	}

	s, viewer, conn, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryShapeGuardTier, repos)
	s.options.currentClientVersion = "official-24.9"
	s.adventureGroupTable = loadAdventureGroupTestTables(t)
	s.onlinePlayers = newOnlinePlayerManager()
	channel := channelcatalog.Channel{ID: 42}
	viewer.channel = channel
	target5 := &gameSession{conn: &bufferConn{}, channel: channel, accountID: "other5", selectedCharacterID: 5, townActorOwnerChannel: 42}
	target7 := &gameSession{conn: &bufferConn{}, channel: channel, accountID: "other7", selectedCharacterID: 7, townActorOwnerChannel: 42}
	s.bindGameSessionCharacter(viewer, 1)
	s.bindGameSessionCharacter(target5, 5)
	s.bindGameSessionCharacter(target7, 7)
	for _, info := range []*onlinePlayerInfo{
		{CharacterID: 1, TownID: 1, AreaID: 2, Session: viewer},
		{CharacterID: 5, TownID: 1, AreaID: 2, Session: target5},
		{CharacterID: 6, TownID: 1, AreaID: 2, Session: viewer},
		{CharacterID: 7, TownID: 1, AreaID: 2, Session: target7},
	} {
		s.onlinePlayers.EnterArea(info)
	}
	before, found, err := repos.Settings.Load(ctx, buffswap.Scope("5"))
	if err != nil || !found {
		t.Fatalf("load target Buff state: found=%v err=%v", found, err)
	}

	click := func(target uint16) (uint16, uint32) {
		t.Helper()
		conn.write.Reset()
		if err := s.handleGameCommand(viewer, 1, currentPeerBuffInspectCommand, []byte{byte(target), byte(target >> 8)}); err != nil {
			t.Fatal(err)
		}
		packets := drainUpperPackets(t, conn)
		if len(packets) != 1 || packets[0].msgID != currentPeerBuffInspectCommand {
			t.Fatalf("target %d packets=%+v", target, packets)
		}
		body := packets[0].body
		if len(body) < 16 || body[0] != 1 || binary.LittleEndian.Uint16(body[1:]) != target || body[5] != 1 {
			t.Fatalf("target %d single-skill response=%x", target, body)
		}
		return binary.LittleEndian.Uint16(body[3:]), binary.LittleEndian.Uint32(body[12:])
	}

	for index, want := range []struct {
		skill uint16
		item  uint32
	}{
		{skill: 39, item: 101040494},
		{skill: 40, item: 101040495},
		{skill: 38, item: 101040493},
		{skill: 39, item: 101040494},
	} {
		skill, item := click(5)
		if skill != want.skill || item != want.item {
			t.Fatalf("cycle click %d skill=%d item=%d want=%+v", index+1, skill, item, want)
		}
	}
	if skill, item := click(7); skill != 41 || item != 101040496 {
		t.Fatalf("new target did not start at its current group: skill=%d item=%d", skill, item)
	}
	if skill, item := click(5); skill != 39 || item != 101040494 {
		t.Fatalf("target switch did not reset the cursor: skill=%d item=%d", skill, item)
	}

	s.bindGameSessionCharacter(viewer, 6)
	if skill, item := click(5); skill != 39 || item != 101040494 {
		t.Fatalf("viewer rebind did not reset the cursor: skill=%d item=%d", skill, item)
	}
	s.mu.Lock()
	viewer.characterGeneration++
	s.mu.Unlock()
	if skill, item := click(5); skill != 39 || item != 101040494 {
		t.Fatalf("viewer generation change did not reset the cursor: skill=%d item=%d", skill, item)
	}
	s.mu.Lock()
	target5.characterGeneration++
	s.mu.Unlock()
	if skill, item := click(5); skill != 39 || item != 101040494 {
		t.Fatalf("target generation change did not reset the cursor: skill=%d item=%d", skill, item)
	}

	viewer.conn = r191FailingConn{conn}
	if err := s.handleGameCommand(viewer, 1, currentPeerBuffInspectCommand, []byte{5, 0}); err == nil {
		t.Fatal("failed page write was accepted")
	}
	viewer.conn = conn
	if skill, item := click(5); skill != 40 || item != 101040495 {
		t.Fatalf("failed write advanced the page: skill=%d item=%d", skill, item)
	}
	after, found, err := repos.Settings.Load(ctx, buffswap.Scope("5"))
	if err != nil || !found || !reflect.DeepEqual(before, after) {
		t.Fatalf("page cycling mutated target Buff state: before=%+v after=%+v err=%v", before, after, err)
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

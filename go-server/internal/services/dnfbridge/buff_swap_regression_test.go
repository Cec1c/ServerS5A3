package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/buffswap"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestNativeBuffPartialDeleteSendsRemainingRegistrationAndKeepsBaseLevel(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "on")
	r := newSkillSwitchInventoryRepos(t)
	ctx := context.Background()
	if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{"0:54": {ItemID: 101040493, Count: 1}, "0:55": {ItemID: 100350891, Count: 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Skill.Save(ctx, repo.SkillRecord{CharacterID: "1", Skills: map[int64]repo.SkillState{38: {Level: 10, Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	s, session, conn, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryShapeGuardTier, r)
	for _, body := range [][]byte{{38, 0, 12, 0, 54, 0}, {38, 0, 23, 0, 55, 0}, {38, 0, 12, 44, 255, 255}} {
		if _, err := s.handleSkillSwitchInventoryCommand(session, 1428, body); err != nil {
			t.Fatal(err)
		}
	}
	packets := drainUpperPackets(t, conn)
	if len(packets) != 3 {
		t.Fatalf("mutation packets=%d", len(packets))
	}
	body := packets[2].body
	if packets[2].msgID != 1369 || binary.LittleEndian.Uint16(body) != 38 || body[3+12*5] != 0 || body[3+23*5] != 1 {
		t.Fatalf("partial deletion snapshot=%x", body)
	}
	skills, _, err := r.Skill.Load(ctx, "1")
	if err != nil || skills.Skills[38].Level != 10 {
		t.Fatalf("base skill changed: %+v %v", skills, err)
	}
	conn.write.Reset()
	if _, err := s.handleSkillSwitchInventoryCommand(session, 1428, []byte{38, 0, 12, 0, 99, 0}); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 {
		t.Fatal("rejected item emitted a restore")
	}
}

func TestNativeBuffRestoresAfterSceneReadyContainersOnce(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "on")
	s, session, conn, _ := newSceneReadyReplayFixture(t)
	armLoginRouteUpToOp24(session)
	session.initialTownRouteStage = currentInitialTownRoutePlayerStateSent
	r, _ := s.repositoryGroup()
	c, _, _ := r.Character.Load(context.Background(), "6")
	session.accountID = c.AccountID
	if err := buffswap.Apply(context.Background(), r, "6", c.AccountID, buffswap.Command{Skill: 38, Slot: 33, List: 44, Index: 65535}); err != nil {
		t.Fatal(err)
	}
	if err := s.sendCurrentLegacyTownSceneReadyBoundary(session, "reselect"); err != nil {
		t.Fatal(err)
	}
	stream := conn.write.Bytes()
	restoreOffset := sceneReadyReplayPacketOffsetOfMsgID(t, stream, 1369)
	containerOffset := sceneReadyReplayPacketOffsetOfMsgID(t, stream, 13)
	if restoreOffset < 0 || containerOffset < 0 || restoreOffset <= containerOffset {
		t.Fatalf("restore=%d containers=%d", restoreOffset, containerOffset)
	}
	conn.write.Reset()
	if err := s.sendCurrentLegacyTownSceneReadyBoundary(session, "repeat"); err != nil {
		t.Fatal(err)
	}
	if sceneReadyReplayPacketOffsetOfMsgID(t, conn.write.Bytes(), 1369) >= 0 {
		t.Fatal("duplicate ready reset Buff")
	}
}

func TestNativeBuffUserInfoUsesRegisteredItemsAndAvatarBlobs(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "on")
	r := newSkillSwitchInventoryRepos(t)
	ctx := context.Background()
	if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{"0:54": {ItemID: 101040493, Count: 1}}}); err != nil {
		t.Fatal(err)
	}
	s, session, _, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryShapeGuardTier, r)
	if err := buffswap.Apply(ctx, r, "1", s.accountIDForSession(session), buffswap.Command{Skill: 38, Slot: 12, List: 0, Index: 54}); err != nil {
		t.Fatal(err)
	}
	reader := csharpLegacyUserInfoReader{ctx: ctx, service: s, session: session, characterID: "1"}
	var w packetWriter
	reader.writeNativeBuffSwapUserInfo(&w)
	b := w.bytes()
	if len(b) != 126 || !bytes.Equal(b[:7], []byte{38, 0, 1, 12, 0, 54, 0}) || binary.LittleEndian.Uint32(b[9:13]) != 101040493 {
		t.Fatalf("native item block=%x", b)
	}
	// Exercise the complete source-list1 registration, including both blobs,
	preserved, _, _ := r.Inventory.Load(ctx, "1")
	// and its boundary before the next registered equipment row.
	if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{
		"0:54": preserved.Slots["0:54"],
		"1:7":  {ItemID: 42601, Count: 1, Extra: map[string]string{"avatar_color_data": "0405"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := buffswap.Apply(ctx, r, "1", s.accountIDForSession(session), buffswap.Command{Skill: 38, Slot: 0, List: 1, Index: 7}); err != nil {
		t.Fatal(err)
	}
	var avatar packetWriter
	reader.writeNativeBuffSwapUserInfo(&avatar)
	encoded := avatar.bytes()
	if encoded[2] != 2 || !bytes.Equal(encoded[3:7], []byte{0, 1, 7, 0}) || !bytes.Equal(encoded[126:136], []byte{0, 0, 0, 0, 2, 0, 0, 0, 4, 5}) || !bytes.Equal(encoded[136:140], []byte{12, 0, 54, 0}) {
		t.Fatalf("avatar registration framing=%x", encoded)
	}
}

func TestPeerBuffInspectCarriesOnlyTargetRegistration(t *testing.T) {
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
	if handled, err := s.handleCurrentPeerUserInfoRequest(viewer, []byte{5, 0, 3}); err != nil || !handled {
		t.Fatal(handled, err)
	}
	p := drainUpperPackets(t, conn)
	if len(p) != 2 || p[0].msgID != 2 || p[0].body[0] != 1 || p[1].body[0] != 3 {
		t.Fatalf("inspect packets=%+v", p)
	}
	b := p[0].body
	if binary.LittleEndian.Uint16(b[21:23]) != 5 || b[currentMode1CreateCountOffset] != 0 {
		t.Fatal("Buff envelope did not target peer or exposed worn create rows")
	}
	offset := currentMode1CreateRowsOffset + 4
	if !bytes.Equal(b[offset:offset+7], []byte{38, 0, 1, 23, 0, 55, 0}) || binary.LittleEndian.Uint32(b[offset+9:offset+13]) != 100350891 {
		t.Fatalf("target Buff block=%x", b[offset:])
	}
	if bytes.Contains(b, []byte{0xb1, 0x68, 0xde, 0x3a}) {
		t.Fatal("unregistered private bag item leaked")
	}
	footer := b[len(b)-16:]
	textSize := int(binary.LittleEndian.Uint32(footer[12:16]))
	if string(footer[:8]) != peerBuffViewMagic || binary.LittleEndian.Uint32(footer[8:12]) != 5 || textSize <= 0 || textSize > 8192 || textSize > len(b)-16 {
		t.Fatalf("invalid read-only peer Buff appendix: %x", footer)
	}
	viewText := string(b[len(b)-16-textSize : len(b)-16])
	if !strings.Contains(viewText, "peer 的 Buff 登记") || !strings.Contains(viewText, "基础 Lv10") || !strings.Contains(viewText, "已登记 1 件装备") || strings.Contains(viewText, "987654321") || strings.Contains(viewText, "技能 99") {
		t.Fatalf("peer display exposed unregistered state or omitted selected registration: %q", viewText)
	}
	pageOffset := offset + 126 + 8
	if !bytes.Equal(b[pageOffset:pageOffset+9], []byte{255, 1, 38, 0, 10, 1, 38, 0, 10}) {
		t.Fatalf("selected Buff page=%x", b[pageOffset:])
	}
	after, _, _ := r.Settings.Load(ctx, buffswap.Scope("5"))
	if after.Values["state"] != before.Values["state"] {
		t.Fatal("inspect modified target registration")
	}
	if _, found, _ := r.Settings.Load(ctx, buffswap.Scope("1")); found {
		t.Fatal("inspect copied peer into viewer's registration")
	}
}

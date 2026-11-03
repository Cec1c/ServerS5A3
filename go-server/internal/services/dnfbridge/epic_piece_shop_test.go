package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/epicbook"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func epicShopFixture(t *testing.T, probability int) (*Service, *gameSession, *bufferConn, *runtimeDungeonState) {
	t.Helper()
	source := bridgeHellPartyClearSettlementPVF()
	source[epicbook.ShopConfigPath] = fmt.Sprintf("[npcIndex] 1130\n[epic piece provide] 5\n[epic piece received] 10\n[gabriel appearance probability] %d\n[list change chance] 5\n[list change cost] 100000\n[kind of epic piece] 5\n[same type] 0\n[normal type] 5\n", probability)
	source[currentEpicPieceInfoPath] = "[piece list] 5001 10300001 5002 10300002 5003 10300003 5004 10300004 5005 10300005 5006 10300006 5007 10300007\n[/piece list]"
	for id := 5001; id <= 5007; id++ {
		source[dungeonDropEquipmentList] += fmt.Sprintf("%d `shop/%d.equ`\n", id, id)
		source[fmt.Sprintf("equipment/shop/%d.equ", id)] = "[equipment type] `[coat]`\n[rarity] 4\n[minimum level] 90\n[grade] 9\n"
	}
	s, p, c, r, _ := prepareHellPartyDungeonRuntimeForEntryTest(t, source)
	s.options.currentClientVersion = "official-24.9"
	s.options.epicPiecePush = epicPiecePushAlways
	g, _ := s.repositoryGroup()
	ctx := context.Background()
	a := repo.AccountRecord{AccountID: "account-1", Metadata: map[string]string{}}
	for id := uint32(10300001); id <= 10300007; id++ {
		a.Metadata[repo.AccountEpicPieceMetadataKey(id)] = "10"
	}
	if err := g.Account.Save(ctx, a); err != nil {
		t.Fatal(err)
	}
	character, _, err := g.Character.Load(ctx, "99")
	if err != nil {
		t.Fatal(err)
	}
	character.Stats["gold"] = 600000
	if err = g.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Room.AnnounceAllActors(r.Session); err != nil {
		t.Fatal(err)
	}
	return s, p, c, r
}

// The shop uses the official client profile, whose death request is 88 bytes
// for one combat entry. The legacy hell-party helper sends a rejected 62-byte
// request, so it cannot exercise the shop's real post-death generation hook.
func killEpicShopActors(t *testing.T, s *Service, p *gameSession, keys ...uint32) {
	t.Helper()
	for _, key := range keys {
		body := make([]byte, dungeoncmd.Official249DieMonsterBaseSize+dungeoncmd.Official249DieMonsterEntrySize)
		binary.LittleEndian.PutUint32(body, key)
		binary.LittleEndian.PutUint16(body[4:], currentSceneActorObjectKey(p.selectedCharacterID))
		body[30] = 1
		if err := s.handleDungeonMonsterDeath(p, body); err != nil {
			t.Fatal(err)
		}
	}
}

func killEpicShopRoom(t *testing.T, s *Service, p *gameSession, r *runtimeDungeonState) {
	t.Helper()
	killEpicShopActors(t, s, p, hellPartyRoomMonsterKeys(t, r)...)
	killEpicShopActors(t, s, p, waveHellPartyActorKeys(t, r)...)
	scene, _ := r.Session.Scene()
	if !currentActivityHellReady(r, scene) {
		t.Fatal("official death requests did not retire every frozen hell wave")
	}
}

func shopPackets(t *testing.T, s *Service, c *bufferConn, op uint16) [][]byte {
	t.Helper()
	wire := append([]byte(nil), c.write.Bytes()...)
	var bodies [][]byte
	for len(wire) > 0 {
		packet, rest := splitGameServerUpperPacketWithHeader(t, wire, s.gameUpperHeaderSize())
		wire = rest
		if packet.Header.MsgID == op {
			bodies = append(bodies, append([]byte(nil), packet.Body...))
		}
	}
	return bodies
}

func shopChoice(id uint32) []byte {
	body := make([]byte, 4)
	binary.LittleEndian.PutUint32(body, id)
	return body
}

func TestEpicShopFinalWaveOnlySingleAppearanceAndNoStartRequired(t *testing.T) {
	s, p, c, r := epicShopFixture(t, 100)
	first, last := hellPartyRewardActorKeys(t, r)
	killEpicShopActors(t, s, p, hellPartyRoomMonsterKeys(t, r)...)
	killEpicShopActors(t, s, p, first...)
	killEpicShopActors(t, s, p, last[:len(last)-1]...)
	if r.epicShop != nil || len(shopPackets(t, s, c, 744)) != 0 {
		t.Fatal("shop before final wave")
	}
	killEpicShopActors(t, s, p, last[len(last)-1])
	if r.HellPartyStarted || r.epicShop == nil || !r.epicShop.Sent {
		t.Fatal("missing shop without start notice")
	}
	killEpicShopActors(t, s, p, last[len(last)-1])
	if bodies := shopPackets(t, s, c, 744); len(bodies) != 1 || !reflect.DeepEqual(bodies[0], []byte{1}) {
		t.Fatal(bodies)
	}
	if r.bossDieCheckAccepted {
		t.Fatal("shop ended ordinary dungeon early")
	}
}

func TestEpicShopZeroProbabilityAndRollFrozen(t *testing.T) {
	s, p, c, r := epicShopFixture(t, 0)
	killEpicShopRoom(t, s, p, r)
	if !r.epicShopChecked || r.epicShop != nil || len(shopPackets(t, s, c, 744)) != 0 {
		t.Fatal("zero probability spawned shop")
	}
	s.dungeonChoice = func(int) (int, error) { t.Fatal("appearance rerolled"); return 0, nil }
	scene, _ := r.Session.Scene()
	if err := s.offerCurrentEpicShopLocked(p, r, scene); err != nil {
		t.Fatal(err)
	}
}

func TestEpicShopNativeChoiceRefreshExchangeAndReplay(t *testing.T) {
	s, p, c, r := epicShopFixture(t, 100)
	killEpicShopRoom(t, s, p, r)
	c.write.Reset()
	if err := s.handleCurrentEpicShop(p, 816, shopChoice(10300001)); err != nil {
		t.Fatal(err)
	}
	initial := r.epicShop.Materials
	if bodies := shopPackets(t, s, c, 745); len(bodies) != 1 || len(bodies[0]) != 41 || bodies[0][0] != 5 {
		t.Fatal(bodies)
	} else {
		seen := map[uint32]bool{10300001: true}
		for i := 0; i < 5; i++ {
			id := binary.LittleEndian.Uint32(bodies[0][1+i*8:])
			n := binary.LittleEndian.Uint32(bodies[0][5+i*8:])
			if seen[id] || n != 10 {
				t.Fatal(id, n)
			}
			seen[id] = true
		}
	}
	s.dungeonChoice = func(limit int) (int, error) { return limit - 1, nil }
	if err := s.handleCurrentEpicShop(p, 816, shopChoice(10300001)); err != nil {
		t.Fatal(err)
	}
	if r.epicShop.Materials != initial {
		t.Fatal("free choice replay rerolled quote")
	}
	for i := 0; i < 6; i++ {
		if err := s.handleCurrentEpicShop(p, 817, nil); err != nil {
			t.Fatal(err)
		}
	}
	g, _ := s.repositoryGroup()
	character, _, _ := g.Character.Load(context.Background(), "99")
	if character.Stats["gold"] != 100000 || r.epicShop.Refreshes != 5 {
		t.Fatal(character.Stats, r.epicShop.Refreshes)
	}
	if acks := shopPackets(t, s, c, 817); len(acks) != 6 || acks[5][0] != 0 {
		t.Fatal(acks)
	}
	materials := r.epicShop.Materials
	c.write.Reset()
	if err := s.handleCurrentEpicShop(p, 818, nil); err != nil {
		t.Fatal(err)
	}
	a, _, _ := g.Account.Load(context.Background(), "account-1")
	if !r.epicShop.Complete || a.Metadata[repo.AccountEpicPieceMetadataKey(10300001)] != "15" {
		t.Fatal(a, r.epicShop)
	}
	for _, id := range materials {
		if a.Metadata[repo.AccountEpicPieceMetadataKey(id)] != "0" {
			t.Fatal(a.Metadata)
		}
	}
	updates := shopPackets(t, s, c, currentEpicPieceBalanceSyncOpcode)
	if len(updates) != 1 || len(updates[0]) != 53 || updates[0][0] != 1 || binary.LittleEndian.Uint32(updates[0][1:]) != 6 {
		t.Fatal(updates)
	}
	if err := s.handleCurrentEpicShop(p, 818, nil); err != nil {
		t.Fatal(err)
	}
	after, _, _ := g.Account.Load(context.Background(), "account-1")
	if !reflect.DeepEqual(a, after) {
		t.Fatal("duplicate exchange mutated ledger")
	}
	acks := shopPackets(t, s, c, 818)
	if len(acks) != 2 || acks[0][0] != 1 || acks[1][0] != 0 {
		t.Fatal(acks)
	}
}

func TestEpicShopRejectsWrongRoomMalformedChoiceAndInsufficientMaterials(t *testing.T) {
	s, p, c, r := epicShopFixture(t, 100)
	if err := s.handleCurrentEpicShop(p, 818, nil); err != nil {
		t.Fatal(err)
	}
	killEpicShopRoom(t, s, p, r)
	for _, body := range [][]byte{nil, {1, 2}, shopChoice(5001), append(shopChoice(10300001), 0)} {
		if err := s.handleCurrentEpicShop(p, 816, body); err != nil {
			t.Fatal(err)
		}
	}
	if r.epicShop.Target != 0 {
		t.Fatal("invalid choice accepted")
	}
	if err := s.handleCurrentEpicShop(p, 816, shopChoice(10300001)); err != nil {
		t.Fatal(err)
	}
	g, _ := s.repositoryGroup()
	a, _, _ := g.Account.Load(context.Background(), "account-1")
	a.Metadata[repo.AccountEpicPieceMetadataKey(r.epicShop.Materials[4])] = "9"
	_ = g.Account.Save(context.Background(), a)
	if err := s.handleCurrentEpicShop(p, 818, nil); err != nil {
		t.Fatal(err)
	}
	after, _, _ := g.Account.Load(context.Background(), "account-1")
	if !reflect.DeepEqual(a, after) || r.epicShop.Complete {
		t.Fatal("insufficient material partially committed")
	}
	r.townReturnPending = true
	if err := s.handleCurrentEpicShop(p, 817, nil); err != nil {
		t.Fatal(err)
	}
	if acks := shopPackets(t, s, c, 817); len(acks) != 1 || acks[0][0] != 0 {
		t.Fatal(acks)
	}
	r.townReturnPending = false
	moveDungeonSessionToRoomForTest(t, s, p, 1, 0)
	if !r.epicShop.Complete {
		t.Fatal("leaving hell room retained offer")
	}
	if err := s.handleCurrentEpicShop(p, 818, nil); err != nil {
		t.Fatal(err)
	}
}

func TestEpicShopPartyAppearanceRollSharedAndLateMemberRejected(t *testing.T) {
	s, p, c, r := epicShopFixture(t, 100)
	instance := &dungeonPartyInstance{scenes: map[uint16]*worldmap.DungeonSession{99: r.Session}, sessions: map[uint16]*gameSession{99: p}}
	r.partyInstance = instance
	// Finish through the solo death path, then attach the independently owned
	// party encounter to exercise the same lock and frozen membership rule.
	r.partyInstance = nil
	killEpicShopRoom(t, s, p, r)
	r.epicShop = nil
	r.epicShopChecked = false
	r.partyInstance = instance
	c.write.Reset()
	scene, _ := r.Session.Scene()
	if err := s.offerCurrentEpicShopLocked(p, r, scene); err != nil {
		t.Fatal(err)
	}
	if instance.epicShopEncounter == nil || !instance.epicShopEncounter.Appeared {
		t.Fatal("party roll absent")
	}
	late := *r
	late.epicShop = nil
	late.epicShopChecked = false
	peer := &gameSession{conn: &bufferConn{}, selectedCharacterID: 43}
	peer.dungeon.runtime = &late
	s.dungeonChoice = func(int) (int, error) { t.Fatal("party reroll"); return 0, nil }
	if err := s.offerCurrentEpicShopLocked(peer, &late, scene); err != nil {
		t.Fatal(err)
	}
	if late.epicShop != nil {
		t.Fatal("late member acquired original encounter")
	}
}

func TestEpicShopCommittedExchangeReplyFailureCannotCreditAgain(t *testing.T) {
	s, p, _, r := epicShopFixture(t, 100)
	killEpicShopRoom(t, s, p, r)
	if err := s.handleCurrentEpicShop(p, 816, shopChoice(10300001)); err != nil {
		t.Fatal(err)
	}
	p.conn = &epicDisjointFailedWriteConn{}
	if err := s.handleCurrentEpicShop(p, 818, nil); err == nil {
		t.Fatal("expected response failure")
	}
	g, _ := s.repositoryGroup()
	before, _, _ := g.Account.Load(context.Background(), "account-1")
	if !r.epicShop.Complete || before.Metadata[repo.AccountEpicPieceMetadataKey(10300001)] != "15" {
		t.Fatal("commit lost")
	}
	p.conn = &bufferConn{}
	if err := s.handleCurrentEpicShop(p, 818, nil); err != nil {
		t.Fatal(err)
	}
	after, _, _ := g.Account.Load(context.Background(), "account-1")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("credit repeated")
	}
}

func TestEpicShopPartyPresentPeerReceivesSameEncounter(t *testing.T) {
	s, p, c, r := epicShopFixture(t, 0)
	killEpicShopRoom(t, s, p, r)
	source := s.dungeonMonsterTable.source.(bridgePVFSource)
	source[epicbook.ShopConfigPath] = "[npcIndex] 1130\n[epic piece provide] 5\n[epic piece received] 10\n[gabriel appearance probability] 100\n[list change chance] 5\n[list change cost] 100000\n[kind of epic piece] 5\n[same type] 0\n[normal type] 5\n"
	peerConn := &bufferConn{}
	peer := &gameSession{conn: peerConn, selectedCharacterID: 43}
	peerRuntime := *r
	peerRuntime.epicShop = nil
	peerRuntime.epicShopChecked = false
	peerRuntime.Character.CharacterID = "43"
	peer.dungeon.runtime = &peerRuntime
	instance := &dungeonPartyInstance{scenes: map[uint16]*worldmap.DungeonSession{99: r.Session, 43: r.Session}, sessions: map[uint16]*gameSession{99: p, 43: peer}}
	r.partyInstance = instance
	peerRuntime.partyInstance = instance
	r.epicShopChecked = false
	s.gameSessions = map[uint16]*gameSession{99: p, 43: peer}
	rolls := 0
	s.dungeonChoice = func(int) (int, error) { rolls++; return 0, nil }
	c.write.Reset()
	scene, _ := r.Session.Scene()
	if err := s.offerCurrentEpicShopLocked(p, r, scene); err != nil {
		t.Fatal(err)
	}
	if rolls != 1 || r.epicShop == nil || peerRuntime.epicShop == nil || r.epicShop.Encounter != peerRuntime.epicShop.Encounter {
		t.Fatal("party encounter not shared", rolls)
	}
	if len(shopPackets(t, s, c, 744)) != 1 || len(shopPackets(t, s, peerConn, 744)) != 1 {
		t.Fatal("peer missing native spawn")
	}
	if err := s.offerCurrentEpicShopLocked(peer, &peerRuntime, scene); err != nil {
		t.Fatal(err)
	}
	if rolls != 1 || len(shopPackets(t, s, peerConn, 744)) != 1 {
		t.Fatal("peer repeated appearance")
	}
}

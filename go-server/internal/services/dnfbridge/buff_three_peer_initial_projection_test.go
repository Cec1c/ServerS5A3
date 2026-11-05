package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/buffswap"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	buffThreePeerCasterSkill uint16 = 14
	buffThreePeerCasterSlot  byte   = 12
	buffThreePeerCasterIndex uint16 = 54
	buffThreePeerCasterItem  uint32 = 101040493
	buffThreePeerFirstID     uint16 = 99
	buffThreePeerSecondID    uint16 = 100
	buffThreePeerThirdID     uint16 = 101
)

type buffThreePeerFixture struct {
	service  *Service
	sessions map[uint16]*gameSession
	repos    dnfrepo.Group
}

type buffThreePeerObservation struct {
	phase string
	body  []byte
}

// This regression models only the confirmed no-change entry case: three party
// members enter the same dungeon in different orders, and the caster's Buff
// registration/skill rows are already persisted before entry. It exercises both
// the post-op29 projection and the finish-loading resend owners. Assertions are
// byte-level only; this does not claim native actor creation, Buff calculation
// or gameplay acceptance.
func TestBuffThreePeerInitialProjectionPreservesCasterRowsForBothReceivers(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "on")
	t.Setenv(peerSkillPageEnv, peerSkillPageValuePeer)
	ids := []uint16{buffThreePeerFirstID, buffThreePeerSecondID, buffThreePeerThirdID}
	orders := [][]uint16{
		{buffThreePeerFirstID, buffThreePeerSecondID, buffThreePeerThirdID},
		{buffThreePeerFirstID, buffThreePeerThirdID, buffThreePeerSecondID},
		{buffThreePeerSecondID, buffThreePeerFirstID, buffThreePeerThirdID},
		{buffThreePeerSecondID, buffThreePeerThirdID, buffThreePeerFirstID},
		{buffThreePeerThirdID, buffThreePeerFirstID, buffThreePeerSecondID},
		{buffThreePeerThirdID, buffThreePeerSecondID, buffThreePeerFirstID},
	}
	for _, casterID := range ids {
		for _, order := range orders {
			casterID, order := casterID, append([]uint16(nil), order...)
			t.Run(fmt.Sprintf("caster_%d/order_%d_%d_%d", casterID, order[0], order[1], order[2]), func(t *testing.T) {
				f := newBuffThreePeerFixture(t)
				seedBuffThreePeerCaster(t, f, casterID)
				observed := make(map[uint16][]buffThreePeerObservation)
				for _, enteringID := range order {
					session := f.sessions[enteringID]
					enterBuffThreePeerDungeon(t, f.service, session, "initial_"+fmt.Sprint(enteringID))
					collectBuffThreePeerMode1(t, f, casterID, "initial", observed)
					markBuffThreePeerFinishLoading(session)
					f.service.finishDungeonPartyPeerActorProjection(session, "finish_loading_"+fmt.Sprint(enteringID))
					collectBuffThreePeerMode1(t, f, casterID, "finish", observed)
				}
				for _, receiverID := range ids {
					if receiverID == casterID {
						continue
					}
					assertBuffThreePeerMode1Rows(t, receiverID, casterID, observed[receiverID])
				}
			})
		}
	}
}

func newBuffThreePeerFixture(t *testing.T) *buffThreePeerFixture {
	t.Helper()
	var third *gameSession
	s, first, second, _, secondRuntime := partyDungeonDeathFixture(t, func(s *Service, leader, member *gameSession) {
		repos, ok := s.repositoryGroup()
		if !ok {
			t.Fatal("fixture has no repository group")
		}
		character, found, err := repos.Character.Load(context.Background(), "100")
		if err != nil || !found {
			t.Fatalf("load third template: found=%t err=%v", found, err)
		}
		character = dnfrepo.CloneCharacter(character)
		character.CharacterID = "101"
		character.Name = "third"
		if err := repos.Character.Save(context.Background(), character); err != nil {
			t.Fatal(err)
		}
		third = &gameSession{conn: &bufferConn{}, channel: leader.channel, residentChannel: leader.channel}
		s.bindGameSessionCharacter(third, buffThreePeerThirdID)
		state := runtimePartyStateSnapshot(leader)
		state.Members = append(state.Members, alignedcmd.PartyMemberState{
			UserID: buffThreePeerThirdID, UserState: 1, HPPercent: 100, MPPercent: 100,
		})
		for _, session := range []*gameSession{leader, member, third} {
			storeRuntimePartyState(session, state)
		}
		if _, ok := s.bootstrapManagedRuntimePartyForSession(leader); !ok {
			t.Fatal("three-peer lobby was not registered")
		}
	})
	s.options.currentClientVersion = "official-24.9"
	if third == nil {
		t.Fatal("third session was not constructed")
	}
	runtime, _, err := s.prepareDungeonRuntime(
		context.Background(), third, dungeoncmd.SelectDungeonRequest{DungeonID: 700, Difficulty: 1},
	)
	if err != nil {
		t.Fatalf("prepare third runtime: %v", err)
	}
	third.dungeon.mu.Lock()
	third.dungeon.runtime = runtime
	third.dungeon.mu.Unlock()
	scene, ok := runtime.Session.Scene()
	if !ok {
		t.Fatal("third runtime has no scene")
	}
	instance := s.adoptPartyDungeonInstance(third, runtime, scene)
	if instance == nil || instance != secondRuntime.partyInstance {
		t.Fatal("third member did not join the shared dungeon instance")
	}
	s.recordDungeonPartyRunKey(third, runtime, scene)
	sessions := map[uint16]*gameSession{buffThreePeerFirstID: first, buffThreePeerSecondID: second, buffThreePeerThirdID: third}
	for _, session := range sessions {
		session.accountID = "account-1"
		session.dungeonPartyRunMu.Lock()
		session.dungeonPartyRun = dungeonPartyRunKey{}
		session.dungeonPartyRunValid = false
		session.dungeonPartyRunMu.Unlock()
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		t.Fatal("fixture repository group disappeared")
	}
	return &buffThreePeerFixture{service: s, sessions: sessions, repos: repos}
}

func seedBuffThreePeerCaster(t *testing.T, f *buffThreePeerFixture, casterID uint16) {
	t.Helper()
	session := f.sessions[casterID]
	if session == nil {
		t.Fatalf("missing caster session %d", casterID)
	}
	ctx := context.Background()
	id := fmt.Sprint(casterID)
	if err := f.repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: id,
		Slots:       map[string]dnfrepo.ItemStack{"0:54": {ItemID: int64(buffThreePeerCasterItem), Count: 1}},
	}); err != nil {
		t.Fatalf("seed caster inventory: %v", err)
	}
	if err := f.repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: id}); err != nil {
		t.Fatalf("seed caster equipment: %v", err)
	}
	if err := f.repos.Skill.Save(ctx, dnfrepo.SkillRecord{
		CharacterID: id,
		Skills:      map[int64]dnfrepo.SkillState{int64(buffThreePeerCasterSkill): {Level: 10, Enabled: true}},
	}); err != nil {
		t.Fatalf("seed caster skills: %v", err)
	}
	if err := buffswap.ApplyCurrent(ctx, f.repos, id, session.accountID, buffswap.Command{
		Skill: buffThreePeerCasterSkill, Slot: buffThreePeerCasterSlot,
		List: 0, Index: buffThreePeerCasterIndex,
	}); err != nil {
		t.Fatalf("seed caster registration: %v", err)
	}
}

func enterBuffThreePeerDungeon(t *testing.T, s *Service, session *gameSession, source string) {
	t.Helper()
	session.dungeon.mu.Lock()
	runtime := session.dungeon.runtime
	session.dungeon.mu.Unlock()
	if runtime == nil || runtime.Session == nil {
		t.Fatalf("session %d has no dungeon runtime", session.selectedCharacterID)
	}
	scene, ok := runtime.Session.Scene()
	if !ok {
		t.Fatalf("session %d has no dungeon scene", session.selectedCharacterID)
	}
	s.recordDungeonPartyRunKey(session, runtime, scene)
	if err := s.projectDungeonPartyPeerActors(session, source); err != nil {
		t.Fatalf("session %d initial peer projection: %v", session.selectedCharacterID, err)
	}
}

func markBuffThreePeerFinishLoading(session *gameSession) {
	session.finishLoadingMu.Lock()
	session.currentFinishLoadingStateSent = true
	session.currentFinishLoadingCompletionSent = true
	session.postFinishLoadingPlayerStateSent = true
	session.finishLoadingMu.Unlock()
}

func collectBuffThreePeerMode1(t *testing.T, f *buffThreePeerFixture, casterID uint16, phase string, observed map[uint16][]buffThreePeerObservation) {
	t.Helper()
	for receiverID, session := range f.sessions {
		conn := session.conn.(*bufferConn)
		frames := splitUpperFrames(t, conn.write.Bytes())
		conn.write.Reset()
		for _, frame := range frames {
			body := frame.body
			if frame.msgID == 2 && len(body) >= 23 && body[0] == 1 && binary.LittleEndian.Uint16(body[21:23]) == casterID {
				observed[receiverID] = append(observed[receiverID], buffThreePeerObservation{phase: phase, body: append([]byte(nil), body...)})
			}
		}
	}
}

func buffThreePeerRegistration(body []byte) (uint32, bool) {
	prefix := []byte{byte(buffThreePeerCasterSkill), byte(buffThreePeerCasterSkill >> 8), 1,
		buffThreePeerCasterSlot, 0, byte(buffThreePeerCasterIndex), byte(buffThreePeerCasterIndex >> 8)}
	offset := bytes.Index(body, prefix)
	if offset < 0 || offset+13 > len(body) {
		return 0, false
	}
	return binary.LittleEndian.Uint32(body[offset+9 : offset+13]), true
}

func buffThreePeerSkillPage(body []byte) ([]byte, bool) {
	page := []byte{peerSkillPageSelectedResourceGroup,
		1, byte(buffThreePeerCasterSkill), byte(buffThreePeerCasterSkill >> 8), 10,
		1, byte(buffThreePeerCasterSkill), byte(buffThreePeerCasterSkill >> 8), 10}
	if !bytes.Contains(body, page) {
		return nil, false
	}
	return page, true
}

func assertBuffThreePeerMode1Rows(t *testing.T, receiverID, casterID uint16, observations []buffThreePeerObservation) {
	t.Helper()
	if len(observations) == 0 {
		t.Fatalf("receiver %d never received caster %d mode1", receiverID, casterID)
	}
	if observations[0].phase != "initial" || observations[len(observations)-1].phase != "finish" {
		t.Fatalf("receiver %d caster %d phase endpoints=%s..%s, want initial..finish",
			receiverID, casterID, observations[0].phase, observations[len(observations)-1].phase)
	}
	firstItem, firstOK := buffThreePeerRegistration(observations[0].body)
	firstPage, firstPageOK := buffThreePeerSkillPage(observations[0].body)
	lastItem, lastOK := buffThreePeerRegistration(observations[len(observations)-1].body)
	lastPage, lastPageOK := buffThreePeerSkillPage(observations[len(observations)-1].body)
	if !firstOK || !lastOK || firstItem != buffThreePeerCasterItem || lastItem != buffThreePeerCasterItem {
		t.Fatalf("receiver %d caster %d registration first=%08x/%t last=%08x/%t want %08x",
			receiverID, casterID, firstItem, firstOK, lastItem, lastOK, buffThreePeerCasterItem)
	}
	if !firstPageOK || !lastPageOK || !bytes.Equal(firstPage, lastPage) {
		t.Fatalf("receiver %d caster %d skill page first=%x/%t last=%x/%t",
			receiverID, casterID, firstPage, firstPageOK, lastPage, lastPageOK)
	}
	initial, finish := 0, 0
	for index, observation := range observations {
		item, ok := buffThreePeerRegistration(observation.body)
		page, pageOK := buffThreePeerSkillPage(observation.body)
		if !ok || item != firstItem || !pageOK || !bytes.Equal(page, firstPage) {
			t.Fatalf("receiver %d caster %d %s mode1[%d] registration/page changed", receiverID, casterID, observation.phase, index)
		}
		switch observation.phase {
		case "initial":
			initial++
		case "finish":
			finish++
		}
	}
	if initial == 0 || finish == 0 {
		t.Fatalf("receiver %d caster %d lacks distinct initial/finish mode1: initial=%d finish=%d", receiverID, casterID, initial, finish)
	}
}

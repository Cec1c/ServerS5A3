package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type partyFrameBatchCharacterRepository struct {
	dnfrepo.CharacterRepository
	loadIDs   []string
	afterLoad func(string, int)
}

func (repository *partyFrameBatchCharacterRepository) Load(
	ctx context.Context,
	characterID string,
) (dnfrepo.CharacterRecord, bool, error) {
	repository.loadIDs = append(repository.loadIDs, characterID)
	record, found, err := repository.CharacterRepository.Load(ctx, characterID)
	if repository.afterLoad != nil {
		repository.afterLoad(characterID, len(repository.loadIDs))
	}
	return record, found, err
}

func (repository *partyFrameBatchCharacterRepository) reset() {
	repository.loadIDs = nil
	repository.afterLoad = nil
}

type partyFrameBatchRosterFixture struct {
	service  *Service
	state    alignedcmd.PartyState
	sessions []*gameSession
	conns    []*bufferConn
	store    *fakeCharacterStore
	counter  *partyFrameBatchCharacterRepository
}

func newPartyFrameBatchRosterFixture(t *testing.T, count int) *partyFrameBatchRosterFixture {
	t.Helper()
	ids := make([]uint16, 0, count)
	for index := 0; index < count; index++ {
		ids = append(ids, uint16(2001+index))
	}
	service, manager, sessionsByID, generations := newPositionRegressionParty(t, ids...)
	repositories := testRepositoryGroup()
	store := repositories.Character.(*fakeCharacterStore)
	for index, id := range ids {
		store.records[strconv.Itoa(int(id))] = dnfrepo.CharacterRecord{
			CharacterID: strconv.Itoa(int(id)),
			AccountID:   "batch-parties",
			Name:        "party_frame_" + strconv.Itoa(index),
			Job:         "1",
			Level:       40 + index,
			Slot:        index,
			Stats:       map[string]int64{"delete_flag": 0},
		}
	}
	counter := &partyFrameBatchCharacterRepository{CharacterRepository: store}
	repositories.Character = counter
	service.repositoryProvider = func() (dnfrepo.Group, bool) { return repositories, true }

	snapshot, found := manager.SnapshotByUser(ids[0], generations[ids[0]])
	if !found {
		t.Fatal("party frame fixture lost its runtime party")
	}
	state := service.publishRuntimePartySnapshot(snapshot)
	sessions := make([]*gameSession, 0, count)
	conns := make([]*bufferConn, 0, count)
	for _, id := range ids {
		session := sessionsByID[id]
		sessions = append(sessions, session)
		conns = append(conns, session.conn.(*bufferConn))
	}
	return &partyFrameBatchRosterFixture{
		service:  service,
		state:    state,
		sessions: sessions,
		conns:    conns,
		store:    store,
		counter:  counter,
	}
}

func (fixture *partyFrameBatchRosterFixture) resetConns() {
	for index, session := range fixture.sessions {
		session.upperSeq = 0
		fixture.conns[index].write.Reset()
	}
}

func (fixture *partyFrameBatchRosterFixture) captureDungeonBaseline(t *testing.T) [][]byte {
	t.Helper()
	fixture.resetConns()
	for _, receiver := range fixture.sessions {
		if err := fixture.service.sendGameUpperRawClassCodec(
			receiver,
			0x0099,
			runtimePartyRealtimeInfoBody(fixture.state),
			0,
			true,
		); err != nil {
			t.Fatalf("baseline op153 for %d: %v", receiver.selectedCharacterID, err)
		}
		for _, actor := range fixture.sessions {
			if _, err := fixture.service.sendCurrentPartyActorFrameProjectionForDungeonPeer(
				receiver,
				actor,
				fixture.state,
				"party_frame_batch_test_baseline",
			); err != nil {
				t.Fatalf("baseline op9 receiver=%d actor=%d: %v",
					receiver.selectedCharacterID, actor.selectedCharacterID, err)
			}
		}
	}
	return fixture.captureConns()
}

func (fixture *partyFrameBatchRosterFixture) captureConns() [][]byte {
	wire := make([][]byte, len(fixture.conns))
	for index, conn := range fixture.conns {
		wire[index] = append([]byte(nil), conn.write.Bytes()...)
	}
	return wire
}

func requirePartyFrameBatchWire(t *testing.T, got, want [][]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("wire receivers=%d want=%d", len(got), len(want))
	}
	for index := range want {
		if !bytes.Equal(got[index], want[index]) {
			t.Fatalf("receiver %d wire changed: got=%x want=%x", index, got[index], want[index])
		}
	}
}

func requirePartyFrameBatchUniqueReads(t *testing.T, fixtures *partyFrameBatchRosterFixture, want int) {
	t.Helper()
	ids := fixtures.counter.loadIDs
	if len(ids) != want {
		t.Fatalf("character repository loads=%d (%v) want one per member=%d", len(ids), ids, want)
	}
	seen := make(map[string]bool, want)
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("character repository reloaded %s within one roster: %v", id, ids)
		}
		seen[id] = true
	}
}

func TestDungeonPartyFrameRosterBatchesCharacterReads(t *testing.T) {
	for _, memberCount := range []int{3, 4} {
		t.Run(strconv.Itoa(memberCount), func(t *testing.T) {
			fixture := newPartyFrameBatchRosterFixture(t, memberCount)
			t.Setenv(dungeonPartyRunKeyTownResetEnv, "off")
			enterDungeonRun(t, fixture.service, fixture.sessions, 7)

			baseline := fixture.captureDungeonBaseline(t)
			fixture.counter.reset()
			fixture.resetConns()
			delivered := fixture.service.sendDungeonPartyFrameRoster(fixture.state, "party_frame_batch_test")
			if delivered != memberCount*memberCount {
				t.Fatalf("delivered=%d want %d", delivered, memberCount*memberCount)
			}
			requirePartyFrameBatchUniqueReads(t, fixture, memberCount)
			requirePartyFrameBatchWire(t, fixture.captureConns(), baseline)
		})
	}
}

func TestTownPartyFrameRosterBatchesCharacterReads(t *testing.T) {
	for _, memberCount := range []int{3, 4} {
		t.Run(strconv.Itoa(memberCount), func(t *testing.T) {
			fixture := newPartyFrameBatchRosterFixture(t, memberCount)
			fixture.counter.reset()
			if err := fixture.service.sendRuntimePartyRosterLocal(
				fixture.sessions[0],
				fixture.state,
				"party_frame_batch_town_test",
			); err != nil {
				t.Fatalf("send town roster: %v", err)
			}
			requirePartyFrameBatchUniqueReads(t, fixture, memberCount)
		})
	}
}

type partyFrameBatchFailConn struct {
	bufferConn
	writes int
	err    error
}

func (conn *partyFrameBatchFailConn) Write(data []byte) (int, error) {
	conn.writes++
	if conn.writes == 1 {
		return conn.bufferConn.Write(data)
	}
	return 0, conn.err
}

func TestDungeonPartyFrameRosterDoesNotReuseBatchAfterFailure(t *testing.T) {
	const memberCount = 3
	fixture := newPartyFrameBatchRosterFixture(t, memberCount)
	t.Setenv(dungeonPartyRunKeyTownResetEnv, "off")
	enterDungeonRun(t, fixture.service, fixture.sessions, 11)
	baseline := fixture.captureDungeonBaseline(t)

	fixture.counter.reset()
	fixture.resetConns()
	failing := &partyFrameBatchFailConn{err: errors.New("roster write failed")}
	fixture.sessions[0].conn = failing
	if delivered := fixture.service.sendDungeonPartyFrameRoster(fixture.state, "party_frame_batch_failure"); delivered != (memberCount-1)*memberCount {
		t.Fatalf("failed receiver delivered=%d want=%d", delivered, (memberCount-1)*memberCount)
	}
	if failing.writes != 1+memberCount {
		t.Fatalf("failed receiver write attempts=%d want op153 plus one failed op9 per member", failing.writes)
	}
	requirePartyFrameBatchUniqueReads(t, fixture, memberCount)

	healthy := &bufferConn{}
	fixture.sessions[0].conn = healthy
	fixture.conns[0] = healthy
	fixture.counter.reset()
	fixture.resetConns()
	if delivered := fixture.service.sendDungeonPartyFrameRoster(fixture.state, "party_frame_batch_retry"); delivered != memberCount*memberCount {
		t.Fatalf("retry delivered=%d want=%d", delivered, memberCount*memberCount)
	}
	requirePartyFrameBatchUniqueReads(t, fixture, memberCount)
	requirePartyFrameBatchWire(t, fixture.captureConns(), baseline)
}

func TestPartyFrameCharacterBatchReloadsAfterBindingGenerationChange(t *testing.T) {
	fixture := newPartyFrameBatchRosterFixture(t, 3)
	session := fixture.sessions[1]
	firstIdentity, ok := fixture.service.boundGameSessionCharacterSnapshot(session)
	if !ok {
		t.Fatal("fixture peer is not bound")
	}
	batch := newPartyFrameCharacterBatch(fixture.service)
	ctx := context.Background()
	_, firstName, _, firstFound := batch.loadCharacter(ctx, firstIdentity)
	if !firstFound {
		t.Fatal("first character load was not found")
	}
	if len(fixture.counter.loadIDs) != 1 {
		t.Fatalf("first load count=%d want 1", len(fixture.counter.loadIDs))
	}

	key := strconv.Itoa(int(session.selectedCharacterID))
	record := fixture.store.records[key]
	record.Name = "generation_two"
	fixture.store.records[key] = record
	fixture.service.mu.Lock()
	fixture.service.allocateGameSessionCharacterGenerationLocked(session, nil)
	fixture.service.mu.Unlock()
	secondIdentity, ok := fixture.service.boundGameSessionCharacterSnapshot(session)
	if !ok || secondIdentity.generation == firstIdentity.generation {
		t.Fatal("fixture did not produce a new binding generation")
	}
	_, secondName, _, secondFound := batch.loadCharacter(ctx, secondIdentity)
	if !secondFound || firstName == secondName || secondName != "generation_two" {
		t.Fatalf("names first=%q second=%q found=%t, want a fresh generation row", firstName, secondName, secondFound)
	}
	if len(fixture.counter.loadIDs) != 2 {
		t.Fatalf("generation change loads=%v want a second repository read", fixture.counter.loadIDs)
	}
	_, _, _, againFound := batch.loadCharacter(ctx, secondIdentity)
	if !againFound || len(fixture.counter.loadIDs) != 2 {
		t.Fatalf("current-generation reuse loads=%v want 2", fixture.counter.loadIDs)
	}
}

func TestPartyFrameBatchDropsProjectionWhenMemberGenerationChangesDuringReads(t *testing.T) {
	const memberCount = 3
	fixture := newPartyFrameBatchRosterFixture(t, memberCount)
	receiver := fixture.sessions[0]
	actor := fixture.sessions[0]
	changingPeer := fixture.sessions[1]
	fixture.counter.reset()
	fixture.counter.afterLoad = func(_ string, count int) {
		if count != memberCount {
			return
		}
		fixture.counter.afterLoad = nil
		fixture.service.mu.Lock()
		fixture.service.allocateGameSessionCharacterGenerationLocked(changingPeer, nil)
		fixture.service.mu.Unlock()
	}
	batch := newPartyFrameCharacterBatch(fixture.service)
	receiver.conn.(*bufferConn).write.Reset()
	sent, err := fixture.service.sendCurrentPartyActorFrameProjectionForDungeonPeer(
		receiver,
		actor,
		fixture.state,
		"party_frame_batch_generation_change",
		batch,
	)
	if err != nil || sent {
		t.Fatalf("changed member projection sent=%t err=%v, want deferred", sent, err)
	}
	if receiver.conn.(*bufferConn).write.Len() != 0 {
		t.Fatalf("stale projection wrote bytes: %x", receiver.conn.(*bufferConn).write.Bytes())
	}
	if len(fixture.counter.loadIDs) != memberCount {
		t.Fatalf("first projection loads=%v want one per member", fixture.counter.loadIDs)
	}

	fixture.counter.reset()
	receiver.conn.(*bufferConn).write.Reset()
	sent, err = fixture.service.sendCurrentPartyActorFrameProjectionForDungeonPeer(
		receiver,
		changingPeer,
		fixture.state,
		"party_frame_batch_generation_retry",
		batch,
	)
	if err != nil || !sent {
		t.Fatalf("current generation projection sent=%t err=%v, want delivery", sent, err)
	}
	if len(fixture.counter.loadIDs) != 1 || fixture.counter.loadIDs[0] != strconv.Itoa(int(changingPeer.selectedCharacterID)) {
		t.Fatalf("generation retry loads=%v want only the changed binding", fixture.counter.loadIDs)
	}
}

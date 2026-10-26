package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/party"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// These tests pin the batch profile sender at the real upper-wire boundary.
// The town fixture supplies the viewer/actor pair and the second viewer is
// joined through the central party manager. Existing production code and older
// cross-scene tests are left unchanged.

type currentCrossSceneProfileBatchFixture struct {
	*townPartyActorHPFixture
	viewer2     *gameSession
	viewer2Conn *bufferConn
}

func newCurrentCrossSceneProfileBatchFixture(t *testing.T) *currentCrossSceneProfileBatchFixture {
	t.Helper()
	base := newTownPartyActorHPFixture(t, true)
	s := base.service
	s.options.currentClientVersion = "official-24.9"
	base.target.accountID = "dnf:1"
	base.actor.accountID = "dnf:2"
	base.target.townActorOwnerChannel = byte(base.target.channel.ID)
	base.actor.townActorOwnerChannel = byte(base.actor.channel.ID)
	base.target.connectionTownActorOwnerChannel = byte(base.target.channel.ID)
	base.actor.connectionTownActorOwnerChannel = byte(base.actor.channel.ID)

	repositories, ok := s.repositoryGroup()
	if !ok {
		t.Fatal("missing repository group")
	}
	if err := repositories.Account.Save(context.Background(), dnfrepo.AccountRecord{AccountID: "dnf:3", State: "active"}); err != nil {
		t.Fatalf("save third account: %v", err)
	}
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "1001",
		AccountID:   "dnf:3",
		Name:        "viewer2",
		Job:         "1",
		Level:       90,
		Stats: map[string]int64{
			"town_id": 38, "area_id": 1, "pos_x": 410, "pos_y": 210,
			"direction": 5, "area_state": 3,
		},
	}); err != nil {
		t.Fatalf("save third character: %v", err)
	}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repositories, true }

	viewer2Conn := &bufferConn{}
	viewer2 := &gameSession{
		conn:                            viewer2Conn,
		connID:                          "cross-scene-profile-batch-viewer-2",
		channel:                         base.target.channel,
		residentChannel:                 base.target.residentChannel,
		accountID:                       "dnf:3",
		selectedCharacterID:             1001,
		townSceneReadyCharacterID:       1001,
		townActorOwnerChannel:           byte(base.target.channel.ID),
		connectionTownActorOwnerChannel: byte(base.target.channel.ID),
	}
	s.bindGameSessionCharacter(viewer2, 1001)

	manager := s.runtimePartyManagerForService()
	snapshot, found := manager.SnapshotByUser(base.target.selectedCharacterID, 0)
	if !found || snapshot.ID == 0 {
		t.Fatal("town party fixture has no central party")
	}
	joined := manager.Join(snapshot.ID, party.RuntimePartyMember{
		UserID:            1001,
		SessionGeneration: viewer2.characterGeneration,
		State:             alignedcmd.PartyMemberState{UserID: 1001, UserState: 1, HPPercent: 100, MPPercent: 100},
	})
	if !joined.OK {
		t.Fatalf("join third viewer to central party: %s", joined.Reason)
	}
	s.publishRuntimePartySnapshot(joined.Party)

	return &currentCrossSceneProfileBatchFixture{
		townPartyActorHPFixture: base,
		viewer2:                 viewer2,
		viewer2Conn:             viewer2Conn,
	}
}

func (f *currentCrossSceneProfileBatchFixture) reset() {
	f.targetConn.write.Reset()
	f.actorConn.write.Reset()
	f.viewer2Conn.write.Reset()
}

func wrapCurrentCrossSceneProfileBatchCharacterLoads(
	t *testing.T,
	service *Service,
	afterLoad func(string),
) map[string]int {
	t.Helper()
	repositories, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("missing repository group")
	}
	source := repositories.Character
	loads := make(map[string]int)
	repositories.Character = crossSceneProfileCharacterReader{
		CharacterRepository: source,
		load: func(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
			loads[id]++
			row, found, err := source.Load(ctx, id)
			if afterLoad != nil {
				afterLoad(id)
			}
			return row, found, err
		},
	}
	service.repositoryProvider = func() (dnfrepo.Group, bool) { return repositories, true }
	return loads
}

func requireCurrentCrossSceneProfileBatchMode8(
	t *testing.T,
	frames []upperFrame,
	actorID uint16,
	owner byte,
) []byte {
	t.Helper()
	if len(frames) != 1 {
		t.Fatalf("cross-scene batch emitted %d frames, want one mode8 profile", len(frames))
	}
	frame := frames[0]
	if frame.msgID != uint16(dnfenum.CmdPacketSetUDPIPPort) || len(frame.body) < currentCrossSceneMode0KeyOffset+2 {
		t.Fatalf("cross-scene batch frame msg=%d body=%x", frame.msgID, frame.body)
	}
	body := frame.body
	if body[currentCrossSceneMode0KeyOffset] == 0 && body[currentCrossSceneMode0KeyOffset+1] == 0 {
		t.Fatalf("cross-scene batch mode0 key is zero: %x", body[:min(len(body), 96)])
	}
	if body[0] != currentCrossSceneMode8Subtype ||
		binary.LittleEndian.Uint16(body[1:3]) != currentCrossSceneMode8Count {
		t.Fatalf("cross-scene batch subtype/count=%d/%d body=%x",
			body[0], binary.LittleEndian.Uint16(body[1:3]), body[:min(len(body), 96)])
	}
	if body[4] != owner {
		t.Fatalf("cross-scene batch owner=%d want connection owner %d", body[4], owner)
	}
	key := binary.LittleEndian.Uint16(body[currentCrossSceneMode0KeyOffset : currentCrossSceneMode0KeyOffset+2])
	if key != actorID {
		t.Fatalf("cross-scene batch mode0 key=%d want actor %d", key, actorID)
	}

	return body
}

func TestCurrentCrossSceneProfileBatchSendsRealMode8Wire(t *testing.T) {
	f := newCurrentCrossSceneProfileBatchFixture(t)
	owner := currentConnectionTownActorOwnerContext(f.target)
	if owner == 0 || owner != byte(f.target.channel.ID) {
		t.Fatalf("fixture connection owner=%d channel=%d", owner, f.target.channel.ID)
	}
	batch := newCurrentCrossSceneProfileBatch(f.service, context.Background())
	if batch == nil {
		t.Fatal("newCurrentCrossSceneProfileBatch returned nil")
	}
	if err := batch.send(f.target, f.actor, owner); err != nil {
		t.Fatalf("send cross-scene batch profile: %v", err)
	}
	body := requireCurrentCrossSceneProfileBatchMode8(
		t,
		splitUpperFrames(t, f.targetConn.write.Bytes()),
		f.actor.selectedCharacterID,
		owner,
	)
	if body[3] != f.service.currentSceneActorRoute() {
		t.Fatalf("cross-scene batch route=%d want %d", body[3], f.service.currentSceneActorRoute())
	}
	// Native mode8 strips each source's subtype/count, retaining the two
	// server/channel prefixes and their complete typed records in order.
	repos, _ := f.service.repositoryGroup()
	character, _, err := repos.Character.Load(context.Background(), "17")
	if err != nil {
		t.Fatal(err)
	}
	mode0, err := f.service.buildCurrentSceneObjectListBodyForSessionInContextStrict(context.Background(), f.actor, 17, character.Name, character, true, owner)
	if err != nil {
		t.Fatal(err)
	}
	mode3 := f.service.buildCurrentSelectedUserInfoMode3BodyInContext(context.Background(), f.actor, repos.LegacyUserInfo, character, true, 17, owner)
	want := append([]byte{8, 1, 0}, mode0[3:]...)
	want = append(want, mode3[3:]...)
	if !bytes.Equal(body, want) {
		t.Fatal("mode8 wire did not retain both complete typed records")
	}
	if f.actorConn.write.Len() != 0 || f.viewer2Conn.write.Len() != 0 {
		t.Fatalf("cross-scene batch wrote to actor=%d or other viewer=%d",
			f.actorConn.write.Len(), f.viewer2Conn.write.Len())
	}
}

func TestCurrentCrossSceneProfileBatchReusesPreparedBodyAndOwners(t *testing.T) {
	f := newCurrentCrossSceneProfileBatchFixture(t)
	loads := wrapCurrentCrossSceneProfileBatchCharacterLoads(t, f.service, nil)
	owner := currentConnectionTownActorOwnerContext(f.target)
	batch := newCurrentCrossSceneProfileBatch(f.service, context.Background())
	if batch == nil {
		t.Fatal("newCurrentCrossSceneProfileBatch returned nil")
	}

	if err := batch.send(f.target, f.actor, owner); err != nil {
		t.Fatalf("send first viewer: %v", err)
	}
	firstBody := requireCurrentCrossSceneProfileBatchMode8(
		t,
		splitUpperFrames(t, f.targetConn.write.Bytes()),
		f.actor.selectedCharacterID,
		owner,
	)
	if loads["29"] != 1 || loads["17"] != 1 {
		t.Fatalf("first viewer owner loads=%v, want viewer and actor once", loads)
	}

	f.reset()
	if err := batch.send(f.viewer2, f.actor, owner); err != nil {
		t.Fatalf("send second viewer: %v", err)
	}
	secondBody := requireCurrentCrossSceneProfileBatchMode8(
		t,
		splitUpperFrames(t, f.viewer2Conn.write.Bytes()),
		f.actor.selectedCharacterID,
		owner,
	)
	if !bytes.Equal(firstBody, secondBody) {
		t.Fatal("same actor/owner batch rebuilt a different typed mode8 body")
	}
	for id, count := range loads {
		if count > 1 {
			t.Fatalf("character %s loaded %d times in one batch", id, count)
		}
	}
	if loads["1001"] != 1 {
		t.Fatalf("second viewer owner loads=%v, want one owner read", loads)
	}
	countLoads := func() int {
		total := 0
		for _, count := range loads {
			total += count
		}
		return total
	}
	totalLoads := countLoads()
	if totalLoads != 3 {
		t.Fatalf("batch owner loads=%v total=%d, want each of three characters once", loads, totalLoads)
	}

	f.reset()
	_ = batch.send(f.target, f.actor, owner)
	_ = batch.send(f.viewer2, f.actor, owner)
	if f.targetConn.write.Len() != 0 || f.viewer2Conn.write.Len() != 0 {
		t.Fatalf("duplicate sends wrote viewer1=%d viewer2=%d",
			f.targetConn.write.Len(), f.viewer2Conn.write.Len())
	}
	if totalLoads = countLoads(); totalLoads != 3 {
		t.Fatalf("duplicate sends changed owner loads=%v total=%d", loads, totalLoads)
	}
}

func TestCurrentCrossSceneProfileBatchRejectsInvalidOwnerIdentityAndMembership(t *testing.T) {
	replaceBinding := func(t *testing.T, f *currentCrossSceneProfileBatchFixture, session *gameSession) {
		t.Helper()
		id := session.selectedCharacterID
		replacement := &gameSession{
			conn:                            &bufferConn{},
			connID:                          "cross-scene-profile-batch-replacement",
			channel:                         session.channel,
			residentChannel:                 session.residentChannel,
			accountID:                       session.accountID,
			selectedCharacterID:             id,
			townSceneReadyCharacterID:       id,
			townActorOwnerChannel:           session.townActorOwnerChannel,
			connectionTownActorOwnerChannel: session.connectionTownActorOwnerChannel,
		}
		f.service.mu.Lock()
		f.service.gameSessions[id] = replacement
		f.service.mu.Unlock()
	}
	leaveCentral := func(t *testing.T, f *currentCrossSceneProfileBatchFixture, id uint16) {
		t.Helper()
		// Keep both local session caches claiming the original party. The
		// central manager is the only authority for cross-scene membership.
		storeRuntimePartyState(f.target, townPartyActorHPPartyState())
		storeRuntimePartyState(f.actor, townPartyActorHPPartyState())
		if result := f.service.runtimePartyManagerForService().LeaveCurrent(id); !result.OK {
			t.Fatalf("leave central party %d: %s", id, result.Reason)
		}
	}

	tests := []struct {
		name   string
		mutate func(*testing.T, *currentCrossSceneProfileBatchFixture)
		owner  func(*currentCrossSceneProfileBatchFixture) byte
	}{
		{
			name: "legacy_profile",
			mutate: func(_ *testing.T, f *currentCrossSceneProfileBatchFixture) {
				f.service.options.currentClientVersion = "90cn-decode-bypass-v1"
			},
		},
		{
			name:  "zero_owner",
			owner: func(*currentCrossSceneProfileBatchFixture) byte { return 0 },
		},
		{
			name: "wrong_owner",
			owner: func(f *currentCrossSceneProfileBatchFixture) byte {
				return currentConnectionTownActorOwnerContext(f.target) + 1
			},
		},
		{
			name: "viewer_left",
			mutate: func(t *testing.T, f *currentCrossSceneProfileBatchFixture) {
				leaveCentral(t, f, f.target.selectedCharacterID)
			},
		},
		{
			name: "actor_left",
			mutate: func(t *testing.T, f *currentCrossSceneProfileBatchFixture) {
				leaveCentral(t, f, f.actor.selectedCharacterID)
			},
		},
		{
			name: "viewer_account",
			mutate: func(_ *testing.T, f *currentCrossSceneProfileBatchFixture) {
				f.target.accountID = "dnf:foreign"
			},
		},
		{
			name: "actor_account",
			mutate: func(_ *testing.T, f *currentCrossSceneProfileBatchFixture) {
				f.actor.accountID = "dnf:foreign"
			},
		},
		{
			name: "viewer_channel",
			mutate: func(_ *testing.T, f *currentCrossSceneProfileBatchFixture) {
				f.target.channel.ID++
			},
		},
		{
			name: "actor_channel",
			mutate: func(_ *testing.T, f *currentCrossSceneProfileBatchFixture) {
				f.actor.channel.ID++
			},
		},
		{
			name: "viewer_binding",
			mutate: func(t *testing.T, f *currentCrossSceneProfileBatchFixture) {
				replaceBinding(t, f, f.target)
			},
		},
		{
			name: "actor_binding",
			mutate: func(t *testing.T, f *currentCrossSceneProfileBatchFixture) {
				replaceBinding(t, f, f.actor)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newCurrentCrossSceneProfileBatchFixture(t)
			if test.mutate != nil {
				test.mutate(t, f)
			}
			owner := currentConnectionTownActorOwnerContext(f.target)
			if test.owner != nil {
				owner = test.owner(f)
			}
			f.reset()
			batch := newCurrentCrossSceneProfileBatch(f.service, context.Background())
			if batch == nil {
				t.Fatal("newCurrentCrossSceneProfileBatch returned nil")
			}
			_ = batch.send(f.target, f.actor, owner)
			if f.targetConn.write.Len() != 0 || f.actorConn.write.Len() != 0 || f.viewer2Conn.write.Len() != 0 {
				t.Fatalf("invalid batch wrote viewer=%d actor=%d other=%d",
					f.targetConn.write.Len(), f.actorConn.write.Len(), f.viewer2Conn.write.Len())
			}
		})
	}
}

func TestCurrentCrossSceneProfileBatchInvalidatesPreparedAppearance(t *testing.T) {
	f := newCurrentCrossSceneProfileBatchFixture(t)
	owner := currentConnectionTownActorOwnerContext(f.target)
	batch := newCurrentCrossSceneProfileBatch(f.service, context.Background())
	if batch == nil {
		t.Fatal("newCurrentCrossSceneProfileBatch returned nil")
	}
	if err := batch.send(f.target, f.actor, owner); err != nil {
		t.Fatalf("warm appearance batch: %v", err)
	}
	requireCurrentCrossSceneProfileBatchMode8(
		t,
		splitUpperFrames(t, f.targetConn.write.Bytes()),
		f.actor.selectedCharacterID,
		owner,
	)
	f.reset()

	invalidateCurrentCrossSceneAppearance(f.actor)
	_ = batch.send(f.viewer2, f.actor, owner)
	if f.viewer2Conn.write.Len() != 0 || f.actorConn.write.Len() != 0 {
		t.Fatalf("stale appearance body wrote viewer=%d actor=%d",
			f.viewer2Conn.write.Len(), f.actorConn.write.Len())
	}

	fresh := newCurrentCrossSceneProfileBatch(f.service, context.Background())
	if fresh == nil {
		t.Fatal("newCurrentCrossSceneProfileBatch returned nil for refreshed appearance")
	}
	if err := fresh.send(f.viewer2, f.actor, owner); err != nil {
		t.Fatalf("send refreshed appearance batch: %v", err)
	}
	requireCurrentCrossSceneProfileBatchMode8(
		t,
		splitUpperFrames(t, f.viewer2Conn.write.Bytes()),
		f.actor.selectedCharacterID,
		owner,
	)
}

func TestCurrentCrossSceneProfileBatchRejectsMembershipChangeDuringPrepare(t *testing.T) {
	f := newCurrentCrossSceneProfileBatchFixture(t)
	owner := currentConnectionTownActorOwnerContext(f.target)
	changed := false
	leaveOK := false
	wrapCurrentCrossSceneProfileBatchCharacterLoads(t, f.service, func(id string) {
		if id != "1001" || changed {
			return
		}
		changed = true
		leaveOK = f.service.runtimePartyManagerForService().LeaveCurrent(1001).OK
	})
	batch := newCurrentCrossSceneProfileBatch(f.service, context.Background())
	if batch == nil {
		t.Fatal("newCurrentCrossSceneProfileBatch returned nil")
	}
	if err := batch.send(f.target, f.actor, owner); err != nil {
		t.Fatalf("warm batch body: %v", err)
	}
	requireCurrentCrossSceneProfileBatchMode8(
		t,
		splitUpperFrames(t, f.targetConn.write.Bytes()),
		f.actor.selectedCharacterID,
		owner,
	)
	f.reset()

	_ = batch.send(f.viewer2, f.actor, owner)
	if !changed {
		t.Fatal("second viewer preparation did not reach its ownership read")
	}
	if !leaveOK {
		t.Fatal("fixture failed to remove the second viewer from the central party")
	}
	if f.viewer2Conn.write.Len() != 0 || f.actorConn.write.Len() != 0 {
		t.Fatalf("membership change during prepare wrote viewer=%d actor=%d",
			f.viewer2Conn.write.Len(), f.actorConn.write.Len())
	}
}

func TestCurrentCrossSceneProfileBatchUsesCentralRaidMembership(t *testing.T) {
	s, viewer, actor, live := newCrossSceneProfileRaidTest(t)
	s.options.currentClientVersion = "official-24.9"
	viewer.connectionTownActorOwnerChannel = byte(viewer.channel.ID)
	actor.connectionTownActorOwnerChannel = byte(actor.channel.ID)
	s.raidMu.Lock()
	live.Battle = &runtimeRaidBattle{
		ChannelID:   viewer.channel.ID,
		ChannelType: viewer.channel.Type,
		MemberAccounts: map[uint16]string{
			viewer.selectedCharacterID: viewer.accountID,
			actor.selectedCharacterID:  actor.accountID,
		},
	}
	s.raidMu.Unlock()
	storeRuntimePartyState(viewer, alignedcmd.PartyState{})
	storeRuntimePartyState(actor, alignedcmd.PartyState{})
	owner := currentConnectionTownActorOwnerContext(viewer)
	viewerConn := viewer.conn.(*bufferConn)
	actorConn := actor.conn.(*bufferConn)
	viewerConn.write.Reset()
	actorConn.write.Reset()

	batch := newCurrentCrossSceneProfileBatch(s, context.Background())
	if batch == nil {
		t.Fatal("newCurrentCrossSceneProfileBatch returned nil")
	}
	if err := batch.send(viewer, actor, owner); err != nil {
		t.Fatalf("send central raid profile: %v", err)
	}
	requireCurrentCrossSceneProfileBatchMode8(
		t,
		splitUpperFrames(t, viewerConn.write.Bytes()),
		actor.selectedCharacterID,
		owner,
	)
	if actorConn.write.Len() != 0 {
		t.Fatalf("central raid batch wrote to the inspected actor: %d", actorConn.write.Len())
	}
}

type currentCrossSceneProfileBatchFailConn struct {
	bufferConn
	writes int
}

func (c *currentCrossSceneProfileBatchFailConn) Write([]byte) (int, error) {
	c.writes++
	return 0, io.ErrClosedPipe
}

func TestCurrentCrossSceneProfileBatchReturnsWriteError(t *testing.T) {
	f := newCurrentCrossSceneProfileBatchFixture(t)
	conn := &currentCrossSceneProfileBatchFailConn{}
	f.target.conn = conn
	owner := currentConnectionTownActorOwnerContext(f.target)
	batch := newCurrentCrossSceneProfileBatch(f.service, context.Background())
	if batch == nil {
		t.Fatal("newCurrentCrossSceneProfileBatch returned nil")
	}
	err := batch.send(f.target, f.actor, owner)
	if !errors.Is(err, io.ErrClosedPipe) || conn.writes != 1 {
		t.Fatalf("cross-scene batch write error=%v writes=%d, want ErrClosedPipe once", err, conn.writes)
	}
	if conn.write.Len() != 0 || f.actorConn.write.Len() != 0 || f.viewer2Conn.write.Len() != 0 {
		t.Fatalf("failed batch wrote bytes: target=%d actor=%d other=%d",
			conn.write.Len(), f.actorConn.write.Len(), f.viewer2Conn.write.Len())
	}
}

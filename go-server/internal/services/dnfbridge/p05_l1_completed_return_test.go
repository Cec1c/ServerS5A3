package dnfbridge

import (
	"bytes"
	"context"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Scheduling fixture only: pause the parent's Deadline query made by the real
// sendFrames WithTimeout, AFTER batch.send has prepared the actor template.
// Other Deadline calls (including repository budgets) are delegated normally.
// No appearance/wire lock, cache warm-up, third viewer, or b.sent mutation.
type p05L1V2DeadlineGate struct {
	context.Context
	once    sync.Once
	entered chan p05L1V2Boundary
	release chan struct{}
}

type p05L1V2Boundary struct {
	at    time.Time
	stack []string
}

func (g *p05L1V2DeadlineGate) Deadline() (time.Time, bool) {
	var pcs [32]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	var stack []string
	var inFrames, inBatch bool
	for {
		frame, more := frames.Next()
		stack = append(stack, frame.Function)
		inFrames = inFrames || strings.HasSuffix(frame.Function, ".(*Service).sendCurrentCrossSceneProfileFramesWithBudget")
		inBatch = inBatch || strings.HasSuffix(frame.Function, ".(*currentCrossSceneProfileBatch).send")
		if !more {
			break
		}
	}
	if inFrames && inBatch {
		g.once.Do(func() {
			g.entered <- p05L1V2Boundary{at: time.Now(), stack: stack}
			select {
			case <-g.release:
			case <-g.Context.Done():
			}
		})
	}
	// Keep the actual parent's deadline, cancellation and values unchanged.
	// WithTimeout computed its own 3s deadline BEFORE querying us; the pause
	// therefore consumes that production budget, rather than restarting it.
	return g.Context.Deadline()
}

// Same completed-return transport entry as completionReturnFixture's
// completeRunAndReturnToTown. Unlike pending-confirmed return, this does not
// consume the later selected-actor appearance tail. No runtime=nil assignment,
// guard bypass, or claim of complete settlement/UI dispatch is made here.
func p05L1V2RunCompletedReturn(t *testing.T, s *Service, session *gameSession, state *runtimeDungeonState) {
	t.Helper()
	if err := state.Session.CompleteCurrentRoom(); err != nil {
		t.Fatalf("fixture complete room: %v", err)
	}
	transition, err := s.prepareCurrentDungeonTownTransitionForSession(context.Background(), session, session.selectedCharacterID)
	if err != nil {
		t.Fatalf("prepare completed town transition: %v", err)
	}
	if err := s.sendCurrentCompletedDungeonReturnToTown(session, state, transition,
		uint16(dnfenum.CmdPacketEplpCommand), "p05_l1_v2_completed_town_route"); err != nil {
		t.Fatalf("real completed town return: %v", err)
	}
	if session.dungeon.runtime != nil || !state.townReturnOp24Sent ||
		state.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted ||
		!currentPartyTownReturnSceneReady(session) {
		t.Fatalf("completed return incomplete: runtime_detached=%t op24_sent=%t status=%s town_ready=%t",
			session.dungeon.runtime == nil, state.townReturnOp24Sent,
			state.Session.Snapshot().Run.Status, currentPartyTownReturnSceneReady(session))
	}
}

func TestP05L1CompletedReturnLateMode8MustPreserveTownHPV2(t *testing.T) {
	for _, viewerReturnsLast := range []bool{false, true} {
		name := "actor_returns_last"
		if viewerReturnsLast {
			name = "viewer_returns_last"
		}
		t.Run(name, func(t *testing.T) { runP05L1CompletedReturnV2(t, false, viewerReturnsLast) })
	}
}

func TestP05L1CompletedReturnAppearanceChangeRejectsLateMode8V2(t *testing.T) {
	runP05L1CompletedReturnV2(t, true, false)
}

func runP05L1CompletedReturnV2(t *testing.T, invalidateActor, viewerReturnsLast bool) {
	t.Helper()
	// This is the existing keep mode, not a different party policy or new join.
	t.Setenv(dungeonPartyCompletionReturnEnv, dungeonPartyCompletionReturnValueKeep)
	t.Setenv(dungeonPartyCompletionReturnTownCopresenceEnv, "on")
	f := newCompletionReturnPartyFixture(t) // exactly two centrally bound members
	s, viewer, actor := f.service, f.leader, f.member
	s.options.currentClientVersion = "official-24.9"
	repos, ok := s.repositoryGroup()
	if !ok {
		t.Fatal("missing existing memory repositories")
	}
	// The return fixture normally does not need names/session accounts; the
	// strict batch sender does. Keep its repository identities and generations.
	for _, session := range []*gameSession{viewer, actor} {
		key := strconv.Itoa(int(session.selectedCharacterID))
		character, found, err := repos.Character.Load(context.Background(), key)
		if err != nil || !found {
			t.Fatalf("load fixture character %s: found=%t err=%v", key, found, err)
		}
		character.Name = "p05-l1-" + key
		if err := repos.Character.Save(context.Background(), character); err != nil {
			t.Fatal(err)
		}
		session.accountID = character.AccountID
		session.connectionTownActorOwnerChannel = byte(session.channel.ID)
		session.dungeon.runtime.Character = character
	}
	// Town peer traffic is delivered through each recipient's real events
	// queue. Start both owners so the completed-return fan-out can run before
	// this fixture snapshots its buffers or inspects the receiver wire.
	startTownPeerTest(t, s, viewer, nil)
	startTownPeerTest(t, s, actor, nil)
	first, firstRuntime := viewer, f.leaderRuntime
	last, lastRuntime := actor, f.memberRuntime
	if viewerReturnsLast {
		first, firstRuntime = actor, f.memberRuntime
		last, lastRuntime = viewer, f.leaderRuntime
	}
	p05L1V2RunCompletedReturn(t, s, first, firstRuntime)
	drainTownPeerTest(t, s, viewer)
	drainTownPeerTest(t, s, actor)
	if !s.sessionHasActiveDungeonRuntime(last) || s.sessionHasActiveDungeonRuntime(first) {
		t.Fatal("fixture needs the same party with exactly one member already returned")
	}
	if len(s.currentTownPartyRealtimeBody(viewer, actor)) != 0 {
		t.Fatal("fixture must exercise the active-runtime no-projection branch")
	}
	partyBefore, found := s.runtimePartyManagerForService().SnapshotByUser(viewer.selectedCharacterID, 0)
	if !found || len(partyBefore.Members) != 2 || partyBefore.ID == 0 {
		t.Fatal("fixture needs exactly the original two-member central party")
	}
	viewerBefore, vok := s.boundGameSessionCharacterSnapshot(viewer)
	actorBefore, aok := s.boundGameSessionCharacterSnapshot(actor)
	if !vok || !aok || !s.currentCrossSceneProfileMembership(viewer, actor, viewer.selectedCharacterID, actor.selectedCharacterID) {
		t.Fatal("strict profile membership/bindings are not valid")
	}
	owner := currentConnectionTownActorOwnerContext(viewer)
	revision := currentCrossSceneAppearanceRevision(actor)
	f.leaderConn.write.Reset()
	f.memberConn.write.Reset()

	parent, cancel := context.WithTimeout(context.Background(), 2*createWriteTimeout)
	gate := &p05L1V2DeadlineGate{Context: parent, entered: make(chan p05L1V2Boundary, 1), release: make(chan struct{})}
	batch := newCurrentCrossSceneProfileBatch(s, gate)
	done := make(chan error, 1)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate.release) }) }
	joined := false
	t.Cleanup(func() {
		cancel()
		release()
		if !joined {
			select {
			case <-done:
			case <-time.After(createWriteTimeout):
				t.Error("fixture cleanup: profile sender failed to terminate")
			}
		}
	})
	go func() { done <- batch.send(viewer, actor, owner) }()
	var boundary p05L1V2Boundary
	select {
	case boundary = <-gate.entered:
	case err := <-done:
		joined = true
		t.Fatalf("fixture never reached real post-projection sendFrames boundary: %v", err)
	case <-parent.Done():
		t.Fatal("fixture timeout before post-projection boundary; no L1 result")
	}
	t.Logf("schedule=post-projection/pre-lock parent-Deadline stack=%q", boundary.stack)
	// Channel handoff makes these reads safe: the sender is paused inside
	// Deadline and neither holds a lock nor writes the batch/maps/connection.
	key := currentCrossSceneProfileKey{actor: actor.selectedCharacterID, owner: owner}
	template, ready := batch.bodies[key]
	if !ready || len(batch.bodies) != 1 || len(batch.sent) != 0 || f.leaderConn.write.Len() != 0 {
		t.Fatal("fixture did not pause the first, unsent two-member profile")
	}
	if template.data[townMode0FixtureHPOffset(t, template.data)] != 0 {
		t.Fatal("fixture original mode8 template is not the zero-HP baseline")
	}

	p05L1V2RunCompletedReturn(t, s, last, lastRuntime)
	drainTownPeerTest(t, s, viewer)
	drainTownPeerTest(t, s, actor)
	partyAfter, found := s.runtimePartyManagerForService().SnapshotByUser(viewer.selectedCharacterID, 0)
	revisionAfter := currentCrossSceneAppearanceRevision(actor)
	ownerAfter := currentConnectionTownActorOwnerContext(viewer)
	routeAfter := s.currentSceneActorRoute()
	v, a := batch.peers[viewerBefore.character], batch.peers[actorBefore.character]
	if v == nil || a == nil {
		t.Fatal("prepared batch lost its captured identities")
	}
	viewerBound := s.boundGameSessionCharacterCurrent(viewerBefore)
	actorBound := s.boundGameSessionCharacterCurrent(actorBefore)
	membership := s.currentCrossSceneProfileMembership(viewer, actor, viewer.selectedCharacterID, actor.selectedCharacterID)
	t.Logf("guards revision=%d->%d owner=%d->%d route=%d->%d party_found=%t party=%d->%d member_count=%d->%d membership=%t",
		revision, revisionAfter, owner, ownerAfter, batch.route, routeAfter, found,
		partyBefore.ID, partyAfter.ID, len(partyBefore.Members), len(partyAfter.Members), membership)
	t.Logf("guards viewer character=%d->%d generation=%d->%d account=%q->%q channel=%d->%d canonical=%t captured_peer_current=%t",
		viewerBefore.character, viewer.selectedCharacterID, viewerBefore.generation, viewer.characterGeneration,
		v.account, viewer.accountID, v.channel, currentTownPresenceChannelID(viewer), viewerBound, v.current(s))
	t.Logf("guards actor character=%d->%d generation=%d->%d account=%q->%q channel=%d->%d canonical=%t captured_peer_current=%t",
		actorBefore.character, actor.selectedCharacterID, actorBefore.generation, actor.characterGeneration,
		a.account, actor.accountID, a.channel, currentTownPresenceChannelID(actor), actorBound, a.current(s))
	for _, member := range partyAfter.Members {
		t.Logf("guards retained_party_member uid=%d generation=%d", member.UserID, member.SessionGeneration)
	}
	if !found || partyAfter.ID != partyBefore.ID || len(partyAfter.Members) != 2 ||
		!viewerBound || !actorBound || !membership || !v.current(s) || !a.current(s) ||
		revisionAfter != revision || ownerAfter != owner || routeAfter != batch.route {
		t.Fatal("completed return changed a captured guard; not the L1 interleaving")
	}
	if s.sessionHasActiveDungeonRuntime(viewer) || s.sessionHasActiveDungeonRuntime(actor) {
		t.Fatal("normal town HP would still be ineligible")
	}
	// Require the completed-return path ITSELF to have sent the normal HP.
	// No extra HP send or injected body is used to manufacture the ordering.
	var wantHP byte
	hpFrames := 0
	for index, rest := 0, f.leaderConn.write.Bytes(); len(rest) != 0; index++ {
		packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
		if packet.Header.Classification == 0 && packet.Header.MsgID == 0x0099 {
			for _, row := range parseRealtimeInfoRows(t, packet.Body) {
				if row.uid == actor.selectedCharacterID {
					hpFrames++
					wantHP = row.hp
					t.Logf("completed-return wire[%d] op153 actor=%d hp=%d", index, row.uid, row.hp)
					if row.hp != 100 {
						t.Fatalf("completed return HP=%d want fixture central state 100", row.hp)
					}
				}
			}
		}
		rest = tail
	}
	if hpFrames == 0 || wantHP != 100 {
		t.Fatal("completed-return path did not actually send actor HP=100 to the same viewer")
	}
	if invalidateActor {
		// Negative control: the real appearance invalidator must reject the
		// old prepared revision. Never assign the revision or bypass callback.
		invalidateCurrentCrossSceneAppearance(actor)
		got := currentCrossSceneAppearanceRevision(actor)
		t.Logf("negative-control actor revision=%d->%d", revisionAfter, got)
		if got != revisionAfter+1 {
			t.Fatalf("appearance invalidator revision=%d want=%d", got, revisionAfter+1)
		}
	}
	prefix := append([]byte(nil), f.leaderConn.write.Bytes()...)
	if elapsed := time.Since(boundary.at); elapsed >= createWriteTimeout {
		t.Fatalf("fixture spent %s inside the original %s send budget; no L1 result", elapsed, createWriteTimeout)
	}
	t.Logf("schedule=completed-return,own-town-HP-written actor=%d hp=%d elapsed=%s", actor.selectedCharacterID, wantHP, time.Since(boundary.at))
	release()
	select {
	case err := <-done:
		joined = true
		if err != nil {
			t.Fatalf("old profile send error (including budget expiry is NOT a passing guard): %v", err)
		}
	case <-parent.Done():
		t.Fatal("fixture timeout joining old profile sender; no L1 result")
	}

	wire := append([]byte(nil), f.leaderConn.write.Bytes()...)
	if !bytes.HasPrefix(wire, prefix) {
		t.Fatal("receiver stream changed before the release boundary")
	}
	// Record the whole receiver stream, not just the last helper's body.
	for index, rest := 0, wire; len(rest) != 0; index++ {
		packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
		t.Logf("wire[%d] class=%d op=%d body=%x", index, packet.Header.Classification, packet.Header.MsgID, packet.Body)
		rest = tail
	}
	if invalidateActor {
		if !bytes.Equal(wire, prefix) || len(batch.sent) != 0 {
			t.Fatal("negative control: changed actor revision still delivered old mode8")
		}
		t.Log("negative-control=revision-rejected; no appended frame, no send error/timeout")
		return
	}
	if len(wire) == len(prefix) {
		t.Fatal("unchanged guard case needs the actual late mode8, not a silently dropped frame")
	}
	late, trailing := splitCurrentGameServerUpperPacketAuto(t, wire[len(prefix):])
	if late.Header.Classification != 0 || len(trailing) != 0 {
		t.Fatal("expected exactly one class0 late profile frame")
	}
	body := requireCurrentCrossSceneProfileBatchMode8(t, splitUpperFrames(t, wire[len(prefix):]), actor.selectedCharacterID, owner)
	gotHP := body[townMode0FixtureHPOffset(t, body)]
	t.Logf("order=town-op153(hp=%d)->late-mode8(hp=%d), same receiver/actor/owner", wantHP, gotHP)
	if gotHP == 0 || gotHP != wantHP {
		t.Fatalf("L1 completed-return: real late mode8 HP=%d overwrites prior valid town HP=%d", gotHP, wantHP)
	}
}

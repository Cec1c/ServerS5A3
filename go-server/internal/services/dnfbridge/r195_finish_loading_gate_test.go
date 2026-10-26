package dnfbridge

import "testing"

// The three finish-loading gates are read from another member's event loop
// (landing resend), so every write and that cross-loop read must share the
// per-session lock. A consistent snapshot is what makes "scene stable" mean the
// same thing on both sides.
func TestR195FinishLoadingGateSnapshotAndReset(t *testing.T) {
	session := &gameSession{}
	if gates := session.finishLoadingGateSnapshot(); gates != (finishLoadingGates{}) {
		t.Fatalf("fresh gates=%+v", gates)
	}
	session.setFinishLoadingGateState(true)
	session.setFinishLoadingGateCompletion(true)
	if peerSceneStableForPeerActorLandingResend(session) {
		t.Fatal("two of three gates must not count as a stable scene")
	}
	session.setFinishLoadingGatePostFinish(true)
	if !peerSceneStableForPeerActorLandingResend(session) {
		t.Fatal("all three gates must count as a stable scene")
	}
	session.setFinishLoadingGates(finishLoadingGates{})
	if gates := session.finishLoadingGateSnapshot(); gates != (finishLoadingGates{}) {
		t.Fatalf("reset gates=%+v", gates)
	}
	if peerSceneStableForPeerActorLandingResend(session) {
		t.Fatal("scene stayed stable after the generation reset")
	}
}

// The writer follows the real order (state -> completion -> post-finish, then
// the reverse reset). Any snapshot outside those legal prefixes would mean the
// reader observed a torn triplet, which is exactly what the lock prevents.
func TestR195FinishLoadingGateSnapshotHasNoImpossibleCombination(t *testing.T) {
	session := &gameSession{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for n := 0; n < 2000; n++ {
			session.setFinishLoadingGateState(true)
			session.setFinishLoadingGateCompletion(true)
			session.setFinishLoadingGatePostFinish(true)
			session.setFinishLoadingGatePostFinish(false)
			session.setFinishLoadingGateCompletion(false)
			session.setFinishLoadingGateState(false)
		}
	}()
	legal := map[finishLoadingGates]bool{
		{}:                              true,
		{State: true}:                   true,
		{State: true, Completion: true}: true,
		{State: true, Completion: true, PostFinish: true}: true,
		{Completion: true, PostFinish: true}:              true,
		{PostFinish: true}:                                true,
	}
	for {
		gates := session.finishLoadingGateSnapshot()
		if !legal[gates] {
			t.Fatalf("impossible gate combination observed: %+v", gates)
		}
		select {
		case <-done:
			return
		default:
		}
	}
}

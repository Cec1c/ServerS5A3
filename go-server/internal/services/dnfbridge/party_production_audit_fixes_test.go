package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestOrdinaryPartyNativeAdmissionRetriesTransientNonZeroPCR1(t *testing.T) {
	f := newInviteAcceptDungeonFixture(t)
	s := f.service
	s.options.currentClientVersion = "official-24.9"
	t.Setenv("DNFBRIDGE_PARTY_NATIVE_ADMISSION", "")
	f.formPartyThroughClientAccept(t)

	if err := s.handleDungeonSelectUpper(f.leader, nativeAdmissionBody()); err != nil {
		t.Fatal(err)
	}
	s.partyNativeAdmission.mu.Lock()
	p := s.partyNativeAdmission.bySession[f.leader]
	s.partyNativeAdmission.mu.Unlock()
	if p == nil {
		t.Fatal("ordinary op16 did not start native connection admission")
	}
	if !p.retryUntilReady {
		t.Fatal("ordinary op16 native connection admission did not enable multi-round retry")
	}

	// Round 1: follower reports transient non-zero PCR1.
	reply1, nonce1, ok := nativeAdmissionActiveRoundReplyForTest(s, p, f.follower, 3)
	if !ok {
		t.Fatal("missing active round 1")
	}
	if err := s.handlePartyNativeConnectionReply(f.follower, reply1); err != nil {
		t.Fatal(err)
	}

	// Wait for round 2 with a distinct nonce.
	deadline := time.Now().Add(2 * time.Second)
	var nonce2 uint64
	for time.Now().Before(deadline) {
		_, curNonce, active := nativeAdmissionActiveRoundReplyForTest(s, p, f.leader, 0)
		if active && curNonce != nonce1 {
			nonce2 = curNonce
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if nonce2 == 0 {
		t.Fatal("ordinary op16 did not rotate to round 2 after transient non-zero PCR1")
	}

	// Round 2: both peers report ready (status=0); dungeon entry must succeed.
	for _, peer := range p.peers {
		reply, _, active := nativeAdmissionActiveRoundReplyForTest(s, p, peer.session, 0)
		if !active {
			t.Fatalf("round 2 disappeared before replying for char %d", peer.character)
		}
		if err := s.handlePartyNativeConnectionReply(peer.session, reply); err != nil {
			t.Fatal(err)
		}
	}
	waitNativePartyAdmissionForTest(t, s, p)

	for _, session := range []*gameSession{f.leader, f.follower} {
		session.dungeon.mu.Lock()
		entered := session.dungeon.runtime != nil
		session.dungeon.mu.Unlock()
		if !entered {
			t.Fatalf("char %d did not enter dungeon after round-2 native readiness", session.selectedCharacterID)
		}
	}
}

func TestPartyNativeConnectionErrorKindClassification(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{errors.New("client native roster or UDP connection is not ready"), "peer_not_ready"},
		{errors.New("client native connection query timed out"), "query_timeout"},
		{context.DeadlineExceeded, "query_timeout"},
		{errors.New("party changed before native query"), "party_changed"},
		{errors.New("connection query member offline"), "member_offline"},
		{errors.New("native party admission busy"), "busy"},
		{errors.New("native party connection proof expired or changed"), "party_changed"},
		{errors.New("proof expired"), "proof_expired"},
		{errors.New("unexpected failure"), "other"},
	}
	for _, tc := range cases {
		got := partyNativeConnectionErrorKind(tc.err)
		if got != tc.want {
			t.Fatalf("partyNativeConnectionErrorKind(%q) = %q, want %q", tc.err, got, tc.want)
		}
		if !townSelectorDiagnosticFieldAllowed("error_kind", got) {
			t.Fatalf("error_kind %q rejected by townSelectorDiagnosticFieldAllowed", got)
		}
	}
}

func TestCrossSceneProfileBatchTimeoutScaling(t *testing.T) {
	if got := crossSceneProfileBatchTimeout(1); got != createWriteTimeout {
		t.Fatalf("timeout(1) = %v, want %v", got, createWriteTimeout)
	}
	if got := crossSceneProfileBatchTimeout(4); got != 5*time.Second {
		t.Fatalf("timeout(4) = %v, want 5s", got)
	}
	if got := crossSceneProfileBatchTimeout(16); got != 11*time.Second {
		t.Fatalf("timeout(16) = %v, want 11s", got)
	}
	if got := crossSceneProfileBatchTimeout(20); got != 13*time.Second {
		t.Fatalf("timeout(20) = %v, want 13s", got)
	}
	if got := crossSceneProfileBatchTimeout(40); got != crossSceneProfileBatchMaxTimeout {
		t.Fatalf("timeout(40) = %v, want cap %v", got, crossSceneProfileBatchMaxTimeout)
	}
}

func TestPartyLoadingPreparationWatchdogDelayAndSlowThreshold(t *testing.T) {
	now := time.Now()
	// op37 took 9.5s, preparation just started: watchdog must wait ~5s for
	// preparation rather than firing in 1s and false-warning all members.
	cycle := &partyDungeonLoadingCycle{
		started:        now.Add(-9500 * time.Millisecond),
		prepareStarted: now,
	}
	delay := partyLoadingPreparationWatchdogDelay(cycle)
	if delay < 4*time.Second || delay > 5*time.Second+50*time.Millisecond {
		t.Fatalf("preparation watchdog delay after slow op37 = %v, want ~5s", delay)
	}

	// Near the 25s overall cycle deadline, watchdog must still wake in time.
	nearDeadline := &partyDungeonLoadingCycle{
		started:        now.Add(-23500 * time.Millisecond),
		prepareStarted: now,
	}
	nearDelay := partyLoadingPreparationWatchdogDelay(nearDeadline)
	if nearDelay > 2*time.Second {
		t.Fatalf("preparation watchdog delay near 25s deadline = %v, want <=2s", nearDelay)
	}
}

func TestPartyUDPRelayAdvertisedPortUpdatePreservesLiveLegs(t *testing.T) {
	relay, err := newCurrentPartyUDPRelay(true, "127.0.0.1", testCurrentPartyUDPRelayPortStart(t), 16, nil)
	if err != nil {
		t.Fatalf("new UDP relay: %v", err)
	}
	t.Cleanup(func() { _ = relay.Close() })

	bindings1 := []currentPartyUDPRelayBinding{
		{characterID: 101, generation: 1, address: [4]byte{127, 0, 0, 1}, advertisedPort: 31001},
		{characterID: 202, generation: 1, address: [4]byte{127, 0, 0, 1}, advertisedPort: 31002},
	}
	if !relay.Sync(77, bindings1) {
		t.Fatal("initial relay sync failed")
	}
	lookupPair := func(partyID, a, b uint16) (*currentPartyUDPRelayLeg, *currentPartyUDPRelayLeg) {
		t.Helper()
		relay.mu.Lock()
		ab := relay.legs[currentPartyUDPRelayLegKey{partyID: partyID, ownerID: a, peerID: b}]
		ba := relay.legs[currentPartyUDPRelayLegKey{partyID: partyID, ownerID: b, peerID: a}]
		relay.mu.Unlock()
		if ab == nil || ba == nil {
			t.Fatalf("missing directed legs for party %d: AB=%v BA=%v", partyID, ab != nil, ba != nil)
		}
		return ab, ba
	}
	abBefore, baBefore := lookupPair(77, 101, 202)
	_, abPortBefore, _ := relay.Endpoint(77, 101, 202)
	_, baPortBefore, _ := relay.Endpoint(77, 202, 101)

	// Open two local UDP sockets and exchange datagrams so both legs learn endpoints.
	connA, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer connA.Close()
	connB, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer connB.Close()

	dstAB := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(abPortBefore)}
	dstBA := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(baPortBefore)}
	if _, err := connA.WriteToUDP([]byte("hello-from-A"), dstAB); err != nil {
		t.Fatal(err)
	}
	if _, err := connB.WriteToUDP([]byte("hello-from-B"), dstBA); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	_ = connA.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := connA.ReadFromUDP(buf); err != nil {
		t.Fatalf("A did not receive forwarded packet: %v", err)
	}
	_ = connB.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := connB.ReadFromUDP(buf); err != nil {
		t.Fatalf("B did not receive forwarded packet: %v", err)
	}

	// Re-sync with changed CS op2 advertisedPort ONLY (same char, generation, IP).
	bindings2 := []currentPartyUDPRelayBinding{
		{characterID: 101, generation: 1, address: [4]byte{127, 0, 0, 1}, advertisedPort: 39991},
		{characterID: 202, generation: 1, address: [4]byte{127, 0, 0, 1}, advertisedPort: 39992},
	}
	if !relay.Sync(77, bindings2) {
		t.Fatal("advertisedPort-only relay sync failed")
	}
	abAfter, baAfter := lookupPair(77, 101, 202)
	if abAfter != abBefore || baAfter != baBefore {
		t.Fatal("advertisedPort change tore down live relay legs")
	}
	_, abPortAfter, _ := relay.Endpoint(77, 101, 202)
	_, baPortAfter, _ := relay.Endpoint(77, 202, 101)
	if abPortAfter != abPortBefore || baPortAfter != baPortBefore {
		t.Fatalf("relay ports changed on advertisedPort update: %d->%d, %d->%d", abPortBefore, abPortAfter, baPortBefore, baPortAfter)
	}
	statsAB := abAfter.relayLegStatsSnapshot()
	statsBA := baAfter.relayLegStatsSnapshot()
	if !statsAB.EndpointKnown || !statsBA.EndpointKnown {
		t.Fatalf("learned endpoints lost on advertisedPort update: AB=%t BA=%t", statsAB.EndpointKnown, statsBA.EndpointKnown)
	}
	if statsAB.AdvertisedPort != 39991 || statsBA.AdvertisedPort != 39992 {
		t.Fatalf("advertisedPort not updated in place: AB=%d BA=%d", statsAB.AdvertisedPort, statsBA.AdvertisedPort)
	}

	// Subsequent datagrams must forward immediately with zero new unlearned drops.
	unlearnedBefore := abAfter.traffic.unlearned.Load()
	if _, err := connA.WriteToUDP([]byte("after-op2-update"), dstAB); err != nil {
		t.Fatal(err)
	}
	_ = connB.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := connB.ReadFromUDP(buf)
	if err != nil || string(buf[:n]) != "after-op2-update" {
		t.Fatalf("forwarding broke after advertisedPort update: n=%d err=%v", n, err)
	}
	if got := abAfter.traffic.unlearned.Load(); got != unlearnedBefore {
		t.Fatalf("unlearned increased after advertisedPort update: %d -> %d", unlearnedBefore, got)
	}

	// Verify forwardDatagram on a closed opposite socket does not falsely count write_failed.
	_ = baAfter.conn.Close()
	relay.forwardDatagram(abAfter, baAfter, []byte("during-close"), time.Now())
	if got := abAfter.traffic.failed.Load(); got != 0 {
		t.Fatalf("closed-socket write falsely incremented write_failed to %d", got)
	}
}

func TestPartyPreflightTimeoutAndErrorKindDiagnostics(t *testing.T) {
	if got := partyDungeonEntryPreflightTimeout(1); got != createWriteTimeout {
		t.Fatalf("preflight timeout(1) = %v, want %v", got, createWriteTimeout)
	}
	if got := partyDungeonEntryPreflightTimeout(4); got != 7*time.Second {
		t.Fatalf("preflight timeout(4) = %v, want 7s", got)
	}
	if got := partyDungeonEntryPreflightTimeout(8); got != 8*time.Second {
		t.Fatalf("preflight timeout(8) = %v, want 8s cap", got)
	}

	cases := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("party member 100 is not present in the leader town channel/area"), "member_area_mismatch"},
		{fmt.Errorf("party member 100 has no resident game session"), "member_offline"},
		{fmt.Errorf("party member 100 has stale party state"), "member_stale_party"},
		{fmt.Errorf("party member 100 selector is not ready: town_scene_position_not_reported"), "member_selector_not_ready"},
		{fmt.Errorf("prepare party member 100 selector: %w", context.DeadlineExceeded), "preflight_deadline"},
		{fmt.Errorf("prepare party member 100 selector: write broken pipe"), "member_selector_sync_failed"},
		{fmt.Errorf("topology mismatch got dungeon=1"), "topology_mismatch"},
		{fmt.Errorf("freeze party member 100 town return origin: missing"), "member_town_origin_missing"},
		{fmt.Errorf("Anton hatchery transfer follower has another active run"), "anton_transfer_mismatch"},
	}
	for _, tc := range cases {
		got := partyDungeonPreflightErrorKind(tc.err)
		if got != tc.want {
			t.Fatalf("partyDungeonPreflightErrorKind(%q) = %q, want %q", tc.err, got, tc.want)
		}
		if !townSelectorDiagnosticFieldAllowed("error_kind", got) {
			t.Fatalf("preflight error_kind %q rejected by townSelectorDiagnosticFieldAllowed", got)
		}
	}
	for _, boolKey := range []string{"member_in_dungeon", "member_town_ready"} {
		if !townSelectorDiagnosticFieldAllowed(boolKey, true) {
			t.Fatalf("%s rejected by townSelectorDiagnosticFieldAllowed", boolKey)
		}
	}
}

package dnfbridge

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"longheng.io/server/internal/platform/eventloop"
)

type r195CoinPublicWirePacket struct {
	FrameHex string `json:"frame_hex"`
	BodyHex  string `json:"body_hex"`
}

type r195CoinPublicSessionWire struct {
	Role             string                     `json:"role"`
	CharacterID      uint16                     `json:"character_id"`
	WireHex          string                     `json:"wire_hex"`
	Notifications775 []r195CoinPublicWirePacket `json:"notifications_775"`
}

func r195CoinPublic775Packets(t *testing.T, wire []byte) []r195CoinPublicWirePacket {
	t.Helper()
	var packets []r195CoinPublicWirePacket
	for len(wire) > 0 {
		packet, rest := splitGameServerUpperPacket(t, wire)
		frameLen := len(wire) - len(rest)
		if packet.Header.Classification == 0 && packet.Header.MsgID == 775 {
			packets = append(packets, r195CoinPublicWirePacket{
				FrameHex: hex.EncodeToString(wire[:frameLen]),
				BodyHex:  hex.EncodeToString(packet.Body),
			})
		}
		wire = rest
	}
	return packets
}

func r195CoinPublicBarrier(t *testing.T, f *raidStartGuardFixture, session *gameSession, source string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	if err := f.service.callGameSession(ctx, session, source, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func r195ExportCoinPublicWire(t *testing.T, f *raidStartGuardFixture, payer, peer *gameSession, payerWire, peerWire []byte) {
	t.Helper()
	directory := os.Getenv("DNF90_COIN_FIXTURE_DIR")
	if directory == "" {
		return
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	battle := f.state.Battle
	fixture := map[string]any{
		"scope":            "real reviewActualLukeCoinFixtureForDungeon(false,5311); public handleDungeonUseCoin; bounded game-session event-loop barriers",
		"dungeon_id":       uint32(5311),
		"phase":            battle.Phase,
		"phase_coin":       battle.PhaseCoins[battle.Phase],
		"progress_version": battle.ProgressVersion,
		"sessions": []r195CoinPublicSessionWire{
			{Role: "payer", CharacterID: payer.selectedCharacterID, WireHex: hex.EncodeToString(payerWire), Notifications775: r195CoinPublic775Packets(t, payerWire)},
			{Role: "healthy_peer", CharacterID: peer.selectedCharacterID, WireHex: hex.EncodeToString(peerWire), Notifications775: r195CoinPublic775Packets(t, peerWire)},
		},
	}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "coin-wire.json"), append(encoded, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestR195LukeUseCoinPublicPathPublishesCommitted775AndRejectsStaleQueue(t *testing.T) {
	f, payer, runtime, _, _ := reviewActualLukeCoinFixtureForDungeon(t, false, 5311)
	if runtime == nil {
		t.Fatal("real Luke public coin fixture unavailable")
	}
	f.service.gameplayTimers = newFakeCurrentDungeonDeathTimerQueue()
	runtime.lifecycleToken = 1
	reviewLukeCoinDie(t, f, payer)

	battle := f.state.Battle
	peer, found := f.service.onlineGameSession(20)
	if !found || peer == nil {
		t.Fatal("real Luke coin fixture did not bind healthy peer 20")
	}
	if runtimeRaidMemberIndex(*f.state, 20) < 0 || battle.MemberAccounts[20] != "account-20" {
		t.Fatal("healthy peer is not a frozen member of the current Luke run")
	}
	if !f.service.currentRaidAuthenticated(payer) || !f.service.currentRaidAuthenticated(peer) {
		t.Fatal("Luke coin fixture session identity is not healthy")
	}
	payerConn, peerConn := &deadlineAuditV10Conn{}, &deadlineAuditV10Conn{}
	payer.conn, peer.conn = payerConn, peerConn
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	for _, session := range []*gameSession{payer, peer} {
		current := session
		if err := f.service.startGameSessionEvents(ctx, current); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { stopTestGameSessionEvents(t, f.service, current) })
	}

	stale := cloneRuntimeRaidState(*f.state)
	beforeCount, beforeVersion := battle.PhaseCoins[battle.Phase], battle.ProgressVersion
	if beforeCount != 0 {
		t.Fatalf("real phase coin fixture starts at %d, want 0", beforeCount)
	}
	callCtx, callCancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer callCancel()
	if err := f.service.callGameSession(callCtx, payer, "r195-public-op41-use-coin", func() error {
		return f.service.handleDungeonUseCoin(payer, reviewLukeCoinRequest(payer))
	}); err != nil {
		t.Fatal(err)
	}
	if runtime.deathReturnWaiting || battle.PhaseCoins[battle.Phase] != 1 || battle.ProgressVersion != beforeVersion+1 {
		t.Fatalf("public op41 commit: waiting=%t coin=%d version=%d", runtime.deathReturnWaiting, battle.PhaseCoins[battle.Phase], battle.ProgressVersion)
	}

	// The handler queues progress after releasing dungeon.mu through untracked
	// goroutines. Wait for both subscribers to publish the committed revision,
	// then use the same medium queue as an explicit ordering barrier. This avoids
	// assuming how many life/coin projections the handler happened to enqueue.
	reviewManagerWaitVersion(t, payer, battle.ProgressVersion)
	reviewManagerWaitVersion(t, peer, battle.ProgressVersion)
	r195CoinPublicBarrier(t, f, payer, "r195-775-payer-observe-barrier")
	r195CoinPublicBarrier(t, f, peer, "r195-775-peer-observe-barrier")
	payerWire, peerWire := payerConn.bytes(), peerConn.bytes()
	if got := r195CoinProjectionValues(t, payerWire); !bytes.Equal(got, []byte{1}) {
		t.Fatalf("payer public wire 775=%v, want [1]", got)
	}
	if got := r195CoinProjectionValues(t, peerWire); !bytes.Equal(got, []byte{1}) {
		t.Fatalf("healthy peer public wire 775=%v, want [1]", got)
	}
	r195ExportCoinPublicWire(t, f, payer, peer, payerWire, peerWire)

	metrics := payer.events.loop.MetricsSnapshot()
	peerMetrics := peer.events.loop.MetricsSnapshot()
	f.service.queueCurrentRaidDungeonProgress(stale)
	reviewManagerWaitMetrics(t, payer,
		metrics.Submitted[eventloop.PriorityMedium]+1,
		metrics.Processed[eventloop.PriorityMedium]+1,
	)
	reviewManagerWaitMetrics(t, peer,
		peerMetrics.Submitted[eventloop.PriorityMedium]+1,
		peerMetrics.Processed[eventloop.PriorityMedium]+1,
	)
	r195CoinPublicBarrier(t, f, payer, "r195-stale-payer-queue-barrier")
	r195CoinPublicBarrier(t, f, peer, "r195-stale-peer-queue-barrier")
	if got := r195CoinProjectionValues(t, payerConn.bytes()); !bytes.Equal(got, []byte{1}) {
		t.Fatalf("stale pre-commit queue changed payer 775=%v", got)
	}
	if got := r195CoinProjectionValues(t, peerConn.bytes()); !bytes.Equal(got, []byte{1}) {
		t.Fatalf("stale pre-commit queue changed peer 775=%v", got)
	}
	if battle.PhaseCoins[battle.Phase] != 1 || battle.ProgressVersion != beforeVersion+1 {
		t.Fatalf("stale queue changed live coin/version: coin=%d version=%d", battle.PhaseCoins[battle.Phase], battle.ProgressVersion)
	}
}

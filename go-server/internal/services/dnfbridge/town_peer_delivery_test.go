package dnfbridge

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"longheng.io/server/internal/platform/eventloop"
)

func startTownPeerTest(t *testing.T, s *Service, p *gameSession, info *onlinePlayerInfo) {
	t.Helper()
	s.bindGameSessionCharacter(p, p.selectedCharacterID)
	if info != nil {
		s.onlinePlayers.EnterArea(info)
	}
	if err := s.startGameSessionEvents(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.closeGameTransport(); stopTestGameSessionEvents(t, s, p) })
}

func drainTownPeerTest(t *testing.T, s *Service, p *gameSession) {
	t.Helper()
	if p.events == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*createWriteTimeout)
	defer cancel()
	if err := s.callGameSession(ctx, p, "test-town-drain", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestTownPeerDeliveryDoesNotWaitForSlowRecipients(t *testing.T) {
	for _, count := range []int{1, 8, 32} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s := &Service{onlinePlayers: newOnlinePlayerManager()}
			var peers []onlinePlayerInfo
			for i := 0; i < count; i++ {
				left, right := net.Pipe()
				t.Cleanup(func() { left.Close(); right.Close() })
				p := &gameSession{conn: left, connID: fmt.Sprintf("slow-%d", i), selectedCharacterID: uint16(i + 1)}
				info := onlinePlayerInfo{Session: p, CharacterID: p.selectedCharacterID, TownID: 1, AreaID: 1}
				startTownPeerTest(t, s, p, &info)
				peers = append(peers, info)
			}
			healthy := &gameSession{conn: &bufferConn{}, connID: "healthy", selectedCharacterID: 100}
			info := onlinePlayerInfo{Session: healthy, CharacterID: 100, TownID: 1, AreaID: 1}
			startTownPeerTest(t, s, healthy, &info)
			peers = append(peers, info)
			started := time.Now()
			s.broadcastTownPlayerLeave(200, peers)
			if time.Since(started) > time.Second {
				t.Fatal("producer waited on peer network")
			}
			drainTownPeerTest(t, s, healthy)
			if healthy.conn.(*bufferConn).write.Len() == 0 {
				t.Fatal("healthy peer skipped")
			}
			for i := 0; i < count; i++ {
				peers[i].Session.closeGameTransport()
			}
		})
	}
}

func TestTownPeerDeliveryPreservesOrderAndDropsOldScene(t *testing.T) {
	s := &Service{onlinePlayers: newOnlinePlayerManager()}
	p := &gameSession{conn: &bufferConn{}, selectedCharacterID: 10}
	info := onlinePlayerInfo{Session: p, CharacterID: 10, TownID: 1, AreaID: 1}
	startTownPeerTest(t, s, p, &info)
	entered, release := make(chan struct{}), make(chan struct{})
	err := p.events.loop.Submit(eventloop.Event{Type: "block", Data: gameSessionEventTask{source: "block", run: func() error { close(entered); <-release; return nil }}})
	if err != nil {
		t.Fatal(err)
	}
	waitTestSignal(t, entered, "block not entered")
	var order []int
	for i := 0; i < 3; i++ {
		n := i
		s.queueTownPeerDelivery(info, "ordered", nil, func() error { order = append(order, n); return nil })
	}
	close(release)
	drainTownPeerTest(t, s, p)
	if fmt.Sprint(order) != "[0 1 2]" {
		t.Fatal(order)
	}
	stale := info
	s.onlinePlayers.EnterArea(&info)
	var ran atomic.Bool
	s.queueTownPeerDelivery(stale, "stale-scene", nil, func() error { ran.Store(true); return nil })
	drainTownPeerTest(t, s, p)
	if ran.Load() {
		t.Fatal("old scene executed after reentry")
	}
}

func TestTownPeerDeliveryInactiveQueueNeverRunsInline(t *testing.T) {
	s := &Service{onlinePlayers: newOnlinePlayerManager()}
	p := &gameSession{conn: &bufferConn{}, selectedCharacterID: 10}
	info := onlinePlayerInfo{Session: p, CharacterID: 10}
	s.onlinePlayers.EnterArea(&info)
	ran := false
	s.queueTownPeerDelivery(info, "inactive", nil, func() error { ran = true; return nil })
	if ran {
		t.Fatal("inactive queue ran inline")
	}
}

func TestTownPeerDeliveryQueueFullClosesOnlyOverloadedPeer(t *testing.T) {
	s := &Service{onlinePlayers: newOnlinePlayerManager()}
	conn := &shortGameWriteConn{bufferConn: &bufferConn{}}
	p := &gameSession{conn: conn, selectedCharacterID: 10}
	info := onlinePlayerInfo{Session: p, CharacterID: 10}
	startTownPeerTest(t, s, p, &info)
	entered, release := make(chan struct{}), make(chan struct{})
	if err := p.events.loop.Submit(eventloop.Event{Type: "block", Data: gameSessionEventTask{source: "block", run: func() error { close(entered); <-release; return nil }}}); err != nil {
		t.Fatal(err)
	}
	waitTestSignal(t, entered, "not blocked")
	defer close(release)
	for i := 0; i < gameSessionEventQueueSize; i++ {
		if err := p.events.loop.Submit(eventloop.Event{Type: "fill", Data: gameSessionEventTask{source: "fill", run: func() error { return nil }}}); err != nil {
			t.Fatal(err)
		}
	}
	s.queueTownPeerDelivery(info, "overflow", nil, func() error { t.Error("overflow callback executed"); return nil })
	if !conn.closed {
		t.Fatal("overloaded recipient remained live after required scene packet lost")
	}
}

package dnfbridge

import (
	"bytes"
	"longheng.io/server/internal/platform/eventloop"
	"testing"
)

func TestTownMovementCoalescesLatestAndOverflowDoesNotDisconnect(t *testing.T) {
	for _, full := range []bool{false, true} {
		s := &Service{onlinePlayers: newOnlinePlayerManager()}
		conn := &guildPresenceQueueConn{bufferConn: &bufferConn{}}
		p := &gameSession{conn: conn, selectedCharacterID: 10}
		target := onlinePlayerInfo{Session: p, CharacterID: 10, TownID: 1, AreaID: 1}
		startTownPeerTest(t, s, p, &target)
		actor := onlinePlayerInfo{Session: &gameSession{}, CharacterID: 11, TownID: 1, AreaID: 1}
		s.onlinePlayers.EnterArea(&actor)
		entered, release := make(chan struct{}), make(chan struct{})
		if err := p.events.loop.Submit(eventloop.Event{Type: "block", Data: gameSessionEventTask{source: "block", run: func() error { close(entered); <-release; return nil }}}); err != nil {
			t.Fatal(err)
		}
		waitTestSignal(t, entered, "blocked")
		if full {
			for range gameSessionEventQueueSize {
				if err := p.events.loop.Submit(eventloop.Event{Type: "fill", Data: gameSessionEventTask{source: "fill", run: func() error { return nil }}}); err != nil {
					t.Fatal(err)
				}
			}
		}
		var latest []byte
		for i := 0; i < 100; i++ {
			latest = buildCurrentTownUserPositionNotificationBodyWithMotionState(currentSceneActorObjectKey(11), uint16(i), 200, 0, 0)
			s.queueTownPeerPacket(target, &actor, currentTownUserPositionNotificationMsgID, latest, "test-move")
		}
		p.townMoveMu.Lock()
		if full && len(p.townMoves) != 0 {
			t.Error("rejected movement leaked")
		}
		if !full {
			if len(p.townMoves) != 1 {
				t.Error("one queued movement per actor expected")
			}
			for _, v := range p.townMoves {
				if !bytes.Equal(v.body, latest) {
					t.Error("latest position lost")
				}
			}
		}
		p.townMoveMu.Unlock()
		if conn.closed.Load() {
			t.Error("movement overflow disconnected recipient")
		}
		close(release)
		drainTownPeerTest(t, s, p)
		if conn.closed.Load() {
			t.Error("healthy movement delivery closed socket")
		}
		if !full && conn.write.Len() == 0 {
			t.Error("latest movement was not delivered")
		}
		p.townMoveMu.Lock()
		if len(p.townMoves) != 0 {
			t.Error("consumed movement leaked")
		}
		p.townMoveMu.Unlock()
	}
}

package dnfbridge

import (
	"encoding/binary"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

// Exercise the real move handler, not only the vote writer: a server follower
// plan carries the leader's coordinates, while only a real client request owns
// that session's arrival evidence. The synthetic target is deliberately outside
// the maze so this regression has no movement or outgoing packet side effects.
func TestDungeonMoveInternalPlanNeverCastsClientArrivalVote(t *testing.T) {
	service, runtime := prepareSyntheticMoveRuntime(t, false)
	instance := &dungeonPartyInstance{members: map[uint16]time.Time{99: time.Now(), 100: time.Now()}}
	runtime.partyInstance = instance
	session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
	current, ok := runtime.Session.Scene()
	if !ok {
		t.Fatal("fixture has no current room")
	}
	source := runtimeDungeonRoomKeyFromScene(current)
	body := make([]byte, dungeoncmd.MoveMapRequestSize)
	body[0] = 2
	binary.LittleEndian.PutUint32(body[2:6], 1231)
	binary.LittleEndian.PutUint32(body[6:10], 221)
	key := partyDungeonDoorArrivalKey{Source: source, Target: runtimeDungeonRoomKey{X: 2}}
	if err := service.handleDungeonMoveMapPlanned(session, body, &runtimePartyDungeonMovePlan{}, false); err != nil {
		t.Fatal(err)
	}
	if len(instance.doorArrivals) != 0 {
		t.Fatal("internal follower plan fabricated a client vote")
	}
	if err := service.handleDungeonMoveMapPlanned(session, body, nil, false); err != nil {
		t.Fatal(err)
	}
	if votes := instance.doorArrivals[key]; len(votes) != 1 || votes[99].X != 1231 || votes[99].Y != 221 {
		t.Fatalf("real client request votes=%+v", votes)
	}
	// A later internal plan must not overwrite genuine client coordinates.
	binary.LittleEndian.PutUint32(body[2:6], 77)
	if err := service.handleDungeonMoveMapPlanned(session, body, &runtimePartyDungeonMovePlan{}, false); err != nil {
		t.Fatal(err)
	}
	if votes := instance.doorArrivals[key]; len(votes) != 1 || votes[99].X != 1231 {
		t.Fatalf("internal replay overwrote actual client arrival: %+v", votes)
	}
	if session.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("invalid fixture move emitted packets")
	}
	if after, ok := runtime.Session.Scene(); !ok || runtimeDungeonRoomKeyFromScene(after) != source {
		t.Fatal("invalid fixture move changed current room")
	}
}

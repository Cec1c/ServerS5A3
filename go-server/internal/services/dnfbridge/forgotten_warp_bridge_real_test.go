package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestRealPVFForgottenWarpBridgeSendsNativeTransitionsAndRejectsUndeclaredTarget(t *testing.T) {
	s, g := round17ForgottenService(t)
	session := seedDungeonDailyCountCharacter(t, g, "17", 17, "forgotten-account")
	bindDungeonSelectorOriginForTestAt(t, s, session, 107, 101, 250, 224)
	runtime, _, err := s.prepareDungeonRuntime(context.Background(), session, dungeoncmd.SelectDungeonRequest{DungeonID: 4000})
	if err != nil {
		t.Fatal(err)
	}
	session.dungeon.runtime = runtime
	move := func(x, y byte) {
		t.Helper()
		body := make([]byte, dungeoncmd.MoveMapRequestSize)
		body[0], body[1] = x, y
		if err := s.handleDungeonMoveMap(session, body); err != nil {
			t.Fatal(err)
		}
	}
	conn := session.conn.(*bufferConn)
	move(8, 6)
	if runtime.Session.Snapshot().Run.Current != (worldmap.RoomCoordinate{X: 8, Y: 6}) {
		t.Fatal("could not reach real warp hub")
	}
	conn.write.Reset()
	before := runtime.Session.Snapshot()
	// An undeclared non-adjacent destination cannot mutate the active run.
	move(0, 0)
	if conn.write.Len() != 0 || !reflect.DeepEqual(before, runtime.Session.Snapshot()) {
		t.Fatal("undeclared warp changed run or sent a start map")
	}
	for _, target := range [][2]byte{{0, 6}, {8, 6}, {1, 7}} {
		conn.write.Reset()
		move(target[0], target[1])
		want := worldmap.RoomCoordinate{X: int64(target[0]), Y: int64(target[1])}
		snapshot := runtime.Session.Snapshot()
		if snapshot.Run.Current != want || snapshot.Scene.Coordinate != want {
			t.Fatalf("warp target=%v actual=%v", want, snapshot.Run.Current)
		}
		p, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
		if p.Header.MsgID != 29 || p.Header.Classification != 0 || p.Body[0] != target[0] || p.Body[1] != target[1] {
			t.Fatalf("native warp header=%+v body=%x", p.Header, p.Body)
		}
		if target != [2]byte{8, 6} && (len(p.Body) < 18 || int64(binary.LittleEndian.Uint32(p.Body[14:18])) != snapshot.Scene.Map.Map.ID) {
			t.Fatalf("new-room map disagrees with committed PVF scene: %x", p.Body)
		}
	}
}

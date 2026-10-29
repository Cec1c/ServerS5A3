package dnfbridge

import (
	"context"
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/premium"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestRealPVFDimensionAcceptedCountProjectionIsReadOnly(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current PVF")
	}
	s, repos, queue := newDungeonDailyCountTestService(t, path)
	s.options.gameUpperHeader = gameUpperHeaderServer16
	session := seedDungeonDailyCountCharacter(t, repos, "19", 19, "dimension-count-account")
	s.gameSessions = map[uint16]*gameSession{19: session}
	ctx := context.Background()
	character, _, err := repos.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	character.Stats["dimension_gate_daily_entry_day"] = premium.ServiceDayID(queue.now)
	character.Stats["dimension_gate_daily_entry_used"] = 6
	character.Stats["dimension_gate_daily_entry_extra_granted"] = 3
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	before, _, _ := repos.Character.Load(ctx, "19")
	for i := 0; i < 2; i++ {
		if err := s.sendCurrentDimensionGateEntranceInfo(session, "accepted_relogin_count"); err != nil {
			t.Fatal(err)
		}
		conn := session.conn.(*bufferConn)
		packet, rest := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
		conn.write.Reset()
		if packet.Header.MsgID != 812 || packet.Header.Classification != 0 || len(rest) != 0 || len(packet.Body) != 9 || binary.LittleEndian.Uint32(packet.Body) != 5 || binary.LittleEndian.Uint32(packet.Body[4:]) != 2 || packet.Body[8] != 0 {
			t.Fatalf("projection=%+v rest=%x", packet, rest)
		}
	}
	after, _, _ := repos.Character.Load(ctx, "19")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("count projection mutated character")
	}
	queue.now = queue.now.Add(24 * time.Hour)
	if err := s.sendCurrentDimensionGateEntranceInfo(session, "accepted_daily_reset"); err != nil {
		t.Fatal(err)
	}
	conn := session.conn.(*bufferConn)
	packet, rest := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Header.MsgID != 812 || len(rest) != 0 || len(packet.Body) != 9 || binary.LittleEndian.Uint32(packet.Body) != 0 || binary.LittleEndian.Uint32(packet.Body[4:]) != 0 {
		t.Fatalf("reset projection=%+v", packet)
	}
	after, _, _ = repos.Character.Load(ctx, "19")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("daily projection wrote reset state")
	}
}

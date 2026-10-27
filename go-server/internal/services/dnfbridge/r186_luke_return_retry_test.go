package dnfbridge

import (
	"errors"
	"io"
	"testing"
)

func TestRealPVFR186LukeSpreadTownRetryKeepsOneAck(t *testing.T) {
	f, p := prepareR165LukeSpreadSanctumRealResultFixture(t)
	s, r := f.service, p.dungeon.runtime
	f.conn.write.Reset()
	failing := &completedReturnAckOp24FailureConn{bufferConn: f.conn, t: t, err: io.ErrUnexpectedEOF, failOnce: true}
	p.conn = failing
	if err := s.handleDungeonEplpCommand(p, []byte{1, 2}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("first town write error = %v", err)
	}
	if p.dungeon.runtime != r || !r.settlementCardExitAckSent || r.townReturnOp24Sent {
		t.Fatal("failed town write lost pending result owner")
	}
	if err := s.handleDungeonEplpCommand(p, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if p.dungeon.runtime != nil || !r.townReturnOp24Sent {
		t.Fatal("town retry did not complete")
	}
	frames := splitUpperFrames(t, f.conn.write.Bytes())
	if n := r133RegressionCountMsg(frames, 72); n != 1 {
		t.Fatalf("one result action must keep one acknowledgement across retry, got %d", n)
	}
	if n := r133RegressionCountMsg(frames, 24); n != 1 {
		t.Fatalf("successful town routes=%d", n)
	}
	if r.settlementClearRewardSent {
		t.Fatal("retry manufactured ordinary reward")
	}
}

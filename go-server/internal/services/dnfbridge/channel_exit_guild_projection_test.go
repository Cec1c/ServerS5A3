package dnfbridge

import (
	"bytes"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

func TestChannelExitStopsLateGuildIdentityProjection(t *testing.T) {
	s, p, _ := guildMileageFixture(t)
	g, err := s.currentGuildForSelectedCharacter(p)
	if err != nil || g == nil {
		t.Fatalf("guild=%v err=%v", g, err)
	}
	p.townSceneReadyCharacterID = p.selectedCharacterID
	p.initialTownLegacySceneReadyAccepted = true
	p.initialTownGuildStateProjectionSent = false
	c := p.conn.(*bufferConn)
	c.write.Reset()
	if err := s.handleCurrentChannelExit(p, []byte{1}, "guild-exit-test"); err != nil {
		t.Fatal(err)
	}
	ack := append([]byte(nil), c.write.Bytes()...)
	packet, rest := splitCurrentGameServerUpperPacketAuto(t, ack)
	if packet.Header.MsgID != uint16(dnfenum.CmdPacketExit) || !bytes.Equal(packet.Body, []byte{1}) || len(rest) != 0 {
		t.Fatalf("exit ACK=%+v rest=%x", packet, rest)
	}
	if err := s.sendCurrentGuildManagementState(p, g); err != nil {
		t.Fatal(err)
	}
	p.townMu.Lock()
	err = s.writeCurrentGuildManagementState(p, g) // timer path already owns townMu
	if err == nil {
		err = s.sendCurrentInitialTownGuildStateProjectionLocked(p, "late-scene-callback")
	}
	p.townMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(c.write.Bytes(), ack) {
		t.Fatal("late guild packets were written after EXIT acknowledged scene teardown")
	}
	if p.initialTownGuildStateProjectionSent {
		t.Fatal("late scene callback was incorrectly marked as a delivered guild projection")
	}
}

func TestChannelExitWaitsForInflightGuildProjection(t *testing.T) {
	s, p, _ := guildMileageFixture(t)
	g, err := s.currentGuildForSelectedCharacter(p)
	if err != nil || g == nil {
		t.Fatalf("guild=%v err=%v", g, err)
	}
	// No position capture needs townMu: exercise the ACK ordering itself.
	p.channel.Group = "pvp"
	c := p.conn.(*bufferConn)
	c.write.Reset()
	p.townMu.Lock()
	started, done := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		done <- s.handleCurrentChannelExit(p, []byte{1}, "inflight-guild-test")
	}()
	<-started
	select {
	case err := <-done:
		p.townMu.Unlock()
		t.Fatalf("EXIT passed an in-flight guild projection: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	err = s.writeCurrentGuildManagementState(p, g)
	p.townMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("EXIT did not resume after the guild projection completed")
	}
	seenGrade, seenExit := false, false
	for wire := c.write.Bytes(); len(wire) > 0; {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
		wire = rest
		if seenExit {
			t.Fatal("guild output followed the EXIT ACK")
		}
		seenGrade = seenGrade || packet.Header.MsgID == 127
		if packet.Header.MsgID == uint16(dnfenum.CmdPacketExit) {
			seenExit = true
		}
	}
	if !seenGrade || !seenExit {
		t.Fatalf("grade=%t exit=%t", seenGrade, seenExit)
	}
}

func TestFailedChannelExitKeepsGuildProjectionAvailable(t *testing.T) {
	s, p, _ := guildMileageFixture(t)
	g, err := s.currentGuildForSelectedCharacter(p)
	if err != nil || g == nil {
		t.Fatalf("guild=%v err=%v", g, err)
	}
	p.townSceneReadyCharacterID = p.selectedCharacterID
	c := p.conn.(*bufferConn)
	p.conn = &firstChannelFailedWriteConn{}
	if err = s.handleCurrentChannelExit(p, []byte{1}, "failed-guild-exit-test"); err == nil {
		t.Fatal("failed EXIT ACK accepted")
	}
	p.conn = c
	c.write.Reset()
	if err = s.sendCurrentGuildManagementState(p, g); err != nil {
		t.Fatal(err)
	}
	if len(guildReply(t, p, 127)) != 1 {
		t.Fatal("failed EXIT disabled the existing scene's guild identity")
	}
}

package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	onlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"os"
	"testing"
)

func TestOrdinaryBridgeActualOwner688GrantReplayAndNineByteProjection(t *testing.T) {
	s, a, b, conn := prepareMonthlyBridge(t)
	state, err := s.settleCurrentOrdinaryAttendance(a, true, false)
	if err != nil || state.Days != 1 || state.Claimed != 0 || !state.CanClaim {
		t.Fatalf("registered%+v err%v", state, err)
	}
	body := make([]byte, 4)
	binary.LittleEndian.PutUint32(body, 331)
	if err := s.handleGameCommand(a, 1, 688, body); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if ack.Header.MsgID != 688 || !bytes.Equal(ack.Body, buildCurrentOrdinaryAttendanceACK(true)) {
		t.Fatalf("native ACK%+v", ack)
	}
	projection, _ := splitGameServerUpperPacket(t, rest)
	if projection.Header.MsgID != 1386 || !bytes.Equal(projection.Body, []byte{0, 1, 0, 0, 0, 1, 0, 0, 0}) {
		t.Fatalf("native projection%+v", projection)
	}
	if monthlyMailCount(t, s, "1") != 1 || monthlyMailCount(t, s, "5") != 0 {
		t.Fatal("owner grant")
	}
	g, _ := s.repositoryGroup()
	mail, _, _ := g.Mailbox.Load(nil, "1")
	for _, m := range mail.Mails {
		if len(m.Attachments) != 1 || m.Attachments[0].ItemID != 490010214 || m.Attachments[0].Count != 1 || !m.Attachments[0].ExpireAt.After(s.gameplayNow()) {
			t.Fatalf("actual reward%+v", m)
		}
	}
	conn.write.Reset()
	if err := s.handleGameCommand(a, 1, 688, body); err != nil {
		t.Fatal(err)
	}
	if monthlyMailCount(t, s, "1") != 1 {
		t.Fatal("replay duplicate")
	}
	if _, err := s.handleCurrentOrdinaryAttendanceClaim(b, 688, 1, body); err != nil {
		t.Fatal(err)
	}
	if monthlyMailCount(t, s, "5") != 0 {
		t.Fatal("foreign account claimed another owner's day")
	}
}

func TestOrdinaryBridgeOwnerAndMalformedRequestCannotGrant(t *testing.T) {
	s, a, _, conn := prepareMonthlyBridge(t)
	if _, err := s.settleCurrentOrdinaryAttendance(a, true, false); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		owner   uint32
		class   byte
		extra   int
		handled bool
	}{{332, 1, 0, false}, {331, 0, 0, true}, {331, 1, 4, true}} {
		conn.write.Reset()
		body := make([]byte, 4+c.extra)
		binary.LittleEndian.PutUint32(body, c.owner)
		handled, err := s.handleCurrentOrdinaryAttendanceClaim(a, 688, c.class, body)
		if err != nil || handled != c.handled || monthlyMailCount(t, s, "1") != 0 {
			t.Fatalf("request%+v handled%v err%v", c, handled, err)
		}
		if c.owner == 331 && c.class == 1 {
			ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
			if !bytes.Equal(ack.Body, []byte{0, 1, 75, 1, 0, 0}) {
				t.Fatalf("owner-preserving failure%+v", ack)
			}
		}
	}
}

func TestOrdinaryBridgeStaleGenerationDoesNotRegisterOrClaim(t *testing.T) {
	s, a, _, _ := prepareMonthlyBridge(t)
	g, _ := s.repositoryGroup()
	s.repositoryProvider = func() (repo.Group, bool) { s.mu.Lock(); a.characterGeneration++; s.mu.Unlock(); return g, true }
	if state, err := s.settleCurrentOrdinaryAttendance(a, true, true); err == nil {
		t.Fatalf("stale owner accepted%+v", state)
	}
	if monthlyMailCount(t, s, "1") != 0 {
		t.Fatal("stale mail")
	}
}

func TestOrdinaryBridgeCatalogUsesActualCurrentPopupAndDisabledPreservesOldBody(t *testing.T) {
	ids := currentSpendTimeJoustCatalogGoldenRewards
	old, err := buildCurrentActivityEventInfoBodyWithWeekly(ids, 10800, false, false, true, true, true, 2418, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := buildCurrentActivityEventInfoBodyWithOrdinary(ids, 10800, false, false, true, true, true, 2418, true, true, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(old, disabled) {
		t.Fatal("disabled ordinary changed existing catalog")
	}
	body, err := buildCurrentActivityEventInfoBodyWithOrdinary(ids, 10800, false, false, true, true, true, 2418, true, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	shape := parseCurrentSpendTimeEventInfoBody(t, body)
	var found bool
	for _, r := range shape.baseRows {
		if r.eventID == 331 {
			found = true
			if !r.hasExtended || r.route != "attendance daily event/0/0" || r.start != 0 || r.end != 0 {
				t.Fatalf("wrong native route%+v", r)
			}
		}
	}
	if !found || !shape.hasEvent(331) {
		t.Fatal("no current owner331 catalog")
	}
	for _, r := range shape.activeRows {
		if r.eventID == 331 && r.params != [12]uint32{} {
			t.Fatal("client configured invented331 parameters")
		}
	}
	if p := os.Getenv("ORDINARY_NATIVE_FIXTURE"); p != "" {
		if err := os.WriteFile(p, body, 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOrdinaryBridgeClosedControlCannotClaimRegisteredDays(t *testing.T) {
	s, a, _, conn := prepareMonthlyBridge(t)
	if _, err := s.settleCurrentOrdinaryAttendance(a, true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.activityControls.Save(context.Background(), s.activityControls.Snapshot().Revision, []onlineevent.ControlChange{{ID: 331, Enabled: false}}); err != nil {
		t.Fatal(err)
	}
	if err := s.initializeActivityControls(context.Background()); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 4)
	binary.LittleEndian.PutUint32(body, 331)
	if _, err := s.handleCurrentOrdinaryAttendanceClaim(a, 688, 1, body); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
	if !bytes.Equal(ack.Body, buildCurrentOrdinaryAttendanceACK(false)) || monthlyMailCount(t, s, "1") != 0 {
		t.Fatal("closed activity granted or short owner failure")
	}
}

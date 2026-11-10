package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/buffswap"
	"longheng.io/server/internal/modules/dnf/dnfenum"
)

func seedPairedSelectedBuffs(t *testing.T, s *Service, session *gameSession) {
	t.Helper()
	repos, ok := s.repositoryGroup()
	if !ok {
		t.Fatal("missing repository")
	}
	for group, skill := range []uint16{38, 39} {
		if err := buffswap.ApplyCurrentGroup(context.Background(), repos, "1", session.accountID, byte(group), buffswap.Command{Skill: skill, Slot: 33, List: 44, Index: 0xffff}); err != nil {
			t.Fatal(err)
		}
	}
}

type pairedInitialFailConn struct {
	*bufferConn
	fail bool
}

func (c *pairedInitialFailConn) Write(data []byte) (int, error) {
	if c.fail && len(data) >= 3 && binary.LittleEndian.Uint16(data[1:3]) == currentBuffSwapRowsID {
		return 0, errors.New("paired initial group write failed")
	}
	return c.bufferConn.Write(data)
}

func TestBuffPairedSelectedMode1RestoresBeforeMarkingComplete(t *testing.T) {
	t.Setenv("DNFBRIDGE_BUFF_GROUP_SYNC", "on")
	s, session, conn, _ := r191CacheFixture(t)
	seedPairedSelectedBuffs(t, s, session)
	if err := s.sendSelectedSceneUserInfo23Mode1Only(session, "paired_select"); err != nil {
		t.Fatal(err)
	}
	packets := drainUpperPackets(t, conn)
	if len(packets) != 3 || packets[0].msgID != uint16(dnfenum.CmdPacketSetUDPIPPort) || packets[0].body[0] != 1 {
		t.Fatalf("mode1/group order=%+v", packets)
	}
	for i, p := range packets[1:] {
		if p.msgID != 1367 || len(p.body) != 170 || p.body[1] != byte(i) || binary.LittleEndian.Uint16(p.body[2:]) != uint16(38+i) {
			t.Fatalf("group%d=%+v", i, p)
		}
	}
	if !session.selectedUserInfoRefreshSent {
		t.Fatal("mode1 completion not recorded")
	}
	if err := s.sendSelectedSceneUserInfo23Mode1Only(session, "duplicate"); err != nil || conn.write.Len() != 0 {
		t.Fatalf("duplicate output err=%v bytes=%d", err, conn.write.Len())
	}
}

func TestBuffPairedSelectedMode1FailedRestoreRemainsRetryable(t *testing.T) {
	t.Setenv("DNFBRIDGE_BUFF_GROUP_SYNC", "on")
	s, session, conn, _ := r191CacheFixture(t)
	seedPairedSelectedBuffs(t, s, session)
	failure := &pairedInitialFailConn{bufferConn: conn, fail: true}
	session.conn = failure
	if err := s.sendSelectedSceneUserInfo23Mode1Only(session, "failed_group"); err == nil {
		t.Fatal("lost failed group write")
	}
	if session.selectedUserInfoRefreshSent {
		t.Fatal("failed group marked actor complete")
	}
	conn.write.Reset()
	failure.fail = false
	if err := s.sendSelectedSceneUserInfo23Mode1Only(session, "retry_group"); err != nil {
		t.Fatal(err)
	}
	packets := drainUpperPackets(t, conn)
	if len(packets) != 3 || packets[1].msgID != 1367 || packets[2].msgID != 1367 {
		t.Fatalf("retry groups=%+v", packets)
	}
}

func TestBuffPairedInitialActorBindingRestoresBothGroups(t *testing.T) {
	t.Setenv("DNFBRIDGE_BUFF_GROUP_SYNC", "on")
	s, session, conn, _ := r191CacheFixture(t)
	seedPairedSelectedBuffs(t, s, session)
	session.initialTownRouteCharacterID = 1
	session.initialTownRouteStage = currentInitialTownRouteObjectSent
	mode1 := buildCurrentActorBindingMode1Body(currentSceneActorObjectKey(1), 0)
	if err := s.sendCurrentTownActorPrerequisitesLocked(session, 1, nil, mode1); err != nil {
		t.Fatal(err)
	}
	packets := drainUpperPackets(t, conn)
	if len(packets) != 3 || packets[0].msgID != uint16(dnfenum.CmdPacketSetUDPIPPort) || packets[1].msgID != 1367 || packets[2].msgID != 1367 {
		t.Fatalf("binding groups=%+v", packets)
	}
	if packets[1].body[1] != 0 || packets[2].body[1] != 1 {
		t.Fatal("binding used wrong native group")
	}
	if session.initialTownRouteStage != currentInitialTownRouteActorBound {
		t.Fatal("binding route did not commit")
	}
}

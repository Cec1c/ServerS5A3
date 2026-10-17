package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestOfficial249Op16EntryKeepsPermissionBeforeRoomHandoff(t *testing.T) {
	s, session := newDungeonSelectRefusalFixture(t, dnfrepo.CharacterRecord{
		CharacterID: "99", AccountID: "account-1", Level: 20,
		Stats: map[string]int64{"fatigue": 100, "town_id": 38, "area_id": 1, "pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3},
	}, nil)
	s.options.currentClientVersion = "official-24.9"
	bindDungeonSelectorOriginForTestAt(t, s, session, 38, 1, 450, 234)
	body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
	binary.LittleEndian.PutUint32(body, 700)
	binary.LittleEndian.PutUint16(body[9:11], 0xffff)
	if err := s.handleDungeonSelectUpper(session, body); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
	if ack.Header.MsgID != uint16(dnfenum.CmdPacketSelectDungeon) || !bytes.Equal(ack.Body, upperSuccessBody(buildSelectDungeonBodyForCharacter(99))) {
		t.Fatalf("entry ACK=%+v body=%x", ack.Header, ack.Body)
	}
	permission, rest := splitGameServerUpperPacket(t, rest)
	if permission.Header.MsgID != currentDungeonResourceStateMsgID || !bytes.Equal(permission.Body, []byte{1, 0, 0xbc, 2, 0, 0, 0}) {
		t.Fatalf("entry permission=%+v body=%x", permission.Header, permission.Body)
	}
	roomInfo := false
	for len(rest) != 0 {
		p, trailing := splitGameServerUpperPacket(t, rest)
		if p.Header.MsgID == currentDungeonInfoNotification {
			roomInfo = true
		}
		rest = trailing
	}
	if !roomInfo || session.dungeon.runtime == nil || session.dungeon.runtime.Dungeon.ID != 700 {
		t.Fatal("zero permission suppressed committed room handoff")
	}
	repos, _ := s.repositoryGroup()
	_, found, err := repos.DungeonPermission.Load(context.Background(), "99")
	if err != nil || found {
		t.Fatalf("entry unlocked persisted difficulty: found=%t err=%v", found, err)
	}
}

func TestOfficial249CommittedPermissionIsSentAfterClearRewardOnce(t *testing.T) {
	s, rt, session, conn, _ := prepareCompletedSettlementRuntime(t)
	s.options.currentClientVersion = "official-24.9"
	repos := testRepositoryGroup()
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	rt.Dungeon.Metadata.DifficultyLevels = []int64{1, 1, 1, 1, 1}
	rt.Request.Difficulty = 0
	body, state, changed, err := s.commitCurrentDungeonPermission(context.Background(), session, rt)
	if err != nil || !changed || state != 1 {
		t.Fatalf("commit permission=%d changed=%t err=%v", state, changed, err)
	}
	plan := currentDungeonSettlementPacketPlanForTest(t, rt, 73)
	plan.DungeonPermissionBody = body
	rt.settlementResultPlan = &plan
	request := make([]byte, currentDungeonPlayResultBaseSize)
	request[currentDungeonPlayResultClientRankPointOffset] = 73
	conn.write.Reset()
	if err := s.handleDungeonSetPlayResult(session, request); err != nil {
		t.Fatal(err)
	}
	stream := conn.write.Bytes()
	for i, want := range []uint16{currentDungeonPlayResultNoticeMsgID, currentDungeonCharacterStateMsgID, currentDungeonClearRewardMsgID, currentDungeonResourceStateMsgID} {
		p, rest := splitGameServerUpperPacket(t, stream)
		if p.Header.MsgID != want {
			t.Fatalf("packet %d=%d want %d", i, p.Header.MsgID, want)
		}
		if want == currentDungeonResourceStateMsgID && !bytes.Equal(p.Body, body) {
			t.Fatalf("permission body=%x want=%x", p.Body, body)
		}
		stream = rest
	}
	if len(stream) != 0 || !rt.settlementDungeonPermissionSent {
		t.Fatalf("trailing=%x sent=%t", stream, rt.settlementDungeonPermissionSent)
	}
	written := conn.write.Len()
	if err := s.handleDungeonSetPlayResult(session, request); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != written {
		t.Fatal("replayed settlement resent permission")
	}
}

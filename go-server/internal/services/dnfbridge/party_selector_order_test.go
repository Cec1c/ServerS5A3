package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type selectorOrderConn struct {
	net.Conn
	beforeWrite func([]byte) error
}

func (c *selectorOrderConn) Write(p []byte) (int, error) {
	if err := c.beforeWrite(p); err != nil {
		return 0, err
	}
	return c.Conn.Write(p)
}

func selectorOrderFourPlayers(t *testing.T) (*Service, []*gameSession, []*bufferConn) {
	t.Helper()
	repos, s, leader, member, lc, mc := newTownCoPresenceTestService(t)
	players := []*gameSession{leader, member}
	conns := []*bufferConn{lc, mc}
	state := townCoPresencePartyState()
	for i := 3; i <= 4; i++ {
		id := uint16(1000 + i)
		account := fmt.Sprintf("dnf:%d", i)
		if err := repos.Account.Save(context.Background(), dnfrepo.AccountRecord{AccountID: account, State: "active"}); err != nil {
			t.Fatal(err)
		}
		if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{CharacterID: fmt.Sprint(id), AccountID: account, Name: account, Job: "0", Level: 90}); err != nil {
			t.Fatal(err)
		}
		conn := &bufferConn{}
		p := &gameSession{conn: conn, channel: leader.channel, accountID: account, selectedCharacterID: id,
			townActorOwnerChannel: 42, connectionTownActorOwnerChannel: 42, townSceneReadyCharacterID: id}
		p.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: id, PositionValid: true, PositionX: 450, PositionY: 234}
		s.bindGameSessionCharacter(p, id)
		s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: id, AccountID: account, Name: account, TownID: 38, AreaID: 2, PositionX: 450, PositionY: 234, Direction: 5, AreaState: 3, Session: p})
		players = append(players, p)
		conns = append(conns, conn)
		state.Members = append(state.Members, alignedcmd.PartyMemberState{UserID: id, UserState: 1, HPPercent: 100, MPPercent: 100})
	}
	for _, p := range players {
		storeRuntimePartyState(p, state)
		if ready, reason := s.currentTownEnterSelectReady(p); !ready {
			t.Fatalf("fixture %d not ready: %s", p.selectedCharacterID, reason)
		}
	}
	return s, players, conns
}

// The leader can immediately emit its one-shot native peer selector list after
// receiving the open-page response. Observe writes across all four connections,
// not just the packet order within each individual connection.
func TestPartySelectorFollowersPreparedBeforeLeaderOpen(t *testing.T) {
	s, players, conns := selectorOrderFourPlayers(t)
	for _, round := range []struct {
		name   string
		leader int
	}{{"cold", 0}, {"reopen", 0}, {"leader_transfer", 2}} {
		t.Run(round.name, func(t *testing.T) {
			state := runtimePartyStateSnapshot(players[0])
			state.UserID = players[round.leader].selectedCharacterID
			seen := make([]bool, len(players))
			leaderOpened := false
			for i, p := range players {
				storeRuntimePartyState(p, state)
				p.enterSelectDungeonSent, p.enterSelectDungeonAckSent, p.enterSelectDungeonContextSent = false, false, false
				conns[i].write.Reset()
				index := i
				p.conn = &selectorOrderConn{Conn: conns[i], beforeWrite: func(b []byte) error {
					if len(b) < 16 {
						t.Fatal("short upper packet")
					}
					op := binary.LittleEndian.Uint16(b[1:3])
					if index == round.leader && (op == 15 || op == currentDungeonContextMsgID) {
						for peer, ready := range seen {
							if peer != round.leader && !ready {
								t.Errorf("leader op%d precedes follower %d op27", op, players[peer].selectedCharacterID)
							}
						}
						leaderOpened = true
					}
					if op == currentDungeonContextMsgID {
						seen[index] = true
					}
					return nil
				}}
			}
			if err := s.sendEnterSelectDungeonState(players[round.leader], "selector_order", false, true); err != nil {
				t.Fatal(err)
			}
			if !leaderOpened {
				t.Fatal("leader never opened")
			}
			for i, p := range players {
				if !seen[i] {
					t.Errorf("member %d missing context", i)
				}
				if p.preDungeonContextPlayerStateSent || p.selectPreviewActorRemoved {
					t.Error("selector replaced town actor")
				}
				if got := runtimePartyStateSnapshot(p); got.PartyID != state.PartyID || got.UserID != state.UserID {
					t.Error("party identity changed")
				}
			}
		})
	}
}

func TestPartySelectorFollowerWriteFailureDoesNotOpenLeader(t *testing.T) {
	for _, failed := range []int{1, 3} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			s, players, conns := selectorOrderFourPlayers(t)
			failure := errors.New("selector follower write failed")
			players[failed].conn = &selectorOrderConn{Conn: conns[failed], beforeWrite: func([]byte) error { return failure }}
			for _, c := range conns {
				c.write.Reset()
			}
			if err := s.sendEnterSelectDungeonState(players[0], "selector_order_failure", false, true); !errors.Is(err, failure) {
				t.Fatalf("error=%v", err)
			}
			assertLeaderSelectorFailureAck(t, conns[0])
			if players[0].enterSelectDungeonContextSent {
				t.Fatal("leader sent op27 context despite follower failure")
			}
		})
	}
}

func TestPartySelectorFollowerWriteAndReplyFailureJoinErrors(t *testing.T) {
	s, players, conns := selectorOrderFourPlayers(t)
	followerFailure := errors.New("selector follower write failed")
	replyFailure := errors.New("selector failure reply write failed")
	players[1].conn = &selectorOrderConn{Conn: conns[1], beforeWrite: func([]byte) error { return followerFailure }}
	players[0].conn = &selectorOrderConn{Conn: conns[0], beforeWrite: func([]byte) error { return replyFailure }}
	for _, c := range conns {
		c.write.Reset()
	}
	err := s.sendEnterSelectDungeonState(players[0], "selector_order_failure_reply", false, true)
	if !errors.Is(err, followerFailure) {
		t.Fatalf("missing follower failure: %v", err)
	}
	if !errors.Is(err, replyFailure) {
		t.Fatalf("missing reply failure: %v", err)
	}
}

// assertLeaderSelectorFailureAck 断言队长只收到原协议 EnterSelectDungeon 失败回包：
// 有明确失败 ACK、无成功 ACK、无 op27 选图上下文。
func assertLeaderSelectorFailureAck(t *testing.T, conn *bufferConn) {
	t.Helper()
	stream := conn.write.Bytes()
	sawFailure := false
	for len(stream) != 0 {
		packet, rest := splitLongHengGameServerUpperPacket(t, stream)
		switch {
		case packet.Header.MsgID == uint16(dnfenum.CmdPacketEnterSelectDungeon) && len(packet.Body) >= 2 && packet.Body[0] == 0:
			if packet.Body[1] != currentSelectDungeonInvalidSelectionStateReason {
				t.Fatalf("leader failure reason = %#x, want %#x", packet.Body[1], currentSelectDungeonInvalidSelectionStateReason)
			}
			sawFailure = true
		case packet.Header.MsgID == uint16(dnfenum.CmdPacketEnterSelectDungeon) && len(packet.Body) >= 1 && packet.Body[0] == 1:
			t.Fatal("leader sent success ack despite follower failure")
		case packet.Header.MsgID == currentDungeonContextMsgID:
			t.Fatal("leader sent op27 context despite follower failure")
		}
		stream = rest
	}
	if !sawFailure {
		t.Fatal("leader sent no EnterSelectDungeon failure ack")
	}
}

package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestRaidTwentyMemberRosterFanout(t *testing.T) {
	s, leader, rejoining, lc, mc, initial := prepareApprovedCurrentRaid(t)
	members := []*gameSession{leader, rejoining}
	conns := []*bufferConn{lc, mc}
	live := s.raids[initial.RaidKey]
	repos, _ := s.repositoryGroup()
	for i := 2; i < 20; i++ {
		id := uint16(100 + i)
		key := strconv.Itoa(int(id))
		account := "dnf:" + key
		conn := &bufferConn{}
		member := &gameSession{conn: conn, connID: key, channel: leader.channel, accountID: account, loginPacketSeen: true, accountFromToken: true, selectedCharacterID: id}
		if err := repos.Character.Save(context.Background(), repo.CharacterRecord{CharacterID: key, AccountID: account, Name: key, Level: 90, Job: "0"}); err != nil {
			t.Fatal(err)
		}
		s.bindGameSessionCharacter(member, id)
		live.Members = append(live.Members, runtimeRaidMemberState{CharID: id, Name: key, Level: 90, GroupIndex: byte(i/4 + 1), SlotOrder: byte(i % 4)})
		members = append(members, member)
		conns = append(conns, conn)
	}
	for _, m := range members {
		m.connectionTownActorOwnerChannel = byte(m.channel.ID)
	}
	check := func(t *testing.T, rejoin, departed bool) {
		t.Helper()
		expected, err := s.buildRuntimeRaidRosterBody(*live)
		if err != nil {
			t.Fatal(err)
		}
		var total int
		for _, conn := range conns {
			_, appearances := currentRaidProfileAndAppearanceFrameCounts(t, conn.write.Bytes())
			total += appearances
		}
		t.Logf("mode8 frames: %d", total)
		for i, conn := range conns {
			if departed && members[i] == rejoining {
				continue
			}
			roster, actors := 0, map[uint16]int{}
			for _, frame := range splitUpperFrames(t, conn.write.Bytes()) {
				if frame.msgID == raid.RaidMemberRefreshMsgID && bytes.Equal(frame.body, expected) {
					roster++
				}
				if frame.msgID == 2 && len(frame.body) >= 78 && frame.body[0] == 8 {
					actors[binary.LittleEndian.Uint16(frame.body[76:78])]++
				}
			}
			if roster != 1 {
				t.Fatalf("viewer %d roster frames=%d", i, roster)
			}
			for _, actor := range members {
				want := 0
				if rejoin && members[i] != actor && (members[i] == rejoining || actor == rejoining) {
					want = 1
				}
				if got := actors[actor.selectedCharacterID]; got != want {
					t.Fatalf("viewer %d actor %d frames=%d want %d", i, actor.selectedCharacterID, got, want)
				}
			}
		}
	}
	t.Run("rename", func(t *testing.T) {
		if err := s.handleOnlineModifyRaidInfo(leader, 670, currentRaidRequestFixture(0, "rename", 0, 0)); err != nil {
			t.Fatal(err)
		}
		check(t, false, false)
	})
	for _, c := range conns {
		c.write.Reset()
	}
	live.Started = true
	t.Run("rejoin", func(t *testing.T) {
		if err := s.handleOnlineRejoinRaid(rejoining, 668, rejoinContextRequest(live.RaidKey)); err != nil {
			t.Fatal(err)
		}
		check(t, true, false)
	})
	for _, c := range conns {
		c.write.Reset()
	}
	t.Run("departure", func(t *testing.T) {
		if err := s.handleOnlineLeaveRaid(rejoining, 665, []byte{0, 0}); err != nil {
			t.Fatal(err)
		}
		check(t, false, true)
	})
}

func TestCrossSceneProfileExpiredRaidBudgetStillDeliversRoster(t *testing.T) {
	s, a, b, ac, bc, state := prepareApprovedCurrentRaid(t)
	a.connectionTownActorOwnerChannel = byte(a.channel.ID)
	b.connectionTownActorOwnerChannel = byte(b.channel.ID)
	var log bytes.Buffer
	s.logger = slog.New(slog.NewTextHandler(&log, nil))
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()
	batch := newCurrentRaidProfileBatch(s, ctx)
	s.sendCurrentRaidCrossSceneProfiles(batch, a, state)
	if err := s.broadcastRuntimeRaidProjectionInContext(ctx, state, true, 0, true, false); err != nil {
		t.Fatal(err)
	}
	for _, conn := range []*bufferConn{ac, bc} {
		frames := splitUpperFrames(t, conn.write.Bytes())
		if len(frames) == 0 || frames[0].msgID != raid.RaidMemberRefreshMsgID {
			t.Fatalf("missing roster after deadline: %v", crossSceneProfileActorFrameIDs(frames))
		}
	}
	if bytes.Contains(log.Bytes(), []byte("game-peer-userinfo-profile-refresh-failed")) {
		t.Fatal("expired batch attempted another appearance")
	}
}

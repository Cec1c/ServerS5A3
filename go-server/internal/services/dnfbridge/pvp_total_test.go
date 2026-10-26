package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfpvp "longheng.io/server/internal/modules/dnf/pvp"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
	"testing"
	"time"
)

func totalTestPackets(t *testing.T, p *gameSession) []dnfproto.ChannelPacket {
	t.Helper()
	conn := p.conn.(*bufferConn)
	wire := append([]byte(nil), conn.write.Bytes()...)
	conn.write.Reset()
	var packets []dnfproto.ChannelPacket
	for len(wire) > 0 {
		p, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
		packets = append(packets, p)
		wire = rest
	}
	return packets
}

func TestPVPTotalSelectionLoadingAndThreeLives(t *testing.T) {
	s := pvpRoomTestService(t)
	s.gameplayTimers = newFakeCurrentDungeonDeathTimerQueue()
	a := &gameSession{conn: &bufferConn{}, accountID: "pvp-29", selectedCharacterID: 29, channel: s.catalog.Channels()[1]}
	b := &gameSession{conn: &bufferConn{}, accountID: "pvp-30", selectedCharacterID: 30, channel: a.channel}
	viewer := &gameSession{conn: &bufferConn{}, selectedCharacterID: 31, channel: a.channel}
	seedPVPRoomTestSession(s, a)
	room := a.pvpRoom
	b.pvpRoom = room
	viewer.pvpRoom = room
	room.battleMode, room.started = 8, true
	room.match = &currentPVPMatch{winner: 255, total: newCurrentPVPTotalMatch(), startedAt: s.gameplayNow()}
	room.members[1] = &currentPVPMember{session: b, characterID: 30}
	room.members[2] = &currentPVPMember{session: viewer, characterID: 31}
	room.seatState[1], room.seatState[2] = 2, 3
	s.pvpTotalCatalog = pvpDailyTestCatalog()
	repos, _ := s.repositoryGroup()
	for seat, p := range []*gameSession{a, b} {
		if err := repos.Account.Save(context.Background(), repo.AccountRecord{AccountID: p.accountID}); err != nil {
			t.Fatal(err)
		}
		team := &currentPVPTotalTeam{}
		for i, id := range []int{int(p.selectedCharacterID), 40 + seat*3, 41 + seat*3} {
			c := repo.CharacterRecord{CharacterID: strconv.Itoa(id), AccountID: p.accountID, Name: strconv.Itoa(id), Slot: i, Level: 90, Job: strconv.Itoa(i), Stats: map[string]int64{}}
			if err := repos.Character.Save(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			team.snapshot.Team.Characters[i], team.snapshot.Characters[i] = c.CharacterID, c
		}
		room.members[seat].total = team
	}
	for _, p := range []*gameSession{a, b, viewer} {
		if err := s.handleCurrentPVPTotalOrder(p, []byte{0}); err != nil {
			t.Fatal(err)
		}
	}
	if room.match.total.selected[0] != 255 {
		t.Fatal("accepted selection before loading")
	}
	if err := s.handleCurrentPVPMatchSignal(a, 298, nil); err != nil {
		t.Fatal(err)
	}
	if len(totalTestPackets(t, a)) != 0 {
		t.Fatal("opened selection before peer loaded")
	}
	if err := s.handleCurrentPVPMatchSignal(b, 298, nil); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*gameSession{a, b, viewer} {
		packets := totalTestPackets(t, p)
		if len(packets) != 1 || packets[0].Header.MsgID != 804 || len(packets[0].Body) != 0 {
			t.Fatal("invalid native selection", packets)
		}
	}
	if err := s.handleCurrentPVPMatchSignal(b, 298, nil); err != nil {
		t.Fatal(err)
	}
	if len(totalTestPackets(t, b)) != 0 {
		t.Fatal("duplicate load reopened selection")
	}
	if err := s.handleCurrentPVPTotalOrder(a, []byte{2}); err != nil {
		t.Fatal(err)
	}
	if err := s.handleCurrentPVPTotalOrder(a, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := s.handleCurrentPVPTotalOrder(viewer, []byte{0}); err != nil {
		t.Fatal(err)
	}
	if room.match.total.selected[0] != 2 || room.match.total.selected[2] != 255 {
		t.Fatal("duplicate/observer changed selection")
	}
	if err := s.handleCurrentPVPTotalOrder(b, []byte{0}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*gameSession{a, b, viewer} {
		packets := totalTestPackets(t, p)
		if len(packets) != 1 || packets[0].Header.MsgID != 803 || !bytes.Equal(packets[0].Body, []byte{29, 0, 0, 0, 2, 30, 0, 0, 0, 0}) {
			t.Fatal("invalid fixed two-owner selection", packets)
		}
	}
	death := []byte{30, 0, 29, 0, 0, 80, 0}
	for round := 0; round < 3; round++ {
		if err := s.handleCurrentPVPDeath(b, death); err != nil {
			t.Fatal(err)
		}
		if round < 2 {
			if room.match.resultSent || room.match.dead[1] || !room.match.total.defeated[1][round] || !room.match.total.choosing {
				t.Fatal("team ended on early character death")
			}
			kills := room.match.kills[0]
			if err := s.handleCurrentPVPDeath(b, death); err != nil {
				t.Fatal(err)
			}
			if room.match.kills[0] != kills {
				t.Fatal("repeated death counted twice")
			}
			if err := s.handleCurrentPVPTotalOrder(b, []byte{byte(round)}); err != nil {
				t.Fatal(err)
			}
			if room.match.total.selected[1] != 255 {
				t.Fatal("defeated character reused")
			}
			if err := s.handleCurrentPVPTotalOrder(b, []byte{byte(round + 1)}); err != nil {
				t.Fatal(err)
			}
			if room.match.total.choosing || room.match.total.selected[0] != 2 {
				t.Fatal("survivor lost its actor")
			}
		} else if !room.match.resultSent || room.match.winner != 0 {
			t.Fatal("three exhausted characters did not end match")
		}
	}
	if room.match.total.kills[0] != [3]uint32{0, 0, 3} {
		t.Fatal("per-character all-kill lost")
	}
	for _, p := range []*gameSession{a, b} {
		for _, packet := range totalTestPackets(t, p) {
			if packet.Header.MsgID == 48 && len(packet.Body) != 129 {
				t.Fatal("total certification truncated", len(packet.Body))
			}
		}
	}
	for _, id := range room.members[0].total.snapshot.Team.Characters {
		c, _, _ := repos.Character.Load(context.Background(), id)
		if c.Stats[dnfpvp.ExperienceKey] != 450 || c.Stats[dnfpvp.WinsKey] != 0 {
			t.Fatal("manual total settlement polluted ranked counters", c.Stats)
		}
	}
}

func TestPVPTotalRequestNameAndNativeSkillSlots(t *testing.T) {
	var w packetWriter
	w.writeRawDstr(rosterNameBytes("王者队伍"))
	w.writeBytes([]byte{1, 4, 9})
	name, slots, err := parsePVPTotalTeamName(w.bytes(), 3)
	if err != nil || name != "王者队伍" || !bytes.Equal(slots, []byte{1, 4, 9}) {
		t.Fatal(name, slots, err)
	}
	for _, b := range [][]byte{nil, w.bytes()[:5], append(w.bytes(), 0), {255, 255, 255, 255}} {
		if _, _, err := parsePVPTotalTeamName(b, 3); err == nil {
			t.Fatal("bad string accepted", b)
		}
	}
	r := repo.SkillRecord{Skills: map[int64]repo.SkillState{20: {Level: 3}, 46: {Level: 7}}, Layouts: map[int]repo.SkillLayout{0: {198: 46}}}
	b, err := buildCurrentPVPTotalSkills(r, 0)
	if err != nil || !bytes.Equal(b, []byte{2, 0, 198, 46, 0, 7, 255, 20, 0, 3}) {
		t.Fatal("actual shortcuts/learned levels lost", b, err)
	}
	request := []byte{1, 7, 0, 0, 0, 255, 255, 255, 255, 255, 255, 255, 255, 0}
	parsed, err := parseCurrentPVPMatchingRequest(request)
	if err != nil || parsed.gameType != 7 {
		t.Fatal(parsed, err)
	}
	binary.LittleEndian.PutUint16(request[1:], 8)
	if _, err := parseCurrentPVPMatchingRequest(request); err == nil {
		t.Fatal("confused scene8 with gameType7")
	}
}

func TestPVPTotalTimeoutExhaustsOnlyCurrentActorAndWatchdog(t *testing.T) {
	s := pvpRoomTestService(t)
	q := newFakeCurrentDungeonDeathTimerQueue()
	s.gameplayTimers = q
	a := &gameSession{conn: &bufferConn{}, selectedCharacterID: 29, channel: s.catalog.Channels()[1]}
	b := &gameSession{conn: &bufferConn{}, selectedCharacterID: 30, channel: a.channel}
	seedPVPRoomTestSession(s, a)
	room := a.pvpRoom
	room.battleMode, room.started = 8, true
	room.match = &currentPVPMatch{winner: 255, startedAt: q.now, total: newCurrentPVPTotalMatch()}
	room.members[1] = &currentPVPMember{session: b, characterID: 30}
	room.seatState[1] = 2
	b.pvpRoom = room
	m := room.match.total
	m.selected[0], m.selected[1] = 2, 0
	m.loaded[0], m.loaded[1] = true, true
	if err := s.finishCurrentPVPTotalTimeoutLocked(room, a, 0); err != nil {
		t.Fatal(err)
	}
	if m.defeated[1] != [3]bool{true, false, false} || !m.choosing || room.match.settlementPending || m.selected[0] != 2 {
		t.Fatal("timeout ended whole team")
	}
	task := q.task(t, 0)
	q.now = q.now.Add(45 * time.Second)
	q.fire(task, false)
	if m.choosing || m.selected[1] != 1 || m.selected[0] != 2 {
		t.Fatal("watchdog selected exhausted actor or replaced survivor")
	}
	q.fire(task, true)
	if m.selected[1] != 1 {
		t.Fatal("stale selection timer changed round")
	}
	if err := s.finishCurrentPVPTotalTimeoutLocked(room, a, 255); err != nil {
		t.Fatal(err)
	}
	if !m.defeated[0][2] || !m.defeated[1][1] || room.match.settlementPending {
		t.Fatal("draw did not exhaust both current actors")
	}
	q.fire(task, true)
	if !m.choosing || m.selected[0] != 255 || m.selected[1] != 255 {
		t.Fatal("previous phase watchdog selected actors in new phase")
	}
	if err := s.scheduleCurrentPVPTotalPhaseLocked(room, false); err != nil {
		t.Fatal(err)
	}
	s.removeCurrentPVPMemberLocked(b, false)
	if _, _, active := q.counts(); active != 0 {
		t.Fatal("disconnect left total timer", active)
	}
}

package dnfbridge

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type r160ProfileRow struct {
	Character uint16 `json:"character"`
	Channel   byte   `json:"channel"`
	Server    byte   `json:"server"`
	Level     uint16 `json:"level"`
	Grow      byte   `json:"grow"`
	Job       byte   `json:"job"`
	Name      string `json:"name"`
}

type r160RosterRow struct {
	Character uint16 `json:"character"`
	Name      string `json:"name"`
	Job       byte   `json:"job"`
	Grow      byte   `json:"grow"`
	Group     byte   `json:"group"`
	Slot      byte   `json:"slot"`
}

type r160ActorEvidence struct {
	Receiver uint16 `json:"receiver"`
	Actor    uint16 `json:"actor"`
	Mode0    int    `json:"mode0"`
	Mode1    int    `json:"mode1"`
	Op9      int    `json:"op9"`
}

type r160FailWritesConn struct {
	bufferConn
	fail   bool
	writes int
}

func (c *r160FailWritesConn) Write(data []byte) (int, error) {
	c.writes++
	if c.fail {
		return 0, errors.New("injected peer actor write failure")
	}
	return c.bufferConn.Write(data)
}

type r160OwnershipSnapshot struct {
	Participation map[uint16]struct {
		RunID      string
		DungeonID  uint32
		Phase      byte
		Attempt    uint64
		Generation uint64
	}
	Cleared      map[string]currentRaidClearedRun
	MemberGroups map[uint16]byte
}

func r160CaptureRaidOwnership(f *raidStartGuardFixture) r160OwnershipSnapshot {
	f.service.raidMu.Lock()
	defer f.service.raidMu.Unlock()
	snapshot := r160OwnershipSnapshot{
		Participation: make(map[uint16]struct {
			RunID      string
			DungeonID  uint32
			Phase      byte
			Attempt    uint64
			Generation uint64
		}, 4),
		Cleared:      map[string]currentRaidClearedRun{},
		MemberGroups: map[uint16]byte{},
	}
	if f.state.Battle == nil {
		return snapshot
	}
	for id, entry := range f.state.Battle.Participation {
		snapshot.Participation[id] = struct {
			RunID      string
			DungeonID  uint32
			Phase      byte
			Attempt    uint64
			Generation uint64
		}{entry.RunID, entry.DungeonID, entry.Phase, entry.Attempt, entry.Generation}
	}
	for runID, clear := range f.state.Battle.ClearedRuns {
		clear.Participants = append([]uint16(nil), clear.Participants...)
		snapshot.Cleared[runID] = clear
	}
	for id, group := range f.state.Battle.MemberGroups {
		snapshot.MemberGroups[id] = group
	}
	return snapshot
}

func r160UpperPeerActorCounts(frames []upperFrame, peer uint16) (mode0, mode1, op9 int) {
	for _, frame := range frames {
		if frame.msgID == 2 && len(frame.body) >= 5 {
			switch {
			case frame.body[0] == 0 && len(frame.body) >= 78 && binary.LittleEndian.Uint16(frame.body[76:]) == peer:
				mode0++
			case frame.body[0] == 1 && len(frame.body) >= 23 && binary.LittleEndian.Uint16(frame.body[21:]) == peer:
				mode1++
			}
		}
		if frame.msgID == 9 && len(frame.body) >= 12 &&
			binary.LittleEndian.Uint16(frame.body[4:]) == peer && frame.body[9] == currentSceneOp9ActorDisplayKind {
			op9++
		}
	}
	return mode0, mode1, op9
}

func r160ParseProfiles(t *testing.T, body []byte) []r160ProfileRow {
	t.Helper()
	if len(body) < 4 {
		t.Fatalf("752 shorter than count: %x", body)
	}
	count := int(binary.LittleEndian.Uint32(body))
	if len(body) != 4+57*count {
		t.Fatalf("752 length=%d count=%d", len(body), count)
	}
	rows := make([]r160ProfileRow, 0, count)
	for i := 0; i < count; i++ {
		row := body[4+i*57 : 4+(i+1)*57]
		rows = append(rows, r160ProfileRow{
			Character: binary.LittleEndian.Uint16(row),
			Channel:   row[3],
			Level:     binary.LittleEndian.Uint16(row[4:]),
			Grow:      row[6],
			Job:       row[7],
			Server:    row[8],
			Name:      string(trimPacketName(row[10:40])),
		})
	}
	return rows
}

func trimPacketName(raw []byte) []byte {
	for i, value := range raw {
		if value == 0 {
			return raw[:i]
		}
	}
	return raw
}

func r160ParseRoster(t *testing.T, body []byte) []r160RosterRow {
	t.Helper()
	if len(body) < 9 {
		t.Fatalf("591 shorter than header: %x", body)
	}
	offset := 8
	count := int(body[offset])
	offset++
	rows := make([]r160RosterRow, 0, count)
	for i := 0; i < count; i++ {
		if offset+3 > len(body) {
			t.Fatalf("591 row %d short at character: %x", i, body)
		}
		row := r160RosterRow{Character: binary.LittleEndian.Uint16(body[offset:])}
		offset += 3
		if offset+4 > len(body) {
			t.Fatalf("591 row %d short at name length", i)
		}
		nameLength := int(binary.LittleEndian.Uint32(body[offset:]))
		offset += 4
		if nameLength < 0 || offset+nameLength+14 > len(body) {
			t.Fatalf("591 row %d invalid name length %d", i, nameLength)
		}
		row.Name = string(body[offset : offset+nameLength])
		offset += nameLength
		row.Job = body[offset]
		row.Grow = body[offset+1]
		row.Group = body[offset+2]
		row.Slot = body[offset+3]
		offset += 4
		offset += 4 // field48 level
		offset += 3 // field52..56
		offset += 4 // field60
		offset += 2 // field64
		offset += 4 // field66 leader marker
		offset++    // current-client extra byte
		if offset+4 > len(body) {
			t.Fatalf("591 row %d short at extra name length", i)
		}
		extraLength := int(binary.LittleEndian.Uint32(body[offset:]))
		offset += 4
		if extraLength < 0 || offset+extraLength > len(body) {
			t.Fatalf("591 row %d invalid extra name length %d", i, extraLength)
		}
		offset += extraLength
		rows = append(rows, row)
	}
	if offset != len(body) {
		t.Fatalf("591 trailing bytes start=%d total=%d", offset, len(body))
	}
	return rows
}

func r160ParseOccupancy(t *testing.T, body []byte) map[uint32][]uint16 {
	t.Helper()
	if len(body) < 7 || body[0] != 1 {
		t.Fatalf("584 invalid body %x", body)
	}
	count := int(body[6])
	if len(body) != 7+2*count {
		t.Fatalf("584 length=%d count=%d", len(body), count)
	}
	ids := make([]uint16, 0, count)
	for i := 0; i < count; i++ {
		ids = append(ids, binary.LittleEndian.Uint16(body[7+i*2:]))
	}
	return map[uint32][]uint16{binary.LittleEndian.Uint32(body[1:5]): ids}
}

func r160FindFrames(t *testing.T, raw []byte) []dnfproto.ChannelPacket {
	t.Helper()
	frames := make([]dnfproto.ChannelPacket, 0, 32)
	for len(raw) > 0 {
		packet, rest := splitGameServerUpperPacket(t, raw)
		raw = rest
		frames = append(frames, packet)
	}
	return frames
}

func r160ActorKey(t *testing.T, frame dnfproto.ChannelPacket) (mode byte, key uint16, ok bool) {
	t.Helper()
	if frame.Header.MsgID != 2 || len(frame.Body) < 5 {
		return 0, 0, false
	}
	mode = frame.Body[0]
	switch mode {
	case 0:
		if len(frame.Body) < 78 {
			return mode, 0, false
		}
		return mode, binary.LittleEndian.Uint16(frame.Body[76:]), true
	case 1:
		if len(frame.Body) < 23 {
			return mode, 0, false
		}
		return mode, binary.LittleEndian.Uint16(frame.Body[21:]), true
	default:
		return mode, 0, false
	}
}

func r160AntonLegalFixture(t *testing.T) (*raidStartGuardFixture, []*gameSession, []*deadlineAuditV10Conn) {
	t.Helper()
	catalog, _ := actualAntonRewardFixture(t)
	names := []string{"r160-sword", "r160-priest", "r160-gunner"}
	jobs := []string{"0", "1", "2"}
	grows := []int64{0x21, 0x11, 0x31}
	setup := func(i int, _ uint16, character repo.CharacterRecord) repo.CharacterRecord {
		character.Name = names[i]
		character.Job = jobs[i]
		character.Stats["grow_type"] = grows[i]
		return character
	}
	f, owners, conns, _ := deadlineAuditV10AdmitConfigured(t, catalog.source.(*platformpvf.Archive), false, 3, setup)
	if len(owners) != 3 {
		t.Fatalf("owners=%d want 3", len(owners))
	}
	// The legacy admission fixture pre-seeds only member keys. Refill the same
	// rows through the production constructor so 591 models a live lobby whose
	// names/job/grow came from the repository, without changing group placement.
	f.service.raidMu.Lock()
	for i, member := range f.state.Members {
		f.state.Members[i] = f.service.newRuntimeRaidMemberLocked(member.CharID, member.GroupIndex, member.SlotOrder)
	}
	f.service.raidMu.Unlock()
	return f, owners, conns
}

func r160EnterRaidGroup(t *testing.T, f *raidStartGuardFixture, owners []*gameSession, leader int, dungeonID uint32) []uint16 {
	t.Helper()
	p := owners[leader]
	req := encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{DungeonID: dungeonID, Difficulty: 2})
	deadlineAuditV10Event(t, f.service, p, func() error { return f.service.handleDungeonSelectUpper(p, req) })
	group := f.state.Members[leader].GroupIndex
	ids := make([]uint16, 0, runtimeRaidGroupSize)
	for i, member := range f.state.Members {
		if member.GroupIndex != group {
			continue
		}
		peer := owners[i]
		if peer.dungeon.runtime == nil || peer.dungeon.runtime.raidEntry == nil {
			t.Fatalf("character %d did not receive a legal runtime at select", peer.selectedCharacterID)
		}
		ids = append(ids, peer.selectedCharacterID)
		deadlineAuditV10Event(t, f.service, peer, func() error {
			return f.service.sendFinishLoadingStatus(peer, make([]byte, 8))
		})
	}
	until := time.Now().Add(createWriteTimeout)
	for time.Now().Before(until) {
		f.service.raidMu.Lock()
		done := true
		for _, id := range ids {
			if f.state.Battle == nil || f.state.Battle.Participation[id].Session == nil {
				done = false
			}
		}
		f.service.raidMu.Unlock()
		if done {
			break
		}
		time.Sleep(time.Millisecond)
	}
	return ids
}

func r160EnterRaidMember(t *testing.T, f *raidStartGuardFixture, owner *gameSession, dungeonID uint32) {
	t.Helper()
	req := encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{DungeonID: dungeonID, Difficulty: 2})
	deadlineAuditV10Event(t, f.service, owner, func() error { return f.service.handleDungeonSelectUpper(owner, req) })
	if owner.dungeon.runtime == nil || owner.dungeon.runtime.raidEntry == nil {
		t.Fatalf("character %d did not receive a legal solo runtime at select", owner.selectedCharacterID)
	}
	deadlineAuditV10Event(t, f.service, owner, func() error {
		return f.service.sendFinishLoadingStatus(owner, make([]byte, 8))
	})
	until := time.Now().Add(createWriteTimeout)
	for time.Now().Before(until) {
		f.service.raidMu.Lock()
		done := f.state.Battle != nil && f.state.Battle.Participation[owner.selectedCharacterID].Session != nil
		f.service.raidMu.Unlock()
		if done {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("character %d solo entry did not commit participation", owner.selectedCharacterID)
}

func r160MakeSolo(t *testing.T, f *raidStartGuardFixture, owners ...*gameSession) {
	t.Helper()
	manager := f.service.runtimePartyManagerForService()
	for _, owner := range owners {
		identity, ok := f.service.boundGameSessionCharacterSnapshot(owner)
		if !ok {
			t.Fatalf("character %d identity unavailable", owner.selectedCharacterID)
		}
		manager.Leave(identity.character, identity.generation)
		storeRuntimePartyState(owner, alignedcmd.PartyState{})
		forgetDungeonPartyRunKey(owner)
	}
}

func TestR160LegalEntryProjectionAndPeerActorLifecycle(t *testing.T) {
	f, owners, conns := r160AntonLegalFixture(t)
	for _, conn := range conns {
		conn.reset()
	}
	first := r160EnterRaidGroup(t, f, owners, 0, 210)
	second := r160EnterRaidGroup(t, f, owners, 2, 210)
	if fmt.Sprint(first) != "[19 20]" || fmt.Sprint(second) != "[21]" {
		t.Fatalf("legal groups first=%v second=%v", first, second)
	}

	// The projection is committed by the later landing member. Wait for the
	// authoritative idempotent sets instead of assuming an arbitrary sleep.
	until := time.Now().Add(createWriteTimeout)
	for time.Now().Before(until) {
		owners[0].dungeonPartyRunMu.Lock()
		left := owners[0].dungeonPeerProjected[20]
		owners[0].dungeonPartyRunMu.Unlock()
		owners[1].dungeonPartyRunMu.Lock()
		right := owners[1].dungeonPeerProjected[19]
		owners[1].dungeonPartyRunMu.Unlock()
		if left && right {
			break
		}
		time.Sleep(time.Millisecond)
	}

	actorEvidence := make([]r160ActorEvidence, 0, 2)
	for i, receiver := range []*gameSession{owners[0], owners[1]} {
		peerID := []uint16{20, 19}[i]
		frames := r160FindFrames(t, conns[i].bytes())
		evidence := r160ActorEvidence{Receiver: receiver.selectedCharacterID, Actor: peerID}
		for _, frame := range frames {
			if mode, key, ok := r160ActorKey(t, frame); ok && key == peerID {
				if mode == 0 {
					evidence.Mode0++
				} else {
					evidence.Mode1++
				}
			}
			if frame.Header.MsgID == 9 && len(frame.Body) >= 12 &&
				binary.LittleEndian.Uint16(frame.Body[4:]) == peerID && frame.Body[9] == currentSceneOp9ActorDisplayKind {
				evidence.Op9++
			}
		}
		if evidence.Mode0 == 0 || evidence.Mode1 == 0 || evidence.Op9 == 0 {
			t.Fatalf("legal Anton entry did not project a complete peer actor: %+v", evidence)
		}
		actorEvidence = append(actorEvidence, evidence)
	}

	occupancyEvidence := make(map[uint16]map[uint32][]uint16)
	activeParticipation := make(map[uint16]bool)
	for i, conn := range conns {
		receiver := owners[i].selectedCharacterID
		for _, frame := range r160FindFrames(t, conn.bytes()) {
			if frame.Header.Classification != 0 || frame.Header.MsgID != 584 || len(frame.Body) < 7 || frame.Body[5] != 2 {
				continue
			}
			decoded := r160ParseOccupancy(t, frame.Body)
			occupancyEvidence[receiver] = decoded
			for _, id := range decoded[210] {
				activeParticipation[id] = true
			}
		}
	}
	activeIDs := make([]int, 0, len(activeParticipation))
	for id := range activeParticipation {
		activeIDs = append(activeIDs, int(id))
	}
	sort.Ints(activeIDs)
	if fmt.Sprint(activeIDs) != "[19 20 21]" {
		t.Fatalf("actual-entry 584 add set=%v want [19 20 21]", activeIDs)
	}

	for _, conn := range conns {
		conn.reset()
	}
	body := make([]byte, 4)
	binary.LittleEndian.PutUint32(body, f.state.RaidKey)
	deadlineAuditV10Event(t, f.service, owners[2], func() error {
		return f.service.handleOnlineRejoinRaid(owners[2], 668, body)
	})
	// 752/591 are synchronous roster publication; 584 is the existing queued
	// participation projection. Wait for that owner cursor before inspecting the
	// connection, rather than classifying a scheduling delay as a missing packet.
	until = time.Now().Add(createWriteTimeout)
	for time.Now().Before(until) {
		f.service.raidMu.Lock()
		version := uint64(0)
		if f.state.Battle != nil {
			version = f.state.Battle.ProgressVersion
		}
		f.service.raidMu.Unlock()
		done := true
		for _, owner := range owners {
			owner.raidProjection.mu.Lock()
			if owner.raidProjection.version < version {
				done = false
			}
			owner.raidProjection.mu.Unlock()
			if !done {
				break
			}
		}
		if done {
			break
		}
		time.Sleep(time.Millisecond)
	}

	want := map[uint16]r160ProfileRow{
		19: {Character: 19, Channel: byte(f.state.RaidKey >> 16), Server: byte(f.state.RaidKey >> 24), Level: 90, Grow: 0x21, Job: 0, Name: "r160-sword"},
		20: {Character: 20, Channel: byte(f.state.RaidKey >> 16), Server: byte(f.state.RaidKey >> 24), Level: 90, Grow: 0x11, Job: 1, Name: "r160-priest"},
		21: {Character: 21, Channel: byte(f.state.RaidKey >> 16), Server: byte(f.state.RaidKey >> 24), Level: 90, Grow: 0x31, Job: 2, Name: "r160-gunner"},
	}
	wantGroups := map[uint16]byte{19: 1, 20: 1, 21: 2}
	profileEvidence := make(map[uint16][]r160ProfileRow)
	rosterEvidence := make(map[uint16][]r160RosterRow)
	for i, conn := range conns {
		packetID := owners[i].selectedCharacterID
		frames := r160FindFrames(t, conn.bytes())
		profileSeen := false
		rosterSeen := false
		for _, frame := range frames {
			if frame.Header.Classification != 0 {
				continue
			}
			switch frame.Header.MsgID {
			case 752:
				if profileSeen {
					continue
				}
				profileSeen = true
				rows := r160ParseProfiles(t, frame.Body)
				if len(rows) != 3 {
					t.Fatalf("receiver %d profile count=%d want 3", packetID, len(rows))
				}
				for _, row := range rows {
					if row != want[row.Character] {
						t.Fatalf("receiver %d profile=%+v want=%+v", packetID, row, want[row.Character])
					}
				}
				profileEvidence[packetID] = rows
			case 591:
				if rosterSeen {
					continue
				}
				rosterSeen = true
				rows := r160ParseRoster(t, frame.Body)
				if len(rows) != 3 {
					t.Fatalf("receiver %d roster count=%d want 3", packetID, len(rows))
				}
				for _, row := range rows {
					expected := want[row.Character]
					if row.Name != expected.Name || row.Job != expected.Job || row.Grow != expected.Grow || row.Group != wantGroups[row.Character] {
						t.Fatalf("receiver %d roster=%+v want identity=%+v group=%d", packetID, row, expected, wantGroups[row.Character])
					}
				}
				rosterEvidence[packetID] = rows
			}
		}
		if !profileSeen || !rosterSeen {
			seenIDs := make([]string, 0, len(frames))
			for _, frame := range frames {
				seenIDs = append(seenIDs, fmt.Sprintf("c%d/%d", frame.Header.Classification, frame.Header.MsgID))
			}
			t.Fatalf("receiver %d rejoin missing 752/591: profiles=%t roster=%t frames=%v", packetID, profileSeen, rosterSeen, seenIDs)
		}
	}

	if out := os.Getenv("R160_ACTOR_OUTPUT"); out != "" {
		payload := map[string]any{
			"source":          "actual public666/op16/op37 and rejoin668; memory lobby/characters/network fixtures; real PVF",
			"profiles":        profileEvidence,
			"roster":          rosterEvidence,
			"occupancy":       occupancyEvidence,
			"peer_actor_wire": actorEvidence,
			"gui_acceptance":  false,
			"native_consumer": "not executed in this Go test; op2/op9 consumer verification is separate",
			"frame_hash_note": "packet bodies retained in test diagnostics only when explicitly requested",
		}
		raw, _ := json.MarshalIndent(payload, "", "  ")
		if err := os.MkdirAll(out, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, "r160-handler-actor.json"), append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestR160DungeonPartyJoinProjectsPeerActors(t *testing.T) {
	service, leader, follower, _, _ := partyDungeonDeathFixture(t)
	leaderConn := leader.conn.(*bufferConn)
	followerConn := follower.conn.(*bufferConn)
	leaderConn.write.Reset()
	followerConn.write.Reset()
	state := runtimePartyStateSnapshot(leader)
	if state.PartyID <= 0 || len(runtimePartyMembers(state)) != 2 {
		t.Fatalf("party fixture state=%+v", state)
	}
	// The reported case is party formation after both characters are already
	// inside the encounter. Drop the pre-entry run keys to model that ordering.
	forgetDungeonPartyRunKey(leader)
	forgetDungeonPartyRunKey(follower)

	// This is the post-accept integration point used by party invite, direct
	// entry and directory join. Both sessions are already active in the same
	// dungeon run; the roster update alone must not be the only projection.
	service.refreshTownPartyActorsAfterJoin(state, "test_dungeon_party_join")

	for i, receiver := range []*gameSession{leader, follower} {
		peerID := []uint16{follower.selectedCharacterID, leader.selectedCharacterID}[i]
		conn := []*bufferConn{leaderConn, followerConn}[i]
		mode0, mode1, op9 := r160UpperPeerActorCounts(splitUpperFrames(t, conn.write.Bytes()), peerID)
		if mode0 == 0 || mode1 == 0 || op9 == 0 {
			t.Fatalf("receiver %d after in-dungeon party join: peer=%d mode0=%d mode1=%d op9=%d", receiver.selectedCharacterID, peerID, mode0, mode1, op9)
		}
	}
}

func TestR160DungeonPartyJoinActorRetryAfterWriteFailure(t *testing.T) {
	service, leader, follower, _, _ := partyDungeonDeathFixture(t)
	forgetDungeonPartyRunKey(leader)
	forgetDungeonPartyRunKey(follower)
	state := runtimePartyStateSnapshot(leader)
	leaderConn := leader.conn.(*bufferConn)
	followerConn := follower.conn.(*bufferConn)
	leaderConn.write.Reset()
	followerConn.write.Reset()
	originalConn := follower.conn
	broken := &r160FailWritesConn{fail: true}
	follower.conn = broken
	service.refreshTownPartyActorsAfterJoin(state, "test_post_join_write_failure")
	if broken.writes == 0 {
		t.Fatal("injected post-join write failure was not exercised")
	}
	if _, stillClaimed := follower.dungeonPeerProjected[leader.selectedCharacterID]; stillClaimed {
		t.Fatal("failed peer projection remained claimed and cannot retry")
	}

	broken.fail = false
	follower.conn = originalConn
	leaderConn.write.Reset()
	followerConn.write.Reset()
	service.refreshTownPartyActorsAfterJoin(state, "test_post_join_write_failure_retry")
	mode0, mode1, op9 := r160UpperPeerActorCounts(splitUpperFrames(t, followerConn.write.Bytes()), leader.selectedCharacterID)
	if mode0 == 0 || mode1 == 0 || op9 == 0 {
		t.Fatalf("retry did not restore receiver=%d peer=%d: mode0=%d mode1=%d op9=%d", follower.selectedCharacterID, leader.selectedCharacterID, mode0, mode1, op9)
	}
}

func TestR160AntonPostEntrySameRunJoinProjectsPeerActors(t *testing.T) {
	f, owners, conns := r160AntonLegalFixture(t)
	for _, conn := range conns[:2] {
		conn.reset()
	}
	// The positive contract is one committed Anton run. The two members enter
	// legally together, then their party is removed and recreated after entry.
	if got := r160EnterRaidGroup(t, f, owners, 0, 210); fmt.Sprint(got) != "[19 20]" {
		t.Fatalf("actual Anton group=%v want [19 20]", got)
	}
	owners[0].dungeon.mu.Lock()
	leftRunID := owners[0].dungeon.runtime.raidEntry.RunID
	owners[0].dungeon.mu.Unlock()
	owners[1].dungeon.mu.Lock()
	rightRunID := owners[1].dungeon.runtime.raidEntry.RunID
	owners[1].dungeon.mu.Unlock()
	if leftRunID == "" || rightRunID == "" || leftRunID != rightRunID {
		t.Fatalf("the positive case requires one committed Anton run: %q/%q", leftRunID, rightRunID)
	}
	before := r160CaptureRaidOwnership(f)
	r160MakeSolo(t, f, owners[0], owners[1])

	state, result, ok := f.service.createOrJoinManagedRuntimeParty(owners[1], owners[0])
	if !ok && result.Reason == "already_member" {
		state = runtimePartyStateSnapshot(owners[0])
		ok = state.PartyID > 0
	}
	if !ok || state.PartyID <= 0 || len(runtimePartyMembers(state)) < 2 {
		t.Fatalf("same-run post-entry join failed: ok=%t result=%+v state=%+v", ok, result, state)
	}
	for _, owner := range owners[:2] {
		if _, ready := f.service.currentDungeonPartyRunKey(owner); ready {
			t.Fatalf("character %d retained a pre-join dungeon run key", owner.selectedCharacterID)
		}
	}
	for _, conn := range conns {
		conn.reset()
	}
	// The production post-join refresh must materialize both active-dungeon
	// keys itself; the caller does not pre-populate them.
	f.service.refreshTownPartyActorsAfterJoin(state, "test_anton_post_entry_same_run")
	leaderKey, leaderReady := f.service.currentDungeonPartyRunKey(owners[0])
	followerKey, followerReady := f.service.currentDungeonPartyRunKey(owners[1])
	if !leaderReady || !followerReady || leaderKey != followerKey ||
		leaderKey.PartyID != uint16(state.PartyID) || leaderKey.RaidRunID == "" {
		t.Fatalf("post-join refresh keys leader=%+v/%t follower=%+v/%t state=%+v",
			leaderKey, leaderReady, followerKey, followerReady, state)
	}
	for i, receiver := range owners[:2] {
		peer := owners[1-i].selectedCharacterID
		mode0, mode1, op9 := r160UpperPeerActorCounts(splitUpperFrames(t, conns[i].write.Bytes()), peer)
		if mode0 == 0 || mode1 == 0 || op9 == 0 {
			t.Fatalf("receiver %d actual Anton same-run peer=%d frames=%d/%d/%d", receiver.selectedCharacterID, peer, mode0, mode1, op9)
		}
	}
	if after := r160CaptureRaidOwnership(f); !reflect.DeepEqual(before, after) {
		t.Fatalf("same-run party actor projection changed raid/reward ownership\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestR160AntonPostEntryCrossRunJoinNeverProjectsPeerActors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		dungeonID uint32
		reason    string
	}{
		{name: "same_dungeon_different_run", dungeonID: 210, reason: "different raid RunID"},
		{name: "different_dungeon", dungeonID: 211, reason: "different dungeon"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, owners, conns := r160AntonLegalFixture(t)
			r160MakeSolo(t, f, owners...)
			for _, conn := range conns {
				conn.reset()
			}
			// These IDs come from the real Anton battle configuration. Select the
			// requested map for the isolation test without changing admission or
			// reward ownership.
			f.service.raidMu.Lock()
			configured := false
			for i := range f.state.Battle.Dungeons {
				if f.state.Battle.Dungeons[i].ID == tc.dungeonID {
					f.state.Battle.Dungeons[i].State = 0
					configured = true
				}
			}
			f.service.raidMu.Unlock()
			if !configured {
				t.Fatalf("real Anton battle config does not contain dungeon %d", tc.dungeonID)
			}
			// Two genuinely independent solo entries through the same legal Anton
			// raid. Their frozen RunIDs must decide actor isolation after regrouping.
			r160EnterRaidMember(t, f, owners[0], 210)
			r160EnterRaidMember(t, f, owners[2], tc.dungeonID)
			before := r160CaptureRaidOwnership(f)
			for _, owner := range owners {
				forgetDungeonPartyRunKey(owner)
			}
			state, result, ok := f.service.createOrJoinManagedRuntimeParty(owners[2], owners[0])
			if !ok || state.PartyID <= 0 {
				t.Fatalf("cross-run lobby join setup failed: ok=%t result=%+v", ok, result)
			}
			for _, owner := range owners {
				f.service.recordCurrentDungeonPartyRunKey(owner)
			}
			left, leftOK := f.service.currentDungeonPartyRunKey(owners[0])
			right, rightOK := f.service.currentDungeonPartyRunKey(owners[2])
			if !leftOK || !rightOK || left == right {
				t.Fatalf("%s must remain separated: left=%+v/%t right=%+v/%t", tc.reason, left, leftOK, right, rightOK)
			}
			for _, conn := range conns {
				conn.reset()
			}
			f.service.refreshTownPartyActorsAfterJoin(state, "test_anton_post_entry_cross_run")
			check := func(receiver, peer int) {
				mode0, mode1, op9 := r160UpperPeerActorCounts(splitUpperFrames(t, conns[receiver].write.Bytes()), owners[peer].selectedCharacterID)
				if mode0 != 0 || mode1 != 0 || op9 != 0 {
					t.Fatalf("%s crossed actor projection receiver=%d peer=%d mode0=%d mode1=%d op9=%d", tc.reason, owners[receiver].selectedCharacterID, owners[peer].selectedCharacterID, mode0, mode1, op9)
				}
			}
			check(0, 2)
			check(2, 0)
			if after := r160CaptureRaidOwnership(f); !reflect.DeepEqual(before, after) {
				t.Fatalf("%s changed raid/reward ownership\nbefore=%+v\nafter=%+v", tc.reason, before, after)
			}
		})
	}
}

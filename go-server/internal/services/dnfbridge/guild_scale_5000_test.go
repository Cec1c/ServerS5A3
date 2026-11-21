package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/metrics"
)

func TestGuildPermanentBenefitManagementUsesMaxInt32Sentinel(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	exp := now.Add(30 * 24 * time.Hour)
	g := &dnfguild.Guild{
		ID:          1,
		Name:        "不许开桂",
		MasterName:  "袜子不干",
		MemberLimit: 5000,
		Benefits: map[byte]dnfguild.Benefit{
			1:  {Type: 1, Value: 10, ExpiresAt: exp},
			3:  {Type: 3, Value: 167, ExpiresAt: exp},
			5:  {Type: 5, Value: 169},
			7:  {Type: 7, Value: 0},
			21: {Type: 21, Value: 5000, ExpiresAt: exp},
		},
	}
	rows := parseGuildManagementBenefitRows(t, buildCurrentGuildManagementBody(g, 1, now, dnfguild.DefaultMasterGrade))
	byType := map[byte]guildManagementBenefitRow{}
	for _, row := range rows {
		byType[row.typ] = row
	}
	if got := byType[5]; got.typ != 5 || got.expires != math.MaxInt32 || got.mode != 0 || got.value != 169 {
		t.Fatalf("permanent stat benefit row=%+v want expires=0x7fffffff value=169", got)
	}
	if got := byType[6]; got.typ != 6 || got.expires != 0 || got.mode != 0 || got.value != 5000 {
		t.Fatalf("member capacity benefit row=%+v want expires=0 value=5000", got)
	}
	if got := byType[7]; got.typ != 7 || got.expires != 0 || got.mode != 1 || got.value != 0 {
		t.Fatalf("teleport benefit row=%+v want expires=0 mode=1 value=0", got)
	}
	for _, timed := range []byte{1, 3, 21} {
		if got := byType[timed]; got.expires != uint32(exp.Unix()) {
			t.Fatalf("timed benefit %d expires=%d want %d", timed, got.expires, exp.Unix())
		}
	}
}

func decodeMemberListChunkRows(t *testing.T, body []byte, first, cursor bool) (guildID uint32, total uint16, charIDs []uint32) {
	t.Helper()
	if len(body) < 1 || body[0] != 1 {
		t.Fatalf("member list reply not success: %x", body)
	}
	off := 1
	guildID = binary.LittleEndian.Uint32(body[off : off+4])
	off += 4
	if first {
		_, next, ok := readCurrentRawDstr(body, off)
		if !ok {
			t.Fatalf("invalid guild name dstr at %d", off)
		}
		off = next
		total = binary.LittleEndian.Uint16(body[off : off+2])
		off += 2
	}
	count := int(binary.LittleEndian.Uint16(body[off : off+2]))
	off += 2
	charIDs = make([]uint32, 0, count)
	for range count {
		charID := binary.LittleEndian.Uint32(body[off : off+4])
		off += 4
		_, next, ok := readCurrentRawDstr(body, off) // name
		if !ok {
			t.Fatalf("invalid member name at %d", off)
		}
		off = next
		_, next, ok = readCurrentRawDstr(body, off) // memo
		if !ok {
			t.Fatalf("invalid member memo at %d", off)
		}
		off = next
		off += 2 + 1 + 1 + 1 + 1 + 1 + 1 + 1 + 1 + 4 + 4 + 1
		_, next, ok = readCurrentRawDstr(body, off) // empty dstr
		if !ok {
			t.Fatalf("invalid member extra dstr at %d", off)
		}
		off = next
		off += 1 + 1 + 1 + 1
		charIDs = append(charIDs, charID)
	}
	if cursor {
		if off+4 != len(body) || binary.LittleEndian.Uint32(body[off:off+4]) != 0 {
			t.Fatalf("cursor mismatch off=%d len=%d", off, len(body))
		}
	} else if off != len(body) {
		t.Fatalf("trailing bytes off=%d len=%d", off, len(body))
	}
	return guildID, total, charIDs
}

func decodeGradeChunkRows(t *testing.T, body []byte, first bool) (guildID uint32, total uint16, names []string) {
	t.Helper()
	if len(body) < 1 || body[0] != 1 {
		t.Fatalf("grade reply not success: %x", body)
	}
	off := 1
	guildID = binary.LittleEndian.Uint32(body[off : off+4])
	off += 4
	if first {
		total = binary.LittleEndian.Uint16(body[off : off+2])
		off += 2
	}
	count := int(binary.LittleEndian.Uint16(body[off : off+2]))
	off += 2
	names = make([]string, 0, count)
	for range count {
		off++ // region
		rawName, next, ok := readCurrentRawDstr(body, off)
		if !ok {
			t.Fatalf("invalid grade name at %d", off)
		}
		off = next
		off += 3 // grade, group, representative
		names = append(names, string(rawName))
	}
	if off != len(body) {
		t.Fatalf("grade trailing bytes off=%d len=%d", off, len(body))
	}
	return guildID, total, names
}

func TestGuildScale5000ChunkedRosterGradesAndHongbaoSafety(t *testing.T) {
	const totalMembers = 5000
	s, peers, repos := guildTextFixture(t)
	ctx := context.Background()
	reg, err := dnfguild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	delete(reg.Guilds, 2)
	g := reg.Guilds[1]
	g.MemberLimit = totalMembers
	seed, _, _ := repos.Character.Load(ctx, "2")
	for i := 3; i <= totalMembers; i++ {
		id := fmt.Sprint(i)
		c := dnfrepo.CloneCharacter(seed)
		c.CharacterID, c.AccountID, c.Name = id, "a"+id, fmt.Sprintf("M%04d", i)
		if err := repos.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
		m := dnfguild.Member{
			ServerGroup:               0,
			CharacterID:               id,
			AccountID:                 c.AccountID,
			Name:                      c.Name,
			Job:                       c.Job,
			GrowType:                  "33",
			Level:                     c.Level,
			Grade:                     dnfguild.DefaultMemberGrade,
			JoinedAt:                  s.gameplayNow(),
			AttendedAt:                s.gameplayNow(),
			DungeonContributionPoints: new(uint32),
		}
		// Give top-30 members (10..39) highest scores, 40..1539 score 100, and viewer #5000 score 0.
		switch {
		case i >= 10 && i < 40:
			m.ContributionPoints = uint32(5000 - (i - 10))
		case i >= 40 && i < 1540:
			m.ContributionPoints = 100
		}
		g.Members[id] = m
	}
	if err := dnfguild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	viewer := &gameSession{
		conn:                      &bufferConn{},
		connID:                    "viewer-5000",
		accountID:                 "a5000",
		selectedCharacterID:       5000,
		characterGeneration:       5000,
		townSceneReadyCharacterID: 5000,
	}
	s.gameSessions[5000] = viewer
	_ = peers

	// 1. op 140 streams op 140 + op 141 chunks totaling 5,000 unique members.
	for _, tc := range []struct {
		op     uint16
		cursor bool
	}{
		{uint16(dnfenum.CmdPacketGuildAllMemberList), true},
		{uint16(dnfenum.CmdPacketGuildMemerList), false},
	} {
		t.Run(fmt.Sprintf("roster_op%d", tc.op), func(t *testing.T) {
			viewer.conn.(*bufferConn).write.Reset()
			guildDispatch(t, s, viewer, tc.op, nil)
			seen := map[uint32]bool{}
			packets := 0
			var announcedTotal uint16
			for wire := viewer.conn.(*bufferConn).write.Bytes(); len(wire) > 0; {
				p, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
				wire = rest
				if p.Header.MsgID != tc.op && p.Header.MsgID != uint16(dnfenum.CmdPacketGuildAllMemberListNext) {
					continue
				}
				if len(p.Body) >= 32768 {
					t.Fatalf("op%d chunk #%d too large: %d bytes", p.Header.MsgID, packets, len(p.Body))
				}
				first := packets == 0
				if first && p.Header.MsgID != tc.op {
					t.Fatalf("first chunk opcode=%d want %d", p.Header.MsgID, tc.op)
				}
				if !first && p.Header.MsgID != uint16(dnfenum.CmdPacketGuildAllMemberListNext) {
					t.Fatalf("continuation chunk opcode=%d want 141", p.Header.MsgID)
				}
				chunkCursor := tc.cursor || !first
				guildID, total, ids := decodeMemberListChunkRows(t, p.Body, first, chunkCursor)
				if guildID != 1 {
					t.Fatalf("guildID=%d want 1", guildID)
				}
				if first {
					announcedTotal = total
				}
				if len(ids) == 0 || len(ids) > currentGuildMemberListChunkSize {
					t.Fatalf("chunk #%d len=%d", packets, len(ids))
				}
				for _, id := range ids {
					if seen[id] {
						t.Fatalf("duplicate member %d", id)
					}
					seen[id] = true
				}
				packets++
			}
			wantPackets := (totalMembers + currentGuildMemberListChunkSize - 1) / currentGuildMemberListChunkSize
			if announcedTotal != totalMembers || len(seen) != totalMembers || packets != wantPackets {
				t.Fatalf("announced=%d seen=%d packets=%d wantPackets=%d", announcedTotal, len(seen), packets, wantPackets)
			}
		})
	}

	// 2. op 1452 streams op 1452 + op 1453 chunks totaling 5,000 unique grades.
	viewer.conn.(*bufferConn).write.Reset()
	guildDispatch(t, s, viewer, currentGuildGradesOpcode, nil)
	seenNames := map[string]bool{}
	gradePackets := 0
	var announcedGrades uint16
	for wire := viewer.conn.(*bufferConn).write.Bytes(); len(wire) > 0; {
		p, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
		wire = rest
		if p.Header.MsgID != currentGuildGradesOpcode && p.Header.MsgID != currentGuildGradesNextOpcode {
			continue
		}
		if len(p.Body) >= 32768 {
			t.Fatalf("grade chunk #%d too large: %d bytes", gradePackets, len(p.Body))
		}
		first := gradePackets == 0
		if first && p.Header.MsgID != currentGuildGradesOpcode {
			t.Fatalf("first grade chunk opcode=%d want 1452", p.Header.MsgID)
		}
		if !first && p.Header.MsgID != currentGuildGradesNextOpcode {
			t.Fatalf("continuation grade chunk opcode=%d want 1453", p.Header.MsgID)
		}
		guildID, total, names := decodeGradeChunkRows(t, p.Body, first)
		if guildID != 1 {
			t.Fatalf("grade guildID=%d want 1", guildID)
		}
		if first {
			announcedGrades = total
		}
		for _, name := range names {
			seenNames[name] = true
		}
		gradePackets++
	}
	wantGradePackets := (totalMembers + currentGuildGradeListChunkSize - 1) / currentGuildGradeListChunkSize
	if announcedGrades != totalMembers || len(seenNames) != totalMembers || gradePackets != wantGradePackets {
		t.Fatalf("grades announced=%d seen=%d packets=%d wantPackets=%d", announcedGrades, len(seenNames), gradePackets, wantGradePackets)
	}

	// 3. op 1301 caps to 1,000 rows (preventing DNF.exe 40,000-byte stack overflow),
	// preserves exact Top-30 contributors, and always includes 0-point viewer #5000.
	viewer.conn.(*bufferConn).write.Reset()
	guildDispatch(t, s, viewer, currentGuildHongbaoPointsOpcode, nil)
	pointsBody := guildReply(t, viewer, currentGuildHongbaoPointsOpcode)
	if len(pointsBody) != 5+currentGuildHongbaoPointsMaxMembers*40 || pointsBody[0] != 1 {
		t.Fatalf("op1301 body len=%d status=%d", len(pointsBody), pointsBody[0])
	}
	if count := binary.LittleEndian.Uint32(pointsBody[1:5]); count != currentGuildHongbaoPointsMaxMembers {
		t.Fatalf("op1301 count=%d want 1000", count)
	}
	scores := map[uint32]uint32{}
	for i := range currentGuildHongbaoPointsMaxMembers {
		row := pointsBody[5+40*i : 5+40*(i+1)]
		id := binary.LittleEndian.Uint32(row[:4])
		scores[id] = binary.LittleEndian.Uint32(row[35:39])
	}
	for i := 10; i < 40; i++ {
		if got, ok := scores[uint32(i)]; !ok || got != uint32(5000-(i-10)) {
			t.Fatalf("top-30 contributor %d score=%d ok=%v", i, got, ok)
		}
	}
	if got, ok := scores[5000]; !ok || got != 0 {
		t.Fatalf("viewer #5000 omitted from op1301: ok=%v score=%d", ok, got)
	}
}

func TestGuildBroadcastCoalescesActiveSessionsAndSharesReadCache(t *testing.T) {
	s, peers, repos := guildRosterLargeFixture(t, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	characters := &guildRosterDisplayCharacters{}
	repos.Guild = &presenceVersionReadUOW{
		GuildUnitOfWork:     repos.Guild,
		GuildReadUnitOfWork: repos.Guild.(dnfrepo.GuildReadUnitOfWork),
		namespace:           &dnfrepo.GuildSnapshotNamespace{},
		counts:              characters,
	}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	s.performance = newBridgePerformanceMetrics(metrics.New())

	release := make(chan struct{})
	for _, p := range peers {
		p.ctx = ctx
		p.guild.memberListRequested = true
		if err := s.startGameSessionEvents(ctx, p); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { stopTestGameSessionEvents(t, s, p) })
		entered := make(chan struct{})
		if err := s.tryPostGameSessionCharacterEvent(p, "gate", p.selectedCharacterID, p.characterGeneration, func() error {
			close(entered)
			<-release
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		waitTestSignal(t, entered, "broadcast gate")
	}

	reg, err := dnfguild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	// Five back-to-back join broadcasts coalesce into 1 queued event per peer.
	for range 5 {
		s.broadcastCurrentGuildMembers(reg.Guilds[1], peers[0].selectedCharacterID)
	}
	for _, p := range peers[1:] {
		if got := p.events.loop.QueueLengths().Medium; got != 1 {
			t.Fatalf("peer %s queued=%d want 1", p.connID, got)
		}
	}
	close(release)
	for _, p := range peers {
		if err := s.callGameSession(ctx, p, "drain", func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	// Shared cache keeps full-member batch query to 1 across the 7 refreshed peers
	// (remaining 6 peers validate only their own single character ID).
	fullBatches := 0
	for _, batch := range characters.ids {
		if len(batch) > 1 {
			fullBatches++
		}
	}
	if fullBatches != 1 {
		t.Fatalf("full character batches=%d want 1 shared hydration across 7 peers", fullBatches)
	}
}

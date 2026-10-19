package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	dnfpvp "longheng.io/server/internal/modules/dnf/pvp"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func pvpDailyTestCatalog() *dnfpvp.Catalog {
	c := &dnfpvp.Catalog{ExperienceBounds: []uint32{0, 12000, 27000, 47000, 77000, 125000, 185000, 265000, 385000, 529000, 709000}}
	c.RatingRules = &dnfpvp.RatingRules{InitialRating: 1000, WarmupGames: 10, WarmupWeight: 120,
		MatchExperiencePercent: [2]int64{50, 100}, ManualExperiencePercent: [2]int64{45, 45},
		Warmup: []dnfpvp.Weight{{Threshold: 0, Value: 100}}, Rating: []dnfpvp.Weight{{Threshold: 0, Value: 22}}}
	for i := 1; i < len(c.ExperienceBounds); i++ {
		c.RatingRules.GradePoints = append(c.RatingRules.GradePoints, [2]int64{0, 1000})
	}
	for i, id := range []uint16{113, 118, 131} {
		c.Pools[i] = []dnfpvp.Mission{{ID: id, Tier: i, Weight: 10, RankMax: 30, Target: 3, Condition: []string{"hereafter join", "first aerial attack", "winning streak"}[i]}}
	}
	return c
}

func TestCurrentPVPDailyWireAndRestore(t *testing.T) {
	s := pvpRoomTestService(t)
	session := &gameSession{conn: &bufferConn{}, accountID: "pvp-29", selectedCharacterID: 29, channel: s.catalog.Channels()[1]}
	session.channel.Type = 8
	if err := s.sendCurrentPVPDailyState(session); err != nil {
		t.Fatal(err)
	}
	wire := session.conn.(*bufferConn).write.Bytes()
	cert, wire := splitCurrentGameServerUpperPacketAuto(t, wire)
	wallet, wire := splitCurrentGameServerUpperPacketAuto(t, wire)
	list, wire := splitCurrentGameServerUpperPacketAuto(t, wire)
	if len(wire) != 0 || cert.Header.MsgID != 48 || len(cert.Body) != 51 || binary.LittleEndian.Uint32(cert.Body[12:]) != 0 || binary.LittleEndian.Uint32(cert.Body[16:]) != 12000 ||
		wallet.Header.MsgID != 14 || len(wallet.Body) != 122 || binary.LittleEndian.Uint16(wallet.Body[3:]) != 2 || binary.LittleEndian.Uint32(wallet.Body[5:]) != 2 ||
		list.Header.MsgID != 323 || len(list.Body) != 31 || list.Body[0] != 3 || binary.LittleEndian.Uint16(list.Body[1:]) != 113 || binary.LittleEndian.Uint32(list.Body[7:]) != 3 {
		t.Fatalf("cert=%+v list=%+v", cert, list)
	}
	repos, _ := s.repositoryGroup()
	c, _, _ := repos.Character.Load(context.Background(), "29")
	c.Stats[dnfpvp.CertificationKey] = 7
	c.Stats[dnfpvp.WinsKey] = 4
	c.Stats[dnfpvp.LossesKey] = 2
	c.Stats[dnfpvp.EmblemsKey] = 80
	c.Stats["pvp_rank_point"] = 1240
	c.Stats["pvp_max_rank_point"] = 1300
	if err := dnfrepo.SaveCharacterFields(context.Background(), repos.Character, c, dnfrepo.CharacterFieldStats); err != nil {
		t.Fatal(err)
	}
	session.conn = &bufferConn{}
	if err := s.sendCurrentPVPDailyState(session); err != nil {
		t.Fatal(err)
	}
	cert, wire = splitCurrentGameServerUpperPacketAuto(t, session.conn.(*bufferConn).write.Bytes())
	wallet, _ = splitCurrentGameServerUpperPacketAuto(t, wire)
	if binary.LittleEndian.Uint32(cert.Body[34:]) != 7 || binary.LittleEndian.Uint32(cert.Body) != 4 || binary.LittleEndian.Uint32(cert.Body[4:]) != 2 || binary.LittleEndian.Uint32(cert.Body[26:]) != 1240 || binary.LittleEndian.Uint32(cert.Body[30:]) != 1300 || binary.LittleEndian.Uint32(wallet.Body[9:]) != 80 {
		t.Fatal("certification was not restored")
	}
	state, _ := s.prepareCurrentPVPDaily(session)
	missionBody, err := buildCurrentPVPMissions(state.Rows, state.NormalRows)
	if err != nil || !reflect.DeepEqual(list.Body, missionBody) {
		t.Fatal("reconnect rerolled missions")
	}
	// The same record must survive a return/login in an ordinary town.
	session.channel.Type = 1
	session.conn = &bufferConn{}
	if err := s.sendCurrentPVPDailyState(session); err != nil {
		t.Fatal(err)
	}
	townCert, wire := splitCurrentGameServerUpperPacketAuto(t, session.conn.(*bufferConn).write.Bytes())
	townWallet, wire := splitCurrentGameServerUpperPacketAuto(t, wire)
	if len(wire) != 0 || !reflect.DeepEqual(townCert.Body, cert.Body) || !reflect.DeepEqual(townWallet.Body, wallet.Body) {
		t.Fatal("town lost PVP record/wallet")
	}
}

func TestCurrentPVPRecordPublishedOnFirstPrivateTownEntry(t *testing.T) {
	s := pvpRoomTestService(t)
	repos, _ := s.repositoryGroup()
	character, _, _ := repos.Character.Load(context.Background(), "29")
	character.Stats[dnfpvp.WinsKey] = 17
	character.Stats[dnfpvp.LossesKey] = 3
	character.Stats[dnfpvp.RankPointKey] = 1142
	if err := repos.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	session := &gameSession{conn: &bufferConn{}, accountID: character.AccountID, selectedCharacterID: 29, channel: s.catalog.Channels()[0]}
	for _, id := range []uint16{29, 30} {
		session.selectedCharacterID = id
		session.accountID = "pvp-" + strconv.Itoa(int(id))
		session.conn.(*bufferConn).write.Reset()
		character, _, _ = repos.Character.Load(context.Background(), strconv.Itoa(int(id)))
		if err := s.sendCurrentPVPDailyState(session); err != nil {
			t.Fatal(err)
		}
		cert, wire := splitCurrentGameServerUpperPacketAuto(t, session.conn.(*bufferConn).write.Bytes())
		wallet, wire := splitCurrentGameServerUpperPacketAuto(t, wire)
		if cert.Header.MsgID != 48 || wallet.Header.MsgID != 14 || len(wire) != 0 {
			t.Fatal("private town did not publish personal record and wallet before any UI request")
		}
		if binary.LittleEndian.Uint32(cert.Body) != statU32(character, dnfpvp.WinsKey, 0) ||
			binary.LittleEndian.Uint32(cert.Body[4:]) != statU32(character, dnfpvp.LossesKey, 0) ||
			binary.LittleEndian.Uint32(cert.Body[26:]) != statU32(character, dnfpvp.RankPointKey, 0) {
			t.Fatal("first-open record is stale or leaked from the previous character")
		}
	}
}

func TestCurrentPVPExperienceBarNativeBounds(t *testing.T) {
	catalog := pvpDailyTestCatalog()
	for _, tc := range []struct {
		experience, grade, progress, needed int64
	}{
		{0, 0, 0, 12000},
		{1000, 0, 1000, 12000},
		{11950, 0, 11950, 12000},
		{12000, 1, 0, 15000},
		{12400, 1, 400, 15000},
		{13400, 1, 1400, 15000},
		{27000, 2, 0, 20000},
	} {
		state := dnfpvp.Snapshot{Character: dnfrepo.CharacterRecord{Stats: map[string]int64{
			dnfpvp.ExperienceKey: tc.experience, dnfpvp.GradeKey: tc.grade,
		}}}
		body, err := buildCurrentPVPCertification(state, catalog)
		if err != nil {
			t.Fatal(err)
		}
		// Native sub_3292590 uses (EXP - lower) / (upper - lower).
		experience := int64(binary.LittleEndian.Uint32(body[8:]))
		lower := int64(binary.LittleEndian.Uint32(body[12:]))
		upper := int64(binary.LittleEndian.Uint32(body[16:]))
		if experience-lower != tc.progress || upper-lower != tc.needed {
			t.Fatalf("EXP %d grade %d: native bar %d/%d, want %d/%d", tc.experience, tc.grade,
				experience-lower, upper-lower, tc.progress, tc.needed)
		}
	}
}

func TestCurrentPVPMissionReportsValidateAssignment(t *testing.T) {
	s := pvpRoomTestService(t)
	room := &currentPVPRoom{id: 1, battleMode: 2, matchingToken: 1, started: true, seatState: [8]byte{1, 2}, match: &currentPVPMatch{startedAt: time.Now()}}
	for seat, id := range []uint16{29, 30} {
		p := &gameSession{conn: &bufferConn{}, accountID: "pvp-" + strconv.Itoa(int(id)), selectedCharacterID: id, channel: s.catalog.Channels()[1], pvpRoom: room}
		p.channel.Type = 8
		room.members[seat] = &currentPVPMember{session: p, characterID: id}
	}
	a := room.members[0].session
	for _, body := range [][]byte{{1, 113, 0}, {1, 119, 0}, {2, 118, 0, 118, 0}, {1, 118}, {1, 118, 0, 0}} {
		if err := s.handleCurrentPVPMissionReport(a, 876, body); err != nil {
			t.Fatal(err)
		}
		if len(room.match.missionReports[0]) != 0 {
			t.Fatal("unassigned, wrong-kind, duplicate, or malformed report accepted")
		}
	}
	for i := 0; i < 2; i++ {
		if err := s.handleCurrentPVPMissionReport(a, 876, []byte{1, 118, 0}); err != nil {
			t.Fatal(err)
		}
	}
	if len(room.match.missionReports[0]) != 1 || !room.match.missionReports[0][118] {
		t.Fatal("valid report lost or duplicated")
	}
}

func TestPVPEmblemInventoryProjectionPreservesReservedSlot(t *testing.T) {
	for _, existing := range []int64{0, 2, 600} {
		t.Run(strconv.FormatInt(existing, 10), func(t *testing.T) {
			s := pvpRoomTestService(t)
			repos, _ := s.repositoryGroup()
			ctx := context.Background()
			c, _, _ := repos.Character.Load(ctx, "29")
			c.Stats[dnfpvp.EmblemsKey] = 80
			if err := repos.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			slots := map[string]dnfrepo.ItemStack{}
			if existing != 0 {
				slots["0:2"] = dnfrepo.ItemStack{ItemID: existing, Count: 7}
			}
			if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "29", Slots: slots}); err != nil {
				t.Fatal(err)
			}
			session := &gameSession{selectedCharacterID: 29, accountID: "pvp-29"}
			body, _, _, ok := s.buildCurrentItemListBodyForSession(ctx, session, 0)
			if !ok {
				t.Fatal("wallet list unavailable")
			}
			wantItem, wantCount := uint32(2), uint32(80)
			if existing == 600 {
				wantItem, wantCount = 600, 7
			}
			count := int(binary.LittleEndian.Uint16(body[3:5]))
			found := 0
			for i := 0; i < count; i++ {
				row := body[5+i*currentItemListEntryWireSize:]
				if binary.LittleEndian.Uint16(row) != 2 {
					continue
				}
				found++
				if binary.LittleEndian.Uint32(row[2:]) != wantItem || binary.LittleEndian.Uint32(row[6:]) != wantCount {
					t.Fatalf("wallet row=%x", row[:10])
				}
			}
			if found != 1 {
				t.Fatalf("slot2 count=%d", found)
			}
		})
	}
}

func TestRealScriptPVFDailyMissions(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	a, err := platformpvf.LoadArchive(platformpvf.Options{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	c, err := dnfpvp.Load(a)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Pools[0]) != 3 || len(c.Pools[1]) != 11 || len(c.Pools[2]) != 3 || c.ExperienceBounds[1] != 12000 {
		t.Fatalf("runtime PVF catalog=%+v", c)
	}
	categories := map[string]int{}
	for _, mission := range c.Normal {
		categories[mission.Category]++
	}
	if !reflect.DeepEqual(categories, map[string]int{"training": 43, "rating": 19, "battle": 24, "victory": 22}) {
		t.Fatalf("normal PVF categories=%v", categories)
	}
	s := pvpRoomTestService(t)
	s.pvpDailyCatalog = c
	session := &gameSession{conn: &bufferConn{}, accountID: "pvp-29", selectedCharacterID: 29, channel: s.catalog.Channels()[1]}
	session.channel.Type = 8
	for attempt := 0; attempt < 2; attempt++ {
		session.conn = &bufferConn{}
		if err := s.sendCurrentPVPDailyState(session); err != nil {
			t.Fatal(err)
		}
		_, wire := splitCurrentGameServerUpperPacketAuto(t, session.conn.(*bufferConn).write.Bytes())
		_, wire = splitCurrentGameServerUpperPacketAuto(t, wire)
		list, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
		if list.Header.MsgID != 323 || len(rest) != 0 || len(list.Body) != 751 || list.Body[0] != 75 {
			t.Fatalf("native target list: id=%d bytes=%d count=%d", list.Header.MsgID, len(list.Body), list.Body[0])
		}
		rows := map[uint16]dnfpvp.Row{}
		for offset := 1; offset < len(list.Body); offset += 10 {
			id := binary.LittleEndian.Uint16(list.Body[offset:])
			rows[id] = dnfpvp.Row{ID: id, Progress: binary.LittleEndian.Uint32(list.Body[offset+2:]), Target: binary.LittleEndian.Uint32(list.Body[offset+6:])}
		}
		if rows[499].Target != 100 || rows[500].Target != 333 || rows[545].Target != 2 || rows[565].Target != 1 || rows[604].Target != math.MaxUint32 || rows[566].ID != 0 || rows[800].ID != 0 {
			t.Fatalf("native target filtering/sentinels=%v", rows)
		}
	}
	t.Logf("PVF normal definitions=%d categories=%v; swordman native op323=75 rows/751 bytes", len(c.Normal), categories)
}

func TestPVPMissionWireRejectsTruncationAndDuplicates(t *testing.T) {
	rows := make([]dnfpvp.Row, 255)
	for i := range rows {
		rows[i] = dnfpvp.Row{ID: uint16(i + 1), Target: 1}
	}
	body, err := buildCurrentPVPMissions(rows)
	if err != nil || len(body) != 2551 || body[0] != 255 {
		t.Fatal("maximum native list rejected")
	}
	for _, extra := range [][]dnfpvp.Row{{{ID: 499, Target: 1}}, {{ID: 1, Target: 1}}} {
		if _, err := buildCurrentPVPMissions(rows, extra); err == nil {
			t.Fatal("wrapped native count accepted")
		}
	}
	if _, err := buildCurrentPVPMissions(rows[:1], rows[:1]); err == nil {
		t.Fatal("duplicate ID accepted")
	}
}

type pvpFailNthConn struct {
	bufferConn
	writes, failAt int
}

func (c *pvpFailNthConn) Write(body []byte) (int, error) {
	c.writes++
	if c.writes == c.failAt {
		return 0, errors.New("injected PVP progress write failure")
	}
	return c.bufferConn.Write(body)
}

func TestPVPDailyResultPublicationRetry(t *testing.T) {
	s := pvpRoomTestService(t)
	s.questCatalog = buildPVPResultQuestCatalog(t)
	s.pvpDailyCatalog.Normal = []dnfpvp.Mission{
		{ID: 499, Target: 100, Condition: "hereafter join"},
		{ID: 522, Target: 100, Condition: "hereafter winning count"},
	}
	queue := newFakeCurrentDungeonDeathTimerQueue()
	s.gameplayTimers = queue
	room := &currentPVPRoom{id: 1, battleMode: 2, matchingToken: 1, started: true, seatState: [8]byte{1, 2},
		match: &currentPVPMatch{startedAt: s.gameplayNow()}}
	s.pvpRooms = map[uint16]*currentPVPRoom{1: room}
	for seat, id := range []uint16{29, 30} {
		p := &gameSession{conn: &pvpFailNthConn{}, accountID: "pvp-" + strconv.Itoa(int(id)), selectedCharacterID: id, channel: s.catalog.Channels()[1], pvpRoom: room}
		p.channel.Type = 8
		room.members[seat] = &currentPVPMember{session: p, characterID: id}
	}
	room.members[0].session.conn.(*pvpFailNthConn).failAt = 5
	if err := s.sendCurrentPVPResultLocked(room, room.members[0].session, 0); err != nil {
		t.Fatal(err)
	}
	if room.match.resultSent || room.match.resultStage[0] != 3 || len(queue.tasks) != 1 {
		t.Fatal("failed update lost publication cursor")
	}
	queue.tasks[0].callback(s.gameplayNow())
	if !room.match.resultSent {
		t.Fatal("retry did not finish result")
	}
	for seat, m := range room.members[:2] {
		wire := m.session.conn.(*pvpFailNthConn).write.Bytes()
		var ids []uint16
		seenResult := false
		for len(wire) > 0 {
			p, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
			wire = rest
			ids = append(ids, p.Header.MsgID)
			if p.Header.MsgID == 47 {
				seenResult = true
				wantEXP := uint32(0)
				if seat == 0 {
					wantEXP = 1000
				}
				if binary.LittleEndian.Uint32(p.Body[6:]) != wantEXP {
					t.Fatal("op47 gain still zero or wrong recipient")
				}
			}
			if p.Header.MsgID == 48 && binary.LittleEndian.Uint32(p.Body[34:]) != 1 {
				t.Fatal("certification repeated")
			}
			if p.Header.MsgID == 48 {
				wantEXP, wantRP := uint32(0), uint32(0)
				if seenResult {
					wantRP = 950
					if seat == 0 {
						wantEXP, wantRP = 1000, 1050
					}
				}
				if binary.LittleEndian.Uint32(p.Body[8:]) != wantEXP || binary.LittleEndian.Uint32(p.Body[26:]) != wantRP {
					t.Fatalf("op48 old/new state: seat=%d after=%v body=%x", seat, seenResult, p.Body)
				}
			}
			if p.Header.MsgID == 323 && (p.Body[0] != 5 || binary.LittleEndian.Uint16(p.Body[31:]) != 499 || binary.LittleEndian.Uint32(p.Body[33:]) != 0) {
				t.Fatal("result reset lost normal task definitions/pre-match progress")
			}
			if p.Header.MsgID == 578 {
				wantCount := byte(2)
				if seat == 0 {
					wantCount = 3
				}
				if p.Body[0] != wantCount || binary.LittleEndian.Uint32(p.Body[3:]) != 1 || binary.LittleEndian.Uint16(p.Body[11:]) != 499 || binary.LittleEndian.Uint32(p.Body[13:]) != 1 {
					t.Fatal("daily/normal deltas missing, repeated, or unchanged tasks republished")
				}
			}
		}
		if !reflect.DeepEqual(ids, []uint16{48, 323, 47, 48, 578}) {
			t.Fatalf("native order/retry ids=%v", ids)
		}
	}
}

func TestPVPManualExperiencePublicationAndGradeTransition(t *testing.T) {
	s := pvpRoomTestService(t)
	repos, _ := s.repositoryGroup()
	ctx := context.Background()
	room := &currentPVPRoom{id: 2, battleMode: 2, started: true, seatState: [8]byte{1, 2, 3},
		match: &currentPVPMatch{startedAt: s.gameplayNow(), completedAt: s.gameplayNow(), winner: 0}}
	for seat, id := range []uint16{29, 30, 31} {
		p := &gameSession{conn: &bufferConn{}, accountID: "pvp-" + strconv.Itoa(int(id)), selectedCharacterID: id, channel: s.catalog.Channels()[1], pvpRoom: room}
		p.channel.Type = 24
		room.members[seat] = &currentPVPMember{session: p, characterID: id, rankPoint: 1234}
		if seat > 1 {
			continue
		}
		character, _, err := repos.Character.Load(ctx, strconv.Itoa(int(id)))
		if err != nil {
			t.Fatal(err)
		}
		character.Stats[dnfpvp.ExperienceKey] = 11950
		character.Stats[dnfpvp.RankPointKey] = 1234
		if err := repos.Character.Save(ctx, character); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.settleCurrentPVPDaily(room); err != nil {
		t.Fatal(err)
	}
	if err := s.publishCurrentPVPResults(room); err != nil {
		t.Fatal(err)
	}
	for seat, member := range room.members[:3] {
		wire := member.session.conn.(*bufferConn).write.Bytes()
		if seat == 2 {
			p, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
			if p.Header.MsgID != 47 || len(rest) != 0 || room.match.settled[seat] != nil {
				t.Fatal("observer received rewards")
			}
			continue
		}
		state := room.match.settled[seat]
		wantDelta, wantGrade := int64(0), byte(0)
		if seat == 0 {
			wantDelta, wantGrade = 450, 1
		}
		if state.ExperienceDelta != wantDelta || state.Character.Stats[dnfpvp.CertificationKey] != 0 || state.Character.Stats[dnfpvp.RankPointKey] != 1234 {
			t.Fatal("manual mode changed ranked state")
		}
		seenResult := false
		for len(wire) > 0 {
			p, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
			wire = rest
			if p.Header.MsgID == 47 {
				seenResult = true
				if int64(int32(binary.LittleEndian.Uint32(p.Body[6:]))) != wantDelta || p.Body[5] != wantGrade {
					t.Fatal("manual op47 gain/grade missing")
				}
			}
			if p.Header.MsgID == 48 && seenResult {
				if p.Body[20] != wantGrade || binary.LittleEndian.Uint32(p.Body[22:]) != 0 || int64(binary.LittleEndian.Uint32(p.Body[8:])) != 11950+wantDelta {
					t.Fatal("manual op48 new EXP/old grade missing")
				}
			}
		}
		beforeLen := member.session.conn.(*bufferConn).write.Len()
		if err := s.publishCurrentPVPResults(room); err != nil {
			t.Fatal(err)
		}
		if member.session.conn.(*bufferConn).write.Len() != beforeLen {
			t.Fatal("manual publication replayed reward")
		}
	}
}

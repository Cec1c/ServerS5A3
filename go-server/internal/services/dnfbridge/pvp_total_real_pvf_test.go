package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfpvp "longheng.io/server/internal/modules/dnf/pvp"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFTotalTargetRewardsAndNativeInventorySync(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	r := memory.NewMemoryGroup()
	ctx := context.Background()
	s := &Service{initialEquipmentArchive: archive, repositoryProvider: func() (repo.Group, bool) { return r, true }}
	raw, _ := json.Marshal(dnfpvp.TotalProgress{Stats: map[string]int64{dnfpvp.CertificationKey: 1000000, dnfpvp.WinsKey: 1000000}})
	if err := r.Account.Save(ctx, repo.AccountRecord{AccountID: "total-reward-fixture", Metadata: map[string]string{dnfpvp.TotalProgressMetadataKey: string(raw)}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1", "2"} {
		if err := r.Character.Save(ctx, repo.CharacterRecord{AccountID: "total-reward-fixture", CharacterID: id, Job: "0", Stats: map[string]int64{dnfpvp.EmblemsKey: 7, dnfpvp.WinsKey: 3}}); err != nil {
			t.Fatal(err)
		}
		if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: id, Slots: map[string]repo.ItemStack{}}); err != nil {
			t.Fatal(err)
		}
	}
	p := &gameSession{conn: &bufferConn{}, accountID: "total-reward-fixture", selectedCharacterID: 1, channel: channelcatalog.Channel{ID: 75, Type: 8, Group: "pvp"}}
	for attempt := 0; attempt < 2; attempt++ {
		if err := s.sendCurrentPVPDailyState(p); err != nil {
			t.Fatal(err)
		}
		packets := totalTestPackets(t, p)
		if len(packets) != 2 || packets[0].Header.MsgID != 14 || packets[1].Header.MsgID != 323 {
			t.Fatal("native task/item publication", packets)
		}
		body := packets[0].Body
		count := int(binary.LittleEndian.Uint16(body[1:]))
		if body[0] != 0 || len(body) != 3+119*count {
			t.Fatal("op14 item rows", len(body), count)
		}
		got := map[uint32]uint32{}
		for i := 0; i < count; i++ {
			row := body[3+i*119:]
			got[binary.LittleEndian.Uint32(row[2:])] += binary.LittleEndian.Uint32(row[6:])
		}
		if got[2] != 14007 || got[10093026] != 12 || got[10093082] != 95 {
			t.Fatal("native reward contents", got)
		}
	}
	for _, id := range []uint16{2, 1} {
		p.selectedCharacterID = id
		if err := s.sendCurrentPVPDailyState(p); err != nil {
			t.Fatal(err)
		}
		_ = totalTestPackets(t, p)
	}
	var emblems int64
	items := map[int64]int64{}
	for _, id := range []string{"1", "2"} {
		c, _, _ := r.Character.Load(ctx, id)
		emblems += c.Stats[dnfpvp.EmblemsKey]
		if c.Stats[dnfpvp.WinsKey] != 3 {
			t.Fatal("ordinary wins changed")
		}
		inv, _, _ := r.Inventory.Load(ctx, id)
		for _, stack := range inv.Slots {
			items[stack.ItemID] += stack.Count
		}
	}
	if emblems != 14014 || items[10093026] != 12 || items[10093082] != 95 {
		t.Fatal("reconnect/character switch duplicated rewards", emblems, items)
	}
	a, _, _ := r.Account.Load(ctx, p.accountID)
	progress, err := dnfpvp.ReadTotalProgress(a)
	if err != nil {
		t.Fatal(err)
	}
	for id := 800; id <= 845; id++ {
		if progress.Stats[fmt.Sprintf("pvp_total_target_%d_paid", id)] != 1 {
			t.Fatal("missing receipt", id)
		}
	}
}

func saveRealPVFTotalTeamThroughNativeRequest(t *testing.T, s *Service, session *gameSession, want dnfpvp.TotalTeam, slots [3]byte) {
	t.Helper()
	var request packetWriter
	request.writeRawDstr(rosterNameBytes(want.Name))
	request.writeBytes(slots[:])
	if err := s.handlePVPTotalTeam(session, 871, request.bytes()); err != nil {
		t.Fatal(err)
	}
	repos, _ := s.repositoryGroup()
	stored, err := dnfpvp.ReadTotalTeam(context.Background(), repos, session.accountID)
	if err != nil || stored.Team != want || stored.Slots != slots {
		t.Fatalf("saved team=%+v, error=%v", stored, err)
	}
	packets := totalTestPackets(t, session)
	// 1CFD700 closes the editor but does not store the team. 1D589B0 must
	// publish its actor-bound state before that ACK can send op7 for a new leader.
	if len(packets) != 2 || packets[0].Header.MsgID != 801 || packets[0].Header.Classification != 0 ||
		!bytes.Equal(packets[0].Body, request.bytes()) || packets[1].Header.MsgID != 871 ||
		packets[1].Header.Classification != 1 || !bytes.Equal(packets[1].Body, append([]byte{1}, slots[:]...)) {
		t.Fatalf("saved team missing native state before confirmation: %v", packets)
	}
}

func TestRealPVFTotalTeamActorsAndCatalog(t *testing.T) {
	testRealPVFTotalTeamActorsAndCatalog(t, [][2]byte{{0, 1}, {1, 1}, {2, 1}})
}

func TestRealPVFTotalUnconfiguredProfessionsAreRejected(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	a, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range [][2]byte{{13, 3}, {15, 1}} {
		if _, err := dnfpvp.LoadPrivateCreation(a, p[0], p[1]); err == nil {
			t.Fatalf("当前PVF已开放 %v，须补充该职业端到端验收", p)
		}
		rows := []repo.CharacterRecord{{CharacterID: "1", AccountID: "a", Slot: 0, Job: fmt.Sprint(p[0]), Level: 85, Stats: map[string]int64{"grow_type": int64(p[1])}}}
 rows=append(rows,repo.CharacterRecord{CharacterID:"2",AccountID:"a",Slot:1,Job:"0",Level:85,Stats:map[string]int64{"grow_type":1}},repo.CharacterRecord{CharacterID:"3",AccountID:"a",Slot:2,Job:"1",Level:85,Stats:map[string]int64{"grow_type":1}})
		if _, err := dnfpvp.ResolveTotalTeam("a", "测试队伍", [3]byte{0, 1, 2}, rows, a); err == nil {
			t.Fatalf("未配置职业被允许组队: %v", p)
		}
	}
}

func testRealPVFTotalTeamActorsAndCatalog(t *testing.T, professions [][2]byte) {
	t.Helper()
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfpvp.LoadTotal(archive)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Pools[0]) != 3 || len(catalog.Pools[1]) != 2 || len(catalog.Pools[2]) != 2 || len(catalog.Normal) != 46 || catalog.RatingRules.GradePoints[0][1] != 1500 {
		t.Fatal("wrong real total-match PVF catalog")
	}
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	s := &Service{options: options{accountPrefix: "dnf:"}, initialEquipmentArchive: archive, repositoryProvider: func() (repo.Group, bool) { return repos, true }}
	if err := s.preloadCharacterStatTable(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repos.Account.Save(ctx, repo.AccountRecord{AccountID: "dnf:1"}); err != nil {
		t.Fatal(err)
	}
	session := &gameSession{conn: &bufferConn{}, accountID: "dnf:1"}
	var ids [3]string
	for i, profession := range professions {
		job, grow := profession[0], profession[1]
		session.conn.(*bufferConn).write.Reset()
		request := append(buildCreateRequest(job, fmt.Sprintf("totalactor%d", i)), 0, 0, 0, 0, 0, 0, grow, 0)
		if err := s.handleUpperCreateCharacter(session, request); err != nil {
			t.Fatal(err)
		}
		ack, _ := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
		if ack.Body[0] != 1 {
			t.Fatalf("creation failed %x", ack.Body)
		}
		ids[i] = strconv.Itoa(int(binary.LittleEndian.Uint16(ack.Body[1:])))
	}
	n, _ := strconv.Atoi(ids[0])
	session.selectedCharacterID = uint16(n)
	session.channel = channelcatalog.Channel{ID: 75, Type: 8, Group: "pvp"}
	session.conn.(*bufferConn).write.Reset()
	if err := s.sendCurrentPVPDailyState(session); err != nil {
		t.Fatal(err)
	}
	missionPackets := totalTestPackets(t, session)
	if len(missionPackets) != 2 || missionPackets[0].Header.MsgID != 14 || missionPackets[1].Header.MsgID != 323 || len(missionPackets[1].Body) != 491 || missionPackets[1].Body[0] != 49 {
		t.Fatal("real PVF tasks missing before team registration")
	}
	for i := 0; i < 46; i++ {
		row := missionPackets[1].Body[31+i*10:]
		if binary.LittleEndian.Uint16(row) != uint16(800+i) || binary.LittleEndian.Uint32(row[2:]) != 0 {
			t.Fatal("wrong real target row", i)
		}
	}
	team := dnfpvp.TotalTeam{Name: "巅峰测试", Characters: ids}
	saveRealPVFTotalTeamThroughNativeRequest(t, s, session, team, [3]byte{0, 1, 2})
	// Reopening must read the committed name/slots even when the representative
	// is already the logged-in actor and native op871 does not request op7.
	if err := s.sendCurrentPVPTotalTeamSelection(session); err != nil {
		t.Fatal(err)
	}
	reopened := totalTestPackets(t, session)
	if len(reopened) != 3 || reopened[0].Header.MsgID != 801 || reopened[1].Header.MsgID != 802 || reopened[2].Header.MsgID != 800 {
		t.Fatal("saved team did not restore its native scene state", reopened)
	}
	changedLeader := dnfpvp.TotalTeam{Name: team.Name, Characters: [3]string{ids[1], ids[0], ids[2]}}
	saveRealPVFTotalTeamThroughNativeRequest(t, s, session, changedLeader, [3]byte{1, 0, 2})
	saveRealPVFTotalTeamThroughNativeRequest(t, s, session, team, [3]byte{0, 1, 2})
	loaded, err := s.loadCurrentPVPTotalTeam(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	for i, b := range loaded.actors {
		if len(b) < 120 || b[0] != 85 || b[1] != professions[i][0] || b[2] != professions[i][1] || b[3] != 1 || binary.LittleEndian.Uint32(b[4:]) != 92 {
			t.Fatalf("native actor prefix %d: %x", i, b[:min(12, len(b))])
		}
		t.Logf("actor %s job=%d grow=%d bytes=%d", ids[i], professions[i][0], professions[i][1], len(b))
	}
	state, err := dnfpvp.PrepareTotal(ctx, repos, catalog, session.accountID, ids[0], ids, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	body, err := buildCurrentPVPTotalCertification(state, ids[0], catalog, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 129 {
		t.Fatal("native total48 length", len(body))
	}
	for i := 0; i < 3; i++ {
		row := body[51+26*i : 51+26*(i+1)]
		if binary.LittleEndian.Uint32(row[14:]) != 12000 || binary.LittleEndian.Uint32(row[22:]) != 12000 {
			t.Fatal("native upper bound offset", row)
		}
	}
	// Drive two independently created, real-PVF teams through matching,
	// both load barriers, three actor losses and the durable ranked result.
	peer := &gameSession{conn: &bufferConn{}, accountID: "dnf:2"}
	if err := repos.Account.Save(ctx, repo.AccountRecord{AccountID: peer.accountID}); err != nil {
		t.Fatal(err)
	}
	var peerIDs [3]string
	for i, profession := range professions {
		job, grow := profession[0], profession[1]
		peer.conn.(*bufferConn).write.Reset()
		request := append(buildCreateRequest(job, fmt.Sprintf("totalpeer%d", i)), 0, 0, 0, 0, 0, 0, grow, 0)
		if err := s.handleUpperCreateCharacter(peer, request); err != nil {
			t.Fatal(err)
		}
		ack, _ := splitGameServerUpperPacket(t, peer.conn.(*bufferConn).write.Bytes())
		if ack.Body[0] != 1 {
			t.Fatalf("peer creation: %x", ack.Body)
		}
		peerIDs[i] = strconv.Itoa(int(binary.LittleEndian.Uint16(ack.Body[1:])))
	}
	peerID, _ := strconv.Atoi(peerIDs[0])
	peer.selectedCharacterID = uint16(peerID)
	peer.channel = session.channel
	peer.conn.(*bufferConn).write.Reset()
	saveRealPVFTotalTeamThroughNativeRequest(t, s, peer, dnfpvp.TotalTeam{Name: "对方队伍", Characters: peerIDs}, [3]byte{0, 1, 2})
	s.gameplayTimers = newFakeCurrentDungeonDeathTimerQueue()
	s.gameSessions = map[uint16]*gameSession{}
	for _, p := range []*gameSession{session, peer} {
		p.channel = channelcatalog.Channel{ID: 75, Type: 8, Group: "pvp"}
		p.characterGeneration, p.sceneBootstrapTailSent = 1, true
		p.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: p.selectedCharacterID, TownID: 10, PositionX: 474, PositionY: 234, MovementCode: 5, PositionValid: true}
		p.partyPeer = currentPartyPeerEndpointRegistration{InnerIPv4: [4]byte{127, 0, 0, 1}, OuterIPv4: [4]byte{127, 0, 0, 1}, Port: 12000 + p.selectedCharacterID, MTU: currentPartyPeerMinimumMTU}
		s.gameSessions[p.selectedCharacterID] = p
		p.conn.(*bufferConn).write.Reset()
		if err := s.handleCurrentPVPMatching(p, 520, []byte{1, 7, 0, 0, 0, 255, 255, 255, 255, 255, 255, 255, 255, 0}); err != nil {
			t.Fatal(err)
		}
	}
	room := session.pvpRoom
	if room == nil || peer.pvpRoom != room || room.battleMode != 8 {
		t.Fatal("real total matching did not pair")
	}
	for _, p := range []*gameSession{session, peer} {
		if err := s.completeCurrentPVPMatching(p, binary.LittleEndian.AppendUint32(nil, room.matchingToken)); err != nil {
			t.Fatal(err)
		}
	}
	if !room.started || room.match == nil || room.match.total == nil {
		t.Fatal("real total matching did not start")
	}
	for _, p := range []*gameSession{session, peer} {
		if err := s.handleCurrentPVPMatchSignal(p, 298, nil); err != nil {
			t.Fatal(err)
		}
		if err := s.handleCurrentPVPTotalOrder(p, []byte{0}); err != nil {
			t.Fatal(err)
		}
	}
	// The first owner's early selection precedes the second load and is ignored.
	if err := s.handleCurrentPVPTotalOrder(session, []byte{0}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if i > 0 {
			if err := s.handleCurrentPVPTotalOrder(peer, []byte{byte(i)}); err != nil {
				t.Fatal(err)
			}
		}
		death := binary.LittleEndian.AppendUint16(nil, peer.selectedCharacterID)
		death = binary.LittleEndian.AppendUint16(death, session.selectedCharacterID)
		death = append(death, 0, 100, 0)
		if err := s.handleCurrentPVPDeath(peer, death); err != nil {
			t.Fatal(err)
		}
		if (i == 2) != room.match.resultSent {
			t.Fatal("real total result at wrong actor count", i)
		}
	}
	for _, p := range []*gameSession{session, peer} {
		account, _, err := repos.Account.Load(ctx, p.accountID)
		if err != nil {
			t.Fatal(err)
		}
		progress, err := dnfpvp.ReadTotalProgress(account)
		if err != nil {
			t.Fatal(err)
		}
		if progress.Stats[dnfpvp.CertificationKey] != 1 || progress.Stats[dnfpvp.RankPointKey] == 0 {
			t.Fatal("ranked team progress missing", progress.Stats)
		}
		c, _, err := repos.Character.Load(ctx, strconv.Itoa(int(p.selectedCharacterID)))
		if err != nil {
			t.Fatal(err)
		}
		if c.Stats[dnfpvp.CertificationKey] != 0 || c.Stats[dnfpvp.RankPointKey] != 0 {
			t.Fatal("ordinary rating overwritten")
		}
		seen := map[uint16]bool{}
		for _, packet := range totalTestPackets(t, p) {
			seen[packet.Header.MsgID] = true
			if packet.Header.MsgID == 48 && len(packet.Body) != 129 {
				t.Fatal("real total certification size", len(packet.Body))
			}
			if packet.Header.MsgID == 803 && len(packet.Body) != 10 {
				t.Fatal("real total order size", len(packet.Body))
			}
			if packet.Header.MsgID == 323 && (len(packet.Body) != 491 || packet.Body[0] != 49) {
				t.Fatal("result replaced target rows with daily-only state")
			}
			if packet.Header.MsgID == 578 {
				foundBattle, foundWin := false, false
				for offset := 1; offset < len(packet.Body); offset += 10 {
					id := binary.LittleEndian.Uint16(packet.Body[offset:])
					progress := binary.LittleEndian.Uint32(packet.Body[offset+2:])
					if id == 800 {
						foundBattle = progress == 1
					}
					if id == 824 {
						foundWin = progress == 1
					}
				}
				if !foundBattle || (p == session) != foundWin {
					t.Fatal("real result did not synchronize win/participation targets")
				}
			}
		}
		for _, op := range []uint16{504, 800, 802, 803, 804, 47, 48, 323, 578} {
			if !seen[op] {
				t.Fatal("missing native packet", op)
			}
		}
	}
	// A real missing skill record must reject the whole actor snapshot.
	if err := repos.Skill.Save(ctx, repo.SkillRecord{CharacterID: ids[2], Skills: map[int64]repo.SkillState{800: {Level: 1}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.loadCurrentPVPTotalTeam(ctx, session); err == nil {
		t.Fatal("invalid actor skills accepted")
	}
}

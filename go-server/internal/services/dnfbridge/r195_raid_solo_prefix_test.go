package dnfbridge

import (
	"encoding/binary"
	"reflect"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/raid"
)

type r195SoloPrefixFixture struct {
	service    *Service
	leader     *gameSession
	member     *gameSession
	leaderConn *bufferConn
	memberConn *bufferConn
	raidKey    uint32
	memberKey  uint32
	leaderID   uint16
	memberID   uint16
	awayID     uint16
}

// newR195SoloPrefixFixture uses the existing real current-raid create,
// application and approval handlers before the ordinary-party invite path.
// It deliberately leaves the two characters in separate ordinary and raid
// state until each test chooses its exact transition.
func newR195SoloPrefixFixture(t *testing.T) *r195SoloPrefixFixture {
	t.Helper()
	service, leader, member, leaderConn, memberConn, _ := prepareCurrentRaidApplicationTest(t)
	// The shared friend fixture registered town presence before changing its
	// sessions to raid channel 23. Re-register both owners in that actual channel
	// so a later third member is compared against the same presence scope.
	for _, session := range []*gameSession{leader, member} {
		presence, found := service.onlinePlayers.PlayerForCharacter(session.selectedCharacterID)
		if !found {
			t.Fatal("raid fixture owner has no town presence")
		}
		presence.Session = session
		service.onlinePlayers.EnterArea(&presence)
	}
	if err := service.handleGameCommand(
		member,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketRaidOtherChannelRequestJoin),
		currentRaidApplyFixture(leader.selectedCharacterID),
	); err != nil {
		t.Fatalf("real raid application: %v", err)
	}
	if err := service.handleGameCommand(
		leader,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketResponsePeer),
		currentRaidApprovalFixture(member.selectedCharacterID),
	); err != nil {
		t.Fatalf("real raid approval: %v", err)
	}
	state, found := service.runtimeRaidForMember(leader.selectedCharacterID)
	if !found {
		t.Fatal("approved raid membership is missing")
	}
	leaderConn.write.Reset()
	memberConn.write.Reset()
	return &r195SoloPrefixFixture{
		service:    service,
		leader:     leader,
		member:     member,
		leaderConn: leaderConn,
		memberConn: memberConn,
		raidKey:    state.RaidKey,
		memberKey:  state.RaidKey,
		leaderID:   leader.selectedCharacterID,
		memberID:   member.selectedCharacterID,
	}
}

func (f *r195SoloPrefixFixture) formOrdinaryParty(t *testing.T) {
	t.Helper()
	f.leaderConn.write.Reset()
	f.memberConn.write.Reset()
	ok, reason, _ := f.service.forceInviteParty(f.leaderID, f.memberID, 0, [3]uint32{})
	if !ok {
		t.Fatalf("real ordinary-party invite: %s", reason)
	}
	if err := f.service.handleOnlineResponsePeer(
		f.member,
		uint16(dnfenum.CmdPacketResponsePeer),
		buildResponsePeerBody(f.leaderID, 0, 0),
	); err != nil {
		t.Fatalf("real ordinary-party op11 accept: %v", err)
	}
}

func (f *r195SoloPrefixFixture) leaveOrdinaryParty(t *testing.T, session *gameSession) {
	t.Helper()
	if err := f.service.handleGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketLeaveParty),
		nil,
	); err != nil {
		t.Fatalf("real op13 ordinary-party leave: %v", err)
	}
}

func (f *r195SoloPrefixFixture) state(t *testing.T) runtimeRaidState {
	t.Helper()
	return f.stateFor(t, f.leaderID)
}

func (f *r195SoloPrefixFixture) stateFor(t *testing.T, charID uint16) runtimeRaidState {
	t.Helper()
	state, found := f.service.runtimeRaidForMember(charID)
	if !found {
		t.Fatalf("raid state for character %d disappeared", charID)
	}
	return state
}

func (f *r195SoloPrefixFixture) addAwayRaidMember(t *testing.T, group, slot byte) {
	t.Helper()
	if f.awayID != 0 {
		t.Fatal("away raid member already installed")
	}
	f.awayID = 8
	f.service.raidMu.Lock()
	live := f.service.raids[f.raidKey]
	if live == nil {
		f.service.raidMu.Unlock()
		t.Fatal("raid state disappeared before away member setup")
	}
	if runtimeRaidMemberIndex(*live, f.awayID) >= 0 {
		f.service.raidMu.Unlock()
		t.Fatal("away character unexpectedly joined through a production handler")
	}
	live.Members = append(live.Members, runtimeRaidMemberState{
		CharID: f.awayID, GroupIndex: group, SlotOrder: slot,
		UserState: 1, HPPercent: 100, MPPercent: 100,
	})
	f.service.raidMu.Unlock()
}

func (f *r195SoloPrefixFixture) setRawPlacement(t *testing.T, charID uint16, group, slot byte) {
	t.Helper()
	f.service.raidMu.Lock()
	defer f.service.raidMu.Unlock()
	live := f.service.raids[f.raidKey]
	if live == nil {
		t.Fatal("raid state disappeared during placement setup")
	}
	index := runtimeRaidMemberIndex(*live, charID)
	if index < 0 {
		t.Fatalf("character %d is not in the raid", charID)
	}
	live.Members[index].GroupIndex = group
	live.Members[index].SlotOrder = slot
}

func (f *r195SoloPrefixFixture) setSolo(t *testing.T, charID uint16, value bool) {
	t.Helper()
	f.setSoloForKey(t, f.raidKey, charID, value)
}

func (f *r195SoloPrefixFixture) setSoloForKey(t *testing.T, raidKey uint32, charID uint16, value bool) {
	t.Helper()
	f.service.raidMu.Lock()
	defer f.service.raidMu.Unlock()
	live := f.service.raids[raidKey]
	if live == nil {
		t.Fatal("raid state disappeared during solo setup")
	}
	index := runtimeRaidMemberIndex(*live, charID)
	if index < 0 {
		t.Fatalf("character %d is not in the raid", charID)
	}
	live.Members[index].OrdinaryPartySolo = value
}

func (f *r195SoloPrefixFixture) addSyntheticRaidMember(t *testing.T, charID uint16, group, slot byte) {
	t.Helper()
	f.service.raidMu.Lock()
	defer f.service.raidMu.Unlock()
	live := f.service.raids[f.raidKey]
	if live == nil {
		t.Fatal("raid state disappeared during synthetic member setup")
	}
	if runtimeRaidMemberIndex(*live, charID) >= 0 {
		t.Fatalf("synthetic raid member %d already exists", charID)
	}
	live.Members = append(live.Members, runtimeRaidMemberState{
		CharID: charID, GroupIndex: group, SlotOrder: slot,
		UserState: 1, HPPercent: 100, MPPercent: 100,
	})
}

func (f *r195SoloPrefixFixture) moveMemberToIsolatedRaid(t *testing.T, raidKey uint32) {
	t.Helper()
	f.service.raidMu.Lock()
	defer f.service.raidMu.Unlock()
	source := f.service.raids[f.raidKey]
	if source == nil {
		t.Fatal("anchor raid state disappeared during cross-raid setup")
	}
	index := runtimeRaidMemberIndex(*source, f.memberID)
	if index < 0 {
		t.Fatal("member is not in the anchor raid")
	}
	member := source.Members[index]
	source.Members = append(source.Members[:index], source.Members[index+1:]...)
	member.GroupIndex, member.SlotOrder = 1, 0
	f.service.raids[raidKey] = &runtimeRaidState{
		RaidKey: raidKey, LeaderID: f.memberID, CurrentLayout: true,
		Members: []runtimeRaidMemberState{member},
	}
	f.memberKey = raidKey
}

func (f *r195SoloPrefixFixture) activateRaidParticipation(t *testing.T, charID uint16) {
	t.Helper()
	f.service.raidMu.Lock()
	defer f.service.raidMu.Unlock()
	live := f.service.raids[f.raidKey]
	if live == nil || live.Battle == nil {
		t.Fatal("raid battle disappeared during active-dungeon setup")
	}
	entry := live.Battle.Participation[charID]
	entry.RunID = "r195-solo-prefix-active-run"
	entry.Phase = live.Battle.Phase
	live.Battle.Participation[charID] = entry
}

func (f *r195SoloPrefixFixture) bindOnlineRaidPeer(t *testing.T, charID uint16) (*gameSession, *bufferConn) {
	t.Helper()
	conn := &bufferConn{}
	peer := &gameSession{
		conn:                conn,
		connID:              "r195-solo-prefix-peer",
		channel:             f.leader.channel,
		accountID:           "dnf:2",
		selectedCharacterID: charID,
		loginPacketSeen:     true,
		accountFromToken:    true,
	}
	f.service.bindGameSessionCharacter(peer, charID)
	if f.service.onlinePlayers != nil {
		presence, found := f.service.onlinePlayers.PlayerForCharacter(f.leaderID)
		if !found {
			t.Fatal("inviter town presence is missing")
		}
		presence.CharacterID, presence.AccountID, presence.Session = charID, peer.accountID, peer
		f.service.onlinePlayers.EnterArea(&presence)
		if !f.service.onlinePlayers.PeerInSameArea(charID, f.memberID) {
			t.Fatal("transfer peer did not enter the invitee's current presence scope")
		}
	}
	return peer, conn
}

type r195SoloPrefixFrozen struct {
	memberGroups map[uint16]byte
	accounts     map[uint16]string
	members      map[uint16]currentRaidParticipation
}

func (f *r195SoloPrefixFixture) installBattle(t *testing.T) {
	t.Helper()
	accounts := map[uint16]string{
		f.leaderID: f.leader.accountID,
		f.memberID: f.member.accountID,
	}
	if f.awayID != 0 {
		repositories, ok := f.service.repositoryGroup()
		if !ok || repositories.Character == nil {
			t.Fatal("character repository unavailable for away raid member")
		}
		character, found, err := repositories.Character.Load(nil, strconv.Itoa(int(f.awayID)))
		if err != nil || !found {
			t.Fatalf("load away raid member: found=%t err=%v", found, err)
		}
		accounts[f.awayID] = character.AccountID
	}
	groups := map[uint16]byte{}
	participation := map[uint16]currentRaidParticipation{}

	f.service.raidMu.Lock()
	defer f.service.raidMu.Unlock()
	live := f.service.raids[f.raidKey]
	if live == nil {
		t.Fatal("raid state disappeared before battle setup")
	}
	for _, member := range live.Members {
		groups[member.CharID] = member.GroupIndex
		participation[member.CharID] = currentRaidParticipation{
			RunID: "r195-solo-prefix-frozen-run",
			Phase: 0,
		}
	}
	live.Started = true
	live.Battle = &runtimeRaidBattle{
		InstanceID:     "r195-solo-prefix-frozen-battle",
		Mode:           1,
		ChannelID:      f.leader.channel.ID,
		ChannelType:    byte(f.leader.channel.Type),
		Phase:          1,
		MemberAccounts: accounts,
		MemberGroups:   groups,
		Participation:  participation,
	}
}

func (f *r195SoloPrefixFixture) frozen(t *testing.T) r195SoloPrefixFrozen {
	t.Helper()
	state := f.state(t)
	if state.Battle == nil {
		t.Fatal("raid battle is missing")
	}
	out := r195SoloPrefixFrozen{
		memberGroups: make(map[uint16]byte, len(state.Battle.MemberGroups)),
		accounts:     make(map[uint16]string, len(state.Battle.MemberAccounts)),
		members:      make(map[uint16]currentRaidParticipation, len(state.Battle.Participation)),
	}
	for id, group := range state.Battle.MemberGroups {
		out.memberGroups[id] = group
	}
	for id, account := range state.Battle.MemberAccounts {
		out.accounts[id] = account
	}
	for id, participation := range state.Battle.Participation {
		out.members[id] = participation
	}
	return out
}

func (f *r195SoloPrefixFixture) assertFrozen(t *testing.T, want r195SoloPrefixFrozen) {
	t.Helper()
	got := f.frozen(t)
	if !reflect.DeepEqual(got.memberGroups, want.memberGroups) {
		t.Fatalf("Battle.MemberGroups changed: got=%v want=%v", got.memberGroups, want.memberGroups)
	}
	if !reflect.DeepEqual(got.accounts, want.accounts) {
		t.Fatalf("reward member accounts changed: got=%v want=%v", got.accounts, want.accounts)
	}
	if !reflect.DeepEqual(got.members, want.members) {
		t.Fatalf("reward participation changed: got=%v want=%v", got.members, want.members)
	}
}

func r195SoloPrefixRosters(t *testing.T, raw []byte, raidKey uint32) [][]raidRefreshMemberForTest {
	t.Helper()
	var rosters [][]raidRefreshMemberForTest
	for len(raw) > 0 {
		packet, rest := splitGameServerUpperPacket(t, raw)
		raw = rest
		if packet.Header.Classification != 0 ||
			packet.Header.MsgID != raid.RaidMemberRefreshMsgID ||
			len(packet.Body) < 8 ||
			binary.LittleEndian.Uint32(packet.Body[4:8]) != raid.RaidMemberRefreshMode3 {
			continue
		}
		rosters = append(rosters, parseR195CurrentRaidRoster(t, packet.Body, raidKey))
	}
	return rosters
}

// Current 24.9 rows add a byte and a second narrow DSTR after the legacy core.
// Decode both boundaries instead of feeding current bytes to the legacy parser.
func parseR195CurrentRaidRoster(t *testing.T, body []byte, raidKey uint32) []raidRefreshMemberForTest {
	t.Helper()
	if len(body) < 9 || binary.LittleEndian.Uint32(body[:4]) != raidKey || binary.LittleEndian.Uint32(body[4:8]) != 3 {
		t.Fatal("invalid current roster header")
	}
	offset := 9
	rows := make([]raidRefreshMemberForTest, 0, int(body[8]))
	for i := 0; i < int(body[8]); i++ {
		if len(body)-offset < 7 {
			t.Fatalf("current roster row %d missing name boundary", i)
		}
		row := raidRefreshMemberForTest{CharID: binary.LittleEndian.Uint16(body[offset:]), State: body[offset+2]}
		nameLen := int(binary.LittleEndian.Uint32(body[offset+3:]))
		offset += 7
		if nameLen > len(body)-offset || len(body)-offset-nameLen < 21+5 {
			t.Fatalf("current roster row %d truncated after name", i)
		}
		offset += nameLen
		row.GroupIndex, row.SlotOrder = body[offset+2], body[offset+3]
		offset += 21
		extraLen := int(binary.LittleEndian.Uint32(body[offset+1:]))
		offset += 5
		if extraLen > len(body)-offset {
			t.Fatalf("current roster row %d extra name truncated", i)
		}
		offset += extraLen
		rows = append(rows, row)
	}
	if offset != len(body) {
		t.Fatalf("current roster trailing bytes=%d", len(body)-offset)
	}
	return rows
}

func r195SoloPrefixRosterMember(t *testing.T, rows []raidRefreshMemberForTest, charID uint16) raidRefreshMemberForTest {
	t.Helper()
	for _, row := range rows {
		if row.CharID == charID {
			return row
		}
	}
	t.Fatalf("raid roster has no row for character %d: %+v", charID, rows)
	return raidRefreshMemberForTest{}
}

func (f *r195SoloPrefixFixture) assertRostersForKey(t *testing.T, raw []byte, raidKey uint32, want map[uint16][2]byte) {
	t.Helper()
	rosters := r195SoloPrefixRosters(t, raw, raidKey)
	if len(rosters) == 0 {
		t.Fatal("raid roster 591 is missing")
	}
	for _, rows := range rosters {
		for charID, placement := range want {
			row := r195SoloPrefixRosterMember(t, rows, charID)
			if row.GroupIndex != placement[0] || row.SlotOrder != placement[1] {
				t.Fatalf("character %d 591 group=%d slot=%d, want group=%d slot=%d",
					charID, row.GroupIndex, row.SlotOrder, placement[0], placement[1])
			}
		}
	}
}

func (f *r195SoloPrefixFixture) assertRosters(t *testing.T, raw []byte, want map[uint16][2]byte) {
	t.Helper()
	f.assertRostersForKey(t, raw, f.raidKey, want)
}

func (f *r195SoloPrefixFixture) assertNoRoster(t *testing.T, raw []byte) {
	t.Helper()
	if rosters := r195SoloPrefixRosters(t, raw, f.raidKey); len(rosters) != 0 {
		t.Fatalf("unexpected raid roster 591 count=%d", len(rosters))
	}
}

func r195SoloPrefixMetadataLeaderGroup(t *testing.T, body []byte, raidKey uint32) byte {
	t.Helper()
	if len(body) < 16 || binary.LittleEndian.Uint32(body[:4]) != raidKey || binary.LittleEndian.Uint32(body[4:8]) != 2 {
		t.Fatalf("mode2 metadata header key=%x mode=%d", body[:minIntForR195SoloPrefix(len(body), 8)], metadataModeForR195SoloPrefix(body))
	}
	offset := 12
	nameLen := int(binary.LittleEndian.Uint32(body[offset : offset+4]))
	offset += 4 + nameLen
	// route, state, phase, Field34 and the four metadata flags.
	offset += 11
	if len(body) < offset+3+4 {
		t.Fatalf("mode2 metadata leader record is short: offset=%d len=%d", offset, len(body))
	}
	offset += 2 + 1
	leaderNameLen := int(binary.LittleEndian.Uint32(body[offset : offset+4]))
	offset += 4 + leaderNameLen
	// Field40 and Field44 precede the native PartyIndex byte.
	offset += 2
	if len(body) <= offset {
		t.Fatalf("mode2 metadata group byte is missing: offset=%d len=%d", offset, len(body))
	}
	return body[offset]
}

func metadataModeForR195SoloPrefix(body []byte) uint32 {
	if len(body) < 8 {
		return 0
	}
	return binary.LittleEndian.Uint32(body[4:8])
}

func minIntForR195SoloPrefix(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestR195RaidSoloPrefixLeaveProjectsSoloAndPreservesRaidAuthority(t *testing.T) {
	f := newR195SoloPrefixFixture(t)
	f.addAwayRaidMember(t, 3, 0)
	f.formOrdinaryParty(t)

	// Make the ordinary-party members occupy different planned raid groups.
	// The leave projection must hide both as "单人" without moving either one.
	f.setRawPlacement(t, f.memberID, 2, 0)
	f.installBattle(t)
	beforeLeave := f.state(t)
	wantLeader := mustR195SoloPrefixMember(t, beforeLeave, f.leaderID)
	wantMember := mustR195SoloPrefixMember(t, beforeLeave, f.memberID)
	if wantLeader.GroupIndex != 1 || wantMember.GroupIndex != 2 {
		t.Fatalf("placement setup: leader=%+v member=%+v", wantLeader, wantMember)
	}
	frozen := f.frozen(t)

	f.leaderConn.write.Reset()
	f.memberConn.write.Reset()
	f.leaveOrdinaryParty(t, f.member)

	afterLeave := f.state(t)
	for _, tc := range []struct {
		id    uint16
		group byte
		slot  byte
		solo  bool
	}{
		{f.leaderID, 1, 0, true},
		{f.memberID, 2, 0, true},
		{f.awayID, 3, 0, false},
	} {
		member := mustR195SoloPrefixMember(t, afterLeave, tc.id)
		if member.GroupIndex != tc.group || member.SlotOrder != tc.slot || member.OrdinaryPartySolo != tc.solo {
			t.Fatalf("post-leave character %d group=%d slot=%d solo=%t, want group=%d slot=%d solo=%t",
				tc.id, member.GroupIndex, member.SlotOrder, member.OrdinaryPartySolo, tc.group, tc.slot, tc.solo)
		}
	}
	f.assertFrozen(t, frozen)

	wantRoster := map[uint16][2]byte{
		f.leaderID: {0, 0},
		f.memberID: {0, 0},
		f.awayID:   {3, 0},
	}
	f.assertRosters(t, f.leaderConn.write.Bytes(), wantRoster)
	f.assertRosters(t, f.memberConn.write.Bytes(), wantRoster)

	projection := currentRuntimeRaidMembers(afterLeave)
	for _, charID := range []uint16{f.leaderID, f.memberID} {
		row := r195SoloPrefixRosterMember(t, currentRaidMemberRowsForR195SoloPrefix(projection), charID)
		if row.GroupIndex != 0 || row.SlotOrder != 0 {
			t.Fatalf("projected character %d group=%d slot=%d, want solo 0/0",
				charID, row.GroupIndex, row.SlotOrder)
		}
	}

	// A repeated leave must not move, re-group or re-earn the same raid
	// membership. Another 591 is allowed for network-write retry.
	f.leaderConn.write.Reset()
	f.memberConn.write.Reset()
	f.leaveOrdinaryParty(t, f.member)
	afterRetry := f.state(t)
	for _, tc := range []struct {
		id    uint16
		group byte
		slot  byte
		solo  bool
	}{
		{f.leaderID, 1, 0, true},
		{f.memberID, 2, 0, true},
		{f.awayID, 3, 0, false},
	} {
		member := mustR195SoloPrefixMember(t, afterRetry, tc.id)
		if member.GroupIndex != tc.group || member.SlotOrder != tc.slot || member.OrdinaryPartySolo != tc.solo {
			t.Fatalf("retry character %d group=%d slot=%d solo=%t, want group=%d slot=%d solo=%t",
				tc.id, member.GroupIndex, member.SlotOrder, member.OrdinaryPartySolo, tc.group, tc.slot, tc.solo)
		}
	}
	f.assertFrozen(t, frozen)
	// The helper intentionally re-sends 591 even when the presentation bool is
	// unchanged, so a previous failed write can be retried without changing
	// raid placement or reward ownership.
	f.assertRosters(t, f.leaderConn.write.Bytes(), wantRoster)
	f.assertRosters(t, f.memberConn.write.Bytes(), wantRoster)

	// Rejoin through the existing real invite/accept path. The anchor keeps
	// group 1, the member moves out of group 2 and both lose the solo icon.
	f.formOrdinaryParty(t)
	afterRejoin := f.state(t)
	for _, id := range []uint16{f.leaderID, f.memberID} {
		member := mustR195SoloPrefixMember(t, afterRejoin, id)
		if member.GroupIndex != 1 || member.OrdinaryPartySolo {
			t.Fatalf("rejoined character %d group=%d solo=%t, want group=1 solo=false",
				id, member.GroupIndex, member.OrdinaryPartySolo)
		}
	}
	away := mustR195SoloPrefixMember(t, afterRejoin, f.awayID)
	if away.GroupIndex != 3 || away.OrdinaryPartySolo {
		t.Fatalf("unrelated raid group changed on rejoin: %+v", away)
	}
	f.assertFrozen(t, frozen)
	f.assertRosters(t, f.leaderConn.write.Bytes(), map[uint16][2]byte{
		f.leaderID: {1, 0},
		f.memberID: {1, 1},
		f.awayID:   {3, 0},
	})
}

func TestR195RaidSoloPrefixSameGroupJoinClearsSoloAndRebroadcasts(t *testing.T) {
	f := newR195SoloPrefixFixture(t)
	f.formOrdinaryParty(t)
	f.installBattle(t)
	frozen := f.frozen(t)

	f.leaveOrdinaryParty(t, f.member)
	before := f.state(t)
	beforePlacement := map[uint16][2]byte{}
	for _, id := range []uint16{f.leaderID, f.memberID} {
		member := mustR195SoloPrefixMember(t, before, id)
		if member.GroupIndex != 1 || !member.OrdinaryPartySolo {
			t.Fatalf("leave setup character %d group=%d solo=%t", id, member.GroupIndex, member.OrdinaryPartySolo)
		}
		beforePlacement[id] = [2]byte{member.GroupIndex, member.SlotOrder}
	}

	f.leaderConn.write.Reset()
	f.memberConn.write.Reset()
	f.formOrdinaryParty(t)
	after := f.state(t)
	for _, id := range []uint16{f.leaderID, f.memberID} {
		member := mustR195SoloPrefixMember(t, after, id)
		if [2]byte{member.GroupIndex, member.SlotOrder} != beforePlacement[id] || member.OrdinaryPartySolo {
			t.Fatalf("same-group rejoin character %d was not restored: %+v", id, member)
		}
	}
	f.assertFrozen(t, frozen)

	want := map[uint16][2]byte{
		f.leaderID: {1, 0},
		f.memberID: {1, 1},
	}
	f.assertRosters(t, f.leaderConn.write.Bytes(), want)
	f.assertRosters(t, f.memberConn.write.Bytes(), want)
}

func TestR195RaidSoloPrefixPlanAndNormalizeClearMarkers(t *testing.T) {
	t.Run("plan_same_group_clears_without_moving", func(t *testing.T) {
		raid := runtimeRaidState{
			RaidKey: 1, LeaderID: 19, CurrentLayout: true,
			Members: []runtimeRaidMemberState{
				{CharID: 19, GroupIndex: 1, SlotOrder: 0, OrdinaryPartySolo: true},
				{CharID: 20, GroupIndex: 1, SlotOrder: 1, OrdinaryPartySolo: true},
			},
		}
		party := alignedcmd.PartyState{
			PartyID: 7, UserID: 19,
			Members: []alignedcmd.PartyMemberState{{UserID: 19}, {UserID: 20}},
		}
		candidate, changed := planRuntimeRaidGroupSynchronization(raid, party)
		if !changed || candidate == nil {
			t.Fatal("solo-only join was not reported as a changed plan")
		}
		for _, member := range candidate {
			if member.OrdinaryPartySolo {
				t.Fatalf("joined member retained solo marker: %+v", member)
			}
			if member.GroupIndex != 1 || member.SlotOrder > 1 {
				t.Fatalf("same-group join moved a member: %+v", member)
			}
		}
		for _, member := range raid.Members {
			if !member.OrdinaryPartySolo {
				t.Fatal("plan mutated the live input before commit")
			}
		}
	})

	t.Run("normalize_clears_assigned_and_places_zero", func(t *testing.T) {
		assigned := runtimeRaidState{
			CurrentLayout: true,
			Members: []runtimeRaidMemberState{
				{CharID: 19, GroupIndex: 1, SlotOrder: 0, OrdinaryPartySolo: true},
				{CharID: 20, GroupIndex: 1, SlotOrder: 1},
			},
		}
		if !normalizeRuntimeRaidMemberGroups(&assigned) || assigned.Members[0].OrdinaryPartySolo {
			t.Fatal("normalization did not clear a same-group solo marker")
		}
		if assigned.Members[0].GroupIndex != 1 || assigned.Members[0].SlotOrder != 0 {
			t.Fatalf("solo-only normalization moved the member: %+v", assigned.Members[0])
		}

		unassigned := runtimeRaidState{
			CurrentLayout: true,
			Members: []runtimeRaidMemberState{
				{CharID: 19, GroupIndex: 0, SlotOrder: 0, OrdinaryPartySolo: true},
			},
		}
		if !normalizeRuntimeRaidMemberGroups(&unassigned) {
			t.Fatal("first-start normalization did not report its placement")
		}
		member := unassigned.Members[0]
		if member.GroupIndex == 0 || member.OrdinaryPartySolo {
			t.Fatalf("first-start normalization left an unplaced/solo member: %+v", member)
		}
	})
}

func TestR195RaidSoloPrefixExplicitRaidAssignmentClearsSolo(t *testing.T) {
	f := newR195SoloPrefixFixture(t)
	f.formOrdinaryParty(t)
	f.installBattle(t)
	frozen := f.frozen(t)
	f.setSolo(t, f.memberID, true)

	f.leaderConn.write.Reset()
	f.memberConn.write.Reset()
	if err := f.service.handleGameCommand(
		f.leader,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketRaidManagerWork),
		buildRaidManagerWorkRequestForBridgeTest(0, uint32(f.memberID), 1),
	); err != nil {
		t.Fatalf("explicit same-group raid assignment: %v", err)
	}
	member := mustR195SoloPrefixMember(t, f.state(t), f.memberID)
	if member.GroupIndex != 1 || member.OrdinaryPartySolo {
		t.Fatalf("explicit raid assignment did not clear solo: %+v", member)
	}
	f.assertFrozen(t, frozen)
	f.assertRosters(t, f.memberConn.write.Bytes(), map[uint16][2]byte{
		f.leaderID: {1, 0},
		f.memberID: {1, 1},
	})
}

func TestR195RaidSoloPrefixEffectiveGenerationRepairRefreshesRoster(t *testing.T) {
	f := newR195SoloPrefixFixture(t)
	f.formOrdinaryParty(t)
	f.installBattle(t)
	frozen := f.frozen(t)
	if advanceGameSessionCharacterGeneration(f.member) == 0 {
		t.Fatal("advance member session generation failed")
	}

	f.leaderConn.write.Reset()
	f.memberConn.write.Reset()
	f.leaveOrdinaryParty(t, f.member)
	after := f.state(t)
	for _, id := range []uint16{f.leaderID, f.memberID} {
		member := mustR195SoloPrefixMember(t, after, id)
		if !member.OrdinaryPartySolo || member.GroupIndex != 1 {
			t.Fatalf("generation-repaired leave character %d group=%d solo=%t",
				id, member.GroupIndex, member.OrdinaryPartySolo)
		}
	}
	f.assertFrozen(t, frozen)
	want := map[uint16][2]byte{
		f.leaderID: {0, 0},
		f.memberID: {0, 0},
	}
	f.assertRosters(t, f.leaderConn.write.Bytes(), want)
	f.assertRosters(t, f.memberConn.write.Bytes(), want)
}

func TestR195RaidSoloPrefixRetiredSessionCannotClearOrRefresh(t *testing.T) {
	f := newR195SoloPrefixFixture(t)
	f.formOrdinaryParty(t)
	f.installBattle(t)
	frozen := f.frozen(t)

	replacementConn := &bufferConn{}
	replacement := &gameSession{
		conn:                replacementConn,
		connID:              "r195-solo-prefix-replacement",
		channel:             f.member.channel,
		accountID:           f.member.accountID,
		selectedCharacterID: f.memberID,
		loginPacketSeen:     true,
		accountFromToken:    true,
	}
	f.service.bindGameSessionCharacter(replacement, f.memberID)
	f.service.promoteResidentGameSession(replacement, f.memberID)
	if _, current := f.service.boundGameSessionCharacterSnapshot(f.member); current {
		t.Fatal("old party session remained canonical after promotion")
	}
	f.leaderConn.write.Reset()
	f.memberConn.write.Reset()
	replacementConn.write.Reset()

	if err := f.service.handleGameCommand(
		f.member,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketLeaveParty),
		nil,
	); err != nil {
		t.Fatalf("stale-session op13 was not safely consumed: %v", err)
	}
	state := f.state(t)
	for _, id := range []uint16{f.leaderID, f.memberID} {
		member := mustR195SoloPrefixMember(t, state, id)
		if member.OrdinaryPartySolo {
			t.Fatalf("retired session marked character %d as solo", id)
		}
	}
	if _, found := f.service.runtimePartyManagerForService().SnapshotByUser(f.memberID, 0); !found {
		t.Fatal("retired session removed the replacement session's party membership")
	}
	f.assertFrozen(t, frozen)
	f.assertNoRoster(t, f.leaderConn.write.Bytes())
	f.assertNoRoster(t, f.memberConn.write.Bytes())
	f.assertNoRoster(t, replacementConn.write.Bytes())
}

func TestR195RaidSoloPrefixBoundariesDoNotTouchRaidRoster(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*r195SoloPrefixFixture)
	}{
		{
			name: "legacy_client",
			mutate: func(f *r195SoloPrefixFixture) {
				f.service.options.currentClientVersion = "legacy"
			},
		},
		{
			name: "historical_layout",
			mutate: func(f *r195SoloPrefixFixture) {
				f.service.raidMu.Lock()
				f.service.raids[f.raidKey].CurrentLayout = false
				f.service.raidMu.Unlock()
			},
		},
		{
			name: "fiendwar_route",
			mutate: func(f *r195SoloPrefixFixture) {
				f.service.raidMu.Lock()
				f.service.raids[f.raidKey].RouteOrRaidType = 2
				f.service.raidMu.Unlock()
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newR195SoloPrefixFixture(t)
			f.formOrdinaryParty(t)
			f.installBattle(t)
			frozen := f.frozen(t)
			tc.mutate(f)
			f.leaderConn.write.Reset()
			f.memberConn.write.Reset()

			f.leaveOrdinaryParty(t, f.member)
			state := f.state(t)
			for _, id := range []uint16{f.leaderID, f.memberID} {
				if member := mustR195SoloPrefixMember(t, state, id); member.OrdinaryPartySolo {
					t.Fatalf("%s marked character %d as solo", tc.name, id)
				}
			}
			f.assertFrozen(t, frozen)
			f.assertNoRoster(t, f.leaderConn.write.Bytes())
			f.assertNoRoster(t, f.memberConn.write.Bytes())
		})
	}

	t.Run("no_raid_membership", func(t *testing.T) {
		h := newPartyLeaveHarness(t)
		h.service.options.currentClientVersion = "official-24.9"
		h.dispatchMemberLeave(t)
		if len(h.service.raids) != 0 {
			t.Fatalf("ordinary leave invented raid state: %+v", h.service.raids)
		}
		for name, conn := range map[string]*bufferConn{
			"leader": h.leaderConn,
			"member": h.memberConn,
		} {
			for _, packet := range splitAllGameServerUpperPackets(t, conn.write.Bytes()) {
				if packet.Header.MsgID == raid.RaidMemberRefreshMsgID && packet.Header.Classification == 0 {
					t.Fatalf("%s received invented raid roster after non-raid leave", name)
				}
			}
		}
	})
}

func TestR195RaidSoloPrefixJoinClearsSoloWhenGroupSyncIsRejected(t *testing.T) {
	t.Run("active_dungeon_participant", func(t *testing.T) {
		f := newR195SoloPrefixFixture(t)
		f.setRawPlacement(t, f.leaderID, 1, 0)
		f.setRawPlacement(t, f.memberID, 2, 0)
		f.installBattle(t)
		f.activateRaidParticipation(t, f.memberID)
		frozen := f.frozen(t)
		f.setSolo(t, f.leaderID, true)
		f.setSolo(t, f.memberID, true)

		f.formOrdinaryParty(t)
		state := f.state(t)
		for _, tc := range []struct {
			id    uint16
			group byte
			slot  byte
		}{
			{f.leaderID, 1, 0},
			{f.memberID, 2, 0},
		} {
			member := mustR195SoloPrefixMember(t, state, tc.id)
			if member.OrdinaryPartySolo {
				t.Fatalf("successful ordinary join retained solo for active character %d", tc.id)
			}
			if member.GroupIndex != tc.group || member.SlotOrder != tc.slot {
				t.Fatalf("active participant was regrouped: character=%d group=%d slot=%d",
					tc.id, member.GroupIndex, member.SlotOrder)
			}
		}
		f.assertFrozen(t, frozen)
		f.assertRosters(t, f.leaderConn.write.Bytes(), map[uint16][2]byte{
			f.leaderID: {1, 0},
			f.memberID: {2, 0},
		})
	})

	t.Run("cross_raid_members", func(t *testing.T) {
		f := newR195SoloPrefixFixture(t)
		isolatedKey := (f.raidKey & 0xffff0000) | 0x7001
		f.moveMemberToIsolatedRaid(t, isolatedKey)
		f.setSolo(t, f.leaderID, true)
		f.setSoloForKey(t, f.memberKey, f.memberID, true)

		f.formOrdinaryParty(t)
		leaderState := f.stateFor(t, f.leaderID)
		memberState := f.stateFor(t, f.memberID)
		if mustR195SoloPrefixMember(t, leaderState, f.leaderID).OrdinaryPartySolo ||
			mustR195SoloPrefixMember(t, memberState, f.memberID).OrdinaryPartySolo {
			t.Fatal("successful ordinary join retained solo across different raids")
		}
		f.assertRostersForKey(t, f.leaderConn.write.Bytes(), f.raidKey, map[uint16][2]byte{
			f.leaderID: {1, 0},
		})
		f.assertRostersForKey(t, f.memberConn.write.Bytes(), isolatedKey, map[uint16][2]byte{
			f.memberID: {1, 0},
		})
	})

	t.Run("full_anchor_group", func(t *testing.T) {
		f := newR195SoloPrefixFixture(t)
		f.addAwayRaidMember(t, 1, 1)
		f.addSyntheticRaidMember(t, 30, 1, 2)
		f.addSyntheticRaidMember(t, 31, 1, 3)
		f.setRawPlacement(t, f.memberID, 2, 0)
		f.setSolo(t, f.leaderID, true)
		f.setSolo(t, f.memberID, true)

		f.formOrdinaryParty(t)
		state := f.state(t)
		for _, tc := range []struct {
			id    uint16
			group byte
			slot  byte
		}{
			{f.leaderID, 1, 0},
			{f.memberID, 2, 0},
		} {
			member := mustR195SoloPrefixMember(t, state, tc.id)
			if member.OrdinaryPartySolo {
				t.Fatalf("successful ordinary join retained solo with full anchor group: character=%d", tc.id)
			}
			if member.GroupIndex != tc.group || member.SlotOrder != tc.slot {
				t.Fatalf("full-group rejection changed placement: character=%d group=%d slot=%d",
					tc.id, member.GroupIndex, member.SlotOrder)
			}
		}
		f.assertRosters(t, f.leaderConn.write.Bytes(), map[uint16][2]byte{
			f.leaderID: {1, 0},
			f.memberID: {2, 0},
		})
	})
}

func TestR195RaidSoloPrefixPartyTransferMarksOldSingleton(t *testing.T) {
	f := newR195SoloPrefixFixture(t)
	f.formOrdinaryParty(t)

	peerID := uint16(8)
	_, peerConn := f.bindOnlineRaidPeer(t, peerID)
	f.addAwayRaidMember(t, 1, 2)
	f.leaderConn.write.Reset()
	f.memberConn.write.Reset()
	peerConn.write.Reset()

	ok, reason, _ := f.service.forceInviteParty(peerID, f.memberID, 0, [3]uint32{})
	if !ok {
		t.Fatalf("real transfer-party invite: %s", reason)
	}
	if err := f.service.handleOnlineResponsePeer(
		f.member,
		uint16(dnfenum.CmdPacketResponsePeer),
		buildResponsePeerBody(peerID, 0, 0),
	); err != nil {
		t.Fatalf("real transfer-party op11 accept: %v", err)
	}

	oldSurvivor := mustR195SoloPrefixMember(t, f.state(t), f.leaderID)
	if !oldSurvivor.OrdinaryPartySolo || oldSurvivor.GroupIndex != 1 {
		t.Fatalf("old party singleton was not marked solo: %+v", oldSurvivor)
	}
	for _, tc := range []struct {
		id   uint16
		conn *bufferConn
	}{
		{f.memberID, f.memberConn},
		{peerID, peerConn},
	} {
		member := mustR195SoloPrefixMember(t, f.state(t), tc.id)
		if member.OrdinaryPartySolo {
			t.Fatalf("transfer-party member %d retained solo", tc.id)
		}
		if tc.conn == nil {
			t.Fatal("transfer-party session fixture is incomplete")
		}
	}
	f.assertRosters(t, f.leaderConn.write.Bytes(), map[uint16][2]byte{
		f.leaderID: {0, 0},
		f.memberID: {1, 1},
		peerID:     {1, 2},
	})
}

func TestR195RaidSoloPrefixFailedStartDoesNotClearSolo(t *testing.T) {
	f := newR195SoloPrefixFixture(t)
	f.setRawPlacement(t, f.leaderID, 1, 0)
	f.setRawPlacement(t, f.memberID, 1, 1)
	f.setSolo(t, f.leaderID, true)
	f.setSolo(t, f.memberID, true)
	// Five members in one Anton group make the start fail its capacity gate
	// before any admission transaction or raid-party materialization.
	f.addSyntheticRaidMember(t, 30, 1, 2)
	f.addSyntheticRaidMember(t, 31, 1, 3)
	f.addSyntheticRaidMember(t, 32, 1, 3)
	wantGroups := map[uint16][2]byte{
		f.leaderID: {1, 0},
		f.memberID: {1, 1},
	}

	f.leaderConn.write.Reset()
	_, started, err := f.service.startRuntimeRaidWithEntryCost(f.leader)
	if err != nil {
		t.Fatalf("failed start returned an unexpected transaction error: %v", err)
	}
	if started {
		t.Fatal("oversized Anton group unexpectedly started")
	}
	state := f.state(t)
	for id, placement := range wantGroups {
		member := mustR195SoloPrefixMember(t, state, id)
		if !member.OrdinaryPartySolo {
			t.Fatalf("failed start cleared solo for character %d", id)
		}
		if [2]byte{member.GroupIndex, member.SlotOrder} != placement {
			t.Fatalf("failed start moved character %d: got=%d/%d want=%d/%d",
				id, member.GroupIndex, member.SlotOrder, placement[0], placement[1])
		}
	}
	if state.Started || state.Battle != nil {
		t.Fatal("failed start created a battle or marked the raid started")
	}
	f.assertNoRoster(t, f.leaderConn.write.Bytes())
}

func TestR195RaidSoloPrefixRejoinMode2MetadataUsesSoloGroup(t *testing.T) {
	f := newR195SoloPrefixFixture(t)
	f.formOrdinaryParty(t)
	f.installBattle(t)
	f.leaveOrdinaryParty(t, f.member)
	state := f.state(t)
	if !mustR195SoloPrefixMember(t, state, f.leaderID).OrdinaryPartySolo {
		t.Fatal("leader did not remain a singleton after ordinary-party leave")
	}

	f.memberConn.write.Reset()
	if err := f.service.sendRuntimeRaidMetadata(f.member, state, 2); err != nil {
		t.Fatalf("send rejoin mode2 metadata: %v", err)
	}
	found := false
	for len(f.memberConn.write.Bytes()) > 0 {
		packet, rest := splitGameServerUpperPacket(t, f.memberConn.write.Bytes())
		f.memberConn.write.Reset()
		f.memberConn.write.Write(rest)
		if packet.Header.MsgID != raid.RaidMemberRefreshMsgID || packet.Header.Classification != 0 ||
			len(packet.Body) < 8 || binary.LittleEndian.Uint32(packet.Body[4:8]) != 2 {
			continue
		}
		found = true
		if got := r195SoloPrefixMetadataLeaderGroup(t, packet.Body, f.raidKey); got != 0 {
			t.Fatalf("rejoin metadata leader group=%d, want native solo group 0", got)
		}
	}
	if !found {
		t.Fatal("rejoin mode2 metadata 591 was not emitted")
	}
}

func mustR195SoloPrefixMember(t *testing.T, state runtimeRaidState, charID uint16) runtimeRaidMemberState {
	t.Helper()
	for _, member := range state.Members {
		if member.CharID == charID {
			return member
		}
	}
	t.Fatalf("raid member %d is missing", charID)
	return runtimeRaidMemberState{}
}

func currentRaidMemberRowsForR195SoloPrefix(records []raid.CurrentMemberRecord) []raidRefreshMemberForTest {
	rows := make([]raidRefreshMemberForTest, 0, len(records))
	for _, record := range records {
		rows = append(rows, raidRefreshMemberForTest{
			CharID:     record.Core.CharKey,
			State:      record.Core.Field4,
			GroupIndex: record.Core.GroupIndex,
			SlotOrder:  record.Core.SlotOrder,
		})
	}
	return rows
}

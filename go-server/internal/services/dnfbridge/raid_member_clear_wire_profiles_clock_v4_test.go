package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// These tests exercise actual resource progression and the public info handler.
// The completed dungeon/barriers come from the existing completed-runtime fixture,
// not physical combat. No helper computes the expected Field56 from production.
type clearWireCursor struct {
	t *testing.T
	b []byte
}

func (c *clearWireCursor) take(n int) []byte {
	c.t.Helper()
	if n < 0 || n > len(c.b) {
		c.t.Fatalf("truncated count wire need=%d remain=%d", n, len(c.b))
	}
	b := c.b[:n]
	c.b = c.b[n:]
	return b
}
func (c *clearWireCursor) dstr() { c.take(int(binary.LittleEndian.Uint32(c.take(4)))) }
func (c *clearWireCursor) member() (uint16, byte) {
	id := binary.LittleEndian.Uint16(c.take(2))
	c.take(1)
	c.dstr()
	c.take(4 + 4 + 2) // job, grow, group, slot, level(u32), Field52, Field53: 10 bytes
	count := c.take(1)[0]
	c.take(4 + 2 + 4 + 1)
	c.dstr() // Field60/64/66, ExtraByte, ExtraName
	return id, count
}
func (c *clearWireCursor) roster() map[uint16]byte {
	out := map[uint16]byte{}
	n := int(c.take(1)[0])
	for i := 0; i < n; i++ {
		id, count := c.member()
		if _, ok := out[id]; ok {
			c.t.Fatal("duplicate wire member")
		}
		out[id] = count
	}
	if len(c.b) != 0 {
		c.t.Fatalf("trailing roster bytes=%d", len(c.b))
	}
	return out
}
func auditMemberClearInfoWire(t *testing.T, f *raidStartGuardFixture, session *gameSession, want map[uint16]byte) {
	t.Helper()
	f.conn.write.Reset()
	request := make([]byte, 5)
	request[0] = 2
	binary.LittleEndian.PutUint32(request[1:], f.state.RaidKey)
	if err := f.service.handleOnlineRequestRaidInfo(session, request); err != nil {
		t.Fatal(err)
	}
	raw := consumeCurrentRaidProfiles(t, f.conn.write.Bytes(), cloneRuntimeRaidState(*f.state))
	ack, rest := splitGameServerUpperPacket(t, raw)
	if ack.Header.MsgID != 1359 || ack.Header.Classification != 1 {
		t.Fatal("missing owned ACK1359")
	}
	c := clearWireCursor{t: t, b: ack.Body}
	if c.take(1)[0] != 1 || c.take(1)[0] != 2 || binary.LittleEndian.Uint32(c.take(4)) != f.state.RaidKey {
		t.Fatal("ACK owner/selector")
	}
	if binary.LittleEndian.Uint32(c.take(4)) != f.state.RaidKey {
		t.Fatal("metadata owner")
	}
	c.dstr()
	c.take(3 + 4 + 4)
	leaderID, leaderCount := c.member()
	if leaderID != f.state.LeaderID || leaderCount != want[leaderID] {
		t.Fatal("metadata leader count", leaderID, leaderCount, want)
	}
	if got := c.roster(); !reflect.DeepEqual(got, want) {
		t.Fatal("ACK1359 roster count", got, want)
	}
	noti, tail := splitGameServerUpperPacket(t, rest)
	if noti.Header.MsgID != 591 || noti.Header.Classification != 0 {
		t.Fatal("missing membership NOTI591")
	}
	c = clearWireCursor{t: t, b: noti.Body}
	if binary.LittleEndian.Uint32(c.take(4)) != f.state.RaidKey || binary.LittleEndian.Uint32(c.take(4)) != 3 {
		t.Fatal("NOTI owner/mode")
	}
	if got := c.roster(); !reflect.DeepEqual(got, want) {
		t.Fatal("NOTI591 roster count", got, want)
	}
	// Entry-cost refresh NOTI598 is the only source-proven optional remainder.
	if len(tail) > 0 {
		cost, left := splitGameServerUpperPacket(t, tail)
		if cost.Header.Classification != 0 || cost.Header.MsgID != 598 || len(left) != 0 || len(cost.Body) != 4+5*len(want) || binary.LittleEndian.Uint32(cost.Body) != uint32(len(want)) {
			t.Fatal("unexpected remainder after NOTI591")
		}
		for i := 0; i < len(want); i++ {
			row := cost.Body[4+5*i : 4+5*(i+1)]
			if _, ok := want[binary.LittleEndian.Uint16(row)]; !ok || row[2] > 1 {
				t.Fatal("entry-cost foreign owner/ready")
			}
		}
	}
}

// Typed character metadata fixtures for the real profile producer; identity and
// physical AccountID are loaded unchanged. These names are not live test78 data.
func seedClearWireCharacterProfiles(t *testing.T, f *raidStartGuardFixture) {
	t.Helper()
	for _, member := range f.state.Members {
		key := strconv.Itoa(int(member.CharID))
		c, found, err := f.repos.Character.Load(context.Background(), key)
		if err != nil || !found || c.CharacterID != key || c.AccountID != f.state.Battle.MemberAccounts[member.CharID] {
			t.Fatal("profile fixture lost existing physical identity", key, found, err)
		}
		c.Name = "clear" + key
		c.Job = "0"
		c.Level = 90
		if err := f.repos.Character.Save(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRealPVFRaidMemberAcceptedPhaseClearInfoWireProfilesClock(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to run actual PVF regression")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	program, err := raid.LoadAntonProgram(archive)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := raid.LoadBattleConfiguration(archive, "etc/raid/anton.etc")
	if err != nil {
		t.Fatal(err)
	}
	if len(configuration.PhaseLimitSeconds) != 2 {
		t.Fatal("actual phase clock configuration unavailable")
	}
	f := raidSafetyFixture(t, archive, 3)
	f.service.options.currentClientVersion = "official-24.9"
	leader, peer := f.session(), f.session()
	leader.loginPacketSeen, leader.accountFromToken = true, true
	peer.selectedCharacterID, peer.accountID = 20, "account-20"
	peer.loginPacketSeen, peer.accountFromToken = true, true
	f.service.bindGameSessionCharacter(leader, 19)
	f.service.bindGameSessionCharacter(peer, 20)
	for i := range f.state.Members {
		f.state.Members[i].GroupIndex = 1
	}
	now := time.Now()
	anton, err := program.BeginPhase(0, now.Add(-time.Second), 87152)
	if err != nil {
		t.Fatal(err)
	}
	b := &runtimeRaidBattle{InstanceID: "00112233445566778899aabbccddeeff", Mode: 1, EnterAfter: now.Add(-time.Second), Deadline: now.Add(-time.Second).Add(time.Duration(configuration.PhaseLimitSeconds[0]) * time.Second), StartedAt: now.Add(-time.Second).Add(-time.Duration(configuration.StartDelaySeconds) * time.Second), Configuration: configuration, Anton: anton, ChannelID: leader.channel.ID, ChannelType: leader.channel.Type, Dungeons: anton.Dungeons(), MemberAccounts: map[uint16]string{19: "account-1", 20: "account-20", 21: "account-21"}}
	// CurrentLayout selects the official record codec; it grants no clear owner.
	f.state.Started, f.state.CurrentLayout, f.state.Battle = true, true, b
	seedClearWireCharacterProfiles(t, f)
	auditMemberClearInfoWire(t, f, leader, map[uint16]byte{19: 0, 20: 0, 21: 0})
	for i := 1; i <= 4; i++ {
		l, p := raidCompletedRuntimeFixture(t, leader, 210), raidCompletedRuntimeFixture(t, peer, 210)
		if err := f.service.freezeCurrentRaidDungeonGroupEntry(leader, l, []*runtimePartyPreparedDungeonEntry{{session: peer, runtime: p}}); err != nil {
			t.Fatal(err)
		}
		if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(leader, l, now); err != nil || !changed {
			t.Fatal("accepted owner clear", changed, err)
		}
		if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(peer, p, now); err != nil || changed {
			t.Fatal("same shared run replay", changed, err)
		}
		auditMemberClearInfoWire(t, f, leader, map[uint16]byte{19: byte(i), 20: byte(i), 21: 0})
	}
	stale := raidCompletedRuntimeFixture(t, leader, 213)
	if err := f.service.freezeCurrentRaidDungeonGroupEntry(leader, stale, nil); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(leader, stale, now.Add(301*time.Second)); err == nil || !changed || b.Anton.Epoch() != 2 {
		t.Fatal("real quake reset missing", changed, err)
	}
	auditMemberClearInfoWire(t, f, leader, map[uint16]byte{19: 4, 20: 4, 21: 0})
	if _, _, err := f.service.reduceCurrentRaidDungeonClearLocked(leader, stale, now.Add(301*time.Second)); err == nil {
		t.Fatal("old attempt accepted")
	}
	auditMemberClearInfoWire(t, f, leader, map[uint16]byte{19: 4, 20: 4, 21: 0})
	// Foreign account cannot retrieve retained membership's count or metadata.
	oldAccount := leader.accountID
	leader.accountID = "foreign"
	f.conn.write.Reset()
	req := make([]byte, 5)
	req[0] = 2
	binary.LittleEndian.PutUint32(req[1:], f.state.RaidKey)
	if err := f.service.handleOnlineRequestRaidInfo(leader, req); err != nil || f.conn.write.Len() != 0 {
		t.Fatal("foreign query leaked roster", err)
	}
	leader.accountID = oldAccount
	auditMemberClearInfoWire(t, f, leader, map[uint16]byte{19: 4, 20: 4, 21: 0})
	// Real op668 restores retained membership without clearing phase count.
	rejoin := make([]byte, 4)
	binary.LittleEndian.PutUint32(rejoin, f.state.RaidKey)
	f.conn.write.Reset()
	if err := f.service.handleOnlineRejoinRaid(peer, 668, rejoin); err != nil {
		t.Fatal(err)
	}
	restored, _ := splitGameServerUpperPacket(t, f.conn.write.Bytes())
	if restored.Header.MsgID != 668 || restored.Header.Classification != 1 || !reflect.DeepEqual(restored.Body, []byte{1}) {
		t.Fatal("legitimate retained rejoin was rejected")
	}
	auditMemberClearInfoWire(t, f, peer, map[uint16]byte{19: 4, 20: 4, 21: 0})
	// Explicit leave removes roster membership, not earned reward eligibility.
	if err := f.service.handleOnlineLeaveRaid(peer, 665, []byte{20, 0}); err != nil {
		t.Fatal(err)
	}
	if b.MemberClears[20] != 4 || runtimeRaidMemberIndex(*f.state, 20) >= 0 {
		t.Fatal("leave changed earned count or retained roster")
	}
	auditMemberClearInfoWire(t, f, leader, map[uint16]byte{19: 4, 21: 0})
	f.conn.write.Reset()
	if err := f.service.handleOnlineRejoinRaid(peer, 668, rejoin); err != nil {
		t.Fatal(err)
	}
	rejected, left := splitGameServerUpperPacket(t, f.conn.write.Bytes())
	if rejected.Header.MsgID != 668 || len(rejected.Body) == 0 || rejected.Body[0] != 0 || len(left) != 0 {
		t.Fatal("departed member regained retained membership")
	}
	if b.MemberClears[20] != 4 || runtimeRaidMemberIndex(*f.state, 20) >= 0 {
		t.Fatal("rejected rejoin changed count or membership")
	}
}

func TestRealPVFRaidMemberPhaseAdvanceClearInfoWireProfilesClock(t *testing.T) {
	if os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE") == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to run actual PVF regression")
	}
	// Existing full phase fixture accepts 8 exact shared clears and freezes rewards.
	f, session, _ := firstAntonFlowFixture(t)
	seedClearWireCharacterProfiles(t, f)
	auditMemberClearInfoWire(t, f, session, map[uint16]byte{19: 8, 20: 8})
	flow := f.state.Battle.RewardFlow[0]
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(20*time.Second), zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	state, ok, err := f.service.startRuntimeRaidWithEntryCost(session)
	if !ok || err != nil || state.Battle.Phase != 1 || len(state.Battle.MemberClears) != 0 {
		t.Fatal("legitimate phase advance", ok, err)
	}
	auditMemberClearInfoWire(t, f, session, map[uint16]byte{19: 0, 20: 0})
}

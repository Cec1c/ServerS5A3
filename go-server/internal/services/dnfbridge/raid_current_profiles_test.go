package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/raid"
)

func consumeCurrentRaidProfiles(t *testing.T, raw []byte, state runtimeRaidState) []byte {
	t.Helper()
	ids := make([]uint16, 0, len(state.Members))
	for _, member := range state.Members {
		ids = append(ids, member.CharID)
	}
	return consumeCurrentRaidProfilesForIDs(t, raw, state, ids...)
}

func consumeCurrentRaidProfilesForIDs(t *testing.T, raw []byte, state runtimeRaidState, ids ...uint16) []byte {
	t.Helper()
	p, rest := splitGameServerUpperPacket(t, raw)
	if p.Header.Classification != 0 || p.Header.MsgID != 752 || len(p.Body) != 4+57*len(ids) || binary.LittleEndian.Uint32(p.Body) != uint32(len(ids)) {
		t.Fatal("native character cache must precede raid roster")
	}
	wanted := make(map[uint16]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	for i := range ids {
		row := p.Body[4+i*57 : 4+(i+1)*57]
		if !wanted[binary.LittleEndian.Uint16(row)] || row[3] != byte(state.RaidKey>>16) || row[8] != byte(state.RaidKey>>24) {
			t.Fatal("profile identity differs from authorized roster")
		}
	}
	return rest
}

func currentRaidProfileAndAppearanceFrameCounts(t *testing.T, raw []byte) (int, int) {
	t.Helper()
	profiles, appearances := 0, 0
	for _, frame := range splitUpperFrames(t, raw) {
		if frame.msgID == raid.CurrentRaidProfilesMsgID {
			profiles++
		}
		if frame.msgID == 2 && len(frame.body) > 0 && frame.body[0] == 8 {
			appearances++
		}
	}
	return profiles, appearances
}

func consumeCurrentRaidAppearanceFrames(t *testing.T, raw []byte) ([]byte, int) {
	t.Helper()
	count := 0
	for len(raw) != 0 {
		frame, rest := splitGameServerUpperPacket(t, raw)
		if frame.Header.MsgID != 2 || len(frame.Body) == 0 || frame.Body[0] != 8 {
			return raw, count
		}
		raw = rest
		count++
	}
	return raw, count
}

func TestCurrentRaidJoinProfileAndAppearanceDeliveryIsIncremental(t *testing.T) {
	s, leader, member, lc, mc, state := prepareCurrentRaidInvitationTest(t)
	leader.connectionTownActorOwnerChannel = byte(leader.channel.ID)
	member.connectionTownActorOwnerChannel = byte(member.channel.ID)
	if err := s.handleGameCommand(leader, 1, 10, currentRaidInviteFixture(member.selectedCharacterID)); err != nil {
		t.Fatal(err)
	}
	lc.write.Reset()
	mc.write.Reset()
	if err := s.handleGameCommand(member, 1, 11, currentRaidApprovalFixture(leader.selectedCharacterID)); err != nil {
		t.Fatal(err)
	}
	leaderProfiles, leaderAppearances := currentRaidProfileAndAppearanceFrameCounts(t, lc.write.Bytes())
	memberProfiles, memberAppearances := currentRaidProfileAndAppearanceFrameCounts(t, mc.write.Bytes())
	if leaderProfiles != 1 || leaderAppearances != 1 || memberProfiles != 1 || memberAppearances != 1 {
		t.Fatalf("join profile/appearance counts leader=%d/%d member=%d/%d", leaderProfiles, leaderAppearances, memberProfiles, memberAppearances)
	}
	leaderRest := lc.write.Bytes()
	leaderMetadata, leaderRest := splitGameServerUpperPacket(t, leaderRest)
	leaderRest = consumeCurrentRaidProfilesForIDs(t, leaderRest, state, member.selectedCharacterID)
	leaderRest, _ = consumeCurrentRaidAppearanceFrames(t, leaderRest)
	live, found := s.runtimeRaidForMember(member.selectedCharacterID)
	if !found || len(live.Members) != 2 {
		t.Fatal("missing joined raid")
	}
	wantRoster, err := s.buildRuntimeRaidRosterBody(live)
	if err != nil {
		t.Fatal(err)
	}
	roster, rest := splitGameServerUpperPacket(t, leaderRest)
	if len(rest) != 0 || leaderMetadata.Header.MsgID != 591 || roster.Header.MsgID != 591 ||
		roster.Header.Classification != 0 || !bytes.Equal(roster.Body, wantRoster) {
		t.Fatal("primed leader did not receive metadata, one-row profile, appearance and roster")
	}

	// Reopening from the same connection is already primed; it must send only
	// the current ACK/roster/admission tail, without rebuilding category1 data.
	lc.write.Reset()
	request := []byte{2, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(request[1:], state.RaidKey)
	if err := s.handleOnlineRequestRaidInfo(leader, request); err != nil {
		t.Fatal(err)
	}
	profiles, appearances := currentRaidProfileAndAppearanceFrameCounts(t, lc.write.Bytes())
	if profiles != 0 || appearances != 0 {
		t.Fatalf("primed reopen repeated profiles/appearances=%d/%d", profiles, appearances)
	}
	lc.write.Reset()
	invalidateCurrentCrossSceneAppearance(member)
	if err := s.handleOnlineRequestRaidInfo(leader, request); err != nil {
		t.Fatal(err)
	}
	profiles, appearances = currentRaidProfileAndAppearanceFrameCounts(t, lc.write.Bytes())
	if profiles != 0 || appearances != 1 {
		t.Fatalf("changed member appearance sent profiles/appearances=%d/%d", profiles, appearances)
	}
}

func TestCurrentRaidProfilesUseRepositoryRatherThanCachedDisplayFields(t *testing.T) {
	s, leader, member, lc, _, state := prepareApprovedCurrentRaid(t)
	leader.raidProfileView.clear()
	g, _ := s.repositoryGroup()
	want := make([]raid.CurrentProfileRecord, 0, 2)
	for i, m := range state.Members {
		c, found, err := g.Character.Load(nil, strconv.Itoa(int(m.CharID)))
		if !found || err != nil {
			t.Fatal("missing actual fixture owner")
		}
		c.Name, c.Level, c.Job = []string{"q7a", "7331"}[i], 90, "0"
		if c.Stats == nil {
			c.Stats = make(map[string]int64)
		}
		c.Stats["grow_type"] = 0x21
		if err := g.Character.Save(nil, c); err != nil {
			t.Fatal(err)
		}
		want = append(want, raid.CurrentProfileRecord{CharKey: m.CharID, Channel: 23, Server: 0, Level: 90, Job: 0, Grow: characterGrowTypeByte(c), Name: c.Name})
		state.Members[i].Name, state.Members[i].Level, state.Members[i].Job = "untrusted stale display", 65535, 255
	}
	if err := s.sendCurrentRuntimeRaidProfiles(leader, state); err != nil {
		t.Fatal(err)
	}
	p, rest := splitGameServerUpperPacket(t, lc.write.Bytes())
	expected, err := raid.BuildCurrentRaidProfilesBody(want)
	if err != nil || len(rest) != 0 || p.Header.MsgID != 752 || p.Header.Classification != 0 || !bytes.Equal(p.Body, expected) {
		t.Fatal("profile did not use authoritative properties")
	}
	if dir := os.Getenv("DNF90_CURRENT_RAID_PROFILE_BRIDGE_NATIVE_DIR"); dir != "" {
		if err := os.WriteFile(filepath.Join(dir, "raid-profiles-bridge-752.bin"), p.Body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if member.selectedCharacterID == 0 {
		t.Fatal("member fixture missing")
	}
}

func TestCurrentRaidProfilesNeverFabricateUnavailableProperties(t *testing.T) {
	for _, kind := range []string{"missing", "level", "job", "name"} {
		t.Run(kind, func(t *testing.T) {
			s, leader, _, lc, _, state := prepareApprovedCurrentRaid(t)
			leader.raidProfileView.clear()
			g, _ := s.repositoryGroup()
			for i, m := range state.Members {
				if kind == "missing" {
					state.Members[i].CharID = 60000 + uint16(i)
					continue
				}
				c, _, _ := g.Character.Load(nil, strconv.Itoa(int(m.CharID)))
				switch kind {
				case "level":
					c.Level = 0
				case "job":
					c.Job = "unknown"
				case "name":
					c.Name = ""
				}
				if err := g.Character.Save(nil, c); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.sendCurrentRuntimeRaidProfiles(leader, state); err != nil {
				t.Fatal(err)
			}
			if lc.write.Len() != 0 {
				t.Fatal("unavailable properties created a client actor")
			}
		})
	}
}

func TestCurrentRaidProfileBroadcastIsScopedToAuthenticatedNamespace(t *testing.T) {
	for _, kind := range []string{"channel", "server", "route", "owner", "token", "retired", "legacy_object", "legacy_profile"} {
		t.Run(kind, func(t *testing.T) {
			s, leader, member, lc, mc, state := prepareApprovedCurrentRaid(t)
			switch kind {
			case "channel":
				member.channel.ID = 24
			case "server":
				state.RaidKey ^= 1 << 24
			case "route":
				member.channel.Type = 11
			case "owner":
				member.accountID = "foreign"
			case "token":
				member.accountFromToken = false
			case "retired":
				s.unbindGameSession(member)
			case "legacy_object":
				state.CurrentLayout = false
			case "legacy_profile":
				s.options.currentClientVersion = "legacy"
			}
			if err := s.sendCurrentRuntimeRaidProfiles(member, state); err != nil {
				t.Fatal(err)
			}
			if mc.write.Len() != 0 || lc.write.Len() != 0 || leader.selectedCharacterID == 0 {
				t.Fatal("profile leaked to unauthorized context or another viewer")
			}
		})
	}
}

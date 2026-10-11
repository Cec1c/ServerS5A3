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
	p, rest := splitGameServerUpperPacket(t, raw)
	if p.Header.Classification != 0 || p.Header.MsgID != 752 || len(p.Body) != 4+57*len(state.Members) || binary.LittleEndian.Uint32(p.Body) != uint32(len(state.Members)) {
		t.Fatal("native character cache must precede raid roster")
	}
	for i, member := range state.Members {
		row := p.Body[4+i*57 : 4+(i+1)*57]
		if binary.LittleEndian.Uint16(row) != member.CharID || row[3] != byte(state.RaidKey>>16) || row[8] != byte(state.RaidKey>>24) {
			t.Fatal("profile identity differs from authorized roster")
		}
	}
	return rest
}

func TestCurrentRaidProfilesUseRepositoryRatherThanCachedDisplayFields(t *testing.T) {
	s, leader, member, lc, _, state := prepareApprovedCurrentRaid(t)
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

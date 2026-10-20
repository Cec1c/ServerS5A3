package dnfbridge

import (
	"bytes"
	"context"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
)

func TestCurrentRaidWeeklyAvailabilityPersistsPerCharacterAndModeOnRebind(t *testing.T) {
	t.Setenv("DNFBRIDGE_RAID_WEEKLY_RESET_DAY", "4")
	t.Setenv("DNFBRIDGE_RAID_WEEKLY_RESET_HOUR", "6")
	s, a, b, ac, bc := prepareAccountFriendTest(t)
	g, _ := s.repositoryGroup()
	policy, err := currentRaidWeeklyPolicy()
	if err != nil {
		t.Fatal(err)
	}
	epoch, err := raid.WeeklyEpoch(time.Now(), policy)
	if err != nil {
		t.Fatal(err)
	}
	id := strconv.Itoa(int(a.selectedCharacterID))
	character, found, err := g.Character.Load(context.Background(), id)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	character.Stats = map[string]int64{"raid_weekly_epoch_v1_1": epoch, "raid_weekly_clears_v1_1": 1,
		"raid_weekly_epoch_v1_2": epoch, "raid_weekly_clears_v1_2": 2, "unrelated": 77}
	if err := g.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	for _, session := range []*gameSession{a, b} {
		if err := s.sendCurrentRaidWeeklyAvailability(session, "test_initial_selected_owner"); err != nil {
			t.Fatal(err)
		}
	}
	packet, rest := splitGameServerUpperPacket(t, ac.write.Bytes())
	if packet.Header.Classification != 0 || packet.Header.MsgID != 1442 || len(rest) != 0 || !bytes.Equal(packet.Body, []byte{3, 0, 1, 1, 2, 2, 0}) {
		t.Fatal("own persisted counters were not projected", packet)
	}
	other, rest := splitGameServerUpperPacket(t, bc.write.Bytes())
	if len(rest) != 0 || !bytes.Equal(other.Body, []byte{3, 0, 0, 1, 0, 2, 0}) {
		t.Fatal("another character inherited counters", other)
	}
	ac.write.Reset()
	reconnect := &gameSession{conn: ac, accountID: a.accountID, accountFromToken: true, loginPacketSeen: true, channel: a.channel}
	s.bindGameSessionCharacter(reconnect, a.selectedCharacterID)
	s.promoteResidentGameSession(reconnect, a.selectedCharacterID)
	ac.write.Reset() // Binding may send its established scene/presence lifecycle.
	if err := s.sendCurrentRaidWeeklyAvailability(a, "test_stale_connection"); err != nil || ac.write.Len() != 0 {
		t.Fatal("stale session received weekly data", err)
	}
	if err := s.sendCurrentRaidWeeklyAvailability(reconnect, "test_reconnect"); err != nil {
		t.Fatal(err)
	}
	packet, rest = splitGameServerUpperPacket(t, ac.write.Bytes())
	if len(rest) != 0 || !bytes.Equal(packet.Body, []byte{3, 0, 1, 1, 2, 2, 0}) {
		t.Fatal("rebind lost persisted weekly counters", packet)
	}
	after, _, _ := g.Character.Load(context.Background(), id)
	if after.Stats["unrelated"] != 77 || after.Stats["raid_weekly_clears_v1_1"] != 1 || len(after.Stats) != len(character.Stats) {
		t.Fatal("projection wrote character state")
	}
}

func TestCurrentRaidWeeklyAvailabilityRejectsInconsistentCountersAndUnauthorizedProjection(t *testing.T) {
	for _, bad := range []string{"epoch_without_count", "negative", "unauthenticated", "wrong_account", "legacy"} {
		t.Run(bad, func(t *testing.T) {
			s, a, _, ac, _ := prepareAccountFriendTest(t)
			g, _ := s.repositoryGroup()
			id := strconv.Itoa(int(a.selectedCharacterID))
			character, _, _ := g.Character.Load(context.Background(), id)
			policy, _ := currentRaidWeeklyPolicy()
			epoch, _ := raid.WeeklyEpoch(time.Now(), policy)
			switch bad {
			case "epoch_without_count":
				character.Stats = map[string]int64{"raid_weekly_epoch_v1_1": epoch}
			case "negative":
				character.Stats = map[string]int64{"raid_weekly_epoch_v1_1": epoch, "raid_weekly_clears_v1_1": -1}
			case "unauthenticated":
				a.accountFromToken = false
			case "wrong_account":
				character.AccountID = "unrelated-owner"
			case "legacy":
				s.options.currentClientVersion = "historical"
			}
			if err := g.Character.Save(context.Background(), character); err != nil {
				t.Fatal(err)
			}
			err := s.sendCurrentRaidWeeklyAvailability(a, "test_bad_state")
			if (bad == "epoch_without_count" || bad == "negative") && err == nil {
				t.Fatal("bad counter became a fabricated zero")
			}
			if ac.write.Len() != 0 {
				t.Fatal("unsupported owner/state received a count packet")
			}
		})
	}
}

func TestCurrentRaidWeeklyPolicyRejectsInvalidConfiguration(t *testing.T) {
	for name, raw := range map[string]string{"DNFBRIDGE_RAID_WEEKLY_RESET_DAY": "7", "DNFBRIDGE_RAID_WEEKLY_RESET_HOUR": "24"} {
		t.Setenv(name, raw)
		if _, err := currentRaidWeeklyPolicy(); err == nil {
			t.Fatal("out of range weekly policy accepted")
		}
		t.Setenv(name, "")
	}
}

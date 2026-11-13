package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/buffswap"
)

// V212 carries the paired group envelope. This keeps the missing deployment
// capability reproducible separately from a loss of already committed state.
func TestV212BuffRegistrationAndActorRebuildRequirePairedDeployment(t *testing.T) {
	for _, mode := range []string{"", "off", "on"} {
		t.Run("group_sync_"+mode, func(t *testing.T) {
			t.Setenv("DNFBRIDGE_BUFF_GROUP_SYNC", mode)
			s, session, conn, repos := r191CacheFixture(t)
			for group := byte(0); group < 2; group++ {
				reviewCurrentBuffDispatch(t, s, session, pairedBuffCommand(group, uint16(38+group), 12, 0, 54))
				if mode == "on" {
					pairedBuffExpect(t, conn, group, uint16(38+group), true, 54)
				}
			}
			state, found, err := buffswap.Load(context.Background(), repos, "1", session.accountID)
			if err != nil || found != (mode == "on") {
				t.Fatalf("group mode %q persisted=%t err=%v", mode, found, err)
			}
			if mode == "on" && (state.SelectedSkills[0] != 38 || state.SelectedSkills[1] != 39) {
				t.Fatalf("lost selected groups: %+v", state.SelectedSkills)
			}
			fresh, cold, output, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryShapeGuardTier, repos)
			fresh.options.currentClientVersion = "official-24.9"
			cold.postFinishLoadingPlayerStateSent = true
			if err := fresh.sendCurrentFinishLoadingCharacterState(cold, "v212_dungeon_actor_rebuild"); err != nil {
				t.Fatal(err)
			}
			packets := drainUpperPackets(t, output)
			groups := 0
			for _, packet := range packets {
				if packet.msgID == currentBuffSwapRowsID && len(packet.body) == 170 && packet.body[0] == 0xfe {
					group := packet.body[1]
					if group > 1 || binary.LittleEndian.Uint16(packet.body[2:]) != uint16(38+group) || packet.body[65] != 1 || binary.LittleEndian.Uint16(packet.body[68:]) != 54 {
						t.Fatalf("wrong restored group: %x", packet.body)
					}
					groups++
				}
				if mode != "on" && packet.msgID == currentBuffSwapSnapshotID && binary.LittleEndian.Uint16(packet.body) != 0 {
					t.Fatal("disabled paired deployment invented saved state")
				}
			}
			if mode == "on" && groups != 2 || mode != "on" && groups != 0 {
				t.Fatalf("mode=%q restored paired groups=%d", mode, groups)
			}
		})
	}
}

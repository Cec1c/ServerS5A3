package dnfbridge

import (
	"bytes"
	"context"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/buffswap"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestOfficialBuffRegistrationWithoutStartupSwitch(t *testing.T) {
	for _, value := range []string{"absent", "", "off", "0", "false", "unknown"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(buffSwapReapplyEnv, value)
			if value == "absent" {
				if err := os.Unsetenv(buffSwapReapplyEnv); err != nil {
					t.Fatal(err)
				}
			}
			r := newSkillSwitchInventoryRepos(t)
			if err := r.Inventory.Save(context.Background(), repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{"0:54": {ItemID: 101040493, Count: 1}}}); err != nil {
				t.Fatal(err)
			}
			s, session, conn, logger, path := newSkillSwitchInventoryService(t, skillSwitchInventoryReferenceDrop, r)
			s.options.currentClientVersion = "official-24.9"
			session.sceneBootstrapTailSent = true
			reviewCurrentBuffDispatch(t, s, session, reviewCurrentBuffCommand(38, 33, 45, 0xffff))
			reviewCurrentBuffSnapshot(t, conn, 38, false)
			reviewCurrentBuffDispatch(t, s, session, reviewCurrentBuffCommand(38, 12, 0, 54))
			reviewCurrentBuffProvenance(t, r, 38, 1)
			snapshot := reviewCurrentBuffSnapshot(t, conn, 38, true)
			state, found, err := buffswap.Load(context.Background(), r, "1", "dnf:10001")
			if err != nil || !found || state.Slots[12].ItemID != 101040493 || state.Slots[12].Instance == "" {
				t.Fatalf("owned registration was not saved: %+v %t %v", state, found, err)
			}
			for _, boundary := range []string{"reselect_character", "dungeon_return", "reconnect"} {
				fresh, cold, output, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryReferenceDrop, r)
				fresh.options.currentClientVersion = "official-24.9"
				cold.postFinishLoadingPlayerStateSent = true
				if err := fresh.sendCurrentFinishLoadingCharacterState(cold, boundary); err != nil {
					t.Fatal(err)
				}
				packets := drainUpperPackets(t, output)
				if len(packets) < 3 || packets[0].msgID != 37 || packets[1].msgID != 30 || packets[len(packets)-1].msgID != 1368 || !bytes.Equal(packets[len(packets)-1].body, snapshot) {
					t.Fatalf("%s lost committed registration or wire order: %v", boundary, packets)
				}
			}
			reviewCurrentBuffDispatch(t, s, session, reviewCurrentBuffCommand(38, 12, 45, 0xffff))
			reviewCurrentBuffSnapshot(t, conn, 38, false)
			reviewCurrentBuffDispatch(t, s, session, reviewCurrentBuffCommand(0, 33, 45, 0xffff))
			reviewCurrentBuffProvenance(t, r, 0, 0)
			reviewCurrentBuffSnapshot(t, conn, 0, false)
			log := readPeerEndpointTestLog(t, logger, path)
			if bytes.Contains([]byte(log), []byte("game-legacy-fps-devide-collect")) {
				t.Fatal("current registration was dispatched as legacy FPS data")
			}
		})
	}
}

func TestBuffRegistrationLegacyCompatibilityPolicy(t *testing.T) {
	for _, version := range []string{"", "90cn", "legacy", "official-24.8"} {
		for _, value := range []string{"", "off", "on"} {
			t.Setenv(buffSwapReapplyEnv, value)
			s := &Service{options: options{currentClientVersion: version}}
			if got := s.buffSwapReapplyEnabled(); got != (value == "on") {
				t.Fatalf("legacy profile %q changed its opt-in behavior for %q", version, value)
			}
		}
	}
}

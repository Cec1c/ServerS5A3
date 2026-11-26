package dnfbridge

import (
	"context"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestCompletedAikenRetryRealPVF(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires deployed PVF")
	}
	s, repos, _, _ := newDailyCountAtomicTestService(t, path)
	logger, logPath := newPeerEndpointTestLogger(t)
	s.packetLog = logger
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(readPeerEndpointTestLog(t, logger, logPath))
		}
		_ = logger.Close()
	})
	se := seedDungeonDailyCountCharacter(t, repos, "61", 61, "prereq-account")
	se.loginPacketSeen, se.accountFromToken = true, true
	se.channel = channelcatalog.Channel{ServerID: 1, ID: 11, Type: 1}
	se.residentChannel = se.channel
	s.bindGameSessionCharacter(se, 61)
	if err := repos.Account.Save(context.Background(), dnfrepo.AccountRecord{AccountID: se.accountID}); err != nil {
		t.Fatal(err)
	}
	bindDungeonSelectorOriginForTestAt(t, s, se, 107, 0, 250, 224)
	slots := ekernTicketSlots()
	for k, v := range slots {
		v.Count = 100
		slots[k] = v
	}
	if err := repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: "61", Slots: slots}); err != nil {
		t.Fatal(err)
	}
	r, _, err := s.prepareDungeonRuntime(context.Background(), se, dungeoncmd.SelectDungeonRequest{DungeonID: 5097})
	if err != nil {
		t.Fatal(err)
	}
	if err := freezeCurrentDungeonTownReturnOrigin(se, r); err != nil {
		t.Fatal(err)
	}
	se.dungeon.runtime = r
	completePartyRetryFixture(t, s, se)
	if err := s.handleDungeonSelectCard(se, []byte{0, 0}); err != nil {
		t.Fatal(err)
	}
	if err := s.handleGameCommand(se, 1, 72, []byte{1, 0}); err != nil {
		t.Fatal(err)
	}
	if se.dungeon.runtime == nil || se.dungeon.runtime == r || se.dungeon.runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
		t.Fatal("Aiken retry did not enter a fresh active run")
	}
}

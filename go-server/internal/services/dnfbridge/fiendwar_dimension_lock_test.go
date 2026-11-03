package dnfbridge

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestFiendWarCounterRecordPreservesOwnershipAndDungeonPositionOverlay(t *testing.T) {
	s, p, repos, _ := directFiendWarLoginFixture(t, 261)
	p.selectedCharacterID = 29
	p.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: 29, TownID: 98, AreaID: 2, PositionValid: true}
	before, _, _ := repos.Character.Load(context.Background(), "29")
	type result struct {
		row dnfrepo.CharacterRecord
		err error
	}
	done := make(chan result, 1)
	p.townMu.Lock()
	go func() {
		row, err := s.loadCurrentDungeonCharacterRecord(context.Background(), 29, p)
		done <- result{row, err}
	}()
	select {
	case got := <-done:
		p.townMu.Unlock()
		if got.err != nil || !reflect.DeepEqual(before, got.row) {
			t.Fatalf("durable counter record changed: %+v err=%v", got.row, got.err)
		}
	case <-time.After(2 * time.Second):
		p.townMu.Unlock()
		<-done
		t.Fatal("durable counter record waited on live scene lock")
	}
	projected, err := s.dungeonCharacter(context.Background(), 29, p)
	if err != nil || projected.Stats["town_id"] != 98 || projected.Stats["area_id"] != 2 {
		t.Fatal("dungeon entry lost live Fiend War position overlay", err)
	}
	after, _, _ := repos.Character.Load(context.Background(), "29")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("scene overlay persisted into saved record")
	}
	p.accountID = "another-account"
	if _, err := s.loadCurrentDungeonCharacterRecord(context.Background(), 29, p); !errors.Is(err, errDungeonAccountMismatch) {
		t.Fatalf("counter record bypassed account ownership: %v", err)
	}
}

func TestRealPVFFiendWarDimensionProjectionUnderTownLock(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current PVF")
	}
	s, repos, _ := newDungeonDailyCountTestService(t, path)
	s.options.gameUpperHeader = gameUpperHeaderServer16
	p := seedDungeonDailyCountCharacter(t, repos, "19", 19, "dimension-lock-account")
	p.channel = channelcatalog.Channel{ID: 261, Type: 42, Group: "fiendwar"}
	p.characterGeneration = 1
	p.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: 19, TownID: 98, AreaID: 0, PositionValid: true}
	s.gameSessions = map[uint16]*gameSession{19: p}
	before, _, _ := repos.Character.Load(context.Background(), "19")
	done := make(chan error, 1)
	p.townMu.Lock()
	go func() { done <- s.sendCurrentDimensionGateEntranceInfo(p, "town_lock_regression") }()
	select {
	case err := <-done:
		p.townMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		// Release the deliberately held lock and join the reader even in the
		// failing control, so a regression does not leave a stuck test owner.
		p.townMu.Unlock()
		<-done
		t.Fatal("dimension projection reacquired the town lock held by its caller")
	}
	packet, rest := splitLongHengGameServerUpperPacket(t, p.conn.(*bufferConn).write.Bytes())
	if len(rest) != 0 || packet.Header.MsgID != 812 || len(packet.Body) != 9 {
		t.Fatalf("dimension projection did not reach the wire: %+v", packet)
	}
	after, _, _ := repos.Character.Load(context.Background(), "19")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("read-only count projection changed persisted character")
	}
}

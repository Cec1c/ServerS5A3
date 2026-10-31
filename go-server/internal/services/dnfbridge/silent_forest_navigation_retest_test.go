package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Exercise the real select/giveup/town confirmation handlers. Direct owner
// tests alone cannot detect a navigation path that fails to charge admission.
func TestSilentForestNavigationGiveupConsumesAccountEntries(t *testing.T) {
	s, a, _ := prepareSemesterActualReady(t)
	g, _ := s.repositoryGroup()
	body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
	binary.LittleEndian.PutUint32(body, 6062)
	for i := 1; i <= 11; i++ {
		bindDungeonSelectorOriginForTestAt(t, s, a, 50, 0, 441, 207)
		a.conn.(*bufferConn).write.Reset()
		if err := s.sendEnterSelectDungeonState(a, "forest_retest_selector", false, true); err != nil {
			t.Fatal("open selector", err)
		}
		assertSilentForestRemainingPacket(t, a.conn.(*bufferConn).write.Bytes(), byte(11-i))
		a.conn.(*bufferConn).write.Reset()
		if err := s.handleDungeonSelectUpper(a, body); err != nil {
			t.Fatalf("entry %d: %v", i, err)
		}
		account, found, err := g.Account.Load(context.Background(), a.accountID)
		if err != nil || !found {
			t.Fatal(found, err)
		}
		used, err := dnfdungeon.SilentForestEntryUsed(account, s.gameplayNow())
		want := int64(i)
		if want > 10 {
			want = 10
		}
		if err != nil || used != want {
			t.Fatalf("entry %d account used=%d want=%d err=%v", i, used, want, err)
		}
		if i == 11 {
			if a.dungeon.runtime != nil {
				t.Fatal("eleventh entry installed runtime")
			}
			packets := silentForestPacketsForTest(t, a.conn.(*bufferConn).write.Bytes())
			refused := false
			for _, p := range packets {
				if p.Header.MsgID == 16 && p.Header.Classification == 1 && len(p.Body) > 1 && p.Body[0] == 0 && p.Body[1] == currentSelectDungeonMemberEntryLimitReason {
					refused = true
				}
			}
			if !refused {
				t.Fatal("eleventh entry missing explicit limit refusal")
			}
			break
		}
		r := a.dungeon.runtime
		assertSilentForestRemainingPacket(t, a.conn.(*bufferConn).write.Bytes(), byte(10-i))
		if r == nil || r.Dungeon.ID != 6062 || r.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
			t.Fatalf("entry %d did not start forest", i)
		}
		if err = s.handleDungeonGiveupGame(a, nil); err != nil {
			t.Fatal(err)
		}
		if err = s.commitPendingDungeonReturnForSceneRequest(a, "forest_retest_town_confirmation"); err != nil {
			t.Fatal(err)
		}
		if a.dungeon.runtime != nil || r.Session.Snapshot().Run.Status != worldmap.DungeonRunAbandoned {
			t.Fatal("giveup did not retire forest runtime")
		}
	}
	// A different character on the same account must see and obey the exhausted
	// ledger. Rebinding also prevents this check from relying on the old actor.
	character, found, err := g.Character.Load(context.Background(), "99")
	if err != nil || !found {
		t.Fatal(found, err)
	}
	character.CharacterID = "100"
	character.Name = "ForestSecondCharacter"
	delete(character.Stats, dnfdungeon.EpicEntrySequenceKey)
	if err := g.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	if err := g.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: "100", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	if err := g.Skill.Save(context.Background(), dnfrepo.SkillRecord{CharacterID: "100", Points: dnfrepo.SkillPointState{SyncedLevel: 17}}); err != nil {
		t.Fatal(err)
	}
	s.bindGameSessionCharacter(a, 100)
	bindDungeonSelectorOriginForTestAt(t, s, a, 50, 0, 441, 207)
	a.conn.(*bufferConn).write.Reset()
	if err := s.sendEnterSelectDungeonState(a, "forest_second_character", false, true); err != nil {
		t.Fatal(err)
	}
	assertSilentForestRemainingPacket(t, a.conn.(*bufferConn).write.Bytes(), 0)
	if err := s.handleDungeonSelectUpper(a, body); err != nil || a.dungeon.runtime != nil {
		t.Fatalf("second character bypassed account limit: %v", err)
	}
	// The service-day transition must refresh both the selector and admission.
	clock := s.gameplayTimers.(*fakeCurrentDungeonDeathTimerQueue)
	clock.now = clock.now.Add(24 * time.Hour)
	bindDungeonSelectorOriginForTestAt(t, s, a, 50, 0, 441, 207)
	a.conn.(*bufferConn).write.Reset()
	if err := s.sendEnterSelectDungeonState(a, "forest_next_day", false, true); err != nil {
		t.Fatal(err)
	}
	assertSilentForestRemainingPacket(t, a.conn.(*bufferConn).write.Bytes(), 10)
	a.conn.(*bufferConn).write.Reset()
	if err := s.handleDungeonSelectUpper(a, body); err != nil || a.dungeon.runtime == nil {
		t.Fatalf("next day entry failed: %v", err)
	}
	assertSilentForestRemainingPacket(t, a.conn.(*bufferConn).write.Bytes(), 9)
}

func assertSilentForestRemainingPacket(t *testing.T, stream []byte, remaining byte) {
	t.Helper()
	packets := silentForestPacketsForTest(t, stream)
	count := 0
	for _, p := range packets {
		if p.Header.MsgID != currentAccountDungeonInoutCountMsgID || p.Header.Classification != 0 {
			continue
		}
		count++
		if len(p.Body) != 9 || binary.LittleEndian.Uint32(p.Body) != 1 || binary.LittleEndian.Uint32(p.Body[4:]) != 6062 || p.Body[8] != remaining {
			t.Fatalf("remaining=%d packet=%x", remaining, p.Body)
		}
	}
	if count != 1 {
		t.Fatalf("remaining=%d expected one current count packet, got %d", remaining, count)
	}
}

func silentForestPacketsForTest(t *testing.T, stream []byte) []dnfproto.ChannelPacket {
	t.Helper()
	var packets []dnfproto.ChannelPacket
	for len(stream) > 0 {
		packet, tail := splitCurrentGameServerUpperPacketAuto(t, stream)
		packets = append(packets, packet)
		stream = tail
	}
	return packets
}

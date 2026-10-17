package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"testing"
	"time"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestGriefResultKeepsTenNativeCardsAndEmptySentinel(t *testing.T) {
	items := make([]currentDespairResultItem, 10)
	items[2] = currentDespairResultItem{10160931, 1}
	items[9] = currentDespairResultItem{10160930, 1}
	body, err := buildCurrentGriefResultBody(12345, 50, items)
	if err != nil || currentGriefResultMsgID != 1263 || len(body) != 87 || body[6] != 10 {
		t.Fatalf("body=%x err=%v", body, err)
	}
	for i := 0; i < 10; i++ {
		id := binary.LittleEndian.Uint32(body[7+i*8:])
		count := binary.LittleEndian.Uint32(body[11+i*8:])
		if items[i].ItemID == 0 {
			if id != math.MaxUint32 || count != 0 {
				t.Fatal("empty card collapsed or became an item")
			}
		} else if id != items[i].ItemID || count != items[i].Quantity {
			t.Fatal("card position changed")
		}
	}
	for _, bad := range [][]currentDespairResultItem{nil, items[:9], append(append([]currentDespairResultItem(nil), items...), currentDespairResultItem{})} {
		if _, err := buildCurrentGriefResultBody(1, 1, bad); err == nil {
			t.Fatal("wrong card count accepted")
		}
	}
	items[0] = currentDespairResultItem{0, 1}
	if _, err := buildCurrentGriefResultBody(1, 1, items); err == nil {
		t.Fatal("zero item with quantity accepted")
	}
}

func TestGriefResultFailedWriteRetriesOnlyDedicatedTail(t *testing.T) {
	s, rt, session, _, _ := prepareCompletedSettlementRuntime(t)
	rt.Dungeon.Metadata.IntegerValues = map[string]int64{"tower of grief": 1}
	rt.Dungeon.ID = 5115
	rt.Dungeon.Path = "dungeon/towers/grief/5115_001.dgn"
	plan := currentDungeonSettlementPacketPlanForTest(t, rt, 0)
	rt.settlementResultPlan = &plan
	want := errors.New("grief result write failed")
	conn := &failNthDungeonWriteConn{failAt: 4, err: want}
	session.conn = conn
	request := make([]byte, currentDungeonPlayResultBaseSize)
	if err := s.handleDungeonSetPlayResult(session, request); !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
	if rt.settlementGriefResultSent || rt.settlementPhase >= currentDungeonSettlementPhaseResultShown {
		t.Fatal("failed notice opened exit barrier")
	}
	if err := s.handleDungeonSetPlayResult(session, request); err != nil {
		t.Fatal(err)
	}
	remaining := conn.write.Bytes()
	for _, id := range []uint16{34, 37, 35, 1263} {
		p, tail := splitGameServerUpperPacket(t, remaining)
		remaining = tail
		if p.Header.MsgID != id || p.Header.Classification != 0 {
			t.Fatalf("wrong packet %+v", p.Header)
		}
	}
	if len(remaining) != 0 || !rt.settlementGriefResultSent || rt.settlementDespairResultSent {
		t.Fatal("wrong tower result sequence")
	}
	n := conn.write.Len()
	if err := s.handleDungeonSetPlayResult(session, request); err != nil || conn.write.Len() != n {
		t.Fatal("successful result replay sent twice")
	}
}

func TestGriefResultButtonsAndOrdinaryCardsRemainSeparate(t *testing.T) {
	for _, option := range []byte{0, 1, 2} {
		t.Run(fmt.Sprintf("option%d", option), func(t *testing.T) {
			s, rt, session, conn := prepareOrdinaryCompletedSettlementRuntimeForExitTest(t)
			rt.Dungeon.Metadata.IntegerValues = map[string]int64{"tower of grief": 1}
			if err := s.freezeCurrentDungeonCardRewardStateLocked(session, rt); err != nil || rt.settlementCardRewardState != nil {
				t.Fatal("grief created ordinary card candidates")
			}
			completeSettlementExitBarriersForTest(t, s, rt, session)
			conn.write.Reset()
			if err := s.handleDungeonScoreScrollState(session, nil); err != nil {
				t.Fatal(err)
			}
			if err := s.handleDungeonCardSelectRightState(session, nil); err != nil {
				t.Fatal(err)
			}
			if conn.write.Len() != 0 || rt.settlementCardScrollStateSent || rt.settlementCardLayoutSent || rt.settlementCardRewardCommitted {
				t.Fatal("ordinary cards were opened")
			}
			if err := s.handleDungeonEplpCommand(session, []byte{1, option}); err != nil {
				t.Fatal(err)
			}
			if option == 0 {
				if conn.write.Len() != 0 || session.dungeon.runtime != rt {
					t.Fatal("retry on cleared floor was accepted")
				}
				return
			}
			ack, tail := splitGameServerUpperPacket(t, conn.write.Bytes())
			if ack.Header.MsgID != 72 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, buildCurrentDungeonOp72SuccessBody(1, option)) {
				t.Fatal("wrong result action ACK")
			}
			if session.dungeon.runtime != nil {
				t.Fatal("completed tower owner not retired")
			}
			if option == 2 {
				assertCompletedDungeonTownRouteForTest(t, tail, rt.townReturnTransition.Body)
			} else {
				seen := map[uint16]bool{}
				for len(tail) > 0 {
					p, rest := splitGameServerUpperPacket(t, tail)
					seen[p.Header.MsgID] = true
					tail = rest
				}
				if !seen[15] || !seen[27] {
					t.Fatalf("selector missing: %v", seen)
				}
			}
		})
	}
}

func TestRealPVFGriefProducerRewardsProgressSelectorAndReplay(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("requires fixed published PVF")
	}
	a, err := platformpvf.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	world, err := worldmap.LoadSource(context.Background(), a, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(world)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	for _, floor := range []int{1, 50, 100} {
		t.Run(fmt.Sprintf("floor%d", floor), func(t *testing.T) {
			s, rt, session, conn, _ := prepareCompletedSettlementRuntime(t)
			s.initialEquipmentArchive = a
			s.dungeonMonsterTable = monsters
			s.worldMapTable = world
			s.worldMapResolver = resolver
			d, found := world.FindDungeon(int64(5114 + floor))
			if !found {
				t.Fatal("real grief dungeon missing")
			}
			rt.Dungeon = d
			repos, _ := s.repositoryGroup()
			ctx := context.Background()
			c, _, _ := repos.Character.Load(ctx, "99")
			// The level-cap EXP split also writes the owner's adventure account.
			// The generic low-level combat fixture has no account row yet.
			if _, found, err := repos.Account.Load(ctx, c.AccountID); err != nil {
				t.Fatal(err)
			} else if !found {
				if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: c.AccountID, State: "active", Metadata: map[string]string{}}); err != nil {
					t.Fatal(err)
				}
			}
			c.Level = 90
			c.Stats["exp"] = 0
			c.Stats[dnfdungeon.GriefHighestClearedStatKey] = int64(floor - 1)
			c.Stats[dnfdungeon.DespairHighestClearedStatKey] = 7
			if err := repos.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			if err := repos.Skill.Save(ctx, dnfrepo.SkillRecord{CharacterID: "99", Points: dnfrepo.SkillPointState{TotalSP: 10, RemainingSP: 10, SyncedLevel: 90}}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
				t.Fatal(err)
			}
			rt.Character = c
			rt.clearMapCompletionKey = fmt.Sprintf("real-grief-producer-floor-%d", floor)
			rt.settlementResultPlan = nil
			rt.settlementPlayResultReceived = true
			rt.settlementPlayResultBody = make([]byte, currentDungeonPlayResultBaseSize)
			rt.startedAt = time.Unix(1700000000, 0)
			rt.clearMapCompletionAt = rt.startedAt.Add(12345 * time.Millisecond)
			resources, err := s.currentDungeonSettlementRewardResources(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.produceCurrentDungeonSettlementPlanWithResourcesLocked(ctx, session, rt, resources); err != nil {
				diagnostic, _, _ := repos.Character.Load(ctx, "99")
				t.Fatalf("producer: %v, grief_frozen=%t persisted_floor=%d level=%d exp=%d", err, rt.settlementGriefRewards != nil, diagnostic.Stats[dnfdungeon.GriefHighestClearedStatKey], diagnostic.Level, diagnostic.Stats["exp"])
			}
			body := append([]byte(nil), rt.settlementResultPlan.GriefResultBody...)
			saved, _, _ := repos.Character.Load(ctx, "99")
			inv, _, _ := repos.Inventory.Load(ctx, "99")
			if len(body) != 87 || body[6] != 10 || binary.LittleEndian.Uint16(body[4:6]) != uint16(floor) || saved.Stats[dnfdungeon.GriefHighestClearedStatKey] != int64(floor) || saved.Stats[dnfdungeon.DespairHighestClearedStatKey] != 7 {
				t.Fatalf("partial clear: body=%x stats=%v", body, saved.Stats)
			}
			for i, item := range rt.settlementGriefRewards.Items {
				wireID := binary.LittleEndian.Uint32(body[7+i*8:])
				if item.Stack.ItemID == 0 {
					if wireID != math.MaxUint32 {
						t.Fatal("empty native card missing")
					}
					continue
				}
				if wireID != uint32(item.Stack.ItemID) {
					t.Fatal("displayed uncommitted candidate")
				}
			}
			if (floor == 50 || floor == 100) && binary.LittleEndian.Uint32(body[79:]) != 10160930 {
				t.Fatal("special floor lost guaranteed card10")
			}
			rt.settlementResultPlan = nil
			if err := s.produceCurrentDungeonSettlementPlanWithResourcesLocked(ctx, session, rt, resources); err != nil {
				t.Fatal(err)
			}
			after, _, _ := repos.Character.Load(ctx, "99")
			invAfter, _, _ := repos.Inventory.Load(ctx, "99")
			if !reflect.DeepEqual(saved, after) || !reflect.DeepEqual(inv, invAfter) || !bytes.Equal(body, rt.settlementResultPlan.GriefResultBody) {
				t.Fatal("producer replay changed committed clear")
			}
			next := floor%100 + 1
			fresh := &gameSession{selectedCharacterID: 99, accountID: session.accountID, conn: conn}
			conn.write.Reset()
			if err := s.sendCurrentDungeonContextOp27(fresh, "grief-reconnect-test"); err != nil {
				t.Fatal(err)
			}
			packet, tail := splitGameServerUpperPacket(t, conn.write.Bytes())
			wireFloor := binary.LittleEndian.Uint16(packet.Body[34:36])
			if next == 1 {
				next = 0
			}
			if packet.Header.MsgID != 27 || len(tail) != 0 || wireFloor != uint16(next) || binary.LittleEndian.Uint16(packet.Body[15:17]) != 8 {
				t.Fatalf("independent selector fields: %x", packet.Body)
			}
			selected, err := s.resolveCurrentGriefSelection(fresh, 5115)
			if err != nil || selected != uint32(5114+floor%100+1) {
				t.Fatalf("next selection=%d err=%v", selected, err)
			}
		})
	}
}

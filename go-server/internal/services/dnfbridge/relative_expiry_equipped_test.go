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

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func relativeTrialExpiryFixture(t *testing.T) (*Service, dnfrepo.Group, *fakeCurrentDungeonDeathTimerQueue, dnfrepo.ItemStack) {
	t.Helper()
	path := os.Getenv("DNFBRIDGE_RELATIVE_EXPIRY_PVF")
	if path == "" {
		t.Skip("set DNFBRIDGE_RELATIVE_EXPIRY_PVF to the approved real resource")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if a.Snapshot().Checksum != "25675fba8711216b72800e5ff034df61209b37e7c22d3e4c3b3ad7b1906fc348" {
		t.Fatal("wrong PVF")
	}
	catalog, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	def, err := catalog.ResolveItem(100322469)
	if err != nil || def.UsablePeriodDays != 2 || !def.ExpirationDate.IsZero() {
		t.Fatalf("trial ring definition: %+v %v", def, err)
	}
	issued := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	grant, err := currentPVFItemDefinitionForGrantAt(def, issued)
	if err != nil {
		t.Fatal(err)
	}
	stack, _ := applyCurrentPVFItemExpirationAt(dnfrepo.ItemStack{ItemID: 100322469, Count: 1}, grant, issued)
	stack.Extra["current_exe_equipment_type"] = "21"
	stack.Extra["current_exe_runtime_move"] = "1"
	s, repos, q := newDailyDeleteTestService(t, grant.ExpirationDate.Add(-time.Minute))
	s.pvfItemCatalog = catalog
	return s, repos, q, stack
}

func TestRelativeTrialWornExpiryOwner(t *testing.T) {
	for _, when := range []string{"before", "exact", "after"} {
		t.Run(when, func(t *testing.T) {
			s, repos, q, stack := relativeTrialExpiryFixture(t)
			eq := dnfrepo.EquipmentRecord{CharacterID: "19", Entries: map[string]dnfrepo.EquipmentEntry{
				"21": {SlotIndex: 21, ItemID: stack.ItemID, Bind: stack.Bind, ExpireAt: stack.ExpireAt, RawEntry: stack.RawEntry, Extra: stack.Extra},
				"20": {SlotIndex: 20, ItemID: 100322052},
			}}
			if err := repos.Equipment.Save(context.Background(), eq); err != nil {
				t.Fatal(err)
			}
			switch when {
			case "exact":
				q.now = stack.ExpireAt
			case "after":
				q.now = stack.ExpireAt.Add(time.Second)
			}
			summary, _, err := s.reconcileCurrentDailyDeleteWithRemoved(context.Background(), repos, "19", "account-1", s.pvfItemCatalog)
			if err != nil {
				t.Fatal(err)
			}
			after, _, err := repos.Equipment.Load(context.Background(), "19")
			if err != nil {
				t.Fatal(err)
			}
			_, exists := after.Entries["21"]
			if exists != (when == "before") {
				t.Fatalf("worn expiry: when=%s remains=%v summary=%+v", when, exists, summary)
			}
			if after.Entries["20"].ItemID != 100322052 {
				t.Fatal("permanent ring changed")
			}
		})
	}
}

func TestRelativeTrialOnlineDeadlineClock(t *testing.T) {
	s, repos, q, stack := relativeTrialExpiryFixture(t)
	inv, _, _ := repos.Inventory.Load(context.Background(), "19")
	if inv.Slots == nil {
		inv.Slots = make(map[string]dnfrepo.ItemStack)
	}
	inv.Slots["0:13"] = stack
	if err := repos.Inventory.Save(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	session := &gameSession{conn: &bufferConn{}, connID: "relative-deadline", accountID: "account-1", selectedCharacterID: 19, characterGeneration: 3}
	s.armCurrentDailyDeleteClock(session)
	task := q.task(t, 0)
	if task.delay != stack.ExpireAt.Sub(q.now) {
		t.Fatalf("relative expiry waits until daily boundary: got=%s want=%s", task.delay, stack.ExpireAt.Sub(q.now))
	}
	q.now = stack.ExpireAt
	q.fire(task, false)
	after, _, _ := repos.Inventory.Load(context.Background(), "19")
	if _, exists := after.Slots["0:13"]; exists {
		t.Fatal("online expired ring remained")
	}
	t.Log(fmt.Sprintf("expired at %s without relog", q.now))
}

func TestRelativeTrialWornPreservationAndRollback(t *testing.T) {
	for _, mode := range []string{"gm", "conflict", "missing_source", "future", "wrong_owner", "bad_slot", "final_commit"} {
		t.Run(mode, func(t *testing.T) {
			s, repos, q, stack := relativeTrialExpiryFixture(t)
			q.now = stack.ExpireAt
			switch mode {
			case "gm":
				stack.Extra["expiration_source"] = "gm_override"
			case "conflict":
				stack.Extra["expire_unix"] = "1"
			case "missing_source":
				delete(stack.Extra, "expiration_source")
			case "future":
				q.now = q.now.Add(-time.Nanosecond)
			}
			key := "21"
			if mode == "bad_slot" {
				key = "foreign"
			}
			eq := dnfrepo.EquipmentRecord{CharacterID: "19", Entries: map[string]dnfrepo.EquipmentEntry{key: {SlotIndex: 21, ItemID: stack.ItemID, ExpireAt: stack.ExpireAt, RawEntry: stack.RawEntry, Extra: stack.Extra}}}
			if e := repos.Equipment.Save(context.Background(), eq); e != nil {
				t.Fatal(e)
			}
			inv, _, _ := repos.Inventory.Load(context.Background(), "19")
			inv.Slots = map[string]dnfrepo.ItemStack{"0:13": stack}
			if e := repos.Inventory.Save(context.Background(), inv); e != nil {
				t.Fatal(e)
			}
			beforeEq, _, _ := repos.Equipment.Load(context.Background(), "19")
			beforeInv, _, _ := repos.Inventory.Load(context.Background(), "19")
			account := "account-1"
			if mode == "wrong_owner" {
				account = "other"
			}
			if mode == "final_commit" {
				repos.AccountAssets = rejectRelativeExpiryCommit{repos.AccountAssets}
			}
			_, removed, err := s.reconcileCurrentDailyDeleteWithRemoved(context.Background(), repos, "19", account, s.pvfItemCatalog)
			expectErr := mode == "wrong_owner" || mode == "bad_slot" || mode == "final_commit"
			if (err != nil) != expectErr {
				t.Fatalf("error=%v expected=%t", err, expectErr)
			}
			afterEq, _, _ := repos.Equipment.Load(context.Background(), "19")
			afterInv, _, _ := repos.Inventory.Load(context.Background(), "19")
			if !reflect.DeepEqual(beforeEq, afterEq) || !reflect.DeepEqual(beforeInv, afterInv) || len(removed) != 0 {
				t.Fatal("unproven or aborted expiry changed assets or returned clear intent")
			}
		})
	}
}

type relativeExpiryFailConn struct {
	bufferConn
	fail bool
}

func (c *relativeExpiryFailConn) Write(p []byte) (int, error) {
	if c.fail {
		return 0, errors.New("injected expiry write failure")
	}
	return c.bufferConn.Write(p)
}

func relativeExpiryReadySession(s *Service, conn *relativeExpiryFailConn) *gameSession {
	p := &gameSession{conn: conn, connID: "worn-expiry", accountID: "account-1", selectedCharacterID: 19, characterGeneration: 3, selectedItemListBootstrapCharacterID: 19}
	p.townSceneReadyCharacterID = 19
	p.townActorOwnerChannel = currentConnectionTownActorOwnerContext(p)
	return p
}

func TestRelativeTrialOnlineWornExpiryAndProjectionRetry(t *testing.T) {
	for _, mode := range []string{"normal", "actor_not_ready", "write_failure", "slot_reused", "stale_character"} {
		t.Run(mode, func(t *testing.T) {
			s, repos, q, stack := relativeTrialExpiryFixture(t)
			s.options.currentClientVersion = "official-24.9"
			if err := repos.Equipment.Save(context.Background(), dnfrepo.EquipmentRecord{CharacterID: "19", Entries: map[string]dnfrepo.EquipmentEntry{"21": {SlotIndex: 21, ItemID: stack.ItemID, ExpireAt: stack.ExpireAt, RawEntry: stack.RawEntry, Extra: stack.Extra}}}); err != nil {
				t.Fatal(err)
			}
			c := &relativeExpiryFailConn{}
			p := relativeExpiryReadySession(s, c)
			if mode == "actor_not_ready" {
				p.townSceneReadyCharacterID = 0
			}
			if mode == "write_failure" || mode == "slot_reused" || mode == "stale_character" {
				c.fail = true
			}
			s.armCurrentDailyDeleteClock(p)
			task := q.task(t, 0)
			if task.delay != time.Minute {
				t.Fatalf("clock=%s", task.delay)
			}
			q.now = stack.ExpireAt
			q.fire(task, false)
			eq, _, _ := repos.Equipment.Load(context.Background(), "19")
			if len(eq.Entries) != 0 {
				t.Fatal("worn owner not expired")
			}
			if mode == "normal" {
				if p.pendingItemExpiryProjection != nil {
					t.Fatal("successful projection retained")
				}
			} else {
				if p.pendingItemExpiryProjection == nil {
					t.Fatal("failed projection lost retry")
				}
				c.fail = false
				if mode == "slot_reused" {
					eq.Entries = map[string]dnfrepo.EquipmentEntry{"21": {SlotIndex: 21, ItemID: 100322052, Extra: map[string]string{"current_exe_equipment_type": "21", "current_exe_runtime_move": "1"}}}
					if e := repos.Equipment.Save(context.Background(), eq); e != nil {
						t.Fatal(e)
					}
				}
				if mode == "stale_character" {
					p.selectedCharacterID = 20
					p.characterGeneration++
					c.write.Reset()
					q.fire(q.task(t, 1), true)
					if c.write.Len() != 0 {
						t.Fatal("old timer wrote new character")
					}
					return
				}
				if mode == "actor_not_ready" {
					p.townSceneReadyCharacterID = 19
				}
				q.now = q.now.Add(time.Minute)
				q.fire(q.task(t, 1), false)
				if p.pendingItemExpiryProjection != nil {
					t.Fatal("retry not acknowledged")
				}
			}
			packets := splitAllGameServerUpperPackets(t, c.write.Bytes())
			seen := false
			for _, packet := range packets {
				if packet.Header.Classification == 0 && packet.Header.MsgID == 14 && len(packet.Body) >= 3+currentItemListEntryWireSize && packet.Body[0] == 3 {
					item := binary.LittleEndian.Uint32(packet.Body[5:9])
					want := uint32(math.MaxUint32)
					if mode == "slot_reused" {
						want = 100322052
					}
					if item != want {
						t.Fatalf("projection item=%d want=%d", item, want)
					}
					seen = true
				}
			}
			if !seen {
				t.Fatal("no worn slot projection")
			}
			if bytes.Contains(c.write.Bytes(), []byte("injected")) {
				t.Fatal("diagnostic entered game packet")
			}
		})
	}
}

func TestRelativeTrialGrantProjectionRearmsEarlierDeadline(t *testing.T) {
	s, repos, q, stack := relativeTrialExpiryFixture(t)
	c := &relativeExpiryFailConn{}
	p := relativeExpiryReadySession(s, c)
	s.armCurrentDailyDeleteClock(p)
	if q.task(t, 0).delay == time.Minute {
		t.Fatal("fixture unexpectedly already contained the grant")
	}
	inv, _, _ := repos.Inventory.Load(context.Background(), "19")
	inv.Slots = map[string]dnfrepo.ItemStack{"0:13": stack}
	if e := repos.Inventory.Save(context.Background(), inv); e != nil {
		t.Fatal(e)
	}
	body := buildCurrentItemUpdateBody(0, []currentItemListEntry{currentItemListEntryFromStack(0, 13, stack)})
	if err := s.sendGameUpperRawClass(p, 14, body, 0); err != nil {
		t.Fatal(err)
	}
	if q.task(t, 1).delay != time.Minute {
		t.Fatalf("grant failed to rearm: %s", q.task(t, 1).delay)
	}
}

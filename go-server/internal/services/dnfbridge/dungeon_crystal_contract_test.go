package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func newCrystalDungeonTickTest(t *testing.T, count int64) (*Service, *gameSession, *runtimeDungeonState, dnfrepo.Group, *bufferConn, *fakeCurrentDungeonDeathTimerQueue) {
	t.Helper()
	service, session, runtime := newBackToVillageRuntime(t)
	repos, _ := service.repositoryGroup()
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = time.Now().UTC()
	service.gameplayTimers = queue
	account, _, err := repos.Account.Load(context.Background(), "account-1")
	if err != nil {
		t.Fatal(err)
	}
	premium.Upsert(&account, premium.TypeCrystal, 3600, 1, queue.Now())
	if err := repos.Account.Save(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	if err := repos.AccountInventory.Save(context.Background(), dnfrepo.AccountInventoryRecord{
		AccountID: "account-1", Slots: map[string]dnfrepo.ItemStack{
			"0:354": {ItemID: 3033, Count: count}, "0:355": {ItemID: 3034, Count: count},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.handleCurrentCrystalContractUpdate(session, []byte{0, 0}); err != nil {
		t.Fatal(err)
	}
	conn := session.conn.(*bufferConn)
	conn.write.Reset()
	session.postStartMapPlayerStateSent = true
	return service, session, runtime, repos, conn, queue
}

func crystalTickCount(t *testing.T, repos dnfrepo.Group, slot string) int64 {
	t.Helper()
	inventory, found, err := repos.AccountInventory.Load(context.Background(), "account-1")
	if err != nil || !found {
		t.Fatalf("inventory found=%t err=%v", found, err)
	}
	return inventory.Slots[slot].Count
}

func TestCrystalDungeonTickStartsFromActualPlayerPlacement(t *testing.T) {
	s, p, r, repos, _, q := newCrystalDungeonTickTest(t, 3)
	p.postStartMapPlayerStateSent = false
	scene, ok := r.Session.Scene()
	if !ok {
		t.Fatal("missing scene")
	}
	if err := s.sendCurrentPostStartMapPlayerPlacement(p, r, scene, "crystal_entry_test"); err != nil {
		t.Fatal(err)
	}
	// 真实落位也会安排其它玩法时钟，只核对本功能的具名任务。
	q.mu.Lock()
	task := q.active[r.crystalContractTimerName]
	q.mu.Unlock()
	if task == nil || !r.crystalContractTimerActive {
		t.Fatal("actual player placement did not arm crystal consumption")
	}
	if got := crystalTickCount(t, repos, "0:354"); got != 3 {
		t.Fatalf("entry charged before 30 seconds: %d", got)
	}
	if task.delay != 30*time.Second {
		t.Fatalf("delay=%v", task.delay)
	}
	q.fire(task, false)
	if got := crystalTickCount(t, repos, "0:354"); got != 2 {
		t.Fatalf("actual entry tick count=%d", got)
	}
}

func TestCrystalDungeonTickRefreshesWarehouseAndIgnoresDuplicateCallback(t *testing.T) {
	s, p, r, repos, conn, q := newCrystalDungeonTickTest(t, 3)
	if err := s.armCurrentCrystalContractTimer(p, r); err != nil {
		t.Fatal(err)
	}
	task := q.task(t, 0)
	q.fire(task, false)
	q.fire(task, true)
	if got := crystalTickCount(t, repos, "0:354"); got != 2 {
		t.Fatalf("count=%d", got)
	}
	packet, rest := nextCrystalPacket(t, conn.write.Bytes())
	if packet.Header.Classification != 0 || packet.Header.MsgID != 14 || len(packet.Body) < 13 ||
		packet.Body[0] != 0 || binary.LittleEndian.Uint16(packet.Body[1:3]) != 1 ||
		binary.LittleEndian.Uint16(packet.Body[3:5]) != 354 || binary.LittleEndian.Uint32(packet.Body[5:9]) != 3033 ||
		binary.LittleEndian.Uint32(packet.Body[9:13]) != 2 || len(rest) != 0 {
		t.Fatalf("inventory update=%+v body=%x rest=%x", packet.Header, packet.Body, rest)
	}
	if n, _, active := q.counts(); n != 2 || active != 1 {
		t.Fatalf("timers=%d active=%d", n, active)
	}
}

func TestCrystalDungeonTickLastCubeClearsSelectionAndStops(t *testing.T) {
	s, p, r, repos, conn, q := newCrystalDungeonTickTest(t, 1)
	if err := s.armCurrentCrystalContractTimer(p, r); err != nil {
		t.Fatal(err)
	}
	q.fire(q.task(t, 0), false)
	packet, rest := nextCrystalPacket(t, conn.write.Bytes())
	if packet.Header.MsgID != 14 || binary.LittleEndian.Uint32(packet.Body[5:9]) != 0 {
		t.Fatalf("clear packet=%+v %x", packet.Header, packet.Body)
	}
	state, rest := nextCrystalPacket(t, rest)
	assertCurrentCrystalStatePacket(t, state, []byte{0, 255})
	if len(rest) != 0 || r.crystalContractTimerActive {
		t.Fatalf("trailing=%x active=%t", rest, r.crystalContractTimerActive)
	}
	if got := crystalTickCount(t, repos, "0:354"); got != 0 {
		t.Fatalf("count=%d", got)
	}
	char, _, err := repos.Character.Load(context.Background(), "99")
	if err != nil || char.Stats["premium_crystal_selection"] != -1 {
		t.Fatalf("selection=%v err=%v", char.Stats, err)
	}
	if got := crystalTickCount(t, repos, "0:355"); got != 1 {
		t.Fatalf("unselected count=%d", got)
	}
}

func TestCrystalDungeonTickSelectionChangeKeepsDeadlineCancelInvalidatesStale(t *testing.T) {
	s, p, r, repos, conn, q := newCrystalDungeonTickTest(t, 4)
	if err := s.armCurrentCrystalContractTimer(p, r); err != nil {
		t.Fatal(err)
	}
	task := q.task(t, 0)
	if err := s.handleCurrentCrystalContractUpdate(p, []byte{0, 1}); err != nil {
		t.Fatal(err)
	}
	if n, _, _ := q.counts(); n != 1 {
		t.Fatalf("color change restarted interval: %d", n)
	}
	conn.write.Reset()
	q.fire(task, false)
	if crystalTickCount(t, repos, "0:354") != 4 || crystalTickCount(t, repos, "0:355") != 3 {
		t.Fatal("tick did not consume current selection")
	}
	stale := q.task(t, 1)
	if err := s.handleCurrentCrystalContractUpdate(p, []byte{0, 255}); err != nil {
		t.Fatal(err)
	}
	q.fire(stale, true)
	if crystalTickCount(t, repos, "0:355") != 3 {
		t.Fatal("cancelled callback charged")
	}
	if _, _, active := q.counts(); active != 0 {
		t.Fatalf("cancel active=%d", active)
	}
	if err := s.handleCurrentCrystalContractUpdate(p, []byte{0, 0}); err != nil {
		t.Fatal(err)
	}
	if _, _, active := q.counts(); active != 1 {
		t.Fatalf("reselected active=%d", active)
	}
	q.fire(q.task(t, 2), false)
	if crystalTickCount(t, repos, "0:354") != 3 {
		t.Fatal("reselected cube not charged")
	}
}

func TestCrystalDungeonTickRejectsEndedReplacedForeignPracticeAndTownRuns(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Service, *gameSession, *runtimeDungeonState)
	}{
		{"abandoned", func(_ *Service, _ *gameSession, r *runtimeDungeonState) {
			if err := r.Session.Abandon(); err != nil {
				t.Fatal(err)
			}
		}},
		{"replaced", func(_ *Service, p *gameSession, _ *runtimeDungeonState) { p.dungeon.runtime = &runtimeDungeonState{} }},
		{"town-return", func(_ *Service, _ *gameSession, r *runtimeDungeonState) { r.townReturnPending = true }},
		{"foreign-character", func(_ *Service, p *gameSession, _ *runtimeDungeonState) { p.selectedCharacterID = 19 }},
		{"old-run-token", func(_ *Service, p *gameSession, _ *runtimeDungeonState) { p.dungeon.runToken++ }},
		{"practice", func(_ *Service, _ *gameSession, r *runtimeDungeonState) { r.Request.HellPartyDifficultyFlag = 1 }},
		{"disconnect", func(s *Service, p *gameSession, _ *runtimeDungeonState) { s.unbindGameSession(p) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, p, r, repos, _, q := newCrystalDungeonTickTest(t, 3)
			if err := s.armCurrentCrystalContractTimer(p, r); err != nil {
				t.Fatal(err)
			}
			task := q.task(t, 0)
			tc.change(s, p, r)
			q.fire(task, true)
			if got := crystalTickCount(t, repos, "0:354"); got != 3 {
				t.Fatalf("stale charge=%d", got)
			}
			if _, _, active := q.counts(); active != 0 || r.crystalContractTimerActive {
				t.Fatalf("stopped active=%d flag=%t", active, r.crystalContractTimerActive)
			}
		})
	}
}

func TestCrystalDungeonTickDoesNotArmForPracticeUnselectedOrInactive(t *testing.T) {
	for _, mode := range []string{"practice", "none", "inactive", "not-placed"} {
		t.Run(mode, func(t *testing.T) {
			s, p, r, repos, _, q := newCrystalDungeonTickTest(t, 3)
			switch mode {
			case "practice":
				r.Request.HellPartyDifficultyFlag = 1
			case "not-placed":
				p.postStartMapPlayerStateSent = false
			case "none":
				if err := s.handleCurrentCrystalContractUpdate(p, []byte{0, 255}); err != nil {
					t.Fatal(err)
				}
			case "inactive":
				q.now = q.now.Add(2 * time.Hour)
			}
			if err := s.armCurrentCrystalContractTimer(p, r); err != nil {
				t.Fatal(err)
			}
			if n, _, active := q.counts(); n != 0 || active != 0 {
				t.Fatalf("timers=%d active=%d", n, active)
			}
			if crystalTickCount(t, repos, "0:354") != 3 {
				t.Fatal("inventory changed")
			}
		})
	}
}

type crystalTickFailOnceAccount struct {
	dnfrepo.AccountRepository
	fail bool
}

func (r *crystalTickFailOnceAccount) Load(ctx context.Context, id string) (dnfrepo.AccountRecord, bool, error) {
	if r.fail {
		r.fail = false
		return dnfrepo.AccountRecord{}, false, errors.New("模拟账号读取失败")
	}
	return r.AccountRepository.Load(ctx, id)
}

type crystalTickFailOnceConn struct {
	*bufferConn
	fail bool
}

func (c *crystalTickFailOnceConn) Write(p []byte) (int, error) {
	if c.fail {
		c.fail = false
		return 0, errors.New("模拟库存通知写失败")
	}
	return c.bufferConn.Write(p)
}

func TestCrystalDungeonTickRetriesRepositoryAndContinuesAfterNotifyFailure(t *testing.T) {
	s, p, r, repos, conn, q := newCrystalDungeonTickTest(t, 4)
	if err := s.armCurrentCrystalContractTimer(p, r); err != nil {
		t.Fatal(err)
	}
	original := repos.Account
	repos.Account = &crystalTickFailOnceAccount{AccountRepository: original, fail: true}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	q.fire(q.task(t, 0), false)
	if crystalTickCount(t, repos, "0:354") != 4 {
		t.Fatal("failed transaction charged")
	}
	if _, _, active := q.counts(); active != 1 {
		t.Fatalf("repository retry active=%d", active)
	}
	p.conn = &crystalTickFailOnceConn{bufferConn: conn, fail: true}
	task := q.task(t, 1)
	q.fire(task, false)
	q.fire(task, true)
	if crystalTickCount(t, repos, "0:354") != 3 {
		t.Fatal("notify failure lost or repeated durable debit")
	}
	if _, _, active := q.counts(); active != 1 {
		t.Fatalf("notify failure disabled tick: %d", active)
	}
	q.fire(q.task(t, 2), false)
	if crystalTickCount(t, repos, "0:354") != 2 {
		t.Fatal("next interval not charged")
	}
}

func TestCrystalDungeonTickChecksExpiryAtExecutionAndStopsForMissingCube(t *testing.T) {
	for _, mode := range []string{"expired", "missing"} {
		t.Run(mode, func(t *testing.T) {
			s, p, r, repos, _, q := newCrystalDungeonTickTest(t, 3)
			if err := s.armCurrentCrystalContractTimer(p, r); err != nil {
				t.Fatal(err)
			}
			due := r.crystalContractTimerDueAt
			if mode == "expired" {
				q.now = q.now.Add(2 * time.Hour)
			} else {
				inv, _, err := repos.AccountInventory.Load(context.Background(), "account-1")
				if err != nil {
					t.Fatal(err)
				}
				delete(inv.Slots, "0:354")
				if err := repos.AccountInventory.Save(context.Background(), inv); err != nil {
					t.Fatal(err)
				}
			}
			// 模拟回调已出队但执行被延迟；不得以旧到期时间绕过契约到期。
			task := q.task(t, 0)
			q.mu.Lock()
			delete(q.active, task.name)
			q.mu.Unlock()
			task.callback(due)
			if mode == "expired" && crystalTickCount(t, repos, "0:354") != 3 {
				t.Fatal("expired charged")
			}
			if _, _, active := q.counts(); active != 0 || r.crystalContractTimerActive {
				t.Fatalf("terminal tick active=%d flag=%t", active, r.crystalContractTimerActive)
			}
		})
	}
}

func TestCrystalDungeonExplicitConsumeInvalidatesPendingTick(t *testing.T) {
	s, p, r, repos, _, q := newCrystalDungeonTickTest(t, 4)
	if err := s.armCurrentCrystalContractTimer(p, r); err != nil {
		t.Fatal(err)
	}
	old := q.task(t, 0)
	body := make([]byte, 8)
	binary.LittleEndian.PutUint16(body, 354)
	binary.LittleEndian.PutUint32(body[2:], 3033)
	binary.LittleEndian.PutUint16(body[6:], 4)
	if err := s.handleCurrentCrystalContractCubeUse(p, body); err != nil {
		t.Fatal(err)
	}
	q.fire(old, true)
	if crystalTickCount(t, repos, "0:354") != 3 {
		t.Fatal("old periodic callback charged after explicit consume")
	}
	q.fire(q.task(t, 1), false)
	if crystalTickCount(t, repos, "0:354") != 2 {
		t.Fatal("next interval did not resume")
	}
}

func TestCrystalDungeonRefreshDefersLockAndReadsLatestSelection(t *testing.T) {
	s, p, r, repos, _, q := newCrystalDungeonTickTest(t, 3)
	p.dungeon.mu.Lock()
	s.requestCurrentCrystalContractTimerRefresh(p)
	p.dungeon.mu.Unlock()
	refresh := q.task(t, 0)
	if refresh.delay != 0 {
		t.Fatalf("refresh delay=%v", refresh.delay)
	}
	if err := s.handleCurrentCrystalContractUpdate(p, []byte{0, 255}); err != nil {
		t.Fatal(err)
	}
	q.fire(refresh, false)
	if _, _, active := q.counts(); active != 0 || r.crystalContractTimerActive {
		t.Fatalf("stale activation armed after cancellation: %d", active)
	}
	if crystalTickCount(t, repos, "0:354") != 3 {
		t.Fatal("refresh charged cubes")
	}
}

func TestCrystalDungeonTickResumesAfterUnconfirmedReturnIsCancelled(t *testing.T) {
	s, p, r, repos, _, q := newCrystalDungeonTickTest(t, 3)
	if err := s.armCurrentCrystalContractTimer(p, r); err != nil {
		t.Fatal(err)
	}
	r.townReturnPending = true
	q.fire(q.task(t, 0), false)
	if crystalTickCount(t, repos, "0:354") != 3 {
		t.Fatal("pending return charged")
	}
	p.dungeon.mu.Lock()
	s.cancelCurrentDungeonReturnAfterDungeonEvidenceLocked(p, r, "crystal_tick_test_live_dungeon_evidence")
	p.dungeon.mu.Unlock()
	q.fire(q.task(t, 1), false)
	q.fire(q.task(t, 2), false)
	if crystalTickCount(t, repos, "0:354") != 2 {
		t.Fatal("resumed dungeon lost crystal clock")
	}
}

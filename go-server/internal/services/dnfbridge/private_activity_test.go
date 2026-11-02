package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/activeness"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/cerashop"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/fatigue"
	"longheng.io/server/internal/modules/dnf/packageitem"
	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
	"time"
)

func TestPrivateActivityWireRejectsForgedShapes(t *testing.T) {
	b := make([]byte, 48)
	copy(b, "APQ2")
	binary.LittleEndian.PutUint16(b[4:], 2)
	binary.LittleEndian.PutUint32(b[32:], 1)
	if _, e := parsePrivateActivityRequest(b); e != nil {
		t.Fatal(e)
	}
	for _, offset := range []int{0, 4, 6, 8, 10, 12, 16, 24, 36, 47} {
		wrong := append([]byte(nil), b...)
		wrong[offset] = 9
		if _, e := parsePrivateActivityRequest(wrong); e == nil {
			t.Fatalf("accepted forged offset %d", offset)
		}
	}
	for _, size := range []int{0, 4, 47, 49} {
		if _, e := parsePrivateActivityRequest(make([]byte, size)); e == nil {
			t.Fatal(size)
		}
	}
	if e := (&Service{}).handlePrivateActivity(nil, 1, b); e != nil {
		t.Fatal(e)
	}
}

func TestPrivateActivityFinalRaidOnly(t *testing.T) {
	for _, tc := range []struct {
		mode, phase byte
		single      bool
		retry       int
		want        bool
	}{{1, 0, false, 0, false}, {1, 1, false, 0, true}, {2, 0, false, 0, false}, {2, 1, false, 0, true}, {3, 0, false, 0, true}, {3, 0, false, 1, false}, {1, 1, true, 0, false}, {2, 1, true, 0, false}, {3, 0, true, 0, false}, {0, 1, false, 0, false}, {1, 2, false, 0, false}, {3, 1, false, 0, false}} {
		f := &currentAntonRewardFlow{Mode: tc.mode, Phase: tc.phase, Single: tc.single, FiendWarRetryCount: uint32(tc.retry)}
		if privateActivityFinalRaid(f) != tc.want {
			t.Fatalf("%+v", tc)
		}
	}
}

func TestPrivateActivityRealPVFAndTransactions(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	a, e := platformpvf.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = time.Date(2026, 10, 27, 12, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
	s := &Service{options: options{accountID: "activity-test", pvfPath: path, currentClientVersion: "official-24.9", gameUpperBodyCodec: gameUpperBodyCodecPlain}, initialEquipmentArchive: a, gameplayTimers: queue, repositoryProvider: func() (repo.Group, bool) { return g, true }}
	c, e := s.privateActivityCatalog()
	if e != nil || c == nil {
		t.Fatalf("catalog %v", e)
	}
	if c.Tasks[3].Points != 100 || c.Tasks[5].Target != 100 || c.Rewards[2].HappyTokens != 5500 {
		t.Fatalf("catalog %+v", c)
	}
	if e = g.Account.Save(ctx, repo.AccountRecord{AccountID: "activity-test"}); e != nil {
		t.Fatal(e)
	}
	if e = g.Character.Save(ctx, repo.CharacterRecord{AccountID: "activity-test", CharacterID: "99", Stats: map[string]int64{"fatigue": 156, fatigue.DayKey: premium.ServiceDayID(queue.Now())}}); e != nil {
		t.Fatal(e)
	}
	session := &gameSession{selectedCharacterID: 99, characterGeneration: 4, conn: &bufferConn{}}
	t.Run("authoritative_boss_completion_and_replay", func(t *testing.T) {
		service, runtime := prepareTutorialScopeRuntime(t, tutorialScopeFixtureOptions{disableTutorial: true, singleMonster: true})
		service.initialEquipmentArchive = a
		if runtime.Dungeon.Metadata.Flags == nil {
			runtime.Dungeon.Metadata.Flags = map[string]bool{}
		}
		runtime.Dungeon.Metadata.Flags[currentDimensionGateDungeonPVFFlag] = true
		if _, e := runtime.Room.AnnounceAllActors(runtime.Session); e != nil {
			t.Fatal(e)
		}
		key := runtime.Room.Snapshot().Monsters[0].ObjectKey
		connection := &bufferConn{}
		actor := &gameSession{conn: connection, selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
		if e := service.completeCurrentDungeonBossDieCheckLocked(actor, runtime, "test_unfinished"); e != nil {
			t.Fatal(e)
		}
		if _, cleared, e := runtime.Room.CommitActorDeathReport(key, runtime.Session); e != nil || !cleared {
			t.Fatalf("clear %v %v", cleared, e)
		}
		for i := 0; i < 2; i++ {
			if e := service.handleDungeonBossDieCheck(actor, bossDieCheckRequestBody(99, uint16(key))); e != nil {
				t.Fatal(e)
			}
		}
		repos, _ := service.repositoryGroup()
		ch, _, _ := repos.Character.Load(ctx, "99")
		if ch.Stats["private_activity_v1_points"] != 10 || ch.Stats["private_activity_v1_progress_7"] != 1 {
			t.Fatalf("completion stats %v", ch.Stats)
		}
	})
	t.Run("real_fatigue_debit", func(t *testing.T) {
		for _, n := range []int64{99, 1, 20} {
			if _, e = s.updateCurrentCharacterFatigueAmount(ctx, session, n); e != nil {
				t.Fatal(e)
			}
		}
		ch, _, _ := g.Character.Load(ctx, "99")
		if ch.Stats["fatigue"] != 36 || ch.Stats["private_activity_v1_points"] != 20 || ch.Stats["private_activity_v1_progress_6"] != 100 {
			t.Fatalf("stats %v", ch.Stats)
		}
	})
	t.Run("dungeon_classification", func(t *testing.T) {
		w, e := worldmap.LoadSource(ctx, a, worldmap.Options{})
		if e != nil {
			t.Fatal(e)
		}
		for id, want := range map[int64]uint16{316: 2, 3570: 3, 3571: 3, 5097: 3, 5098: 3, 6004: 8, 6013: 8, 6062: 8, 6063: 8, 6064: 8, 6065: 8, 7100: 7} {
			d, ok := w.FindDungeon(id)
			if !ok {
				t.Fatal(id)
			}
			got, e := s.privateActivityDungeonTask(&runtimeDungeonState{Dungeon: d})
			if e != nil || got != want {
				t.Fatalf("dungeon %d task=%d err=%v", id, got, e)
			}
		}
	})
	t.Run("both_lucky_boxes_single_batch_and_rollback", func(t *testing.T) {
		resolve, reward, e := s.alignedMagicBoxResolversForCommand(dnfenum.CmdPacketUseRandomboxItemExpand)
		if e != nil {
			t.Fatal(e)
		}
		for i, boxID := range []int64{2682272, 10007369} {
			character := fmt.Sprint(101 + i)
			resolution, e := resolve(boxID)
			if e != nil || resolution.OnOpen == nil {
				t.Fatalf("box %d callback %v", boxID, e)
			}
			if e = g.Character.Save(ctx, repo.CharacterRecord{CharacterID: character, AccountID: "activity-test"}); e != nil {
				t.Fatal(e)
			}
			slots := map[string]repo.ItemStack{"0:75": {ItemID: boxID, Count: 4}}
			if resolution.MaterialItemID > 0 {
				slots["0:121"] = repo.ItemStack{ItemID: resolution.MaterialItemID, Count: 100}
			}
			if e = g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: character, Slots: slots}); e != nil {
				t.Fatal(e)
			}
			owner, e := packageitem.NewOwner(g)
			if e != nil {
				t.Fatal(e)
			}
			command := packageitem.MagicBoxCommand{SelectedCharacterID: uint16(101 + i), AccountID: "activity-test", RawListType: 4, ListType: 0, SlotIndex: 75, MaterialSlotIndex: 121}
			failResolve := func(id int64) (alignedcmd.MagicBoxResolution, error) {
				r, e := resolve(id)
				r.OnOpen = func(ch *repo.CharacterRecord) (bool, error) {
					_, _ = resolution.OnOpen(ch)
					return false, activeness.ErrState
				}
				return r, e
			}
			if _, e = owner.ApplyMagicBox(ctx, command, failResolve, reward); !errors.Is(e, activeness.ErrState) {
				t.Fatalf("failed box should rollback: %v", e)
			}
			inventory, _, _ := g.Inventory.Load(ctx, character)
			ch, _, _ := g.Character.Load(ctx, character)
			if inventory.Slots["0:75"].Count != 4 || ch.Stats["private_activity_v1_points"] != 0 {
				t.Fatal("failed open leaked mutations")
			}
			result, e := owner.ApplyMagicBox(ctx, command, resolve, reward)
			if e != nil || !result.Success {
				t.Fatalf("single %d result %+v error %v", boxID, result, e)
			}
			batch, e := owner.ApplyMagicBoxExpand(ctx, packageitem.MagicBoxExpandCommand{SelectedCharacterID: command.SelectedCharacterID, AccountID: command.AccountID, RawListType: 4, ListType: 0, SlotIndex: 75, BoxItemID: uint32(boxID), MaterialSlotIndex: 121, OpenCount: 3}, resolve, reward)
			if e != nil || !batch.Success || batch.OpenCount != 3 {
				t.Fatalf("batch %+v %v", batch, e)
			}
			ch, _, _ = g.Character.Load(ctx, character)
			if ch.Stats["private_activity_v1_points"] != 10 {
				t.Fatalf("box %d stats %v", boxID, ch.Stats)
			}
		}
	})
	t.Run("owned_completion_and_stale_day", func(t *testing.T) {
		if e = s.persistPrivateActivity(ctx, "wrong", "99", 4, queue.Now(), queue.Now()); !errors.Is(e, activeness.ErrOwner) {
			t.Fatal(e)
		}
		for i := 0; i < 3; i++ {
			if e = s.persistPrivateActivity(ctx, "activity-test", "99", 4, queue.Now(), queue.Now()); e != nil {
				t.Fatal(e)
			}
		}
		st, e := s.settlePrivateActivity(ctx, session, 0, 0, 0, 0)
		if e != nil || st.Points != 120 {
			t.Fatalf("state %+v err %v", st, e)
		}
		if _, e = s.settlePrivateActivity(ctx, session, 1, st.Day, st.Fingerprint, 3); !errors.Is(e, activeness.ErrOwner) {
			t.Fatal("stale generation", e)
		}
		for _, stage := range []uint16{1, 2, 3, 1, 2, 3} {
			if _, e = s.settlePrivateActivity(ctx, session, stage, st.Day, st.Fingerprint, 4); e != nil {
				t.Fatal(e)
			}
		}
		account, _, _ := g.Account.Load(ctx, "activity-test")
		counts := cerashop.WalletBalance(account)
		if counts.HappyTokenCera != 6500 {
			t.Fatalf("wallet %+v", counts)
		}
		box, _, _ := g.Mailbox.Load(ctx, "99")
		if len(box.Mails) != 3 {
			t.Fatalf("mails %d", len(box.Mails))
		}
		countsByItem := map[int64]int64{}
		for _, m := range box.Mails {
			for _, item := range m.Attachments {
				countsByItem[item.ItemID] += item.Count
			}
		}
		for id, n := range map[int64]int64{990027401: 1, 990027402: 1, 990027403: 1, 3330: 1500} {
			if countsByItem[id] != n {
				t.Fatalf("attachments %v", countsByItem)
			}
		}
		st, e = s.settlePrivateActivity(ctx, session, 0, 0, 0, 0)
		if e != nil {
			t.Fatal(e)
		}
		wire := encodePrivateActivity(st, 99, 4, 19, 0)
		if len(wire) != 184 || binary.LittleEndian.Uint16(wire[12:]) != 120 || wire[16] != 7 || binary.LittleEndian.Uint32(wire[180:]) != 500 {
			t.Fatalf("wire %x", wire)
		}
	})
}

func TestPrivateActivityFirstHellMonsterWithoutDungeonClear(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	a, e := platformpvf.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	source := bridgeAbyssEpicFragmentPVF()
	s, session, _, runtime, _, g := prepareAbyssEpicFragmentRuntime(t, source)
	s.initialEquipmentArchive = a
	keys := waveHellPartyActorKeys(t, runtime)
	if len(keys) < 2 {
		t.Fatal("need two actors")
	}
	ctx := context.Background()
	check := func(want int64) {
		ch, _, e := g.Character.Load(ctx, "99")
		if e != nil || ch.Stats["private_activity_v1_points"] != want {
			t.Fatalf("points %v %v", ch.Stats, e)
		}
	}
	if e = s.persistPrivateActivityHellKill(ctx, session, runtime, keys[0], false); e != nil {
		t.Fatal(e)
	}
	check(0)
	if e = s.persistPrivateActivityHellKill(ctx, session, runtime, 0xffff, false); e != nil {
		t.Fatal(e)
	}
	check(0)
	killDungeonActors(t, s, session, keys[0])
	check(10)
	if currentDungeonQuestHellPartyCleared(runtime) || runtime.clearMapCompletionPhaseAPersisted {
		t.Fatal("task incorrectly requires dungeon/wave clear")
	}
	killDungeonActors(t, s, session, keys[0])
	check(10)
	killDungeonActors(t, s, session, keys[1:]...)
	check(10)
	at := runtime.privateActivityHellDeaths[keys[0]]
	if at.IsZero() {
		t.Fatal("death time missing")
	}
	q := newFakeCurrentDungeonDeathTimerQueue()
	q.now = at.Add(24 * time.Hour)
	s.gameplayTimers = q
	killDungeonActors(t, s, session, keys[0])
	st, e := s.settlePrivateActivity(ctx, session, 0, 0, 0, 0)
	if e != nil || st.Points != 0 || runtime.privateActivityHellDeaths[keys[0]] != at {
		t.Fatalf("cross-day replay %+v %v", st, e)
	}
	if id, e := s.privateActivityDungeonTask(runtime); e != nil || id == 1 {
		t.Fatal("old boss trigger still active", id, e)
	}
}

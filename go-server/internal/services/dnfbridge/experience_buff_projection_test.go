package dnfbridge

import (
	"context"
	"encoding/binary"
	"math"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/experiencebuff"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

const (
	experiencePotionProjectionTestAccount = "account-1"
	experiencePotionProjectionTestSlot    = int16(75)
	experiencePotionProjectionTestItem    = int64(7440)
)

type experiencePotionProjectionTestRow struct {
	itemID    uint32
	remaining uint32
}

type experiencePotionProjectionTestFixture struct {
	service      *Service
	session      *gameSession
	conn         *bufferConn
	repositories repo.Group
	queue        *fakeCurrentDungeonDeathTimerQueue
}

func experiencePotionProjectionTestNow() time.Time {
	return time.Date(2026, time.November, 2, 12, 0, 0, 0, time.UTC)
}

func cloneExperiencePotionProjectionStats(stats map[string]int64) map[string]int64 {
	cloned := make(map[string]int64, len(stats))
	for key, value := range stats {
		cloned[key] = value
	}
	return cloned
}

func newExperiencePotionProjectionTestFixture(
	t *testing.T,
	characterID uint16,
	generation uint64,
	stats map[string]int64,
) *experiencePotionProjectionTestFixture {
	t.Helper()
	repositories := memory.NewMemoryGroup()
	ctx := context.Background()
	if err := repositories.Character.Save(ctx, repo.CharacterRecord{
		CharacterID: strconv.Itoa(int(characterID)),
		AccountID:   experiencePotionProjectionTestAccount,
		Level:       90,
		Stats:       cloneExperiencePotionProjectionStats(stats),
	}); err != nil {
		t.Fatal(err)
	}

	service := &Service{
		options: options{
			accountID:          experiencePotionProjectionTestAccount,
			gameUpperHeader:    gameUpperHeaderChannel13,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		repositoryProvider: func() (repo.Group, bool) { return repositories, true },
	}
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = experiencePotionProjectionTestNow()
	service.gameplayTimers = queue
	conn := &bufferConn{}
	session := &gameSession{
		conn:                conn,
		connID:              t.Name(),
		accountID:           experiencePotionProjectionTestAccount,
		selectedCharacterID: characterID,
		characterGeneration: generation,
	}
	service.mu.Lock()
	service.gameSessions = map[uint16]*gameSession{characterID: session}
	service.mu.Unlock()
	return &experiencePotionProjectionTestFixture{
		service:      service,
		session:      session,
		conn:         conn,
		repositories: repositories,
		queue:        queue,
	}
}

func (f *experiencePotionProjectionTestFixture) send(t *testing.T) {
	t.Helper()
	f.conn.write.Reset()
	if err := f.service.sendCurrentExperiencePotionEffects(f.session); err != nil {
		t.Fatal(err)
	}
}

func (f *experiencePotionProjectionTestFixture) replaceCurrentStats(t *testing.T, stats map[string]int64) {
	t.Helper()
	characterID := strconv.Itoa(int(f.session.selectedCharacterID))
	character, found, err := f.repositories.Character.Load(context.Background(), characterID)
	if err != nil || !found {
		t.Fatalf("load character %s found=%t err=%v", characterID, found, err)
	}
	character.Stats = cloneExperiencePotionProjectionStats(stats)
	if err := f.repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
}

func (f *experiencePotionProjectionTestFixture) moveBinding(
	t *testing.T,
	characterID uint16,
	generation uint64,
	stats map[string]int64,
) {
	t.Helper()
	ctx := context.Background()
	key := strconv.Itoa(int(characterID))
	character, found, err := f.repositories.Character.Load(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		character = repo.CharacterRecord{
			CharacterID: key,
			AccountID:   experiencePotionProjectionTestAccount,
			Level:       90,
		}
	}
	character.Stats = cloneExperiencePotionProjectionStats(stats)
	if err := f.repositories.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}

	f.service.mu.Lock()
	delete(f.service.gameSessions, f.session.selectedCharacterID)
	f.session.selectedCharacterID = characterID
	f.session.characterGeneration = generation
	f.service.gameSessions[characterID] = f.session
	f.service.mu.Unlock()
}

func (f *experiencePotionProjectionTestFixture) reconnect(
	t *testing.T,
	characterID uint16,
	generation uint64,
) {
	t.Helper()
	if _, found, err := f.repositories.Character.Load(context.Background(), strconv.Itoa(int(characterID))); err != nil || !found {
		t.Fatalf("load reconnect character %d found=%t err=%v", characterID, found, err)
	}
	f.conn = &bufferConn{}
	f.session = &gameSession{
		conn:                f.conn,
		connID:              t.Name() + "-reconnect",
		accountID:           experiencePotionProjectionTestAccount,
		selectedCharacterID: characterID,
		characterGeneration: generation,
	}
	f.service.mu.Lock()
	f.service.gameSessions = map[uint16]*gameSession{characterID: f.session}
	f.service.mu.Unlock()
}

func experiencePotionProjectionTestPackets(t *testing.T, conn *bufferConn) []dnfproto.ChannelPacket {
	t.Helper()
	rest := append([]byte(nil), conn.write.Bytes()...)
	packets := make([]dnfproto.ChannelPacket, 0, 3)
	for len(rest) > 0 {
		packet, trailing := splitGameServerUpperPacket(t, rest)
		packets = append(packets, packet)
		if len(trailing) >= len(rest) {
			t.Fatalf("experience potion packet split made no progress: %x", rest)
		}
		rest = trailing
	}
	return packets
}

func decodeExperiencePotionProjection174(
	t *testing.T,
	packet dnfproto.ChannelPacket,
) []experiencePotionProjectionTestRow {
	t.Helper()
	if packet.Header.Classification != 0 || packet.Header.MsgID != currentExperiencePotionEffectMsgID {
		t.Fatalf("projection header = class %d/op%d, want class0/op%d",
			packet.Header.Classification, packet.Header.MsgID, currentExperiencePotionEffectMsgID)
	}
	if len(packet.Body) == 0 {
		t.Fatal("op174 body is empty")
	}
	count := int(packet.Body[0])
	if len(packet.Body) != 1+count*8 {
		t.Fatalf("op174 body length=%d count=%d, want %d", len(packet.Body), count, 1+count*8)
	}
	rows := make([]experiencePotionProjectionTestRow, 0, count)
	for i := 0; i < count; i++ {
		row := experiencePotionProjectionTestRow{
			itemID:    binary.LittleEndian.Uint32(packet.Body[1+i*8:]),
			remaining: binary.LittleEndian.Uint32(packet.Body[5+i*8:]),
		}
		if i > 0 && rows[i-1].itemID >= row.itemID {
			t.Fatalf("op174 rows are not strictly sorted: %+v then %+v", rows[i-1], row)
		}
		rows = append(rows, row)
	}
	return rows
}

func assertExperiencePotionProjectionRows(
	t *testing.T,
	got []experiencePotionProjectionTestRow,
	want ...experiencePotionProjectionTestRow,
) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("op174 rows=%+v want=%+v", got, want)
	}
}

func TestCurrentExperiencePotionProjection174UsesCountAndCeiledRelativeSeconds(t *testing.T) {
	now := experiencePotionProjectionTestNow()
	clearStarted := now.Add(-time.Second).UnixMilli()
	questStarted := now.Add(-time.Second).UnixMilli()
	f := newExperiencePotionProjectionTestFixture(t, 19, 1, map[string]int64{
		"experience_potion_clear_item_id":      7440,
		"experience_potion_clear_basis_points": 10000,
		"experience_potion_clear_started_ms":   clearStarted,
		"experience_potion_clear_until_ms":     now.Add(1500 * time.Millisecond).UnixMilli(),
		"experience_potion_quest_item_id":      10304122,
		"experience_potion_quest_basis_points": 10000,
		"experience_potion_quest_started_ms":   questStarted,
		"experience_potion_quest_until_ms":     now.Add(60 * time.Second).UnixMilli(),
	})

	f.send(t)
	packets := experiencePotionProjectionTestPackets(t, f.conn)
	if len(packets) != 1 {
		t.Fatalf("projection packet count=%d want=1", len(packets))
	}
	assertExperiencePotionProjectionRows(t, decodeExperiencePotionProjection174(t, packets[0]),
		experiencePotionProjectionTestRow{itemID: 7440, remaining: 2},
		experiencePotionProjectionTestRow{itemID: 10304122, remaining: 60},
	)
	scheduled, cancelled, active := f.queue.counts()
	if scheduled != 1 || cancelled != 0 || active != 1 {
		t.Fatalf("timer counts scheduled=%d cancelled=%d active=%d", scheduled, cancelled, active)
	}
	if task := f.queue.task(t, 0); task.delay != 1500*time.Millisecond {
		t.Fatalf("timer delay=%s want=1.5s", task.delay)
	}
}

func TestCurrentExperiencePotionProjection174SkipsExpiredFutureAndUnreasonableRows(t *testing.T) {
	now := experiencePotionProjectionTestNow()
	started := now.Add(-time.Second).UnixMilli()
	cases := []struct {
		name  string
		stats map[string]int64
	}{
		{
			name: "expired",
			stats: map[string]int64{
				"experience_potion_clear_item_id":      7440,
				"experience_potion_clear_basis_points": 10000,
				"experience_potion_clear_started_ms":   started,
				"experience_potion_clear_until_ms":     now.Add(-time.Millisecond).UnixMilli(),
			},
		},
		{
			name: "future_started",
			stats: map[string]int64{
				"experience_potion_quest_item_id":      10304122,
				"experience_potion_quest_basis_points": 10000,
				"experience_potion_quest_started_ms":   now.Add(time.Millisecond).UnixMilli(),
				"experience_potion_quest_until_ms":     now.Add(time.Minute).UnixMilli(),
			},
		},
		{
			name: "invalid_item_id",
			stats: map[string]int64{
				"experience_potion_clear_item_id":      0,
				"experience_potion_clear_basis_points": 10000,
				"experience_potion_clear_started_ms":   started,
				"experience_potion_clear_until_ms":     now.Add(time.Minute).UnixMilli(),
			},
		},
		{
			name: "invalid_basis_points",
			stats: map[string]int64{
				"experience_potion_clear_item_id":      7440,
				"experience_potion_clear_basis_points": 1000001,
				"experience_potion_clear_started_ms":   started,
				"experience_potion_clear_until_ms":     now.Add(time.Minute).UnixMilli(),
			},
		},
		{
			name: "lifetime_over_bound",
			stats: map[string]int64{
				"experience_potion_clear_item_id":      7440,
				"experience_potion_clear_basis_points": 10000,
				"experience_potion_clear_started_ms":   started,
				"experience_potion_clear_until_ms":     now.Add(30*24*time.Hour + time.Millisecond).UnixMilli(),
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			f := newExperiencePotionProjectionTestFixture(t, 19, 1, testCase.stats)
			f.send(t)
			if f.conn.write.Len() != 0 {
				t.Fatalf("invalid row projected %x", f.conn.write.Bytes())
			}
			scheduled, cancelled, active := f.queue.counts()
			if scheduled != 0 || cancelled != 0 || active != 0 {
				t.Fatalf("invalid row timer scheduled=%d cancelled=%d active=%d", scheduled, cancelled, active)
			}
		})
	}
}

func TestCurrentExperiencePotionProjectionReconnectKeepsAbsoluteRemaining(t *testing.T) {
	now := experiencePotionProjectionTestNow()
	stats := map[string]int64{
		"experience_potion_clear_item_id":      7440,
		"experience_potion_clear_basis_points": 10000,
		"experience_potion_clear_started_ms":   now.Add(-time.Second).UnixMilli(),
		"experience_potion_clear_until_ms":     now.Add(95 * time.Second).UnixMilli(),
	}
	f := newExperiencePotionProjectionTestFixture(t, 19, 1, stats)
	f.send(t)
	first := experiencePotionProjectionTestPackets(t, f.conn)
	if len(first) != 1 {
		t.Fatalf("first projection packet count=%d want=1", len(first))
	}
	assertExperiencePotionProjectionRows(t, decodeExperiencePotionProjection174(t, first[0]),
		experiencePotionProjectionTestRow{itemID: 7440, remaining: 95},
	)

	f.queue.mu.Lock()
	f.queue.now = now.Add(30 * time.Second)
	f.queue.mu.Unlock()
	f.reconnect(t, 19, 2)
	f.send(t)
	reconnected := experiencePotionProjectionTestPackets(t, f.conn)
	if len(reconnected) != 1 {
		t.Fatalf("reconnect projection packet count=%d want=1", len(reconnected))
	}
	assertExperiencePotionProjectionRows(t, decodeExperiencePotionProjection174(t, reconnected[0]),
		experiencePotionProjectionTestRow{itemID: 7440, remaining: 65},
	)
	if task := f.queue.task(t, 1); task.delay != 65*time.Second {
		t.Fatalf("reconnect timer delay=%s want=65s", task.delay)
	}
}

func TestCurrentExperiencePotionProjectionClampsNativeSecondsAndRefreshesSafetyWindow(t *testing.T) {
	now := experiencePotionProjectionTestNow()
	const lifetime = 29 * 24 * time.Hour
	maxNativeSeconds := int64(math.MaxInt32 / 1000)
	f := newExperiencePotionProjectionTestFixture(t, 19, 1, map[string]int64{
		"experience_potion_clear_item_id":      7440,
		"experience_potion_clear_basis_points": 10000,
		"experience_potion_clear_started_ms":   now.UnixMilli(),
		"experience_potion_clear_until_ms":     now.Add(lifetime).UnixMilli(),
	})

	f.send(t)
	first := experiencePotionProjectionTestPackets(t, f.conn)
	if len(first) != 1 {
		t.Fatalf("bounded projection packet count=%d want=1", len(first))
	}
	assertExperiencePotionProjectionRows(t, decodeExperiencePotionProjection174(t, first[0]),
		experiencePotionProjectionTestRow{itemID: 7440, remaining: uint32(maxNativeSeconds)},
	)
	boundedTask := f.queue.task(t, 0)
	if boundedTask.delay != time.Duration(maxNativeSeconds)*time.Second {
		t.Fatalf("bounded native timer delay=%s want=%s", boundedTask.delay, time.Duration(maxNativeSeconds)*time.Second)
	}

	boundary := now.Add(time.Duration(maxNativeSeconds) * time.Second)
	f.queue.mu.Lock()
	f.queue.now = boundary
	f.queue.mu.Unlock()
	f.conn.write.Reset()
	f.queue.fire(boundedTask, false)
	continuation := experiencePotionProjectionTestPackets(t, f.conn)
	if len(continuation) != 1 {
		t.Fatalf("continuation projection packet count=%d want=1", len(continuation))
	}
	remainingSeconds := uint32(int64(lifetime/time.Second) - maxNativeSeconds)
	assertExperiencePotionProjectionRows(t, decodeExperiencePotionProjection174(t, continuation[0]),
		experiencePotionProjectionTestRow{itemID: 7440, remaining: remainingSeconds},
	)
	remainingTask := f.queue.task(t, 1)
	if remainingTask.delay != time.Duration(remainingSeconds)*time.Second {
		t.Fatalf("remaining segment timer delay=%s want=%s", remainingTask.delay, time.Duration(remainingSeconds)*time.Second)
	}

	f.queue.mu.Lock()
	f.queue.now = now.Add(lifetime)
	f.queue.mu.Unlock()
	f.conn.write.Reset()
	f.queue.fire(remainingTask, false)
	expired := experiencePotionProjectionTestPackets(t, f.conn)
	if len(expired) != 1 {
		t.Fatalf("final expiry packet count=%d want=1", len(expired))
	}
	assertExperiencePotionProjectionRows(t, decodeExperiencePotionProjection174(t, expired[0]),
		experiencePotionProjectionTestRow{itemID: 7440, remaining: 0},
	)
	_, _, active := f.queue.counts()
	if active != 0 {
		t.Fatalf("expired native safety-window timer remains active=%d", active)
	}
}

func TestCurrentExperiencePotionProjectionFamilySwapSendsOldZeroThenNewRemaining(t *testing.T) {
	now := experiencePotionProjectionTestNow()
	f := newExperiencePotionProjectionTestFixture(t, 19, 1, map[string]int64{
		"experience_potion_clear_item_id":      7440,
		"experience_potion_clear_basis_points": 10000,
		"experience_potion_clear_started_ms":   now.Add(-time.Second).UnixMilli(),
		"experience_potion_clear_until_ms":     now.Add(70 * time.Second).UnixMilli(),
	})
	f.send(t)
	first := experiencePotionProjectionTestPackets(t, f.conn)
	if len(first) != 1 {
		t.Fatalf("first projection packet count=%d want=1", len(first))
	}
	assertExperiencePotionProjectionRows(t, decodeExperiencePotionProjection174(t, first[0]),
		experiencePotionProjectionTestRow{itemID: 7440, remaining: 70},
	)

	f.replaceCurrentStats(t, map[string]int64{
		"experience_potion_clear_item_id":      490009951,
		"experience_potion_clear_basis_points": 40000,
		"experience_potion_clear_started_ms":   now.UnixMilli(),
		"experience_potion_clear_until_ms":     now.Add(20 * time.Second).UnixMilli(),
	})
	f.send(t)
	second := experiencePotionProjectionTestPackets(t, f.conn)
	if len(second) != 1 {
		t.Fatalf("replacement projection packet count=%d want=1", len(second))
	}
	assertExperiencePotionProjectionRows(t, decodeExperiencePotionProjection174(t, second[0]),
		experiencePotionProjectionTestRow{itemID: 7440, remaining: 0},
		experiencePotionProjectionTestRow{itemID: 490009951, remaining: 20},
	)
	if _, stillOld := f.session.experiencePotionEffects.items[7440]; stillOld {
		t.Fatal("old family item remains in the current projection")
	}
	if _, current := f.session.experiencePotionEffects.items[490009951]; !current {
		t.Fatal("new family item missing from the current projection")
	}
}

func TestCurrentExperiencePotionProjectionGenerationChangeDropsTombstoneAndStaleTimer(t *testing.T) {
	now := experiencePotionProjectionTestNow()
	for _, testCase := range []struct {
		name        string
		characterID uint16
	}{
		{name: "same_character_new_generation", characterID: 19},
		{name: "new_character_new_generation", characterID: 20},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			f := newExperiencePotionProjectionTestFixture(t, 19, 1, map[string]int64{
				"experience_potion_clear_item_id":      7440,
				"experience_potion_clear_basis_points": 10000,
				"experience_potion_clear_started_ms":   now.Add(-time.Second).UnixMilli(),
				"experience_potion_clear_until_ms":     now.Add(time.Minute).UnixMilli(),
			})
			f.send(t)
			if packets := experiencePotionProjectionTestPackets(t, f.conn); len(packets) != 1 {
				t.Fatalf("old generation packet count=%d want=1", len(packets))
			}
			staleTask := f.queue.task(t, 0)

			f.moveBinding(t, testCase.characterID, 2, map[string]int64{
				"experience_potion_quest_item_id":      10304122,
				"experience_potion_quest_basis_points": 1000,
				"experience_potion_quest_started_ms":   now.UnixMilli(),
				"experience_potion_quest_until_ms":     now.Add(45 * time.Second).UnixMilli(),
			})
			f.send(t)
			packets := experiencePotionProjectionTestPackets(t, f.conn)
			if len(packets) != 1 {
				t.Fatalf("new generation packet count=%d want=1", len(packets))
			}
			assertExperiencePotionProjectionRows(t, decodeExperiencePotionProjection174(t, packets[0]),
				experiencePotionProjectionTestRow{itemID: 10304122, remaining: 45},
			)

			scheduledBefore, cancelledBefore, activeBefore := f.queue.counts()
			f.conn.write.Reset()
			staleTask.callback(f.queue.Now())
			if f.conn.write.Len() != 0 {
				t.Fatalf("stale timer wrote into new generation: %x", f.conn.write.Bytes())
			}
			scheduledAfter, cancelledAfter, activeAfter := f.queue.counts()
			if scheduledBefore != scheduledAfter || cancelledBefore != cancelledAfter || activeBefore != activeAfter {
				t.Fatalf("stale timer changed queue counts: before=%d/%d/%d after=%d/%d/%d",
					scheduledBefore, cancelledBefore, activeBefore, scheduledAfter, cancelledAfter, activeAfter)
			}
			if _, staleTombstone := f.session.experiencePotionEffects.items[7440]; staleTombstone {
				t.Fatal("new generation retained the old family tombstone")
			}
		})
	}
}

func TestCurrentExperiencePotionProjectionRenewalRejectsLateCallback(t *testing.T) {
	now := experiencePotionProjectionTestNow()
	stats := map[string]int64{
		"experience_potion_clear_item_id":      7440,
		"experience_potion_clear_basis_points": 10000,
		"experience_potion_clear_started_ms":   now.Add(-time.Second).UnixMilli(),
		"experience_potion_clear_until_ms":     now.Add(time.Minute).UnixMilli(),
	}
	f := newExperiencePotionProjectionTestFixture(t, 19, 1, stats)
	f.send(t)
	old := f.queue.task(t, 0)
	stats["experience_potion_clear_until_ms"] = now.Add(10 * time.Minute).UnixMilli()
	f.replaceCurrentStats(t, stats)
	f.send(t)
	renewed := f.queue.task(t, 1)
	if old.name == renewed.name {
		t.Fatal("renewed projection shares the stale retry timer key")
	}
	f.conn.write.Reset()
	scheduledBefore, cancelledBefore, activeBefore := f.queue.counts()
	// A callback already dispatched when Cancel ran can still arrive later.
	old.callback(f.queue.Now())
	scheduled, cancelled, active := f.queue.counts()
	if f.conn.write.Len() != 0 || scheduled != scheduledBefore || cancelled != cancelledBefore || active != activeBefore {
		t.Fatalf("stale same-generation callback changed renewed projection: bytes=%d timers=%d/%d/%d", f.conn.write.Len(), scheduled, cancelled, active)
	}
	// A retry that was being scheduled concurrently also has an old key.
	if err := f.queue.ScheduleAfter(old.name, time.Second, old.callback); err != nil {
		t.Fatal(err)
	}
	f.queue.fire(f.queue.task(t, 2), false)
	if renewed.cancelled || f.session.experiencePotionEffects.timerName != renewed.name || f.conn.write.Len() != 0 {
		t.Fatal("late retry replaced the active renewal")
	}
	f.queue.mu.Lock()
	f.queue.now = now.Add(10 * time.Minute)
	f.queue.mu.Unlock()
	f.queue.fire(renewed, false)
	packets := experiencePotionProjectionTestPackets(t, f.conn)
	if len(packets) != 1 {
		t.Fatalf("renewed expiry packets=%d", len(packets))
	}
	assertExperiencePotionProjectionRows(t, decodeExperiencePotionProjection174(t, packets[0]), experiencePotionProjectionTestRow{itemID: 7440, remaining: 0})
}

func TestCurrentExperiencePotionProjectionRealChannelReconnectRestoresRemaining(t *testing.T) {
	f := newChannelReconnectPresenceFixture(t)
	now := experiencePotionProjectionTestNow()
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = now
	f.service.gameplayTimers = queue
	c, found, err := f.repositories.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatalf("character found=%t err=%v", found, err)
	}
	c.Stats["experience_potion_clear_item_id"] = 7440
	c.Stats["experience_potion_clear_basis_points"] = 10000
	c.Stats["experience_potion_clear_started_ms"] = now.Add(-5 * time.Minute).UnixMilli()
	c.Stats["experience_potion_clear_until_ms"] = now.Add(10 * time.Minute).UnixMilli()
	if err := f.repositories.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	runChannelReconnectTownEntry(t, f, f.first, channelReconnectPresenceFirstCharacterID)
	count := 0
	for _, packet := range experiencePotionProjectionTestPackets(t, f.first.conn.(*bufferConn)) {
		if packet.Header.Classification == 0 && packet.Header.MsgID == currentExperiencePotionEffectMsgID {
			count++
			assertExperiencePotionProjectionRows(t, decodeExperiencePotionProjection174(t, packet), experiencePotionProjectionTestRow{itemID: 7440, remaining: 600})
		}
	}
	if count != 1 {
		t.Fatalf("channel reconnect effect packets=%d want=1", count)
	}
	after, found, err := f.repositories.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatalf("reload found=%t err=%v", found, err)
	}
	for _, key := range []string{"item_id", "basis_points", "started_ms", "until_ms"} {
		key = "experience_potion_clear_" + key
		if c.Stats[key] != after.Stats[key] {
			t.Fatalf("reconnect modified potion ledger %s", key)
		}
	}
}

type experiencePotionOrdinaryUseTestFixture struct {
	service      *Service
	session      *gameSession
	conn         *bufferConn
	repositories repo.Group
	queue        *fakeCurrentDungeonDeathTimerQueue
}

func newExperiencePotionOrdinaryUseTestFixture(t *testing.T) *experiencePotionOrdinaryUseTestFixture {
	t.Helper()
	const pvfPath = "stackable/experience/clear.stk"
	source := dungeonDropCatalogTestSource{
		"monster/monster.lst":     "",
		"equipment/equipment.lst": "",
		"stackable/stackable.lst": "7440 `experience/clear.stk`\n",
		pvfPath: "[name]\n`synthetic clear experience potion`\n" +
			"[stackable type]\n`[waste]` 0\n" +
			"[stack limit]\n100\n" +
			"[exp bonus rate]\n1.00\n" +
			"[stat change duration]\n60000 `myself`\n" +
			"[cool time]\n60000\n" +
			"[minimum level]\n1\n" +
			"[usable job]\n`[all]`\n[/usable job]\n",
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	repositories := memory.NewMemoryGroup()
	ctx := context.Background()
	if err := repositories.Character.Save(ctx, repo.CharacterRecord{
		CharacterID: "19",
		AccountID:   experiencePotionProjectionTestAccount,
		Level:       90,
		Stats:       make(map[string]int64),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, repo.InventoryRecord{
		CharacterID: "19",
		Slots: map[string]repo.ItemStack{
			"0:75": {
				ItemID: experiencePotionProjectionTestItem,
				Count:  2,
				Extra: map[string]string{
					"item_kind":      "stackable",
					"pvf_path":       pvfPath,
					"stackable_type": "[waste]",
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options: options{
			accountID:            experiencePotionProjectionTestAccount,
			currentClientVersion: "official-24.9",
			gameUpperHeader:      gameUpperHeaderChannel13,
			gameUpperBodyCodec:   gameUpperBodyCodecPlain,
		},
		pvfItemCatalog:     catalog,
		repositoryProvider: func() (repo.Group, bool) { return repositories, true },
	}
	queue := newFakeCurrentDungeonDeathTimerQueue()
	service.gameplayTimers = queue
	conn := &bufferConn{}
	session := &gameSession{
		conn:                conn,
		connID:              t.Name(),
		accountID:           experiencePotionProjectionTestAccount,
		selectedCharacterID: 19,
		characterGeneration: 1,
	}
	service.mu.Lock()
	service.gameSessions = map[uint16]*gameSession{19: session}
	service.mu.Unlock()
	return &experiencePotionOrdinaryUseTestFixture{
		service:      service,
		session:      session,
		conn:         conn,
		repositories: repositories,
		queue:        queue,
	}
}

func TestCurrentExperiencePotionOrdinaryUseStackableAckRefreshProjectionAndDebit(t *testing.T) {
	f := newExperiencePotionOrdinaryUseTestFixture(t)
	body := make([]byte, 15)
	binary.LittleEndian.PutUint16(body[0:2], uint16(experiencePotionProjectionTestSlot))
	binary.LittleEndian.PutUint32(body[3:7], 4242)
	binary.LittleEndian.PutUint32(body[7:11], uint32(experiencePotionProjectionTestItem))

	if err := f.service.handleGameCommand(
		f.session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketUseStackable),
		body,
	); err != nil {
		t.Fatal(err)
	}
	packets := experiencePotionProjectionTestPackets(t, f.conn)
	if len(packets) != 3 {
		t.Fatalf("ordinary potion packet count=%d want=3", len(packets))
	}
	if packets[0].Header.Classification != 1 || packets[0].Header.MsgID != uint16(dnfenum.CmdPacketUseStackable) {
		t.Fatalf("ordinary potion ACK header=class%d/op%d", packets[0].Header.Classification, packets[0].Header.MsgID)
	}
	ack := packets[0].Body
	if len(ack) != 12 || ack[0] != 1 ||
		binary.LittleEndian.Uint16(ack[1:3]) != uint16(experiencePotionProjectionTestSlot) ||
		ack[3] != 0 || binary.LittleEndian.Uint32(ack[4:8]) != 4242 ||
		binary.LittleEndian.Uint32(ack[8:12]) != uint32(experiencePotionProjectionTestItem) {
		t.Fatalf("ordinary potion ACK body=%x", ack)
	}
	if packets[1].Header.Classification != 0 || packets[1].Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) {
		t.Fatalf("ordinary potion refresh header=class%d/op%d", packets[1].Header.Classification, packets[1].Header.MsgID)
	}
	assertExperiencePotionProjectionRows(t, decodeExperiencePotionProjection174(t, packets[2]),
		experiencePotionProjectionTestRow{itemID: uint32(experiencePotionProjectionTestItem), remaining: 60},
	)

	character, found, err := f.repositories.Character.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	now := f.queue.Now()
	if character.Stats["experience_potion_clear_item_id"] != experiencePotionProjectionTestItem ||
		character.Stats["experience_potion_clear_basis_points"] != 10000 ||
		character.Stats["experience_potion_clear_started_ms"] != now.UnixMilli() ||
		character.Stats["experience_potion_clear_until_ms"] != now.Add(time.Minute).UnixMilli() ||
		character.Stats["experience_potion_clear_cooldown_until_ms"] != now.Add(time.Minute).UnixMilli() {
		t.Fatalf("persisted experience ledger=%v", character.Stats)
	}
	if got := experiencebuff.Bonus(character, experiencebuff.Clear, 1000, now); got != 1000 {
		t.Fatalf("persisted clear bonus=%d want=1000", got)
	}
	inventory, found, err := f.repositories.Inventory.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if stack := inventory.Slots["0:75"]; stack.ItemID != experiencePotionProjectionTestItem || stack.Count != 1 {
		t.Fatalf("ordinary potion source stack=%+v want item=%d count=1", stack, experiencePotionProjectionTestItem)
	}

	beforeStats := cloneExperiencePotionProjectionStats(character.Stats)
	f.conn.write.Reset()
	if err := f.service.handleGameCommand(
		f.session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketUseStackable),
		body,
	); err != nil {
		t.Fatal(err)
	}
	character, _, err = f.repositories.Character.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	inventory, _, err = f.repositories.Inventory.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	if stack := inventory.Slots["0:75"]; stack.Count != 1 {
		t.Fatalf("duplicate ordinary potion request debited again: %+v", stack)
	}
	if !reflect.DeepEqual(beforeStats, character.Stats) {
		t.Fatalf("duplicate ordinary potion request changed ledger: before=%v after=%v", beforeStats, character.Stats)
	}
}

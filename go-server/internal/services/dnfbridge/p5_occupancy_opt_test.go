package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	oe "longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestP5ArchiveCatalogCacheCoalescingCancelArchiveSwapAndErrorRetry(t *testing.T) {
	a1 := &platformpvf.Archive{}
	a2 := &platformpvf.Archive{}

	t.Run("concurrent_cold_coalesces_into_single_load", func(t *testing.T) {
		var cache archiveCatalogCache[int]
		var calls atomic.Int32
		started := make(chan struct{})
		release := make(chan struct{})

		const workers = 24
		var wg sync.WaitGroup
		errs := make(chan error, workers)
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				v, err := cache.get(context.Background(), a1, func(ctx context.Context, a *platformpvf.Archive) (int, error) {
					if calls.Add(1) == 1 {
						close(started)
					}
					<-release
					return 42, nil
				})
				if err != nil {
					errs <- err
					return
				}
				if v != 42 {
					errs <- errors.New("unexpected value")
				}
			}()
		}
		<-started
		time.Sleep(15 * time.Millisecond)
		close(release)
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
		if got := calls.Load(); got != 1 {
			t.Fatalf("expected 1 coalesced cold load, got %d", got)
		}
		// Warm hit must not call loader.
		v, err := cache.get(context.Background(), a1, func(ctx context.Context, a *platformpvf.Archive) (int, error) {
			calls.Add(1)
			return 99, nil
		})
		if err != nil || v != 42 || calls.Load() != 1 {
			t.Fatalf("warm hit failed: v=%d err=%v calls=%d", v, err, calls.Load())
		}
	})

	t.Run("leader_cancel_does_not_cache_and_healthy_waiter_succeeds", func(t *testing.T) {
		var cache archiveCatalogCache[int]
		var calls atomic.Int32
		leaderStarted := make(chan struct{})
		leaderCtx, cancelLeader := context.WithCancel(context.Background())

		loader := func(ctx context.Context, a *platformpvf.Archive) (int, error) {
			n := calls.Add(1)
			if n == 1 {
				close(leaderStarted)
				<-ctx.Done()
				return 0, ctx.Err()
			}
			return 77, nil
		}

		var leaderErr error
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, leaderErr = cache.get(leaderCtx, a1, loader)
		}()

		<-leaderStarted
		waiterDone := make(chan error, 1)
		go func() {
			v, err := cache.get(context.Background(), a1, loader)
			if err != nil {
				waiterDone <- err
				return
			}
			if v != 77 {
				waiterDone <- errors.New("wrong waiter value")
				return
			}
			waiterDone <- nil
		}()

		time.Sleep(15 * time.Millisecond)
		cancelLeader()
		wg.Wait()
		if !errors.Is(leaderErr, context.Canceled) {
			t.Fatalf("expected leader context.Canceled, got %v", leaderErr)
		}
		if err := <-waiterDone; err != nil {
			t.Fatalf("healthy waiter failed after leader cancel: %v", err)
		}
		if calls.Load() != 2 {
			t.Fatalf("expected 2 calls (canceled leader + healthy retry), got %d", calls.Load())
		}
	})

	t.Run("corrupted_error_not_cached_and_archive_swap_reloads", func(t *testing.T) {
		var cache archiveCatalogCache[string]
		var calls atomic.Int32
		failErr := errors.New("corrupt pvf")
		_, err := cache.get(context.Background(), a1, func(ctx context.Context, a *platformpvf.Archive) (string, error) {
			calls.Add(1)
			return "", failErr
		})
		if !errors.Is(err, failErr) {
			t.Fatalf("expected %v, got %v", failErr, err)
		}
		// Next request retries and succeeds.
		v1, err := cache.get(context.Background(), a1, func(ctx context.Context, a *platformpvf.Archive) (string, error) {
			calls.Add(1)
			return "from-a1", nil
		})
		if err != nil || v1 != "from-a1" || calls.Load() != 2 {
			t.Fatalf("retry failed: v1=%q err=%v calls=%d", v1, err, calls.Load())
		}
		// Swapping archive pointer (even if path were identical) invalidates cache.
		v2, err := cache.get(context.Background(), a2, func(ctx context.Context, a *platformpvf.Archive) (string, error) {
			calls.Add(1)
			return "from-a2", nil
		})
		if err != nil || v2 != "from-a2" || calls.Load() != 3 {
			t.Fatalf("archive swap reload failed: v2=%q err=%v calls=%d", v2, err, calls.Load())
		}
	})
}

func TestP5ReturneeGiftRulesCacheIsolationAndArchiveSwap(t *testing.T) {
	a1 := &platformpvf.Archive{}
	a2 := &platformpvf.Archive{}
	var cache returneeGiftRulesCache
	var calls atomic.Int32

	loader := func(a *platformpvf.Archive, npc uint32) (oe.ReturneeGiftRules, error) {
		calls.Add(1)
		base := int64(10)
		if a == a2 {
			base = 20
		}
		return oe.ReturneeGiftRules{
			NPC:       npc,
			Level:     20,
			Count:     100,
			Limit:     5,
			MaxPoints: 40000,
			Points:    map[int64][2]int64{1001: {base, base + 5}},
		}, nil
	}

	r1, err := cache.get(context.Background(), a1, 2, loader)
	if err != nil || calls.Load() != 1 {
		t.Fatalf("cold npc=2 failed: %+v %v", r1, err)
	}
	// Mutate caller's Points map; cached entry must stay isolated.
	r1.Points[1001] = [2]int64{999, 999}
	r1.Points[9999] = [2]int64{1, 2}

	r1Again, err := cache.get(context.Background(), a1, 2, loader)
	if err != nil || calls.Load() != 1 {
		t.Fatalf("warm npc=2 failed: %+v %v calls=%d", r1Again, err, calls.Load())
	}
	if r1Again.Points[1001] != [2]int64{10, 15} || len(r1Again.Points) != 1 {
		t.Fatalf("Points map mutation polluted cache: %+v", r1Again.Points)
	}

	// Different NPC loads independently without evicting NPC 2.
	r3, err := cache.get(context.Background(), a1, 3, loader)
	if err != nil || r3.NPC != 3 || calls.Load() != 2 {
		t.Fatalf("npc=3 load failed: %+v %v calls=%d", r3, err, calls.Load())
	}
	if _, err := cache.get(context.Background(), a1, 2, loader); err != nil || calls.Load() != 2 {
		t.Fatalf("npc=2 evicted unexpectedly: calls=%d", calls.Load())
	}

	// Archive swap drops old archive entries and reloads.
	rSwap, err := cache.get(context.Background(), a2, 2, loader)
	if err != nil || calls.Load() != 3 || rSwap.Points[1001] != [2]int64{20, 25} {
		t.Fatalf("archive swap failed: %+v %v calls=%d", rSwap, err, calls.Load())
	}
}

func TestP5CloneEpicCatalogDeepIsolatesNestedSlices(t *testing.T) {
	orig := &oe.EpicCatalog{
		Thresholds: [3]uint32{1800, 9000, 18000},
		Rewards:    [3]oe.AttendancePVFItemReward{{ItemID: 1, Count: 1}, {ItemID: 2, Count: 1}, {ItemID: 3, Count: 1}},
		EntryAlternatives: map[int64][][]oe.AttendancePVFItemReward{
			6063: {
				{{ItemID: 490010214, Count: 1}},
				{{ItemID: 3330, Count: 30}},
			},
		},
	}
	cloned := cloneEpicCatalog(orig)
	cloned.Rewards[0].ItemID = 999
	cloned.EntryAlternatives[6063][0][0].Count = 999
	cloned.EntryAlternatives[6063][1] = nil
	cloned.EntryAlternatives[9999] = nil

	if orig.Rewards[0].ItemID != 1 {
		t.Fatal("Rewards mutated on original")
	}
	if orig.EntryAlternatives[6063][0][0].Count != 1 || len(orig.EntryAlternatives[6063][1]) != 1 || len(orig.EntryAlternatives) != 1 {
		t.Fatalf("EntryAlternatives nested slice/map mutation leaked: %+v", orig.EntryAlternatives)
	}
}

type p5NarrowCharRepoSpy struct {
	repo.CharacterRepository
	fullLoads   atomic.Int32
	narrowLoads atomic.Int32
	lastKeys    []string
	mu          sync.Mutex
}

func (r *p5NarrowCharRepoSpy) Load(ctx context.Context, id string) (repo.CharacterRecord, bool, error) {
	r.fullLoads.Add(1)
	return r.CharacterRepository.Load(ctx, id)
}

func (r *p5NarrowCharRepoSpy) LoadCharacterExtraStats(ctx context.Context, id string, keys ...string) (repo.CharacterMirrorStats, bool, error) {
	r.narrowLoads.Add(1)
	r.mu.Lock()
	r.lastKeys = append([]string(nil), keys...)
	r.mu.Unlock()
	return repo.LoadCharacterExtraStats(ctx, r.CharacterRepository, id, keys...)
}

func TestP5SemesterAndEpicSnapshotsUseNarrowExtraStatsRead(t *testing.T) {
	t.Run("semester_send_uses_narrow_read_and_preserves_toggle_and_bytes", func(t *testing.T) {
		s, session, conn, campaign := seedSemesterAdapter(t)
		enableSemesterRoute(t, s, true)
		g, _ := s.repositoryGroup()
		spy := &p5NarrowCharRepoSpy{CharacterRepository: g.Character}
		g.Character = spy
		s.repositoryProvider = func() (repo.Group, bool) { return g, true }

		// Seed qualified days so NOTI654 body is non-trivial.
		ch, _, _ := spy.CharacterRepository.Load(context.Background(), "1")
		ch.Level = 90
		k := oe.SemesterJournalKey(campaign)
		if ch.Stats == nil {
			ch.Stats = map[string]int64{}
		}
		ch.Stats[k+":day_bitmap"] = 0xFF
		ch.Stats[k+":qualified_days"] = 8
		ch.Stats[k+":version"] = 1
		if err := spy.CharacterRepository.Save(context.Background(), ch); err != nil {
			t.Fatal(err)
		}
		wantState, err := oe.SemesterCharacterSnapshot(ch, campaign, s.gameplayNow())
		if err != nil {
			t.Fatal(err)
		}
		wantBody, err := oe.BuildSemesterNotification(wantState)
		if err != nil {
			t.Fatal(err)
		}

		identity, ok := s.boundGameSessionCharacterSnapshot(session)
		if !ok {
			t.Fatal("missing bound character")
		}
		conn.write.Reset()
		if err := s.sendCurrentSemesterCumulative(session, identity); err != nil {
			t.Fatalf("sendCurrentSemesterCumulative failed: %v", err)
		}
		if spy.fullLoads.Load() != 0 {
			t.Fatalf("expected 0 full Character.Load calls on semester projection, got %d", spy.fullLoads.Load())
		}
		if spy.narrowLoads.Load() != 1 {
			t.Fatalf("expected 1 LoadCharacterExtraStats call, got %d", spy.narrowLoads.Load())
		}
		pkt, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
		if pkt.Header.MsgID != 654 || !bytes.Equal(pkt.Body, wantBody) {
			t.Fatalf("NOTI654 mismatch: got msg=%d body=%x want=%x", pkt.Header.MsgID, pkt.Body, wantBody)
		}

		// Dynamic OFF of activity 216 still disables immediately even with warm catalog cache.
		enableSemesterRoute(t, s, false)
		if err := s.sendCurrentSemesterCumulative(session, identity); err == nil {
			t.Fatal("expected error after disabling activity 216")
		}
	})

	t.Run("epic_send_uses_narrow_read_and_preserves_18000_clamp", func(t *testing.T) {
		s, session, conn, campaign := seedEpicBridge(t)
		g, _ := s.repositoryGroup()
		// Add invitations above 18000 to verify display clamp parity.
		ch, _, _ := g.Character.Load(context.Background(), "1")
		ch.Stats[campaign.Key()+":invitations"] = 25000
		if err := g.Character.Save(context.Background(), ch); err != nil {
			t.Fatal(err)
		}

		spy := &p5NarrowCharRepoSpy{CharacterRepository: g.Character}
		g.Character = spy
		s.repositoryProvider = func() (repo.Group, bool) { return g, true }

		conn.write.Reset()
		if err := s.sendCurrentEpicState(session); err != nil {
			t.Fatalf("sendCurrentEpicState failed: %v", err)
		}
		if spy.narrowLoads.Load() != 1 {
			t.Fatalf("expected 1 LoadCharacterExtraStats call on sendCurrentEpicState, got %d", spy.narrowLoads.Load())
		}
		pkt, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
		if pkt.Header.MsgID != 1455 || len(pkt.Body) != 8 || binary.LittleEndian.Uint32(pkt.Body) != 18000 {
			t.Fatalf("NOTI1455 mismatch: msg=%d body=%x", pkt.Header.MsgID, pkt.Body)
		}
	})
}

func TestP5RealPVFCatalogCacheAndAllocationCollapse(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	a1, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{options: options{currentClientVersion: "official-24.9"}}
	s.initialEquipmentArchive = a1

	// Concurrent cold start across 32 goroutines for both Semester & Epic catalogs.
	const workers = 32
	var wg sync.WaitGroup
	errs := make(chan error, workers*2)
	for i := 0; i < workers; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			cat, err := s.currentSemesterCumulativeCatalog(context.Background())
			if err != nil {
				errs <- err
				return
			}
			if len(cat.Rewards) == 0 || cat.Rewards[0].ItemID == 0 {
				errs <- errors.New("semester reward mismatch")
			}
		}()
		go func() {
			defer wg.Done()
			cat, err := s.currentEpicCatalog(context.Background())
			if err != nil {
				errs <- err
				return
			}
			if cat.Thresholds != [3]uint32{1800, 9000, 18000} {
				errs <- errors.New("epic threshold mismatch")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	// Verify warm allocation count is tiny (vs ~75-80 MiB uncached stackable.lst parse).
	semAllocs := testing.AllocsPerRun(50, func() {
		cat, err := s.currentSemesterCumulativeCatalog(context.Background())
		if err != nil || cat == nil {
			t.Fatal(err)
		}
	})
	epicAllocs := testing.AllocsPerRun(50, func() {
		cat, err := s.currentEpicCatalog(context.Background())
		if err != nil || cat == nil {
			t.Fatal(err)
		}
	})
	// Returnee gift rules cold + warm + isolation on real PVF (NPC 2).
	giftCold, err := s.currentReturneeGiftRules(context.Background(), a1, 2)
	if err != nil || giftCold.Level <= 0 || len(giftCold.Points) == 0 {
		t.Fatalf("real pvf returnee gift cold failed: %+v %v", giftCold, err)
	}
	giftCold.Points[999999] = [2]int64{1, 2}
	giftAllocs := testing.AllocsPerRun(50, func() {
		r, err := s.currentReturneeGiftRules(context.Background(), a1, 2)
		if err != nil || len(r.Points) != len(giftCold.Points)-1 || r.Points[999999] != ([2]int64{}) {
			t.Fatalf("real pvf returnee gift warm failed: %+v %v", r, err)
		}
	})
	t.Logf("Real PVF warm allocs/run: semester=%.1f epic=%.1f returneeGift=%.1f", semAllocs, epicAllocs, giftAllocs)
	if semAllocs > 10 || epicAllocs > 50 || giftAllocs > 20 {
		t.Fatalf("warm cache allocs unexpectedly high: sem=%.1f epic=%.1f gift=%.1f", semAllocs, epicAllocs, giftAllocs)
	}

	// Swapping archive pointer (same file path, new *Archive instance) reloads cleanly.
	s.initialEquipmentMu.Lock()
	s.initialEquipmentArchive = a2
	s.initialEquipmentMu.Unlock()
	if cat, err := s.currentSemesterCumulativeCatalog(context.Background()); err != nil || cat == nil {
		t.Fatalf("archive swap semester reload failed: %v", err)
	}
	if cat, err := s.currentEpicCatalog(context.Background()); err != nil || cat == nil {
		t.Fatalf("archive swap epic reload failed: %v", err)
	}
}

func BenchmarkP5SemesterCatalogRealPVF(b *testing.B) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		b.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		b.Fatal(err)
	}
	b.Run("uncached_baseline", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := oe.LoadSemesterCatalog(context.Background(), archive); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("cached_warm", func(b *testing.B) {
		s := &Service{options: options{currentClientVersion: "official-24.9"}}
		s.initialEquipmentArchive = archive
		if _, err := s.currentSemesterCumulativeCatalog(context.Background()); err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := s.currentSemesterCumulativeCatalog(context.Background()); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkP5EpicCatalogRealPVF(b *testing.B) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		b.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		b.Fatal(err)
	}
	b.Run("uncached_baseline", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := validateCurrentEpicRoadActivity(archive); err != nil {
				b.Fatal(err)
			}
			if _, err := oe.LoadEpicCatalog(context.Background(), archive); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("cached_warm", func(b *testing.B) {
		s := &Service{options: options{currentClientVersion: "official-24.9"}}
		s.initialEquipmentArchive = archive
		if _, err := s.currentEpicCatalog(context.Background()); err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := s.currentEpicCatalog(context.Background()); err != nil {
				b.Fatal(err)
			}
		}
	})
}

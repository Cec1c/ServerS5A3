package dnfbridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	repomysql "longheng.io/server/internal/modules/dnf/repository/mysql"
)

type guildLoadSQL struct {
	repomysql.SQLDBAdapter
	reads, writes atomic.Int64
}

func (d *guildLoadSQL) QueryContext(ctx context.Context, q string, args ...any) (repomysql.SQLRows, error) {
	d.reads.Add(1)
	return d.SQLDBAdapter.QueryContext(ctx, q, args...)
}
func (d *guildLoadSQL) QueryRowContext(ctx context.Context, q string, args ...any) repomysql.SQLRow {
	d.reads.Add(1)
	return d.SQLDBAdapter.QueryRowContext(ctx, q, args...)
}
func (d *guildLoadSQL) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	d.writes.Add(1)
	return d.SQLDBAdapter.ExecContext(ctx, q, args...)
}

type guildLoadSamples struct {
	mu      sync.Mutex
	samples []time.Duration
	errors  int
}

func (r *guildLoadSamples) add(d time.Duration, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.samples = append(r.samples, d)
	if err != nil {
		r.errors++
	}
}
func (r *guildLoadSamples) snapshot() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := append([]time.Duration(nil), r.samples...)
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	quantile := func(p int) float64 {
		if len(v) == 0 {
			return 0
		}
		return float64(v[min(len(v)-1, (len(v)*p+99)/100-1)]) / float64(time.Millisecond)
	}
	return map[string]any{"count": len(v), "errors": r.errors, "p50_ms": quantile(50), "p95_ms": quantile(95), "p99_ms": quantile(99), "max_ms": quantile(100)}
}

// This reproducible load isolates the changed roster/queue path. Probes measure
// scheduling, not client rendering or op39 asset transactions (covered separately).
// Set DNFBRIDGE_GUILD_LOAD_SECONDS=600 for four 150s ABBA phases on a disposable DB.
func TestGuildRosterMixedLoadMySQL(t *testing.T) {
	seconds, _ := strconv.Atoi(os.Getenv("DNFBRIDGE_GUILD_LOAD_SECONDS"))
	if seconds == 0 {
		t.Skip("opt-in 24-session disposable SQL load")
	}
	if seconds < 12 || seconds > 1800 {
		t.Fatal("load duration must be 12..1800 seconds")
	}
	waves := 20
	if seconds < 600 {
		waves = 2 // smoke checks exercise the same path without a multi-minute legacy backlog
	}
	dsn := os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN")
	if dsn == "" {
		t.Fatal("isolated DB is required for requested load")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid isolated DSN")
	}
	if cfg.Net != "tcp" || len(cfg.Addr) < 10 || cfg.Addr[:10] != "127.0.0.1:" {
		t.Fatal("load requires loopback disposable DB")
	}
	cfg.DBName = ""
	cfg.ParseTime = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open isolated DB")
	}
	defer db.Close()
	db.SetMaxOpenConns(32)
	db.SetMaxIdleConns(24)
	name := fmt.Sprintf("guild_roster_load_%d", time.Now().UnixNano())
	plan := dnfrepo.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	ctx := context.Background()
	if err := repomysql.EnsureMySQLSchema(ctx, db, repomysql.SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, "DROP DATABASE `"+name+"`"); err != nil {
			t.Error(err)
		}
	}()
	counted := &guildLoadSQL{SQLDBAdapter: repomysql.SQLDBAdapter{DB: db}}
	group, err := repomysql.NewMySQLGroup(counted, repomysql.MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	seedService, _, seed := guildRosterLargeFixture(t, 24)
	for i := 1; i <= 190; i++ {
		c, _, e := seed.Character.Load(ctx, strconv.Itoa(i))
		if e != nil {
			t.Fatal(e)
		}
		for j := 0; j < 32; j++ {
			c.Stats[fmt.Sprint("load_extra_", j)] = int64(j)
		}
		if err := group.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := dnfguild.Load(ctx, seed.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := dnfguild.Save(ctx, group.Settings, registry, seedService.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	for phase, legacy := range []bool{true, false, false, true} {
		s, peers, _ := guildRosterLargeFixture(t, 24)
		repos := group
		if legacy {
			repos.Character = &guildRosterCountCharacter{CharacterRepository: group.Character}
		}
		s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
		runctx, cancel := context.WithCancel(ctx)
		for _, p := range peers {
			p.ctx = runctx
			p.guild.memberListRequested = true
			if err := s.startGameSessionEvents(runctx, p); err != nil {
				t.Fatal(err)
			}
		}
		requests, presence, probes := &guildLoadSamples{}, &guildLoadSamples{}, &guildLoadSamples{}
		var refused atomic.Int64
		duration := time.Duration(seconds) * time.Second / 4
		beforeReads, beforeWrites := counted.reads.Load(), counted.writes.Load()
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		started := time.Now()
		nextRequest, nextBurst := started, started
		burstCount := 0
		ticker := time.NewTicker(100 * time.Millisecond)
		for time.Since(started) < duration {
			now := time.Now()
			if !now.Before(nextBurst) && burstCount < 3 {
				burstCount++
				nextBurst = started.Add(time.Duration(burstCount) * duration / 3)
				for wave := 0; wave < waves; wave++ {
					for _, p := range peers {
						peer, _ := s.boundGameSessionCharacterSnapshot(p)
						if legacy {
							e := s.tryPostGameSessionCharacterEvent(p, "legacy-presence", peer.character, peer.generation, func() error {
								start := time.Now()
								ctx, cancel := s.currentGuildContext()
								defer cancel()
								var err error
								for _, notice := range []bool{true, false} {
									if err = s.refreshCurrentGuildTextPeer(ctx, 1, peer, boundGameSessionCharacter{}, notice); err != nil {
										break
									}
								}
								presence.add(time.Since(start), err)
								return nil
							})
							if e != nil {
								refused.Add(1)
							}
						} else {
							s.requestCurrentGuildPresenceRefresh(peer, 1)
						}
					}
				}
			}
			for _, p := range peers {
				queued := time.Now()
				if err := s.tryPostGameSessionCharacterEvent(p, "load-ordinary-probe", p.selectedCharacterID, p.characterGeneration, func() error { probes.add(time.Since(queued), nil); return nil }); err != nil {
					refused.Add(1)
				}
				if !now.Before(nextRequest) {
					if err := s.tryPostGameSessionCharacterEvent(p, "load-client-roster", p.selectedCharacterID, p.characterGeneration, func() error {
						start := time.Now()
						err := s.handleCurrentGuildMemberListForOpcode(p, 140)
						requests.add(time.Since(start), err)
						p.conn.(*bufferConn).write.Reset()
						return err
					}); err != nil {
						refused.Add(1)
					}
				}
			}
			if !now.Before(nextRequest) {
				nextRequest = nextRequest.Add(5 * time.Second)
			}
			<-ticker.C
		}
		ticker.Stop()
		drainctx, stopDrain := context.WithTimeout(ctx, 5*time.Minute)
		for _, p := range peers {
			if err := s.callGameSession(drainctx, p, "load-drain", func() error { return nil }); err != nil {
				t.Error(err)
			}
		}
		// Dirty followups may have been appended while the first barrier waited.
		for _, p := range peers {
			if err := s.callGameSession(drainctx, p, "load-final-drain", func() error { return nil }); err != nil {
				t.Error(err)
			}
		}
		stopDrain()
		runtime.ReadMemStats(&after)
		report := map[string]any{"phase": phase, "legacy_full_reads_and_unmerged_queue": legacy, "seconds": time.Since(started).Seconds(), "offered_seconds": duration.Seconds(), "members": 190, "sessions": 24, "waves_per_peak": waves, "peaks": burstCount,
			"sql_reads": counted.reads.Load() - beforeReads, "sql_writes": counted.writes.Load() - beforeWrites, "alloc_bytes": after.TotalAlloc - before.TotalAlloc, "gc_pause_ns": after.PauseTotalNs - before.PauseTotalNs,
			"requests": requests.snapshot(), "legacy_presence": presence.snapshot(), "ordinary_queue": probes.snapshot(), "refused": refused.Load(), "coalesced": s.performance.guildRoster[guildRefreshCoalesced].Load(), "executed": s.performance.guildRoster[guildRefreshExecuted].Load()}
		wire, _ := json.Marshal(report)
		t.Logf("GUILD_ROSTER_LOAD %s", wire)
		if counted.writes.Load() != beforeWrites {
			t.Error("roster-only load wrote persistent state")
		}
		if !legacy && (refused.Load() != 0 || requests.errors != 0 || len(requests.samples) == 0 || len(probes.samples) == 0 || s.performance.guildRoster[guildRefreshFailed].Load() != 0 || s.performance.guildRoster[guildSummaryFallback].Load() != 0 || s.performance.guildRoster[guildSummaryFailed].Load() != 0) {
			t.Error("optimized load dropped work or used failed/full summary reads")
		}
		for _, p := range peers {
			stopTestGameSessionEvents(t, s, p)
		}
		cancel()
	}
}

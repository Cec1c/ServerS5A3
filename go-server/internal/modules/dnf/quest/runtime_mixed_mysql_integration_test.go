//go:build mysql_integration

package quest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"longheng.io/server/internal/modules/dnf/progression"
	repo "longheng.io/server/internal/modules/dnf/repository"
	dbrepo "longheng.io/server/internal/modules/dnf/repository/mysql"
)

// Same synthetic workload compiles against the historical and candidate trees.
// It measures domain/storage operations, NOT complete op39/party/client timing.
func TestRuntimeMixedDisposableMySQL(t *testing.T) {
	if os.Getenv("DNF_RUNTIME_DISPOSABLE_DB") != "1" {
		t.Fatal("requires disposable DB")
	}
	cfg, err := driver.ParseDSN(os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || cfg.Net != "tcp" || host != "127.0.0.1" || cfg.DBName != "" {
		t.Fatal("requires loopback without existing schema")
	}
	cfg.ParseTime = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	name := fmt.Sprintf("runtime_mixed_%d", time.Now().UnixNano())
	defer db.Exec("DROP DATABASE IF EXISTS `" + name + "`")
	plan := repo.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if err := dbrepo.EnsureMySQLSchema(ctx, db, dbrepo.SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	adapter, cleanup := mixedSQLAdapter(db)
	defer cleanup()
	g, err := dbrepo.NewMySQLGroup(adapter, dbrepo.MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	const workers, iterations = 6, 40
	ids := make([]string, workers)
	for w := range ids {
		id := strconv.Itoa(1000 + w)
		ids[w] = id
		stats := map[string]int64{"exp": 100, "gold": 1000000}
		for k := 0; k < 70; k++ {
			stats[fmt.Sprintf("keep_%02d", k)] = int64(k + 1)
		}
		if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: id, AccountID: "a" + id, Name: "Mixed" + id, Level: 89, Stats: stats}); err != nil {
			t.Fatal(err)
		}
		if err := g.Skill.Save(ctx, repo.SkillRecord{CharacterID: id, Points: repo.SkillPointState{SyncedLevel: 89}}); err != nil {
			t.Fatal(err)
		}
		q := repo.QuestRecord{CharacterID: id, States: map[int64]repo.QuestState{}}
		for qid := int64(1); qid <= 674; qid++ {
			extra := map[string]string{}
			for k := 0; k < 6; k++ {
				extra[fmt.Sprint(k)] = "preserved historical receipt"
			}
			q.States[qid] = repo.QuestState{Status: "completed", Extra: extra}
		}
		for qid := int64(1); qid <= 2; qid++ {
			q.States[qid] = repo.QuestState{Status: "active", ProgressValue: 10000, Extra: map[string]string{"keep": "yes"}}
		}
		if err := g.Quest.Save(ctx, q); err != nil {
			t.Fatal(err)
		}
		entry := repo.PetEntry{PetKey: "p", ItemID: 1, Satiety: 100, Extra: map[string]string{"keep": "yes"}, AppliedClearTokens: map[string]bool{}}
		for k := 0; k < 128; k++ {
			token := fmt.Sprint(k)
			entry.AppliedClearTokens[token] = true
			entry.AppliedClearTokenOrder = append(entry.AppliedClearTokenOrder, token)
		}
		if err := g.Pet.Save(ctx, repo.PetRecord{CharacterID: id, EquippedKey: "p", Entries: map[string]repo.PetEntry{"p": entry}}); err != nil {
			t.Fatal(err)
		}
		slots := map[string]repo.ItemStack{}
		for k := 0; k < 100; k++ {
			slots[fmt.Sprintf("1:%d", k+100)] = repo.ItemStack{ItemID: int64(k + 1), Count: 1000, Extra: map[string]string{"keep": "yes"}}
		}
		if err := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: id, Slots: slots}); err != nil {
			t.Fatal(err)
		}
	}
	catalog := &Catalog{byID: map[int64]Definition{1: {ID: 1, Type: "[hunt enemy]", IntData: []int64{-1, -1, 77110, 1, 10000}}, 2: {ID: 2, Type: "[hunt enemy]", IntData: []int64{-1, -1, 77110, 1, 10000}}}}
	owner, err := NewOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	readStatus := func() map[string]int64 {
		rows, err := db.QueryContext(ctx, "SHOW GLOBAL STATUS WHERE Variable_name IN ('Questions','Com_select','Com_insert','Com_update','Com_delete','Com_stmt_prepare','Com_stmt_execute','Com_stmt_close','Bytes_sent','Bytes_received','Innodb_rows_read','Innodb_rows_inserted','Innodb_rows_updated')")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		m := map[string]int64{}
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				t.Fatal(err)
			}
			m[k], _ = strconv.ParseInt(v, 10, 64)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return m
	}
	before := readStatus()
	runtime.GC()
	var memBefore, memAfter runtime.MemStats
	runtime.ReadMemStats(&memBefore)
	start := time.Now()
	if p := os.Getenv("DNF_MIXED_MARKER"); p != "" {
		if err := os.WriteFile(p, []byte("start"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	samples := make([][]int64, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for w, id := range ids {
		wg.Add(1)
		go func(w int, id string) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				target := start.Add(time.Duration(i) * 50 * time.Millisecond)
				if delay := time.Until(target); delay > 0 {
					time.Sleep(delay)
				}
				begin := time.Now()
				var err error
				switch w % 3 {
				case 0:
					p := progression.CommitPlan{CharacterID: id, AccountID: "a" + id, Expected: progression.CommitState{Level: 89, Experience: uint32(100 + i), SkillPoints: repo.SkillPointState{SyncedLevel: 89}}, Next: progression.CommitState{Level: 89, Experience: uint32(101 + i), SkillPoints: repo.SkillPointState{SyncedLevel: 89}}}
					_, err = progression.Commit(ctx, g.CharacterProgression, p)
					if err == nil {
						_, err = owner.ApplyHuntEnemyKill(ctx, catalog, id, HuntEnemyKillInput{EnemyCode: 77110, EnemyType: 1, CompletionKey: fmt.Sprintf("run-%s-%d", id, i), CompletedAt: time.Now()})
					}
				case 1:
					err = g.CharacterPets.WithinCharacterPets(ctx, id, func(_ repo.InventoryRepository, _ repo.EquipmentRepository, pets repo.PetRepository) error {
						r, ok, e := pets.Load(ctx, id)
						if e != nil {
							return e
						}
						if !ok {
							return fmt.Errorf("missing pet")
						}
						old := repo.ClonePet(r).Entries["p"]
						next := r.Entries["p"]
						next.Satiety--
						r.Entries["p"] = next
						if writer, ok := pets.(interface {
							SavePetGrowth(context.Context, repo.PetRecord, string, repo.PetEntry) error
						}); ok {
							return writer.SavePetGrowth(ctx, r, "p", old)
						}
						return repo.SavePetFields(ctx, pets, r, repo.PetFieldEntries)
					})
				case 2:
					// Synthetic consume+grant transaction; no real box or reward RNG is invoked.
					err = g.CharacterItems.WithinCharacterItems(ctx, id, func(inv repo.InventoryRepository, _ repo.EquipmentRepository) error {
						r, ok, e := inv.Load(ctx, id)
						if e != nil {
							return e
						}
						if !ok {
							return fmt.Errorf("missing inventory")
						}
						a, b := r.Slots["1:100"], r.Slots["1:101"]
						a.Count--
						b.Count++
						r.Slots["1:100"], r.Slots["1:101"] = a, b
						return repo.SaveInventoryFields(ctx, inv, r, repo.InventoryFieldSlots)
					})
				}
				if err != nil {
					errs <- fmt.Errorf("worker %d iteration %d: %w", w, i, err)
					return
				}
				samples[w] = append(samples[w], time.Since(begin).Nanoseconds())
			}
		}(w, id)
	}
	wg.Wait()
	elapsed := time.Since(start)
	runtime.ReadMemStats(&memAfter)
	if p := os.Getenv("DNF_MIXED_MARKER"); p != "" {
		if err := os.WriteFile(p, []byte("done"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if t.Failed() {
		return
	}
	after := readStatus()
	for k, v := range before {
		after[k] -= v
	}
	for w, id := range ids {
		c, ok, err := g.Character.Load(ctx, id)
		if err != nil || !ok || c.Stats["gold"] != 1000000 || c.Stats["keep_69"] != 70 {
			t.Fatalf("character changed %s %v", id, err)
		}
		switch w % 3 {
		case 0:
			q, ok, err := g.Quest.Load(ctx, id)
			if err != nil || !ok || len(q.States) != 674 || q.States[1].ProgressValue != 10000-iterations || q.States[2].ProgressValue != 10000-iterations || q.States[600].Extra["5"] != "preserved historical receipt" || c.Stats["exp"] != 100+iterations {
				t.Fatalf("quest/progression state %s %v", id, err)
			}
		case 1:
			p, ok, err := g.Pet.Load(ctx, id)
			if err != nil || !ok || p.Entries["p"].Satiety != 100-iterations || len(p.Entries["p"].AppliedClearTokens) != 128 || p.Entries["p"].Extra["keep"] != "yes" {
				t.Fatalf("pet state %s %v", id, err)
			}
		case 2:
			r, ok, err := g.Inventory.Load(ctx, id)
			if err != nil || !ok || len(r.Slots) != 100 || r.Slots["1:100"].Count != 1000-iterations || r.Slots["1:101"].Count != 1000+iterations {
				t.Fatalf("inventory state %s %v", id, err)
			}
		}
	}
	latency := map[string]map[string]int64{}
	for kind, label := range []string{"quest_and_progression", "pet_satiety", "inventory_consume_grant"} {
		var a []int64
		for w := kind; w < workers; w += 3 {
			a = append(a, samples[w]...)
		}
		sort.Slice(a, func(i, j int) bool { return a[i] < a[j] })
		latency[label] = map[string]int64{"count": int64(len(a)), "p50_ns": a[(len(a)-1)*50/100], "p95_ns": a[(len(a)-1)*95/100], "p99_ns": a[(len(a)-1)*99/100], "max_ns": a[len(a)-1]}
	}
	result := map[string]any{"workers": workers, "iterations": iterations, "operations": workers * iterations, "elapsed_seconds": elapsed.Seconds(), "target_interval_ms": 50, "sql_delta": after, "latency": latency, "allocated_bytes": memAfter.TotalAlloc - memBefore.TotalAlloc, "allocated_objects": memAfter.Mallocs - memBefore.Mallocs, "gc_pause_ns": memAfter.PauseTotalNs - memBefore.PauseTotalNs, "gc_cycles": memAfter.NumGC - memBefore.NumGC, "pool_wait_count": db.Stats().WaitCount, "pool_wait_ns": int64(db.Stats().WaitDuration), "state_verified": true, "full_game_or_party": false}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := os.Getenv("DNF_MIXED_RESULT")
	if path == "" {
		t.Fatal("missing output path")
	}
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
}

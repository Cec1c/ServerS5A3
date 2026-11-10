//go:build mysql_integration

package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sync"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/quest"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

type weddingCancelSQLSnapshot struct {
	characters map[string]r.CharacterRecord
	quests     map[string]r.QuestRecord
}

// Decorate the real transaction, rather than the base repository: both clear
// and cancellation must use the scoped repositories and the same pair locks.
type weddingCancelSQLUnit struct {
	r.CharacterTradeUnitOfWork
	unit   r.WeddingQuestUnitOfWork
	before func()
	inside func(context.Context, r.CharacterRepository, r.QuestRepository, func(r.CharacterRepository, r.QuestRepository) error) error
}

func (u weddingCancelSQLUnit) WithinWeddingQuests(ctx context.Context, ids []string, apply func(r.CharacterRepository, r.QuestRepository) error) error {
	if u.before != nil {
		u.before()
	}
	return u.unit.WithinWeddingQuests(ctx, ids, func(chars r.CharacterRepository, quests r.QuestRepository) error {
		if u.inside != nil {
			return u.inside(ctx, chars, quests, apply)
		}
		return apply(chars, quests)
	})
}

type weddingCancelSQLFailCharacter struct {
	r.CharacterRepository
	failID string
	fault  error
	writes *int
}

func (s weddingCancelSQLFailCharacter) SaveFields(ctx context.Context, c r.CharacterRecord, fields ...r.CharacterField) error {
	if c.CharacterID == s.failID {
		return s.fault
	}
	if err := r.SaveCharacterFields(ctx, s.CharacterRepository, c, fields...); err != nil {
		return err
	}
	(*s.writes)++
	return nil
}

type weddingCancelSQLDeltaGate struct {
	r.QuestRepository
	writer  r.QuestDeltaWriter
	ready   chan struct{}
	release <-chan struct{}
}

func (q *weddingCancelSQLDeltaGate) SaveFields(context.Context, r.QuestRecord, ...r.QuestField) error {
	return errors.New("ordinary clear attempted a full quest-group replacement")
}

func (q *weddingCancelSQLDeltaGate) SaveQuestChanges(ctx context.Context, before, after r.QuestRecord, fields ...r.QuestField) error {
	// Prove that the ordinary planner still carries both old wedding
	// projections. The real SQL delta must ignore those unchanged IDs after
	// cancellation deletes them; this adapter performs no synthetic merge.
	for _, id := range []int64{20737, 20738} {
		for _, rows := range [][2]map[int64]r.QuestState{{before.States, after.States}, {before.Progress, after.Progress}} {
			old, found := rows[0][id]
			next, kept := rows[1][id]
			if !found || !kept || !r.QuestStatesEqual(old, next) {
				return errors.New("ordinary clear fixture did not retain unchanged old wedding snapshot")
			}
		}
	}
	close(q.ready)
	select {
	case <-q.release:
		return q.writer.SaveQuestChanges(ctx, before, after, fields...)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestWeddingCancelDisposableMySQL(t *testing.T) {
	if os.Getenv("DNF_RUNTIME_DISPOSABLE_DB") != "1" {
		t.Fatal("requires explicit disposable database authorization")
	}
	cfg, err := driver.ParseDSN(os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN"))
	if err != nil || cfg.Net != "tcp" || cfg.DBName != "" {
		t.Fatal("requires TCP test DSN without a selected database")
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("requires loopback test database connection")
	}
	cfg.ParseTime, cfg.Loc = true, time.UTC
	openDB := func() *sql.DB {
		t.Helper()
		db, err := sql.Open("mysql", cfg.FormatDSN())
		if err != nil {
			t.Fatal("open isolated cancellation database connection")
		}
		return db
	}
	db := openDB()
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	name := fmt.Sprintf("wedding_cancel_test_%d", time.Now().UnixNano())
	safeName := regexp.MustCompile(`^wedding_cancel_test_[0-9]+$`)
	if !safeName.MatchString(name) {
		t.Fatal("invalid generated cancellation database name")
	}
	if _, err := db.ExecContext(ctx, "CREATE DATABASE `"+name+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
		t.Fatal("create isolated cancellation database failed")
	}
	t.Cleanup(func() {
		if !safeName.MatchString(name) {
			t.Error("refused cleanup outside generated cancellation database")
			return
		}
		dropCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if _, err := db.ExecContext(dropCtx, "DROP DATABASE `"+name+"`"); err != nil {
			t.Error("isolated cancellation database cleanup failed")
		}
	})
	plan := r.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if err := EnsureMySQLSchema(ctx, db, SchemaOptions{AutoCreate: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	newGroup := func(pool *sql.DB) r.Group {
		t.Helper()
		g, err := NewMySQLGroupFromDB(pool, MySQLGroupOptions{DatabasePlan: plan})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	g := newGroup(db)
	unit, ok := g.CharacterTrade.(r.WeddingQuestUnitOfWork)
	if !ok {
		t.Fatal("real MySQL repository has no shared wedding quest transaction")
	}
	now := time.Date(2026, 11, 2, 16, 0, 0, 0, time.UTC)
	rules := &wedding.Rules{MinimumLevel: 30, QuestIDs: []int64{20737, 20738}, EngagementCancellationGold: 500000}
	seed := func(t *testing.T, base int) wedding.PairRequest {
		t.Helper()
		ids := []string{fmt.Sprint(base), fmt.Sprint(base + 1)}
		for i, id := range ids {
			account := "cancel-account-" + id
			if err := g.Account.Save(ctx, r.AccountRecord{AccountID: account}); err != nil {
				t.Fatal(err)
			}
			c := r.CharacterRecord{CharacterID: id, AccountID: account, Name: "Cancel" + id, Job: []string{"0", "11"}[i], Level: 30, UpdatedAt: now,
				Stats: map[string]int64{"gold": int64(900000 + i*100000), "wedding_v1_phase": int64(wedding.Engaged), "wedding_v1_partner": int64(base + 1 - i),
					"wedding_v1_love_points": int64(37 + i), "wedding_v1_love_day": 20261101, "wedding_v1_wedding_day": 20261102,
					"wedding_v1_cooldown_until": now.Add(-time.Hour).Unix()}}
			if err := r.SaveCharacterFields(ctx, g.Character, c, append(r.AllCharacterFields(), r.CharacterFieldWedding)...); err != nil {
				t.Fatal(err)
			}
			q := r.QuestRecord{CharacterID: id, UpdatedAt: now, States: map[int64]r.QuestState{
				20737: {Status: "active", ProgressValue: 5, UpdatedAt: now, Extra: map[string]string{"wedding_partner": ids[1-i], "quest_type": "[condition under clear]"}},
				20738: {Status: "completed", UpdatedAt: now, Extra: map[string]string{"reward_state": "granted"}},
				9001:  {Status: "completed", UpdatedAt: now, Extra: map[string]string{"receipt": "unrelated-history"}},
			}, Progress: map[int64]r.QuestState{
				20737: {Status: "accepted", ProgressValue: 2, UpdatedAt: now},
				20738: {Status: "active", ProgressValue: 1, UpdatedAt: now},
				9002:  {Status: "active", ProgressValue: 7, UpdatedAt: now, Extra: map[string]string{"marker": "unrelated-progress"}},
			}}
			if err := g.Quest.Save(ctx, q); err != nil {
				t.Fatal(err)
			}
		}
		return wedding.PairRequest{Actor: wedding.Actor{AccountID: "cancel-account-" + ids[0], CharacterID: ids[0]},
			Partner: wedding.Actor{AccountID: "cancel-account-" + ids[1], CharacterID: ids[1]}, Now: now.Add(time.Minute)}
	}
	snapshot := func(t *testing.T, req wedding.PairRequest) weddingCancelSQLSnapshot {
		t.Helper()
		freshDB := openDB()
		defer freshDB.Close()
		fresh := newGroup(freshDB)
		s := weddingCancelSQLSnapshot{characters: map[string]r.CharacterRecord{}, quests: map[string]r.QuestRecord{}}
		for _, id := range []string{req.Actor.CharacterID, req.Partner.CharacterID} {
			c, found, err := fresh.Character.Load(ctx, id)
			if err != nil || !found {
				t.Fatalf("fresh cancellation character found=%t err=%v", found, err)
			}
			q, found, err := fresh.Quest.Load(ctx, id)
			if err != nil || !found {
				t.Fatalf("fresh cancellation quest found=%t err=%v", found, err)
			}
			s.characters[id], s.quests[id] = c, q
		}
		return s
	}
	assertCanceled := func(t *testing.T, req wedding.PairRequest, before weddingCancelSQLSnapshot) {
		t.Helper()
		after := snapshot(t, req)
		for id, c := range after.characters {
			state, err := wedding.Read(c)
			old, oldErr := wedding.Read(before.characters[id])
			wantGold := before.characters[id].Stats["gold"]
			if id == req.Actor.CharacterID {
				wantGold -= rules.EngagementCancellationGold
			}
			if err != nil || oldErr != nil || state.Phase != wedding.Single || state.PartnerID != 0 ||
				state.LovePoints != old.LovePoints || state.LastLoveDay != old.LastLoveDay ||
				state.LastWeddingDay != old.LastWeddingDay || state.CooldownUntil != old.CooldownUntil || c.Stats["gold"] != wantGold {
				t.Fatalf("cancel changed personal state or left relationship char=%s state=%+v err=%v", id, state, err)
			}
			q, expected := after.quests[id], r.CloneQuest(before.quests[id])
			for _, qid := range []int64{20737, 20738} {
				delete(expected.States, qid)
				delete(expected.Progress, qid)
			}
			if !reflect.DeepEqual(q.States, expected.States) || !reflect.DeepEqual(q.Progress, expected.Progress) {
				t.Fatal("cancel lost unrelated quests or retained wedding quest projection")
			}
		}
	}
	t.Run("offline-partner-clears-only-wedding-and-preserves-personal-state", func(t *testing.T) {
		req := seed(t, 401)
		before := snapshot(t, req)
		// No socket or online registry exists anywhere in this domain fixture.
		result, err := (wedding.Owner{Repositories: g, Rules: rules}).CancelEngagement(ctx, req)
		if err != nil || result.Actor.CharacterID != req.Actor.CharacterID || result.Partner.CharacterID != req.Partner.CharacterID {
			t.Fatalf("offline partner cancellation result=%+v err=%v", result, err)
		}
		assertCanceled(t, req, before)
		after := snapshot(t, req)
		if _, err := (wedding.Owner{Repositories: g, Rules: rules}).CancelEngagement(ctx, req); !errors.Is(err, wedding.ErrState) || !reflect.DeepEqual(snapshot(t, req), after) {
			t.Fatalf("cancellation replay charged again or changed committed state: %v", err)
		}
	})
	t.Run("insufficient-actor-gold-preserves-complete-pair", func(t *testing.T) {
		req := seed(t, 451)
		c, found, err := g.Character.Load(ctx, req.Actor.CharacterID)
		if err != nil || !found {
			t.Fatal("insufficient-gold fixture character missing")
		}
		c.Stats["gold"] = 499999
		if err := r.SaveCharacterFields(ctx, g.Character, c, r.CharacterFieldStats); err != nil {
			t.Fatal(err)
		}
		before := snapshot(t, req)
		if _, err := (wedding.Owner{Repositories: g, Rules: rules}).CancelEngagement(ctx, req); !errors.Is(err, wedding.ErrGold) || !reflect.DeepEqual(snapshot(t, req), before) {
			t.Fatalf("insufficient gold changed relationship/quests/wallet: %v", err)
		}
	})
	t.Run("callback-error-after-first-quest-delete-rolls-back", func(t *testing.T) {
		req := seed(t, 501)
		before := snapshot(t, req)
		fault := errors.New("isolated wedding quest callback failure")
		err := unit.WithinWeddingQuests(ctx, []string{req.Actor.CharacterID, req.Partner.CharacterID}, func(_ r.CharacterRepository, quests r.QuestRepository) error {
			q, found, err := quests.Load(ctx, req.Actor.CharacterID)
			if err != nil {
				return err
			}
			if !found {
				return errors.New("isolated callback quest missing")
			}
			delete(q.States, 20737)
			delete(q.Progress, 20738)
			if err := r.SaveQuestFields(ctx, quests, q, r.QuestFieldStates, r.QuestFieldProgress); err != nil {
				return err
			}
			return fault
		})
		if !errors.Is(err, fault) || !reflect.DeepEqual(snapshot(t, req), before) {
			t.Fatalf("quest callback error did not roll back complete pair: %v", err)
		}
	})
	t.Run("second-relationship-write-failure-rolls-back-first-and-quest-delete", func(t *testing.T) {
		req := seed(t, 601)
		before := snapshot(t, req)
		fault, writes := errors.New("isolated second wedding relationship write failure"), 0
		faulty := g
		faulty.CharacterTrade = weddingCancelSQLUnit{CharacterTradeUnitOfWork: g.CharacterTrade, unit: unit,
			inside: func(ctx context.Context, chars r.CharacterRepository, quests r.QuestRepository, apply func(r.CharacterRepository, r.QuestRepository) error) error {
				return apply(weddingCancelSQLFailCharacter{CharacterRepository: chars, failID: req.Partner.CharacterID, fault: fault, writes: &writes}, quests)
			}}
		_, err := (wedding.Owner{Repositories: faulty, Rules: rules}).CancelEngagement(ctx, req)
		if !errors.Is(err, fault) || writes != 1 || !reflect.DeepEqual(snapshot(t, req), before) {
			t.Fatalf("half relationship failed rollback: writes=%d err=%v", writes, err)
		}
	})
	text, err := os.ReadFile(filepath.Join("..", "..", "quest", "testdata", "wedding_20737.qst"))
	if err != nil {
		t.Fatal(err)
	}
	secondText, err := os.ReadFile(filepath.Join("..", "..", "quest", "testdata", "wedding_20738.qst"))
	if err != nil {
		t.Fatal(err)
	}
	index, err := pvf.Build(ctx, weddingReceiptQuestSource{
		quest.DefaultList:                       "20737 `event/chn_2014_wedding_01.qst`\n20738 `event/chn_2014_wedding_02.qst`\n",
		"n_quest/event/chn_2014_wedding_01.qst": string(text),
		"n_quest/event/chn_2014_wedding_02.qst": string(secondText),
	}, pvf.BuildOptions{Lists: []string{quest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := quest.Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	orderedGroups := func(firstApplied chan struct{}, release <-chan struct{}, secondAttempt chan struct{}) (r.Group, r.Group) {
		first, second := g, g
		first.CharacterTrade = weddingCancelSQLUnit{CharacterTradeUnitOfWork: g.CharacterTrade, unit: unit,
			inside: func(ctx context.Context, chars r.CharacterRepository, quests r.QuestRepository, apply func(r.CharacterRepository, r.QuestRepository) error) error {
				if err := apply(chars, quests); err != nil {
					return err
				}
				// Domain reads/writes have run; real SQL pair locks remain held
				// until the competing operation has started its transaction.
				close(firstApplied)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}}
		second.CharacterTrade = weddingCancelSQLUnit{CharacterTradeUnitOfWork: g.CharacterTrade, unit: unit,
			before: func() { close(secondAttempt) }}
		return first, second
	}
	for _, clearFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("concurrent-clear-cancel/clear-first=%t", clearFirst), func(t *testing.T) {
			base := 701
			if !clearFirst {
				base = 801
			}
			req := seed(t, base)
			before := snapshot(t, req)
			firstApplied, release, secondAttempt := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			releaseFirst := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseFirst()
			first, second := orderedGroups(firstApplied, release, secondAttempt)
			var clearResult quest.ClearMapPersistResult
			clear := func(repositories r.Group) error {
				owner, err := quest.NewOwner(repositories)
				if err != nil {
					return err
				}
				clearResult, err = owner.ApplyClearMapCompletion(ctx, catalog, req.Actor.CharacterID, quest.ClearMapCompletionInput{
					WeddingPartnerID: req.Partner.CharacterID, DungeonID: 310, MapID: 310010, CompletionKey: "concurrent-wedding-clear-" + req.Actor.CharacterID,
					CompletedAt: now.Add(2 * time.Minute),
				})
				return err
			}
			cancelPair := func(repositories r.Group) error {
				_, err := (wedding.Owner{Repositories: repositories, Rules: rules}).CancelEngagement(ctx, req)
				return err
			}
			firstOperation, secondOperation := clear, cancelPair
			if !clearFirst {
				firstOperation, secondOperation = cancelPair, clear
			}
			firstDone, secondDone := make(chan error, 1), make(chan error, 1)
			go func() { firstDone <- firstOperation(first) }()
			select {
			case <-firstApplied:
			case err := <-firstDone:
				t.Fatalf("first operation failed before transaction gate: %v", err)
			case <-ctx.Done():
				t.Fatal("first transaction gate timed out")
			}
			go func() { secondDone <- secondOperation(second) }()
			select {
			case <-secondAttempt:
			case err := <-secondDone:
				t.Fatalf("competing operation did not use shared wedding transaction: %v", err)
			case <-ctx.Done():
				t.Fatal("competing transaction attempt timed out")
			}
			releaseFirst()
			for _, done := range []<-chan error{firstDone, secondDone} {
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal("ordered clear/cancel transaction did not finish")
				}
			}
			if clearFirst {
				if len(clearResult.Completions) != 1 || clearResult.Completions[0].RemainingTrigger != 4 {
					t.Fatalf("clear before cancellation did not commit exactly once: %+v", clearResult)
				}
			} else if len(clearResult.Completions) != 0 {
				t.Fatalf("stale clear recreated canceled preparation: %+v", clearResult)
			}
			assertCanceled(t, req, before)
			t.Logf("clear_first=%t concurrent_attempt_during_open_transaction=true canceled_quests_absent=true", clearFirst)
		})
	}
	for _, acceptFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("concurrent-preparation-accept-cancel/accept-first=%t", acceptFirst), func(t *testing.T) {
			base := 1001
			if !acceptFirst {
				base = 1101
			}
			req := seed(t, base)
			q, found, err := g.Quest.Load(ctx, req.Actor.CharacterID)
			if err != nil || !found {
				t.Fatal("preparation acceptance fixture quest missing")
			}
			firstQuest := q.States[20737]
			firstQuest.Status, firstQuest.ProgressValue = "completed", 0
			firstQuest.Extra["reward_state"] = "granted"
			q.States[20737] = firstQuest
			delete(q.Progress, 20737)
			delete(q.States, 20738)
			delete(q.Progress, 20738)
			if err := g.Quest.Save(ctx, q); err != nil {
				t.Fatal(err)
			}
			before := snapshot(t, req)
			firstApplied, release, secondAttempt := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			releaseFirst := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseFirst()
			first, second := orderedGroups(firstApplied, release, secondAttempt)
			var accepted quest.AcceptResult
			acceptPreparation := func(repositories r.Group) error {
				owner, err := quest.NewOwner(repositories)
				if err != nil {
					return err
				}
				accepted, err = owner.ApplyAcceptAt(ctx, catalog, quest.CharacterEligibility{Level: 30, Job: 0}, quest.Command{
					AccountID: req.Actor.AccountID, SelectedCharacterID: uint16(base), QuestID: 20738,
				}, now.Add(2*time.Minute))
				return err
			}
			cancelPair := func(repositories r.Group) error {
				_, err := (wedding.Owner{Repositories: repositories, Rules: rules}).CancelEngagement(ctx, req)
				return err
			}
			firstOperation, secondOperation := acceptPreparation, cancelPair
			if !acceptFirst {
				firstOperation, secondOperation = cancelPair, acceptPreparation
			}
			firstDone, secondDone := make(chan error, 1), make(chan error, 1)
			go func() { firstDone <- firstOperation(first) }()
			select {
			case <-firstApplied:
			case err := <-firstDone:
				t.Fatalf("first accept/cancel failed before transaction gate: %v", err)
			case <-ctx.Done():
				t.Fatal("first accept/cancel transaction gate timed out")
			}
			go func() { secondDone <- secondOperation(second) }()
			select {
			case <-secondAttempt:
			case err := <-secondDone:
				t.Fatalf("competing accept/cancel bypassed shared transaction: %v", err)
			case <-ctx.Done():
				t.Fatal("competing accept/cancel attempt timed out")
			}
			releaseFirst()
			for index, done := range []<-chan error{firstDone, secondDone} {
				select {
				case err := <-done:
					if !acceptFirst && index == 1 {
						if !errors.Is(err, quest.ErrQuestNotAcceptable) {
							t.Fatalf("canceled relationship authorized stale acceptance: %v", err)
						}
					} else if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal("ordered accept/cancel transaction did not finish")
				}
			}
			if acceptFirst && (accepted.QuestID != 20738 || accepted.InitTrigger != 1) {
				t.Fatalf("real preparation acceptance did not commit before cancellation: %+v", accepted)
			}
			assertCanceled(t, req, before)
			t.Logf("accept_first=%t real_20738_definition=true canceled_quests_absent=true actor_fee_charged_once=true", acceptFirst)
		})
	}
	t.Run("ordinary-clear-delta-after-cancel-does-not-recreate-old-wedding-rows", func(t *testing.T) {
		req := seed(t, 901)
		q, found, err := g.Quest.Load(ctx, req.Actor.CharacterID)
		if err != nil || !found {
			t.Fatal("ordinary-clear fixture quest record missing")
		}
		q.States[9100] = r.QuestState{Status: "active", ProgressValue: 1, UpdatedAt: now, Extra: map[string]string{"ordinary_guard": "keep"}}
		if err := g.Quest.Save(ctx, q); err != nil {
			t.Fatal(err)
		}
		before := snapshot(t, req)
		ordinaryIndex, err := pvf.Build(ctx, weddingReceiptQuestSource{
			quest.DefaultList:                       "20737 `event/chn_2014_wedding_01.qst`\n9100 `ordinary_clear.qst`\n",
			"n_quest/event/chn_2014_wedding_01.qst": string(text),
			"n_quest/ordinary_clear.qst":            "[grade]\n`[normal]`\n[exposed by npc]\n1\n[level]\n1 99\n[job]\n`[all]`\n[type]\n`[clear map]`\n[int data]\n58569\n[/int data]\n",
		}, pvf.BuildOptions{Lists: []string{quest.DefaultList}})
		if err != nil {
			t.Fatal(err)
		}
		ordinaryCatalog, err := quest.Load(ctx, ordinaryIndex)
		if err != nil {
			t.Fatal(err)
		}
		writer, ok := g.Quest.(r.QuestDeltaWriter)
		if !ok {
			t.Fatal("real MySQL quest repository has no delta writer")
		}
		planned, release := make(chan struct{}), make(chan struct{})
		var releaseOnce sync.Once
		releaseDelta := func() { releaseOnce.Do(func() { close(release) }) }
		defer releaseDelta()
		ordinaryGroup := g
		ordinaryGroup.Quest = &weddingCancelSQLDeltaGate{QuestRepository: g.Quest, writer: writer, ready: planned, release: release}
		ordinaryOwner, err := quest.NewOwner(ordinaryGroup)
		if err != nil {
			t.Fatal(err)
		}
		var ordinaryResult quest.ClearMapPersistResult
		clearDone := make(chan error, 1)
		go func() {
			var err error
			ordinaryResult, err = ordinaryOwner.ApplyClearMapCompletion(ctx, ordinaryCatalog, req.Actor.CharacterID, quest.ClearMapCompletionInput{
				DungeonID: 310, MapID: 58569, CompletionKey: "ordinary-clear-before-cancellation",
				CompletedAt: now.Add(2 * time.Minute),
			})
			clearDone <- err
		}()
		select {
		case <-planned:
		case err := <-clearDone:
			t.Fatalf("ordinary clear failed before real delta gate: %v", err)
		case <-ctx.Done():
			t.Fatal("ordinary clear planning gate timed out")
		}
		if _, err := (wedding.Owner{Repositories: g, Rules: rules}).CancelEngagement(ctx, req); err != nil {
			t.Fatal(err)
		}
		// Cancellation has physically committed before the stale ordinary
		// before-image reaches the original MySQL delta writer.
		assertCanceled(t, req, before)
		releaseDelta()
		select {
		case err := <-clearDone:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("ordinary clear delta did not finish")
		}
		if len(ordinaryResult.Completions) != 1 || ordinaryResult.Completions[0].QuestID != 9100 || ordinaryResult.Completions[0].RemainingTrigger != 0 {
			t.Fatalf("ordinary clear result=%+v", ordinaryResult)
		}
		after := snapshot(t, req)
		ordinary := after.quests[req.Actor.CharacterID].States[9100]
		if ordinary.ProgressValue != 0 || ordinary.Extra["reward_state"] != "pending" || ordinary.Extra["ordinary_guard"] != "keep" || ordinary.Extra["completion_key"] != "ordinary-clear-before-cancellation" {
			t.Fatalf("ordinary task did not complete through actual MySQL delta: %+v", ordinary)
		}
		expected := weddingCancelSQLSnapshot{characters: before.characters, quests: make(map[string]r.QuestRecord, len(before.quests))}
		for id, row := range before.quests {
			expected.quests[id] = r.CloneQuest(row)
		}
		expected.quests[req.Actor.CharacterID].States[9100] = ordinary
		assertCanceled(t, req, expected)
		t.Log("ordinary_delta_after_committed_cancellation=true ordinary_remaining=0 wedding_rows_absent=true actor_fee_charged_once=true")
	})
}

package quest

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

type p4DeltaQuestRepo struct {
	base         dnfrepo.QuestRepository
	fullLoads    atomic.Int64
	headerLoads  atomic.Int64
	idLoads      atomic.Int64
	activeLoads  atomic.Int64
	deltaSaves   atomic.Int64
	fullSaves    atomic.Int64
	loadOverride func(context.Context, string) (dnfrepo.QuestRecord, bool, error)
}

func (r *p4DeltaQuestRepo) Load(ctx context.Context, id string) (dnfrepo.QuestRecord, bool, error) {
	r.fullLoads.Add(1)
	if r.loadOverride != nil {
		return r.loadOverride(ctx, id)
	}
	return r.base.Load(ctx, id)
}

func (r *p4DeltaQuestRepo) Save(ctx context.Context, record dnfrepo.QuestRecord) error {
	r.fullSaves.Add(1)
	return r.base.Save(ctx, record)
}

func (r *p4DeltaQuestRepo) LoadQuestStatesByIDs(ctx context.Context, id string, ids ...int64) (dnfrepo.QuestStateHeaders, bool, error) {
	r.headerLoads.Add(1)
	rec, found, err := r.base.Load(ctx, id)
	if err != nil || !found {
		return dnfrepo.QuestStateHeaders{}, found, err
	}
	return dnfrepo.SelectQuestStateHeaders(rec, ids...), true, nil
}

func (r *p4DeltaQuestRepo) LoadQuestIDs(ctx context.Context, id string, ids []int64) (dnfrepo.QuestRecord, bool, error) {
	r.idLoads.Add(1)
	rec, found, err := r.base.Load(ctx, id)
	if err != nil || !found {
		return dnfrepo.QuestRecord{}, found, err
	}
	out := dnfrepo.QuestRecord{
		PlanningPartial: true,
		CharacterID:     rec.CharacterID,
		UpdatedAt:       rec.UpdatedAt,
	}
	want := make(map[int64]struct{}, len(ids))
	for _, qid := range ids {
		want[qid] = struct{}{}
	}
	for qid, st := range rec.States {
		if _, ok := want[qid]; ok {
			if out.States == nil {
				out.States = make(map[int64]dnfrepo.QuestState)
			}
			out.States[qid] = st
		}
	}
	for qid, st := range rec.Progress {
		if _, ok := want[qid]; ok {
			if out.Progress == nil {
				out.Progress = make(map[int64]dnfrepo.QuestState)
			}
			out.Progress[qid] = st
		}
	}
	return dnfrepo.CloneQuest(out), true, nil
}

func (r *p4DeltaQuestRepo) LoadQuestActivePlanning(ctx context.Context, id string) (dnfrepo.QuestRecord, bool, error) {
	r.activeLoads.Add(1)
	rec, found, err := r.base.Load(ctx, id)
	if err != nil || !found {
		return dnfrepo.QuestRecord{}, found, err
	}
	activeIDs := make(map[int64]struct{})
	for qid, st := range rec.States {
		if dnfrepo.QuestStatusActive(st.Status) {
			activeIDs[qid] = struct{}{}
		}
	}
	for qid, st := range rec.Progress {
		if dnfrepo.QuestStatusActive(st.Status) {
			activeIDs[qid] = struct{}{}
		}
	}
	out := dnfrepo.QuestRecord{
		PlanningPartial: true,
		CharacterID:     rec.CharacterID,
		UpdatedAt:       rec.UpdatedAt,
	}
	for qid := range activeIDs {
		if st, ok := rec.States[qid]; ok {
			if out.States == nil {
				out.States = make(map[int64]dnfrepo.QuestState)
			}
			out.States[qid] = st
		}
		if st, ok := rec.Progress[qid]; ok {
			if out.Progress == nil {
				out.Progress = make(map[int64]dnfrepo.QuestState)
			}
			out.Progress[qid] = st
		}
	}
	return dnfrepo.CloneQuest(out), true, nil
}

func (r *p4DeltaQuestRepo) SaveQuestChanges(ctx context.Context, before, after dnfrepo.QuestRecord, fields ...dnfrepo.QuestField) error {
	r.deltaSaves.Add(1)
	current, found, err := r.base.Load(ctx, after.CharacterID)
	if err != nil {
		return err
	}
	if !found {
		current = dnfrepo.QuestRecord{CharacterID: after.CharacterID}
	}
	current = dnfrepo.CloneQuest(current)
	applyGroup := func(dst *map[int64]dnfrepo.QuestState, prev, next map[int64]dnfrepo.QuestState) {
		for _, qid := range dnfrepo.ChangedQuestIDs(prev, next) {
			if st, ok := next[qid]; ok {
				if *dst == nil {
					*dst = make(map[int64]dnfrepo.QuestState)
				}
				copied := st
				if st.Extra != nil {
					copied.Extra = maps.Clone(st.Extra)
				}
				(*dst)[qid] = copied
			} else if *dst != nil {
				delete(*dst, qid)
			}
		}
	}
	for _, f := range fields {
		switch f {
		case dnfrepo.QuestFieldStates:
			applyGroup(&current.States, before.States, after.States)
		case dnfrepo.QuestFieldProgress:
			applyGroup(&current.Progress, before.Progress, after.Progress)
		}
	}
	return r.base.Save(ctx, current)
}

type p4SettlementSpyUOW struct {
	base  dnfrepo.CharacterSettlementUnitOfWork
	quest dnfrepo.QuestRepository
	calls atomic.Int64
}

func (u *p4SettlementSpyUOW) WithinCharacterSettlement(ctx context.Context, characterID string, apply func(dnfrepo.Group) error) error {
	u.calls.Add(1)
	return u.base.WithinCharacterSettlement(ctx, characterID, func(tx dnfrepo.Group) error {
		if u.quest != nil {
			tx.Quest = u.quest
		}
		return apply(tx)
	})
}

func TestP4RoomClearMapCatalogNonMatchSkipsSettlementAndReadWriteDivergenceStillCommits(t *testing.T) {
	ctx := context.Background()
	// Separate stale read store (missing subquest 3560) vs authoritative write store (holding 3560 + parent 3525).
	staleReadMem := dnfrepomemory.NewMemoryGroup()
	if err := staleReadMem.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: "19",
		States: map[int64]dnfrepo.QuestState{
			9901: {Status: "active", ProgressValue: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	writeMem := dnfrepomemory.NewMemoryGroup()
	if err := writeMem.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "acct"}); err != nil {
		t.Fatal(err)
	}
	seed := roomClearMapActiveRecord()
	seed.States[9901] = dnfrepo.QuestState{Status: "active", ProgressValue: 1}
	if err := writeMem.Quest.Save(ctx, seed); err != nil {
		t.Fatal(err)
	}
	staleReadQuest := &p4DeltaQuestRepo{base: staleReadMem.Quest}
	writeTxQuest := &p4DeltaQuestRepo{base: writeMem.Quest}
	spyUOW := &p4SettlementSpyUOW{base: writeMem.CharacterSettlement, quest: writeTxQuest}
	repos := writeMem
	repos.Quest = staleReadQuest
	repos.CharacterSettlement = spyUOW

	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	catalog := roomClearMapTestCatalog(t)
	now := time.Date(2026, 11, 26, 4, 0, 0, 0, time.UTC)

	// Case A: Non-matching MapID exits from catalog before any repository call or settlement tx.
	res, err := owner.ApplyRoomClearMapCompletion(ctx, catalog, "19", RoomClearMapInput{
		DungeonID:      82,
		MapID:          999999,
		LinkedQuestIDs: []int64{3560},
		CompletionKey:  "run/nomatch",
		CompletedAt:    now,
	})
	if err != nil || res.Applied || len(res.Completions) != 0 {
		t.Fatalf("non-match map result = %+v err=%v", res, err)
	}
	if spyUOW.calls.Load() != 0 || staleReadQuest.headerLoads.Load() != 0 || writeTxQuest.idLoads.Load() != 0 {
		t.Fatalf("catalog non-match should not touch DB: uow=%d staleHeaders=%d txIDs=%d",
			spyUOW.calls.Load(), staleReadQuest.headerLoads.Load(), writeTxQuest.idLoads.Load())
	}

	// Case B: Matching MapID where unlocked read replica lacks 3560 but authoritative write tx has 3560 + 3525.
	// Must NEVER short-circuit on stale read replica; must enter settlement tx, commit 3560 & 3525, and replay idempotently.
	input := RoomClearMapInput{
		DungeonID:      82,
		MapID:          92247,
		LinkedQuestIDs: []int64{3560},
		CompletionKey:  "run/read-write-divergence",
		CompletedAt:    now,
	}
	res, err = owner.ApplyRoomClearMapCompletion(ctx, catalog, "19", input)
	if err != nil {
		t.Fatalf("ApplyRoomClearMapCompletion under read/write divergence: %v", err)
	}
	if !res.Applied || len(res.Completions) != 1 || res.Completions[0].QuestID != 3560 ||
		res.PostCommitQuest.States[3560].Status != "completed" ||
		res.PostCommitQuest.States[3525].ProgressValue != 0 {
		t.Fatalf("expected 3560 completed & 3525 updated despite stale read replica, got %+v", res)
	}
	if staleReadQuest.headerLoads.Load() != 0 || staleReadQuest.fullLoads.Load() != 0 {
		t.Fatalf("must not consult unlocked read repo before settlement: headers=%d full=%d",
			staleReadQuest.headerLoads.Load(), staleReadQuest.fullLoads.Load())
	}
	if spyUOW.calls.Load() != 1 || writeTxQuest.idLoads.Load() != 1 || writeTxQuest.deltaSaves.Load() != 1 {
		t.Fatalf("unexpected tx counts: uow=%d idLoads=%d deltaSaves=%d",
			spyUOW.calls.Load(), writeTxQuest.idLoads.Load(), writeTxQuest.deltaSaves.Load())
	}

	// Replay with same key must be idempotent without a second increment or write.
	replay, err := owner.ApplyRoomClearMapCompletion(ctx, catalog, "19", input)
	if err != nil || replay.Applied || !replay.Idempotent || len(replay.ChangedFields) != 0 {
		t.Fatalf("unexpected replay result: %+v err=%v", replay, err)
	}
	if writeTxQuest.deltaSaves.Load() != 1 {
		t.Fatalf("replay must not re-save: deltaSaves=%d", writeTxQuest.deltaSaves.Load())
	}
}

func TestP4RoomClearMapPostSaveFullReloadMissingOrPartialReturnsPersistVerify(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"missing", "partial"} {
		t.Run(mode, func(t *testing.T) {
			mem := dnfrepomemory.NewMemoryGroup()
			if err := mem.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "acct"}); err != nil {
				t.Fatal(err)
			}
			if err := mem.Quest.Save(ctx, roomClearMapActiveRecord()); err != nil {
				t.Fatal(err)
			}
			deltaQuest := &p4DeltaQuestRepo{
				base: mem.Quest,
				loadOverride: func(_ context.Context, id string) (dnfrepo.QuestRecord, bool, error) {
					if mode == "missing" {
						return dnfrepo.QuestRecord{}, false, nil
					}
					return dnfrepo.QuestRecord{CharacterID: id, PlanningPartial: true}, true, nil
				},
			}
			spyUOW := &p4SettlementSpyUOW{base: mem.CharacterSettlement, quest: deltaQuest}
			repos := mem
			repos.Quest = deltaQuest
			repos.CharacterSettlement = spyUOW
			owner, err := NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			res, err := owner.ApplyRoomClearMapCompletion(ctx, roomClearMapTestCatalog(t), "19", RoomClearMapInput{
				DungeonID:      82,
				MapID:          92247,
				LinkedQuestIDs: []int64{3560},
				CompletionKey:  "run/reload-" + mode,
				CompletedAt:    time.Date(2026, 11, 26, 4, 20, 0, 0, time.UTC),
			})
			if !errors.Is(err, ErrQuestPersistVerify) {
				t.Fatalf("mode=%s expected ErrQuestPersistVerify, got res=%+v err=%v", mode, res, err)
			}
			if res.Applied || len(res.Completions) != 0 || res.PostCommitQuest.CharacterID != "" {
				t.Fatalf("mode=%s failed reload must return zero result, got %+v", mode, res)
			}
		})
	}
}

func TestP4RoomClearMapDeltaStoreUsesBoundedLoadDeltaSaveAndCompletePostCommitQuest(t *testing.T) {
	ctx := context.Background()
	mem := dnfrepomemory.NewMemoryGroup()
	if err := mem.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "acct"}); err != nil {
		t.Fatal(err)
	}
	seed := roomClearMapActiveRecord()
	seed.States[9901] = dnfrepo.QuestState{Status: "active", ProgressValue: 7}
	seed.States[9902] = dnfrepo.QuestState{Status: "completed", ProgressValue: 0, Extra: map[string]string{"keep": "yes"}}
	if err := mem.Quest.Save(ctx, seed); err != nil {
		t.Fatal(err)
	}
	deltaQuest := &p4DeltaQuestRepo{base: mem.Quest}
	spyUOW := &p4SettlementSpyUOW{base: mem.CharacterSettlement, quest: deltaQuest}
	repos := mem
	repos.Quest = deltaQuest
	repos.CharacterSettlement = spyUOW

	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	input := RoomClearMapInput{
		DungeonID:      82,
		MapID:          92247,
		LinkedQuestIDs: []int64{3560},
		CompletionKey:  "run/delta-3560",
		CompletedAt:    time.Date(2026, 11, 26, 4, 5, 0, 0, time.UTC),
	}
	res, err := owner.ApplyRoomClearMapCompletion(ctx, roomClearMapTestCatalog(t), "19", input)
	if err != nil {
		t.Fatalf("ApplyRoomClearMapCompletion: %v", err)
	}
	if !res.Applied || len(res.Completions) != 1 || res.PostCommitQuest.States[3560].Status != "completed" ||
		res.PostCommitQuest.States[3525].ProgressValue != 0 {
		t.Fatalf("unexpected commit result: %+v", res)
	}
	// Unrelated quests must be preserved in both DB and PostCommitQuest (op574 + op356 fidelity).
	if res.PostCommitQuest.States[9901].ProgressValue != 7 || res.PostCommitQuest.States[9902].Extra["keep"] != "yes" {
		t.Fatalf("PostCommitQuest lost unrelated quests: %+v", res.PostCommitQuest.States)
	}
	if deltaQuest.idLoads.Load() != 1 || deltaQuest.deltaSaves.Load() != 1 || deltaQuest.fullSaves.Load() != 0 {
		t.Fatalf("expected bounded LoadQuestIDs + SaveQuestChanges: idLoads=%d deltaSaves=%d fullSaves=%d",
			deltaQuest.idLoads.Load(), deltaQuest.deltaSaves.Load(), deltaQuest.fullSaves.Load())
	}
	// Exactly 1 full Load is expected only to build complete PostCommitQuest after a completion matched.
	if deltaQuest.fullLoads.Load() != 1 {
		t.Fatalf("expected 1 post-commit full load for PostCommitQuest, got %d", deltaQuest.fullLoads.Load())
	}
	stored, found, err := mem.Quest.Load(ctx, "19")
	if err != nil || !found || !reflect.DeepEqual(stored, res.PostCommitQuest) {
		t.Fatalf("stored mismatch: found=%t err=%v\nstored=%+v\npost=%+v", found, err, stored, res.PostCommitQuest)
	}
}

func TestP4ClearMapOwnerUsesHuntPlanningAndBoundedVerify(t *testing.T) {
	ctx := context.Background()
	mem := dnfrepomemory.NewMemoryGroup()
	if err := mem.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "acct"}); err != nil {
		t.Fatal(err)
	}
	catalog := clearMapTestCatalog(t)
	seed := dnfrepo.QuestRecord{
		CharacterID: "19",
		States: map[int64]dnfrepo.QuestState{
			3145: {Status: "active", ProgressValue: 1},
			8888: {Status: "completed", ProgressValue: 0, Extra: map[string]string{"history": "preserved"}},
		},
	}
	if err := mem.Quest.Save(ctx, seed); err != nil {
		t.Fatal(err)
	}
	deltaQuest := &p4DeltaQuestRepo{base: mem.Quest}
	repos := mem
	repos.Quest = deltaQuest
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	input := ClearMapCompletionInput{
		DungeonID:     3,
		MapID:         76126,
		CompletionKey: "clear-3145-key",
		CompletedAt:   time.Date(2026, 11, 26, 4, 10, 0, 0, time.UTC),
	}
	res, err := owner.ApplyClearMapCompletion(ctx, catalog, "19", input)
	if err != nil {
		t.Fatalf("ApplyClearMapCompletion: %v", err)
	}
	if len(res.Completions) != 1 || res.Completions[0].QuestID != 3145 || res.Completions[0].RemainingTrigger != 0 || res.Idempotent {
		t.Fatalf("unexpected clear-map result: %+v", res)
	}
	if deltaQuest.fullLoads.Load() != 0 || deltaQuest.fullSaves.Load() != 0 {
		t.Fatalf("expected zero full quest loads/saves on delta store: fullLoads=%d fullSaves=%d",
			deltaQuest.fullLoads.Load(), deltaQuest.fullSaves.Load())
	}
	if deltaQuest.activeLoads.Load() != 1 || deltaQuest.deltaSaves.Load() != 1 || deltaQuest.idLoads.Load() != 1 {
		t.Fatalf("expected activePlanning + deltaSave + idVerify: active=%d delta=%d id=%d",
			deltaQuest.activeLoads.Load(), deltaQuest.deltaSaves.Load(), deltaQuest.idLoads.Load())
	}
	stored, found, err := mem.Quest.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("reload found=%t err=%v", found, err)
	}
	if stored.States[3145].ProgressValue != 0 || stored.States[3145].Extra["reward_state"] != clearMapRewardPending ||
		stored.States[8888].Extra["history"] != "preserved" {
		t.Fatalf("stored clear-map state corrupted: %+v", stored.States)
	}

	// Idempotent replay also performs zero full loads and zero saves.
	replay, err := owner.ApplyClearMapCompletion(ctx, catalog, "19", input)
	if err != nil || !replay.Idempotent || len(replay.ChangedFields) != 0 {
		t.Fatalf("replay mismatch: res=%+v err=%v", replay, err)
	}
	if deltaQuest.fullLoads.Load() != 0 || deltaQuest.deltaSaves.Load() != 1 {
		t.Fatalf("replay should not full-load or re-save: fullLoads=%d deltaSaves=%d",
			deltaQuest.fullLoads.Load(), deltaQuest.deltaSaves.Load())
	}
}

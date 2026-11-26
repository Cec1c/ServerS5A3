package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"maps"
	"strings"
	"sync/atomic"
	"testing"

	"longheng.io/server/internal/modules/dnf/buffswap"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	"longheng.io/server/internal/modules/dnf/raid"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

type p4NarrowCharacterRepo struct {
	base          dnfrepo.CharacterRepository
	fullLoads     atomic.Int64
	identityLoads atomic.Int64
	mirrorLoads   atomic.Int64
	weddingLoads  atomic.Int64
}

func (r *p4NarrowCharacterRepo) Load(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
	r.fullLoads.Add(1)
	return r.base.Load(ctx, id)
}

func (r *p4NarrowCharacterRepo) Save(ctx context.Context, record dnfrepo.CharacterRecord) error {
	return r.base.Save(ctx, record)
}

func (r *p4NarrowCharacterRepo) ListByAccount(ctx context.Context, accountID string, limit int) ([]dnfrepo.CharacterRecord, error) {
	return r.base.ListByAccount(ctx, accountID, limit)
}

func (r *p4NarrowCharacterRepo) FindIDByName(ctx context.Context, name string) (string, bool, error) {
	return r.base.FindIDByName(ctx, name)
}

func (r *p4NarrowCharacterRepo) NextNumericID(ctx context.Context) (int, error) {
	return r.base.NextNumericID(ctx)
}

func (r *p4NarrowCharacterRepo) LoadCharacterIdentity(ctx context.Context, id string) (dnfrepo.CharacterIdentity, bool, error) {
	r.identityLoads.Add(1)
	rec, found, err := r.base.Load(ctx, id)
	if err != nil || !found {
		return dnfrepo.CharacterIdentity{}, found, err
	}
	return dnfrepo.CharacterIdentity{CharacterID: rec.CharacterID, AccountID: rec.AccountID}, true, nil
}

func (r *p4NarrowCharacterRepo) LoadCharacterMirrorStats(ctx context.Context, id string, keys ...string) (dnfrepo.CharacterMirrorStats, bool, error) {
	r.mirrorLoads.Add(1)
	rec, found, err := r.base.Load(ctx, id)
	if err != nil || !found {
		return dnfrepo.CharacterMirrorStats{}, found, err
	}
	return dnfrepo.SelectCharacterMirrorStats(rec, keys...), true, nil
}

func (r *p4NarrowCharacterRepo) LoadCharacterWedding(ctx context.Context, id string) (dnfrepo.CharacterWeddingSnapshot, bool, error) {
	r.weddingLoads.Add(1)
	rec, found, err := r.base.Load(ctx, id)
	if err != nil || !found {
		return dnfrepo.CharacterWeddingSnapshot{}, found, err
	}
	snap := dnfrepo.CharacterWeddingSnapshot{
		CharacterID: rec.CharacterID,
		AccountID:   rec.AccountID,
		Stats:       make(map[string]int64),
	}
	for k, v := range rec.Stats {
		if dnfrepo.IsCharacterWeddingStat(k) {
			snap.Stats[k] = v
		}
	}
	return snap, true, nil
}

type p4NarrowInventoryRepo struct {
	base             dnfrepo.InventoryRepository
	fullLoads        atomic.Int64
	prefixFoundLoads atomic.Int64
	slotsLoads       atomic.Int64
}

func (r *p4NarrowInventoryRepo) Load(ctx context.Context, id string) (dnfrepo.InventoryRecord, bool, error) {
	r.fullLoads.Add(1)
	return r.base.Load(ctx, id)
}

func (r *p4NarrowInventoryRepo) Save(ctx context.Context, record dnfrepo.InventoryRecord) error {
	return r.base.Save(ctx, record)
}

func (r *p4NarrowInventoryRepo) LoadInventorySlotItemPrefixFound(ctx context.Context, characterID, prefix string, itemID int64) (map[string]dnfrepo.ItemStack, bool, error) {
	r.prefixFoundLoads.Add(1)
	rec, found, err := r.base.Load(ctx, characterID)
	if err != nil || !found || rec.CharacterID != characterID {
		return nil, false, err
	}
	var out map[string]dnfrepo.ItemStack
	for k, st := range rec.Slots {
		if !strings.HasPrefix(k, prefix) || st.ItemID != itemID {
			continue
		}
		if out == nil {
			out = make(map[string]dnfrepo.ItemStack)
		}
		out[k] = st
	}
	return out, true, nil
}

func (r *p4NarrowInventoryRepo) LoadInventorySlots(ctx context.Context, characterID string) (dnfrepo.InventoryRecord, bool, error) {
	r.slotsLoads.Add(1)
	rec, found, err := r.base.Load(ctx, characterID)
	if err != nil || !found {
		return dnfrepo.InventoryRecord{}, found, err
	}
	cloned := dnfrepo.CloneInventory(rec)
	cloned.Warehouse = nil
	return cloned, true, nil
}

type p4BuffReadSnapshotUOW struct {
	dnfrepo.CeraShopAssetUnitOfWork
	group dnfrepo.Group
}

func (u p4BuffReadSnapshotUOW) WithinBuffOwnedSnapshot(
	_ context.Context, _, _, _ string,
	apply func(dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository, dnfrepo.SettingsRepository, dnfrepo.BuffPetMetadataReader, dnfrepo.OwnedLegacyInventoryRepository) error,
) error {
	return apply(u.group.Character, u.group.Inventory, u.group.Equipment, u.group.Settings, u.group.BuffPetMetadata, u.group.OwnedLegacyInventory)
}

func (u p4BuffReadSnapshotUOW) WithinBuffOwnedReadSnapshot(
	_ context.Context, _, _, _ string,
	apply func(dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository, dnfrepo.SettingsRepository, dnfrepo.BuffPetMetadataReader, dnfrepo.OwnedLegacyInventoryRepository) error,
) error {
	return apply(u.group.Character, u.group.Inventory, u.group.Equipment, u.group.Settings, u.group.BuffPetMetadata, u.group.OwnedLegacyInventory)
}

func TestP4PartyFrameBatchUsesNarrowCharacterMirrorStats(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "acct-19",
		Name:        "SlayerP4",
		Job:         "0",
		Level:       90,
		Stats:       map[string]int64{dnfrepo.CharacterGrowTypeStatKey: 0x11, "unrelated": 999},
	}); err != nil {
		t.Fatal(err)
	}
	narrowChar := &p4NarrowCharacterRepo{base: repos.Character}
	repos.Character = narrowChar

	s := &Service{
		repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
	}
	session := &gameSession{selectedCharacterID: 19, characterGeneration: 1, accountID: "acct-19"}
	identity := boundGameSessionCharacter{session: session, character: 19, generation: 1}
	batch := newPartyFrameCharacterBatch(s)

	id, name, rec, ok := batch.loadCharacter(ctx, identity)
	if !ok || id != 19 || name != "SlayerP4" || rec.Level != 90 || rec.Job != "0" || characterGrowTypeByte(rec) != 0x11 {
		t.Fatalf("unexpected partyFrame loadCharacter: id=%d name=%q ok=%t rec=%+v", id, name, ok, rec)
	}
	// Second call hits synchronous batch cache without another repository query.
	_, _, _, ok2 := batch.loadCharacter(ctx, identity)
	if !ok2 {
		t.Fatal("cached loadCharacter returned false")
	}
	if narrowChar.fullLoads.Load() != 0 || narrowChar.mirrorLoads.Load() != 1 {
		t.Fatalf("expected 0 full loads and 1 mirror load, got full=%d mirror=%d",
			narrowChar.fullLoads.Load(), narrowChar.mirrorLoads.Load())
	}
}

func TestP4RaidAdmissionAndEntryCostUseNarrowInventoryPrefixFound(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	const ticketItemID = 10096296
	for id, account := range map[string]string{"19": "acct-a", "20": "acct-b", "21": "acct-c"} {
		if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: id, AccountID: account, Level: 90, Job: "0", Name: "Raid" + id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots: map[string]dnfrepo.ItemStack{
			"00:50": {ItemID: ticketItemID, Count: 2},
			"0:54":  {ItemID: ticketItemID, Count: 1},
			"1:50":  {ItemID: ticketItemID, Count: 7},
			"0:51":  {ItemID: 12345, Count: 99},
		},
		Warehouse: map[string]dnfrepo.ItemStack{
			"0:1": {ItemID: ticketItemID, Count: 10},
		},
	}); err != nil {
		t.Fatal(err)
	}
	// Character 20 has an inventory row with 0 tickets. Character 21 has NO inventory row.
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "20",
		Slots:       map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}

	narrowChar := &p4NarrowCharacterRepo{base: repos.Character}
	narrowInv := &p4NarrowInventoryRepo{base: repos.Inventory}
	repos.Character = narrowChar
	repos.Inventory = narrowInv

	s := &Service{
		repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
		dungeonRuleSource: questListTestSource{
			"etc/raid/anton.etc": "[ENTRY COST]\n10096296 1\n[WAITING ROOM]\n1 0\n[RAID START MINIMUM MEMBER]\n1\n[SINGLE RAID START MINIUM MEMBER]\n1\n[RAID MEMBER MAX]\n20\n",
		},
	}
	rule := raid.AdmissionRule{ItemID: ticketItemID, Count: 1}
	state := runtimeRaidState{
		RaidKey:  23<<16 | 1,
		LeaderID: 19,
		Members: []runtimeRaidMemberState{
			{CharID: 19, GroupIndex: 1, SlotOrder: 0},
			{CharID: 20, GroupIndex: 1, SlotOrder: 1},
			{CharID: 21, GroupIndex: 1, SlotOrder: 2},
		},
	}

	members, err := s.raidStartEntryCostMembers(ctx, state, rule)
	if err != nil {
		t.Fatalf("raidStartEntryCostMembers: %v", err)
	}
	if len(members) != 3 || members[0].Owned != 3 || members[1].Owned != 0 || members[2].Owned != 0 {
		t.Fatalf("unexpected member costs: %+v", members)
	}
	if narrowInv.fullLoads.Load() != 0 || narrowInv.prefixFoundLoads.Load() != 3 {
		t.Fatalf("raidStartEntryCostMembers expected 0 full loads and 3 prefixFound loads, got full=%d prefixFound=%d",
			narrowInv.fullLoads.Load(), narrowInv.prefixFoundLoads.Load())
	}

	// Session refresh helper must return 3 for 19, 0 for 20, and error for 21 (missing inventory row).
	sess19 := &gameSession{selectedCharacterID: 19, accountID: "acct-a", channel: channelcatalog.Channel{ID: 23, Type: dnfenum.RaidChannelType}}
	sess20 := &gameSession{selectedCharacterID: 20, accountID: "acct-b", channel: sess19.channel}
	sess21 := &gameSession{selectedCharacterID: 21, accountID: "acct-c", channel: sess19.channel}
	if got, err := s.runtimeRaidEntryCostOwnedForSession(sess19, rule); err != nil || got != 3 {
		t.Fatalf("sess19 owned=%d err=%v want 3", got, err)
	}
	if got, err := s.runtimeRaidEntryCostOwnedForSession(sess20, rule); err != nil || got != 0 {
		t.Fatalf("sess20 owned=%d err=%v want 0", got, err)
	}
	if _, err := s.runtimeRaidEntryCostOwnedForSession(sess21, rule); err == nil {
		t.Fatal("sess21 missing inventory row must fail snapshot check")
	}
	// Verify raid_admission status helper agrees on non-canonical "00:50" + excludes "1:50".
	admissionState := runtimeRaidState{
		RaidKey:  23<<16 | 1,
		LeaderID: 19,
		Members: []runtimeRaidMemberState{
			{CharID: 19, GroupIndex: 1, SlotOrder: 0},
			{CharID: 20, GroupIndex: 1, SlotOrder: 1},
		},
	}
	statuses, _, err := s.runtimeRaidEntryCostStatusesInContext(ctx, sess19, admissionState, nil)
	if err != nil {
		t.Fatalf("runtimeRaidEntryCostStatusesInContext: %v", err)
	}
	if len(statuses) != 2 || statuses[0].OwnedCount != 3 || !statuses[0].Ready || statuses[1].OwnedCount != 0 || statuses[1].Ready {
		t.Fatalf("unexpected admission statuses for non-canonical main bag key: %+v", statuses)
	}
	if narrowInv.fullLoads.Load() != 0 || narrowChar.fullLoads.Load() != 0 {
		t.Fatalf("expected zero full loads across raid entry cost helpers, got invFull=%d charFull=%d",
			narrowInv.fullLoads.Load(), narrowChar.fullLoads.Load())
	}
}

func TestP4RaidCurrentProfilesAndApplicationUseNarrowCharacterMirrorStats(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "acct-19",
		Name:        "RaidLeader",
		Job:         "3",
		Level:       90,
		Stats:       map[string]int64{dnfrepo.CharacterGrowTypeStatKey: 0x22},
	}); err != nil {
		t.Fatal(err)
	}
	narrowChar := &p4NarrowCharacterRepo{base: repos.Character}
	repos.Character = narrowChar

	s := &Service{
		repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
	}
	state := runtimeRaidState{
		RaidKey: 1<<24 | 23<<16 | 7,
		Members: []runtimeRaidMemberState{{CharID: 19}},
	}
	batch := &currentCrossSceneProfileBatch{ctx: ctx}
	body, err := s.currentRuntimeRaidProfileBody(state, batch)
	if err != nil {
		t.Fatalf("currentRaidProfilesBodyWithBatch: %v", err)
	}
	wantBody, err := raid.BuildCurrentRaidProfilesBody([]raid.CurrentProfileRecord{{
		CharKey: 19,
		Channel: 23,
		Server:  1,
		Level:   90,
		Job:     3,
		Grow:    0x22,
		Name:    "RaidLeader",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, wantBody) {
		t.Fatalf("profiles wire mismatch: got=%x want=%x", body, wantBody)
	}

	sess := &gameSession{
		selectedCharacterID: 19,
		accountID:           "acct-19",
		channel:             channelcatalog.Channel{ID: 23, Type: dnfenum.RaidChannelType},
	}
	appRow, ok := s.currentRaidApplicantRecord(sess, raid.CurrentOtherChannelJoinRequest{ClientValue: 1, NativeValue: 2})
	if !ok || appRow.CharKey != 19 || appRow.Level != 90 || appRow.Job != 3 || appRow.Grow != 0x22 || appRow.Name != "RaidLeader" {
		t.Fatalf("unexpected applicant record: ok=%t row=%+v", ok, appRow)
	}
	if narrowChar.fullLoads.Load() != 0 || narrowChar.mirrorLoads.Load() != 2 {
		t.Fatalf("expected 0 full loads and 2 mirror loads, got full=%d mirror=%d",
			narrowChar.fullLoads.Load(), narrowChar.mirrorLoads.Load())
	}
}

func TestP4DimensionDropCareerCharacterUsesNarrowMirrorStats(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "acct-19",
		Name:        "DimCard",
		Job:         "2",
		Level:       86,
		Stats:       map[string]int64{dnfrepo.CharacterGrowTypeStatKey: 0x13},
	}); err != nil {
		t.Fatal(err)
	}
	narrowChar := &p4NarrowCharacterRepo{base: repos.Character}
	repos.Character = narrowChar
	s := &Service{
		repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
	}
	rec, err := s.loadDimensionDropCareerCharacter(19)
	if err != nil {
		t.Fatalf("loadDimensionDropCareerCharacter: %v", err)
	}
	if rec.Job != "2" || rec.Level != 86 || rec.Stats[dnfrepo.CharacterGrowTypeStatKey] != 0x13 {
		t.Fatalf("unexpected career char: %+v", rec)
	}
	if _, err := s.loadDimensionDropCareerCharacter(999); err == nil {
		t.Fatal("missing character must return error")
	}
	if narrowChar.fullLoads.Load() != 0 || narrowChar.mirrorLoads.Load() != 2 {
		t.Fatalf("expected 0 full loads and 2 mirror loads, got full=%d mirror=%d",
			narrowChar.fullLoads.Load(), narrowChar.mirrorLoads.Load())
	}
}

func TestP4BuffSwapOwnedReadUsesNarrowCharacterAndSlots(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "acct-19",
		Name:        "BuffOwner",
		Job:         "4",
		Level:       90,
		Stats:       map[string]int64{"unrelated_stat": 123},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots: map[string]dnfrepo.ItemStack{
			"0:54": {ItemID: 100501, Count: 1, Extra: map[string]string{"native_buff_instance": "inst-1"}},
		},
		Warehouse: map[string]dnfrepo.ItemStack{
			"0:1": {ItemID: 999999, Count: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "19"}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Settings.Save(ctx, dnfrepo.SettingsRecord{
		Scope: buffswap.Scope("19"),
		Values: map[string]string{
			"state": `{"wire_profile":"current1426-1368","skill":100,"slots":{"0":{"list":0,"index":54,"item_id":100501,"instance":"inst-1","source_kind":"current_instance"}}}`,
		},
	}); err != nil {
		t.Fatal(err)
	}

	narrowChar := &p4NarrowCharacterRepo{base: repos.Character}
	narrowInv := &p4NarrowInventoryRepo{base: repos.Inventory}
	repos.Character = narrowChar
	repos.Inventory = narrowInv
	repos.CeraShopAssets = p4BuffReadSnapshotUOW{CeraShopAssetUnitOfWork: repos.CeraShopAssets, group: repos}

	proj, err := buffswap.LoadResolvedOwned(ctx, repos, "19", "acct-19")
	if err != nil {
		t.Fatalf("LoadResolvedOwned: %v", err)
	}
	if !proj.Found || proj.Owner.Name != "BuffOwner" || proj.Owner.Job != "4" || proj.Items[0].Binding.ItemID != 100501 {
		t.Fatalf("unexpected projection: %+v", proj)
	}
	if narrowChar.fullLoads.Load() != 0 || narrowInv.fullLoads.Load() != 0 {
		t.Fatalf("expected zero full Character/Inventory loads in buffswap read projection, got charFull=%d invFull=%d",
			narrowChar.fullLoads.Load(), narrowInv.fullLoads.Load())
	}
	if narrowChar.identityLoads.Load() != 2 || narrowChar.mirrorLoads.Load() != 1 || narrowInv.slotsLoads.Load() != 1 {
		t.Fatalf("expected 2 identity + 1 mirror + 1 slots load, got identity=%d mirror=%d slots=%d",
			narrowChar.identityLoads.Load(), narrowChar.mirrorLoads.Load(), narrowInv.slotsLoads.Load())
	}
}

type p4ActiveFilteringQuestRepo struct {
	base        dnfrepo.QuestRepository
	fullLoads   atomic.Int64
	headerLoads atomic.Int64
	idLoads     atomic.Int64
	activeLoads atomic.Int64
}

func (r *p4ActiveFilteringQuestRepo) Load(ctx context.Context, id string) (dnfrepo.QuestRecord, bool, error) {
	r.fullLoads.Add(1)
	return r.base.Load(ctx, id)
}

func (r *p4ActiveFilteringQuestRepo) Save(ctx context.Context, record dnfrepo.QuestRecord) error {
	return r.base.Save(ctx, record)
}

func (r *p4ActiveFilteringQuestRepo) SaveQuestChanges(ctx context.Context, before, after dnfrepo.QuestRecord, fields ...dnfrepo.QuestField) error {
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

func (r *p4ActiveFilteringQuestRepo) LoadQuestStatesByIDs(ctx context.Context, id string, ids ...int64) (dnfrepo.QuestStateHeaders, bool, error) {
	r.headerLoads.Add(1)
	rec, found, err := r.base.Load(ctx, id)
	if err != nil || !found {
		return dnfrepo.QuestStateHeaders{}, found, err
	}
	return dnfrepo.SelectQuestStateHeaders(rec, ids...), true, nil
}

func (r *p4ActiveFilteringQuestRepo) LoadQuestIDs(ctx context.Context, id string, ids []int64) (dnfrepo.QuestRecord, bool, error) {
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

func (r *p4ActiveFilteringQuestRepo) LoadQuestActivePlanning(ctx context.Context, id string) (dnfrepo.QuestRecord, bool, error) {
	r.activeLoads.Add(1)
	rec, found, err := r.base.Load(ctx, id)
	if err != nil || !found {
		return dnfrepo.QuestRecord{}, found, err
	}
	// Strip completed/archived quests exactly like MySQL LoadQuestActivePlanning.
	out := dnfrepo.QuestRecord{
		PlanningPartial: true,
		CharacterID:     rec.CharacterID,
		UpdatedAt:       rec.UpdatedAt,
	}
	for qid, st := range rec.States {
		if dnfrepo.QuestStatusActive(st.Status) {
			if out.States == nil {
				out.States = make(map[int64]dnfrepo.QuestState)
			}
			out.States[qid] = st
		}
	}
	for qid, st := range rec.Progress {
		if dnfrepo.QuestStatusActive(st.Status) {
			if out.Progress == nil {
				out.Progress = make(map[int64]dnfrepo.QuestState)
			}
			out.Progress[qid] = st
		}
	}
	return dnfrepo.CloneQuest(out), true, nil
}

func TestP4HellRegionQuestVerifiesArchivedSubQuestViaLoadQuestIDs(t *testing.T) {
	source := bridgeAbyssEpicFragmentPVF()
	source[worldmap.DefaultWorldMapList] = "39 `test.wdm`\n"
	source[dnfquest.DefaultList] = "10109 `sub_hell.qst`\n3145 `boss.qst`\n"
	// Zero-reward [sub] quest auto-archives directly to completed status on completion.
	source["n_quest/sub_hell.qst"] = "[grade] `[sub]`\n[level] 90 99\n[type] `[condition under clear]`\n[sub type] 23\n[int data] -2 39 -1 1\n[/int data]\n"
	source["n_quest/boss.qst"] = "[grade] `[epic]`\n[level] 1 99\n[type] `[clear map]`\n[int data] " + fmt.Sprint(testHellPartyHellMapID) + "\n"
	service, session, conn, runtime, _, repositories := prepareAbyssEpicFragmentRuntime(t, source)
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	service.questCatalog, err = dnfquest.Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.Quest.Save(context.Background(), dnfrepo.QuestRecord{
		CharacterID: "99",
		States: map[int64]dnfrepo.QuestState{
			10109: {Status: "active", ProgressValue: 1},
			3145:  {Status: "active", ProgressValue: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	filteredQuest := &p4ActiveFilteringQuestRepo{base: repositories.Quest}
	wrappedRepos := repositories
	wrappedRepos.Quest = filteredQuest
	service.repositoryProvider = func() (dnfrepo.Group, bool) { return wrappedRepos, true }

	keys := waveHellPartyActorKeys(t, runtime)
	killDungeonActors(t, service, session, keys[:len(keys)-1]...)
	conn.write.Reset()
	killDungeonActors(t, service, session, keys[len(keys)-1:]...)

	if !runtime.hellQuestCompletionSnapshotSent {
		t.Fatal("expected archived [sub] hell quest to verify via LoadQuestIDs and emit op574")
	}
	after, _, err := repositories.Quest.Load(context.Background(), "99")
	if err != nil {
		t.Fatal(err)
	}
	if st := after.States[10109]; st.Status != "completed" || st.ProgressValue != 0 {
		t.Fatalf("expected 10109 archived to completed/0, got %+v", st)
	}
	if filteredQuest.idLoads.Load() < 2 || filteredQuest.activeLoads.Load() < 1 || filteredQuest.fullLoads.Load() != 0 {
		t.Fatalf("expected LoadQuestIDs verify + LoadQuestActivePlanning snapshot with zero full loads, got idLoads=%d activeLoads=%d fullLoads=%d",
			filteredQuest.idLoads.Load(), filteredQuest.activeLoads.Load(), filteredQuest.fullLoads.Load())
	}
	// op574 body must carry remaining active quest 3145 and omit archived completed quest 10109.
	foundSnapshot := false
	for rest := conn.write.Bytes(); len(rest) > 0; {
		packet, next := splitGameServerUpperPacket(t, rest)
		rest = next
		if packet.Header.MsgID == currentActiveQuestSnapshotMsgID {
			if len(packet.Body) < 4 {
				t.Fatalf("short op574 body: %x", packet.Body)
			}
			count := int(binary.LittleEndian.Uint32(packet.Body[:4]))
			if len(packet.Body) != 4+count*6 {
				t.Fatalf("op574 count=%d len=%d", count, len(packet.Body))
			}
			active := make(map[uint16]uint32, count)
			for off := 4; off < len(packet.Body); off += 6 {
				active[binary.LittleEndian.Uint16(packet.Body[off:off+2])] = binary.LittleEndian.Uint32(packet.Body[off+2 : off+6])
			}
			if _, has3145 := active[3145]; !has3145 {
				t.Fatalf("op574 missing remaining active quest 3145: %+v", active)
			}
			if _, has10109 := active[10109]; has10109 {
				t.Fatalf("op574 unexpectedly included archived completed quest 10109: %+v", active)
			}
			foundSnapshot = true
		}
	}
	if !foundSnapshot {
		t.Fatal("expected op574 snapshot packet after archived [sub] hell quest completion")
	}
}

package dnfbridge

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

type perfCountingEquipRepo struct {
	dnfrepo.EquipmentRepository
	loads atomic.Int64
}

func (r *perfCountingEquipRepo) Load(ctx context.Context, id string) (dnfrepo.EquipmentRecord, bool, error) {
	r.loads.Add(1)
	if r.EquipmentRepository == nil {
		return dnfrepo.EquipmentRecord{}, false, nil
	}
	return r.EquipmentRepository.Load(ctx, id)
}

type perfCountingInvRepo struct {
	dnfrepo.InventoryRepository
	loads atomic.Int64
}

func (r *perfCountingInvRepo) Load(ctx context.Context, id string) (dnfrepo.InventoryRecord, bool, error) {
	r.loads.Add(1)
	if r.InventoryRepository == nil {
		return dnfrepo.InventoryRecord{}, false, nil
	}
	return r.InventoryRepository.Load(ctx, id)
}

type perfCountingAccountRepo struct {
	dnfrepo.AccountRepository
	loads atomic.Int64
	saves atomic.Int64
}

func (r *perfCountingAccountRepo) Load(ctx context.Context, id string) (dnfrepo.AccountRecord, bool, error) {
	r.loads.Add(1)
	return r.AccountRepository.Load(ctx, id)
}

func (r *perfCountingAccountRepo) Save(ctx context.Context, record dnfrepo.AccountRecord) error {
	r.saves.Add(1)
	return r.AccountRepository.Save(ctx, record)
}

type perfCountingQuestRepo struct {
	dnfrepo.QuestRepository
	loads atomic.Int64
}

func (r *perfCountingQuestRepo) Load(ctx context.Context, id string) (dnfrepo.QuestRecord, bool, error) {
	r.loads.Add(1)
	return r.QuestRepository.Load(ctx, id)
}

type perfCountingWeddingCharRepo struct {
	dnfrepo.CharacterRepository
	weddingLoads atomic.Int64
}

func (r *perfCountingWeddingCharRepo) LoadCharacterWedding(ctx context.Context, id string) (dnfrepo.CharacterWeddingSnapshot, bool, error) {
	r.weddingLoads.Add(1)
	return dnfrepo.LoadCharacterWedding(ctx, r.CharacterRepository, id)
}

func TestGuildRosterLightweightMode8SkipsEquipInvSkillAndPresenceWedding(t *testing.T) {
	f, repos, viewer1Rec := setupP1WeddingGuildRosterFixture(t)
	s := f.service
	ctx := context.Background()

	// Disable viewer1 wedding reservation to test ordinary guild roster priming.
	viewer1Rec.Stats["wedding_v1_phase"] = 0
	if err := dnfrepo.SaveCharacterFields(ctx, repos.Character, viewer1Rec, dnfrepo.CharacterFieldWedding); err != nil {
		t.Fatal(err)
	}
	// Seed equipment on peer 17 so full inspect vs lightweight cacheOnly mode8 differ visibly.
	if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "17",
		Entries: map[string]dnfrepo.EquipmentEntry{
			"12": {SlotIndex: 12, ItemID: 10010001},
			"13": {SlotIndex: 13, ItemID: 10020001},
		},
	}); err != nil {
		t.Fatal(err)
	}
	f.target.channel.ID, f.target.connectionTownActorOwnerChannel = 11, 11

	equipCounter := &perfCountingEquipRepo{EquipmentRepository: repos.Equipment}
	invCounter := &perfCountingInvRepo{InventoryRepository: repos.Inventory}
	skillCounter := &p0CountingSkillRepo{SkillRepository: repos.Skill}
	weddingCharCounter := &perfCountingWeddingCharRepo{CharacterRepository: repos.Character}
	repos.Equipment = equipCounter
	repos.Inventory = invCounter
	repos.Skill = skillCounter
	repos.Character = weddingCharCounter
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }

	// 1. Opening guild list sends lightweight cacheOnly mode8 with 0 equipment/inventory/skill loads.
	f.targetConn.write.Reset()
	if err := s.handleCurrentGuildMemberListForOpcode(f.target, uint16(dnfenum.CmdPacketGuildAllMemberList)); err != nil {
		t.Fatalf("guild roster open failed: %v", err)
	}
	if equipCounter.loads.Load() != 0 || invCounter.loads.Load() != 0 || skillCounter.loadsFor("17") != 0 {
		t.Fatalf("lightweight roster prime should skip heavy repos: equip=%d inv=%d skill=%d",
			equipCounter.loads.Load(), invCounter.loads.Load(), skillCounter.loadsFor("17"))
	}
	lightFrames := decodeMode8BodiesFromWire(t, f.targetConn.write.Bytes())
	if len(lightFrames) != 1 {
		t.Fatalf("expected 1 lightweight mode8 frame for actor 17, got %d", len(lightFrames))
	}

	// 2. Explicit View Info (cacheOnly=false) still loads equipment & skills and produces larger full mode8.
	f.targetConn.write.Reset()
	access, err := s.loadCurrentGuildMemberProfileAccess(ctx, f.target, currentGuildMemberProfileRequest{
		channel:   11,
		character: 17,
		kind:      0,
		directory: s.currentSceneActorRoute(),
	})
	if err != nil {
		t.Fatalf("loadCurrentGuildMemberProfileAccess failed: %v", err)
	}
	if _, _, err := s.sendCurrentGuildMemberProfileWithPolicyDelivered(ctx, access, false); err != nil {
		t.Fatalf("explicit view info failed: %v", err)
	}
	if equipCounter.loads.Load() == 0 || skillCounter.loadsFor("17") == 0 {
		t.Fatalf("explicit view info must still load full equipment and skills: equip=%d skill=%d",
			equipCounter.loads.Load(), skillCounter.loadsFor("17"))
	}
	fullFrames := decodeMode8BodiesFromWire(t, f.targetConn.write.Bytes())
	if len(fullFrames) != 1 {
		t.Fatalf("expected 1 full mode8 frame for view info, got %d", len(fullFrames))
	}
	if len(lightFrames[0]) >= len(fullFrames[0]) {
		t.Fatalf("expected lightweight mode8 (%dB) to be strictly smaller than full view-info mode8 (%dB)",
			len(lightFrames[0]), len(fullFrames[0]))
	}

	// 3. Presence broadcast still primes reserved-viewer wedding rosters.
	reg, err := dnfguild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	g, _, ok := reg.GuildForCharacter("17")
	if !ok {
		t.Fatal("missing guild for actor 17")
	}
	bound := boundGameSessionCharacter{session: f.target, character: f.target.selectedCharacterID, generation: f.target.characterGeneration}
	beforeWedding := weddingCharCounter.weddingLoads.Load()
	if err := s.sendCurrentGuildRosterDisplayShared(ctx, bound, g, uint16(dnfenum.CmdPacketGuildAllMemberList), true, false, "presence", false, nil, nil, nil); err != nil {
		t.Fatalf("presence guild broadcast failed: %v", err)
	}
	if got := weddingCharCounter.weddingLoads.Load() - beforeWedding; got == 0 {
		t.Fatal("presence guild roster broadcast must prime the reserved viewer's wedding roster")
	}
}

func decodeMode8BodiesFromWire(t *testing.T, raw []byte) [][]byte {
	t.Helper()
	var bodies [][]byte
	for len(raw) > 0 {
		pkt, rest := splitCurrentGameServerUpperPacketAuto(t, raw)
		raw = rest
		if pkt.Header.Classification == 0 && pkt.Header.MsgID == 2 && len(pkt.Body) > 0 && pkt.Body[0] == 8 {
			bodies = append(bodies, append([]byte(nil), pkt.Body...))
		}
	}
	return bodies
}

func TestAdventureGroupDailyLoginSameDayFastPathSkipsSaveAndReload(t *testing.T) {
	shanghai := time.FixedZone("Asia/Shanghai", 8*3600)
	now := time.Now().In(shanghai)
	todayStr := now.Format(time.DateOnly)
	yesterdayStr := now.AddDate(0, 0, -1).Format(time.DateOnly)

	sameDayAccount := dnfrepo.AccountRecord{
		AccountID:            "dnf:adv1",
		RepresentAccountName: "TestAdv",
		Metadata: map[string]string{
			adventuregroup.DailyLoginStateMetadataKey: todayStr + "|7",
			adventureGroupCreatedDateMetadataKey:      "2026-01-02",
		},
	}
	if days, recorded, err := adventuregroup.DailyLoginRecordedForDate(sameDayAccount, now); err != nil || !recorded || days != 7 {
		t.Fatalf("same-day check got days=%d recorded=%t err=%v, want 7/true/nil", days, recorded, err)
	}
	prevDayAccount := dnfrepo.AccountRecord{
		AccountID: "dnf:adv1",
		Metadata: map[string]string{
			adventuregroup.DailyLoginStateMetadataKey: yesterdayStr + "|6",
		},
	}
	if days, recorded, err := adventuregroup.DailyLoginRecordedForDate(prevDayAccount, now); err != nil || recorded || days != 6 {
		t.Fatalf("prev-day check got days=%d recorded=%t err=%v, want 6/false/nil", days, recorded, err)
	}

	repos := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	if err := repos.Account.Save(ctx, sameDayAccount); err != nil {
		t.Fatal(err)
	}
	charRec := dnfrepo.CharacterRecord{
		CharacterID: "501",
		AccountID:   "dnf:adv1",
		Name:        "AdvHero",
		Level:       90,
		Job:         "slayer",
		Stats:       map[string]int64{},
	}
	if err := repos.Character.Save(ctx, charRec); err != nil {
		t.Fatal(err)
	}

	acctCounter := &perfCountingAccountRepo{AccountRepository: repos.Account}
	repos.Account = acctCounter

	s := &Service{
		adventureGroupTable: loadAdventureGroupTestTables(t),
		repositoryProvider:  func() (dnfrepo.Group, bool) { return repos, true },
	}
	conn := &bufferConn{}
	session := &gameSession{
		conn:                conn,
		connID:              "adv-fast-1",
		accountID:           "dnf:adv1",
		selectedCharacterID: 501,
	}

	state, err := s.currentAccountAdventureGroupInfoState(ctx, charRec, true, session)
	if err != nil {
		t.Fatalf("currentAccountAdventureGroupInfoState failed: %v", err)
	}
	if !state.HasAccount || state.Projection.ConsecutiveLoginDays != 7 {
		t.Fatalf("unexpected state: HasAccount=%t days=%d", state.HasAccount, state.Projection.ConsecutiveLoginDays)
	}
	if acctCounter.saves.Load() != 0 {
		t.Fatalf("same-day fast path must not write Account, got %d saves", acctCounter.saves.Load())
	}
	loadsAfterInfo := acctCounter.loads.Load()
	if loadsAfterInfo > 2 {
		t.Fatalf("expected at most 2 Account reads (honor + 1 Load) on same-day fast path, got %d", loadsAfterInfo)
	}

	// Pushing op1340 from cached state performs 0 additional Account.Load calls and matches fresh push byte-for-byte.
	if err := s.sendCurrentAdventureInfoPushFromState(ctx, session, 501, state, nil, "test_fast"); err != nil {
		t.Fatalf("sendCurrentAdventureInfoPushFromState failed: %v", err)
	}
	if acctCounter.loads.Load() != loadsAfterInfo {
		t.Fatalf("sendCurrentAdventureInfoPushFromState should perform 0 extra Account.Load calls, got +%d",
			acctCounter.loads.Load()-loadsAfterInfo)
	}
	fastPkt, fastRest := splitCurrentGameServerUpperPacketAuto(t, conn.write.Bytes())
	if len(fastRest) != 0 {
		t.Fatalf("unexpected trailing bytes after fast-path op1340: %d", len(fastRest))
	}
	conn.write.Reset()
	if err := s.sendCurrentAdventureInfoPushFromAccount(session, 501, "test_fresh"); err != nil {
		t.Fatalf("sendCurrentAdventureInfoPushFromAccount failed: %v", err)
	}
	freshPkt, freshRest := splitCurrentGameServerUpperPacketAuto(t, conn.write.Bytes())
	if len(freshRest) != 0 {
		t.Fatalf("unexpected trailing bytes after fresh op1340: %d", len(freshRest))
	}
	if fastPkt.Header.MsgID != freshPkt.Header.MsgID || fastPkt.Header.Classification != freshPkt.Header.Classification || !bytes.Equal(fastPkt.Body, freshPkt.Body) {
		t.Fatalf("fast-path op1340 payload differs from fresh push (fast=%dB fresh=%dB)", len(fastPkt.Body), len(freshPkt.Body))
	}
}

func TestSelectInventoryBootstrapReusesSnapshotAcrossContainersAndLock(t *testing.T) {
	repos := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	charRec := dnfrepo.CharacterRecord{
		CharacterID: "601",
		AccountID:   "dnf:inv1",
		Name:        "InvHero",
		Level:       90,
		Job:         "slayer",
		Stats:       map[string]int64{"gold": 12345},
	}
	if err := repos.Character.Save(ctx, charRec); err != nil {
		t.Fatal(err)
	}
	invRec := dnfrepo.InventoryRecord{
		CharacterID: "601",
		Slots: map[string]dnfrepo.ItemStack{
			"0:9":  {ItemID: 1001, Count: 1, Extra: map[string]string{"locked": "1"}},
			"1:0":  {ItemID: 2001, Count: 1},
			"7:0":  {ItemID: 3001, Count: 1},
			"38:0": {ItemID: 4001, Count: 1},
		},
		Warehouse: map[string]dnfrepo.ItemStack{
			"2:0": {ItemID: 5001, Count: 3},
		},
	}
	if err := repos.Inventory.Save(ctx, invRec); err != nil {
		t.Fatal(err)
	}
	invCounter := &perfCountingInvRepo{InventoryRepository: repos.Inventory}
	repos.Inventory = invCounter

	s := &Service{
		repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
	}
	conn := &bufferConn{}
	session := &gameSession{
		conn:                   conn,
		connID:                 "inv-boot-1",
		accountID:              "dnf:inv1",
		selectedCharacterID:    601,
		itemListMigrationsDone: true,
	}

	// Build each container individually first (reference bodies without snapshot reuse).
	wantBodies := make(map[byte][]byte)
	for _, lt := range []byte{0, 1, 2, 7, 38} {
		b, _, _, ok := s.buildCurrentSelectInventoryBootstrapBody(ctx, session, lt)
		if !ok {
			t.Fatalf("reference build list %d failed", lt)
		}
		wantBodies[lt] = b
	}
	wantLock, ok := s.buildCurrentItemLockSnapshotForSession(ctx, session)
	if !ok {
		t.Fatal("reference lock snapshot failed")
	}

	// Now build all 5 inventory lists + lock snapshot using one stack-local bootstrapSnapshot.
	invCounter.loads.Store(0)
	var snap selectInventoryBootstrapSnapshot
	for _, lt := range []byte{0, 1, 2, 7, 38} {
		got, _, _, ok := s.buildCurrentSelectInventoryBootstrapBodyWithSnapshot(ctx, session, lt, &snap)
		if !ok {
			t.Fatalf("snapshot build list %d failed", lt)
		}
		if !bytes.Equal(got, wantBodies[lt]) {
			t.Fatalf("list %d body mismatch with snapshot reuse", lt)
		}
	}
	gotLock, ok := s.buildCurrentItemLockSnapshotForSessionWithSnapshot(ctx, session, &snap)
	if !ok {
		t.Fatal("snapshot lock build failed")
	}
	if !bytes.Equal(gotLock, wantLock) {
		t.Fatal("item lock snapshot mismatch with snapshot reuse")
	}
	// List 0 does preflight Load + post-reconcile Load (2); lists 1,2,7,38 + lock do 0!
	if loads := invCounter.loads.Load(); loads > 2 {
		t.Fatalf("expected <=2 Inventory.Load calls across lists 0,1,2,7,38 + lock, got %d", loads)
	}
}

func TestSharedInitialTownQuestSnapshotMatchesFreshLoad(t *testing.T) {
	repos := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	charRec := dnfrepo.CharacterRecord{
		CharacterID: "701",
		AccountID:   "dnf:q1",
		Name:        "QuestHero",
		Level:       90,
		Job:         "slayer",
		Stats:       map[string]int64{},
	}
	if err := repos.Character.Save(ctx, charRec); err != nil {
		t.Fatal(err)
	}
	questRec := dnfrepo.QuestRecord{
		CharacterID: "701",
		States: map[int64]dnfrepo.QuestState{
			100: {Status: "completed"},
			200: {Status: "active", ProgressValue: 3},
		},
	}
	if err := repos.Quest.Save(ctx, questRec); err != nil {
		t.Fatal(err)
	}
	questCounter := &perfCountingQuestRepo{QuestRepository: repos.Quest}
	repos.Quest = questCounter

	s := &Service{
		questCatalog:       buildQuestListTestCatalog(t),
		repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
	}
	conn := &bufferConn{}
	session := &gameSession{
		conn:                conn,
		connID:              "quest-shared-1",
		accountID:           "dnf:q1",
		selectedCharacterID: 701,
	}

	sharedRec, sharedFound, sharedReady, err := s.sendCurrentInitialTownQuestSnapshotsWithSnapshotLocked(ctx, session, "test_shared")
	if err != nil {
		t.Fatalf("sendCurrentInitialTownQuestSnapshotsWithSnapshotLocked failed: %v", err)
	}
	if !sharedReady || !sharedFound {
		t.Fatalf("expected sharedReady=true sharedFound=true, got ready=%t found=%t", sharedReady, sharedFound)
	}
	sharedOp356, err := s.buildCurrentClearQuestListTransportBodyForLoadedRecord(ctx, session, sharedRec, sharedFound, "test_shared")
	if err != nil {
		t.Fatalf("buildCurrentClearQuestListTransportBodyForLoadedRecord failed: %v", err)
	}
	if loads := questCounter.loads.Load(); loads != 1 {
		t.Fatalf("expected exactly 1 Quest.Load across op21 + op574 + op356, got %d", loads)
	}

	freshOp356, err := s.buildCurrentClearQuestListTransportBodyForSession(ctx, session, "test_fresh")
	if err != nil {
		t.Fatalf("buildCurrentClearQuestListTransportBodyForSession failed: %v", err)
	}
	if !bytes.Equal(sharedOp356, freshOp356) {
		t.Fatal("shared op356 transport body differs from fresh Quest.Load op356 body")
	}
}

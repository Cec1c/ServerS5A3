package onlineevent

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

type ownerMetadataWrite struct {
	AccountID string
	Key       string
	Value     string
	At        time.Time
}

type recordingMetadataRepository struct {
	base      dnfrepo.AccountRepository
	fullSaves int
	writes    []ownerMetadataWrite
	writeErr  error
}

var (
	_ dnfrepo.AccountRepository          = (*recordingMetadataRepository)(nil)
	_ dnfrepo.AccountMetadataEntryWriter = (*recordingMetadataRepository)(nil)
)

func (r *recordingMetadataRepository) Load(ctx context.Context, accountID string) (dnfrepo.AccountRecord, bool, error) {
	return r.base.Load(ctx, accountID)
}

func (r *recordingMetadataRepository) Save(ctx context.Context, record dnfrepo.AccountRecord) error {
	r.fullSaves++
	return r.base.Save(ctx, record)
}

func (r *recordingMetadataRepository) SaveMetadataEntry(ctx context.Context, accountID, key, value string, at time.Time) error {
	if r.writeErr != nil {
		return r.writeErr
	}
	r.writes = append(r.writes, ownerMetadataWrite{AccountID: accountID, Key: key, Value: value, At: at})
	writer, ok := r.base.(dnfrepo.AccountMetadataEntryWriter)
	if !ok {
		return dnfrepo.ErrAccountMetadataUnavailable
	}
	return writer.SaveMetadataEntry(ctx, accountID, key, value, at)
}

// plainSaveAccount exposes only db.Store, mimicking a store without the
// single-key writer so the fallback path stays covered.
type plainSaveAccount struct {
	base  dnfrepo.AccountRepository
	saves int
}

var _ dnfrepo.AccountRepository = (*plainSaveAccount)(nil)

func (r *plainSaveAccount) Load(ctx context.Context, accountID string) (dnfrepo.AccountRecord, bool, error) {
	return r.base.Load(ctx, accountID)
}

func (r *plainSaveAccount) Save(ctx context.Context, record dnfrepo.AccountRecord) error {
	r.saves++
	return r.base.Save(ctx, record)
}

// staticSettlement runs the owner callback against one fixed repository group
// so the tests can observe the account writer directly.
type staticSettlement struct{ group dnfrepo.Group }

func (s staticSettlement) WithinCharacterSettlement(_ context.Context, _ string, apply func(dnfrepo.Group) error) error {
	return apply(s.group)
}

func ownerMetadataKeys(count int) map[string]string {
	metadata := make(map[string]string, count+1)
	for i := 0; i < count; i++ {
		metadata[fmt.Sprintf("unrelated:%03d", i)] = fmt.Sprintf("keep-%03d", i)
	}
	return metadata
}

func ownerMetadataDefinition() Definition {
	return Definition{
		ID:       "daily-online",
		Calendar: time.FixedZone("Asia/Shanghai", 8*60*60),
		Stages: []Stage{{
			ID:              "ten_seconds",
			RequiredSeconds: 10,
			Gold:            5,
		}},
	}
}

type ownerMetadataTestEnv struct {
	repositories dnfrepo.Group
	recorder     *recordingMetadataRepository
	owner        *Owner
}

func newOwnerMetadataFixture(t *testing.T, writeErr error) ownerMetadataTestEnv {
	t.Helper()
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: "account-1",
		Metadata:  ownerMetadataFixtureMetadata(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "character-1",
		AccountID:   "account-1",
		Stats:       map[string]int64{"gold": 7},
	}); err != nil {
		t.Fatal(err)
	}
	recorder := &recordingMetadataRepository{base: repositories.Account, writeErr: writeErr}
	settlement := staticSettlement{group: dnfrepo.Group{
		Account:   recorder,
		Character: repositories.Character,
		Inventory: repositories.Inventory,
	}}
	owner, err := NewOwner(dnfrepo.Group{CharacterSettlement: settlement})
	if err != nil {
		t.Fatal(err)
	}
	return ownerMetadataTestEnv{repositories: repositories, recorder: recorder, owner: owner}
}

func ownerMetadataFixtureMetadata() map[string]string {
	return ownerMetadataKeys(128)
}

func TestSaveStateWritesOnlyEventAndChangedCumulativeKeys(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	unrelated := ownerMetadataFixtureMetadata()
	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "account-1", Metadata: unrelated}); err != nil {
		t.Fatal(err)
	}
	recorder := &recordingMetadataRepository{base: repositories.Account}
	account, found, err := recorder.Load(ctx, "account-1")
	if err != nil || !found {
		t.Fatalf("load found=%t err=%v", found, err)
	}
	state := newState("daily-online")
	state.CalendarDate = "2026-08-03"
	cumulativeKey := cumulativeKeyPrefix + "daily-online"
	account.Metadata[cumulativeKey] = "cumulative-ledger"
	now := time.Unix(2_000_000_300, 0).UTC()

	if err := saveState(ctx, recorder, account, state, now, cumulativeKey); err != nil {
		t.Fatal(err)
	}
	if recorder.fullSaves != 0 {
		t.Fatalf("full account saves=%d, want 0", recorder.fullSaves)
	}
	if len(recorder.writes) != 2 {
		t.Fatalf("metadata writes=%+v, want 2", recorder.writes)
	}
	if recorder.writes[0].Key != metadataKey(state.EventID) || recorder.writes[1].Key != cumulativeKey {
		t.Fatalf("metadata write keys=%+v", recorder.writes)
	}
	if recorder.writes[0].AccountID != "account-1" || !recorder.writes[0].At.Equal(now) {
		t.Fatalf("state write=%+v", recorder.writes[0])
	}
	if _, err := parseState(recorder.writes[0].Value, ownerMetadataDefinition()); err != nil {
		t.Fatalf("encoded state is not parseable: %v", err)
	}

	stored, found, err := repositories.Account.Load(ctx, "account-1")
	if err != nil || !found {
		t.Fatalf("reload found=%t err=%v", found, err)
	}
	if len(stored.Metadata) != len(unrelated)+2 {
		t.Fatalf("metadata rows=%d, want %d", len(stored.Metadata), len(unrelated)+2)
	}
	for key, value := range unrelated {
		if stored.Metadata[key] != value {
			t.Fatalf("unrelated key %q changed: %q", key, stored.Metadata[key])
		}
	}
}

func TestSaveStateDeduplicatesChangedKeys(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	recorder := &recordingMetadataRepository{base: repositories.Account}
	account, _, err := recorder.Load(ctx, "account-1")
	if err != nil {
		t.Fatal(err)
	}
	state := newState("daily-online")
	state.CalendarDate = "2026-08-03"
	stateKey := metadataKey(state.EventID)
	if err := saveState(ctx, recorder, account, state, time.Now(), stateKey, "", stateKey); err != nil {
		t.Fatal(err)
	}
	if len(recorder.writes) != 1 || recorder.writes[0].Key != stateKey {
		t.Fatalf("metadata writes=%+v, want one state key", recorder.writes)
	}
}

func TestSaveStateKeepsFullSaveFallbackWithoutWriter(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	plain := &plainSaveAccount{base: repositories.Account}
	account, _, err := plain.Load(ctx, "account-1")
	if err != nil {
		t.Fatal(err)
	}
	state := newState("daily-online")
	state.CalendarDate = "2026-08-03"
	if err := saveState(ctx, plain, account, state, time.Now()); err != nil {
		t.Fatal(err)
	}
	if plain.saves != 1 {
		t.Fatalf("fallback saves=%d, want 1", plain.saves)
	}
	stored, _, err := repositories.Account.Load(ctx, "account-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseState(stored.Metadata[metadataKey(state.EventID)], ownerMetadataDefinition()); err != nil {
		t.Fatalf("fallback did not persist state: %v", err)
	}
}

func TestOwnerMetadataWritesLeaveUnrelatedKeysUntouched(t *testing.T) {
	ctx := context.Background()
	fixture := newOwnerMetadataFixture(t, nil)
	definition := ownerMetadataDefinition()
	location := definition.calendar()
	base := time.Date(2026, 8, 3, 10, 0, 0, 0, location)

	if _, err := fixture.owner.Observe(ctx, ObserveCommand{
		AccountID:    "account-1",
		CharacterID:  "character-1",
		Definition:   definition,
		IntervalFrom: base,
		IntervalTo:   base.Add(10 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if fixture.recorder.fullSaves != 0 {
		t.Fatalf("observe full account saves=%d, want 0", fixture.recorder.fullSaves)
	}
	if len(fixture.recorder.writes) != 1 || fixture.recorder.writes[0].Key != metadataKey(definition.ID) {
		t.Fatalf("observe writes=%+v", fixture.recorder.writes)
	}

	claimedAt := base.Add(11 * time.Second)
	if _, err := fixture.owner.Claim(ctx, ClaimCommand{
		AccountID:   "account-1",
		CharacterID: "character-1",
		Definition:  definition,
		StageID:     "ten_seconds",
		ClaimedAt:   claimedAt,
	}); err != nil {
		t.Fatal(err)
	}
	if fixture.recorder.fullSaves != 0 || len(fixture.recorder.writes) != 2 {
		t.Fatalf("claim saves=%d writes=%+v", fixture.recorder.fullSaves, fixture.recorder.writes)
	}
	if fixture.recorder.writes[1].Key != metadataKey(definition.ID) {
		t.Fatalf("claim wrote unexpected key: %+v", fixture.recorder.writes[1])
	}

	// A replay without a new qualified day must not write anything.
	if _, err := fixture.owner.Claim(ctx, ClaimCommand{
		AccountID:   "account-1",
		CharacterID: "character-1",
		Definition:  definition,
		StageID:     "ten_seconds",
		ClaimedAt:   claimedAt,
	}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.recorder.writes) != 2 {
		t.Fatalf("replay writes=%+v", fixture.recorder.writes)
	}

	stored, found, err := fixture.repositories.Account.Load(ctx, "account-1")
	if err != nil || !found {
		t.Fatalf("reload found=%t err=%v", found, err)
	}
	if len(stored.Metadata) != 129 {
		t.Fatalf("metadata rows=%d, want 129", len(stored.Metadata))
	}
	for key, value := range ownerMetadataFixtureMetadata() {
		if stored.Metadata[key] != value {
			t.Fatalf("unrelated key %q changed: %q", key, stored.Metadata[key])
		}
	}
	if _, err := parseState(stored.Metadata[metadataKey(definition.ID)], definition); err != nil {
		t.Fatalf("persisted state is not parseable: %v", err)
	}
}

var errOwnerMetadataWrite = errors.New("injected owner metadata write failure")

func TestOwnerMetadataWriteFailurePropagates(t *testing.T) {
	ctx := context.Background()
	fixture := newOwnerMetadataFixture(t, errOwnerMetadataWrite)
	definition := ownerMetadataDefinition()
	location := definition.calendar()
	base := time.Date(2026, 8, 3, 10, 0, 0, 0, location)

	_, err := fixture.owner.Observe(ctx, ObserveCommand{
		AccountID:    "account-1",
		CharacterID:  "character-1",
		Definition:   definition,
		IntervalFrom: base,
		IntervalTo:   base.Add(10 * time.Second),
	})
	if !errors.Is(err, errOwnerMetadataWrite) {
		t.Fatalf("err=%v, want injected metadata failure", err)
	}
	if fixture.recorder.fullSaves != 0 {
		t.Fatalf("failure path fell back to a full account save: %d", fixture.recorder.fullSaves)
	}
	stored, _, err := fixture.repositories.Account.Load(ctx, "account-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, found := stored.Metadata[metadataKey(definition.ID)]; found {
		t.Fatal("failed metadata write was still persisted")
	}
}

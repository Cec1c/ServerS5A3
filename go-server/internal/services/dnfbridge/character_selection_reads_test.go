package dnfbridge

import (
	"context"
	"errors"
	"strconv"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type selectionReadsFixture struct {
	account        string
	identities     []dnfrepo.CharacterRecord
	records        map[string]dnfrepo.CharacterRecord
	rosterIdentity dnfrepo.CharacterRecord
}

func newSelectionReadsFixture(count int) selectionReadsFixture {
	account := defaultAccountPrefix + "selection-reads-" + strconv.Itoa(count)
	fixture := selectionReadsFixture{
		account:    account,
		identities: make([]dnfrepo.CharacterRecord, 0, count),
		records:    make(map[string]dnfrepo.CharacterRecord, count),
	}
	for slot := 0; slot < count; slot++ {
		id := strconv.Itoa(700 + slot)
		identity := dnfrepo.CharacterRecord{
			CharacterID: id,
			AccountID:   account,
			Slot:        slot,
		}
		fixture.identities = append(fixture.identities, identity)
		fixture.records[id] = dnfrepo.CharacterRecord{
			CharacterID: id,
			AccountID:   account,
			Slot:        slot,
			Name:        "selection_" + strconv.Itoa(slot),
			Job:         "0",
			Level:       70,
			Stats: map[string]int64{
				"delete_flag":             0,
				"selection_probe_marker":  int64(1000 + slot),
				"selection_probe_profile": int64(slot + 1),
			},
		}
	}
	// The two-byte legacy request addresses row zero from the roster sent to
	// this connection. Keep the selected SQL slot away from zero whenever there
	// is another row, so an accidental SQL-slot lookup resolves the wrong row.
	fixture.rosterIdentity = fixture.identities[count-1]
	return fixture
}

type selectionReadsCharacterRepository struct {
	dnfrepo.CharacterRepository
	identities []dnfrepo.CharacterRecord

	loadIDs            []string
	listByAccountCalls int
	listIdentityCalls  int
	identityAccount    string
	identityLimit      int
}

func (r *selectionReadsCharacterRepository) Load(ctx context.Context, characterID string) (dnfrepo.CharacterRecord, bool, error) {
	r.loadIDs = append(r.loadIDs, characterID)
	return r.CharacterRepository.Load(ctx, characterID)
}

func (r *selectionReadsCharacterRepository) ListByAccount(ctx context.Context, account string, limit int) ([]dnfrepo.CharacterRecord, error) {
	r.listByAccountCalls++
	return r.CharacterRepository.ListByAccount(ctx, account, limit)
}

func (r *selectionReadsCharacterRepository) ListCharacterIdentities(_ context.Context, account string, limit int) ([]dnfrepo.CharacterRecord, error) {
	r.listIdentityCalls++
	r.identityAccount = account
	r.identityLimit = limit
	if limit <= 0 {
		limit = defaultCharacterSlots
	}
	out := make([]dnfrepo.CharacterRecord, 0, limit)
	for _, identity := range r.identities {
		if identity.AccountID != account || identity.Stats["delete_flag"] != 0 {
			continue
		}
		out = append(out, dnfrepo.CloneCharacter(identity))
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

type selectionReadsFallbackCharacterRepository struct {
	dnfrepo.CharacterRepository
	listByAccountCalls int
}

func (r *selectionReadsFallbackCharacterRepository) ListByAccount(ctx context.Context, account string, limit int) ([]dnfrepo.CharacterRecord, error) {
	r.listByAccountCalls++
	return r.CharacterRepository.ListByAccount(ctx, account, limit)
}

type selectionReadsEquipmentRepository struct {
	dnfrepo.EquipmentRepository
	loadIDs []string
}

func (r *selectionReadsEquipmentRepository) Load(ctx context.Context, characterID string) (dnfrepo.EquipmentRecord, bool, error) {
	r.loadIDs = append(r.loadIDs, characterID)
	return r.EquipmentRepository.Load(ctx, characterID)
}

type selectionReadsQuestRepository struct {
	dnfrepo.QuestRepository
	loadIDs []string
}

func (r *selectionReadsQuestRepository) Load(ctx context.Context, characterID string) (dnfrepo.QuestRecord, bool, error) {
	r.loadIDs = append(r.loadIDs, characterID)
	return r.QuestRepository.Load(ctx, characterID)
}

func selectionReadsTestGroup(fixture selectionReadsFixture) (
	dnfrepo.Group,
	*selectionReadsCharacterRepository,
	*selectionReadsEquipmentRepository,
	*selectionReadsQuestRepository,
) {
	repos := testRepositoryGroup()
	store := repos.Character.(*fakeCharacterStore)
	store.records = fixture.records

	characters := &selectionReadsCharacterRepository{
		CharacterRepository: store,
		identities:          fixture.identities,
	}
	equipment := &selectionReadsEquipmentRepository{EquipmentRepository: repos.Equipment}
	quests := &selectionReadsQuestRepository{QuestRepository: repos.Quest}
	repos.Character = characters
	repos.Equipment = equipment
	repos.Quest = quests
	return repos, characters, equipment, quests
}

func TestCharacterSelectionIdentitiesUsesNarrowReaderAtRosterBoundaries(t *testing.T) {
	for _, count := range []int{1, 16, 17, 32} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			fixture := newSelectionReadsFixture(count)
			_, characters, _, _ := selectionReadsTestGroup(fixture)

			got, err := characterSelectionIdentities(context.Background(), characters, fixture.account)
			if err != nil {
				t.Fatalf("read selection identities: %v", err)
			}
			if len(got) != count {
				t.Fatalf("identity rows=%d, want %d", len(got), count)
			}
			for index, identity := range got {
				want := fixture.identities[index]
				if identity.CharacterID != want.CharacterID || identity.AccountID != fixture.account || identity.Slot != want.Slot {
					t.Fatalf("identity row %d=%+v, want id=%s account=%s slot=%d", index, identity, want.CharacterID, fixture.account, want.Slot)
				}
			}
			if characters.listIdentityCalls != 1 || characters.identityAccount != fixture.account || characters.identityLimit != defaultCharacterSlots {
				t.Fatalf("narrow calls=%d account=%q limit=%d, want one account=%q limit=%d", characters.listIdentityCalls, characters.identityAccount, characters.identityLimit, fixture.account, defaultCharacterSlots)
			}
			if characters.listByAccountCalls != 0 {
				t.Fatalf("narrow read called full ListByAccount %d times", characters.listByAccountCalls)
			}
			if len(characters.loadIDs) != 0 {
				t.Fatalf("identity listing loaded full records: %v", characters.loadIDs)
			}
		})
	}
}

func TestCharacterSelectionIdentitiesFallsBackWithoutNarrowReader(t *testing.T) {
	fixture := newSelectionReadsFixture(17)
	repos := testRepositoryGroup()
	store := repos.Character.(*fakeCharacterStore)
	store.records = fixture.records
	fallback := &selectionReadsFallbackCharacterRepository{CharacterRepository: store}

	got, err := characterSelectionIdentities(context.Background(), fallback, fixture.account)
	if err != nil {
		t.Fatalf("read fallback selection identities: %v", err)
	}
	if len(got) != 17 {
		t.Fatalf("fallback identity rows=%d, want 17", len(got))
	}
	if fallback.listByAccountCalls != 1 {
		t.Fatalf("fallback ListByAccount calls=%d, want 1", fallback.listByAccountCalls)
	}
}

func TestLoadSelectedCharacterIdentityRejectsStableBindingChanges(t *testing.T) {
	tests := []struct {
		name      string
		change    func(*dnfrepo.CharacterRecord, *dnfrepo.CharacterRecord, string)
		wantLoads int
	}{
		{
			name: "identity_owner_changed",
			change: func(identity, _ *dnfrepo.CharacterRecord, _ string) {
				identity.AccountID = "other-account"
			},
			wantLoads: 0,
		},
		{
			name: "full_record_owner_changed",
			change: func(_, record *dnfrepo.CharacterRecord, _ string) {
				record.AccountID = "other-account"
			},
			wantLoads: 1,
		},
		{
			name: "record_deleted",
			change: func(_, record *dnfrepo.CharacterRecord, _ string) {
				record.Stats["delete_flag"] = 1
			},
			wantLoads: 1,
		},
		{
			name: "slot_changed_before_load",
			change: func(_, record *dnfrepo.CharacterRecord, _ string) {
				record.Slot++
			},
			wantLoads: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSelectionReadsFixture(17)
			identity := fixture.rosterIdentity
			record := fixture.records[identity.CharacterID]
			tc.change(&identity, &record, fixture.account)
			fixture.records[identity.CharacterID] = record
			_, characters, _, _ := selectionReadsTestGroup(fixture)

			got, ok := loadSelectedCharacterIdentity(context.Background(), characters, identity, fixture.account)
			if ok || got.CharacterID != "" || got.AccountID != "" {
				t.Fatalf("changed identity was accepted: ok=%t record=%+v", ok, got)
			}
			if len(characters.loadIDs) != tc.wantLoads {
				t.Fatalf("full Load calls=%d, want %d (ids=%v)", len(characters.loadIDs), tc.wantLoads, characters.loadIDs)
			}
		})
	}
}

func TestResolveSelectedCharacterUsesBoundRosterRowAcrossRosterSizes(t *testing.T) {
	request := []byte{0, 0}
	parsed := parseSelectCharacterRequest(request)
	if parsed.source != "legacy_slot_or_char_id" || parsed.slot != 0 || parsed.charID != 0 {
		t.Fatalf("legacy request parse=%+v, want row-zero legacy_slot_or_char_id with zero char id", parsed)
	}

	for _, count := range []int{1, 16, 17, 32} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			fixture := newSelectionReadsFixture(count)
			repos, characters, equipment, quests := selectionReadsTestGroup(fixture)
			target := fixture.records[fixture.rosterIdentity.CharacterID]
			session := &gameSession{
				accountID:           fixture.account,
				currentRenameRoster: []dnfrepo.CharacterRecord{fixture.rosterIdentity},
			}
			service := &Service{
				options:            options{accountPrefix: defaultAccountPrefix},
				repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
			}

			charID, charName, selected, found, slot := service.resolveSelectedCharacter(context.Background(), session, request)
			if !found {
				t.Fatal("sent roster row zero did not resolve")
			}
			if charID != 700+uint16(count-1) || charName != target.Name || slot != target.Slot {
				t.Fatalf("resolved id=%d name=%q slot=%d, want id=%d name=%q slot=%d", charID, charName, slot, 700+uint16(count-1), target.Name, target.Slot)
			}
			if selected.CharacterID != target.CharacterID || selected.AccountID != fixture.account || selected.Slot != target.Slot {
				t.Fatalf("selected identity=%+v, want target %+v", selected, target)
			}
			if selected.Stats["selection_probe_marker"] != target.Stats["selection_probe_marker"] || selected.Stats["selection_probe_profile"] != target.Stats["selection_probe_profile"] {
				t.Fatalf("selected Stats=%v, want target Stats=%v", selected.Stats, target.Stats)
			}
			if len(characters.loadIDs) != 1 || characters.loadIDs[0] != target.CharacterID {
				t.Fatalf("full character Load ids=%v, want only target %s once", characters.loadIDs, target.CharacterID)
			}
			if characters.listIdentityCalls != 1 || characters.identityAccount != fixture.account || characters.identityLimit != defaultCharacterSlots {
				t.Fatalf("narrow calls=%d account=%q limit=%d", characters.listIdentityCalls, characters.identityAccount, characters.identityLimit)
			}
			if characters.listByAccountCalls != 0 {
				t.Fatalf("selection called full ListByAccount %d times", characters.listByAccountCalls)
			}
			if len(equipment.loadIDs) == 0 {
				t.Fatal("selected character equipment reconciliation was not exercised")
			}
			for _, id := range equipment.loadIDs {
				if id != target.CharacterID {
					t.Fatalf("selection read unselected character equipment for %s", id)
				}
			}
			if len(quests.loadIDs) != 0 {
				t.Fatalf("selection read character quests: %v", quests.loadIDs)
			}
		})
	}
}

type selectionReadsPresenceReader struct {
	exists bool
	err    error
	calls  int
}

func (r *selectionReadsPresenceReader) Exists(_ context.Context, _ string) (bool, error) {
	r.calls++
	return r.exists, r.err
}

func TestInitializationRecordExistsUsesCapabilityAndPreservesFallback(t *testing.T) {
	t.Run("capability_true", func(t *testing.T) {
		presence := &selectionReadsPresenceReader{exists: true}
		fallbackCalls := 0
		got, err := initializationRecordExists(context.Background(), presence, "record", func(context.Context, string) (string, bool, error) {
			fallbackCalls++
			return "", false, errors.New("fallback must not run")
		})
		if err != nil || !got || presence.calls != 1 || fallbackCalls != 0 {
			t.Fatalf("capability result=%t err=%v exists_calls=%d fallback_calls=%d", got, err, presence.calls, fallbackCalls)
		}
	})

	t.Run("capability_false", func(t *testing.T) {
		presence := &selectionReadsPresenceReader{exists: false}
		fallbackCalls := 0
		got, err := initializationRecordExists(context.Background(), presence, "record", func(context.Context, string) (string, bool, error) {
			fallbackCalls++
			return "", true, nil
		})
		if err != nil || got || presence.calls != 1 || fallbackCalls != 0 {
			t.Fatalf("capability result=%t err=%v exists_calls=%d fallback_calls=%d", got, err, presence.calls, fallbackCalls)
		}
	})

	t.Run("fallback_empty", func(t *testing.T) {
		got, err := initializationRecordExists(context.Background(), struct{}{}, "record", func(context.Context, string) (string, bool, error) {
			return "", false, nil
		})
		if err != nil || got {
			t.Fatalf("empty fallback result=%t err=%v", got, err)
		}
	})

	t.Run("fallback_error", func(t *testing.T) {
		sentinel := errors.New("selection presence fallback sentinel")
		got, err := initializationRecordExists(context.Background(), struct{}{}, "record", func(context.Context, string) (string, bool, error) {
			return "", true, sentinel
		})
		if got || !errors.Is(err, sentinel) {
			t.Fatalf("fallback error result=%t err=%v, want false and sentinel", got, err)
		}
	})
}

package dnfbridge

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// A real GUI save on the paired current EXE changed only word 74 of the
// 186-byte account image options. A persisted checkbox alone leaves the live
// actor using its previously attached appearance components.
func TestAwakeningAvatarOptionSaveUpdatesOnlyNativeAwakeningBit(t *testing.T) {
	s, session, c, repos := testGameOptionFixture(t)
	s.options.currentClientVersion = "official-24.9"
	ch, _, err := repos.Character.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	ch.Stats = map[string]int64{"user_state_bits": 0x0b}
	if err = repos.Character.Save(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	previous := make([]byte, 186)
	previous[148] = 1
	if err = s.saveAccountGameOptionValues(session, "account-1", map[string]string{"main_game_option_hex": hex.EncodeToString(previous)}); err != nil {
		t.Fatal(err)
	}
	current := append([]byte(nil), previous...)
	current[148] = 0
	if err = s.handleSaveGameOption1(session, testLengthPrefixedBlob(current)); err != nil {
		t.Fatal(err)
	}
	packets := drainUpperPackets(t, c)
	if len(packets) != 1 || packets[0].msgID != 357 || hex.EncodeToString(packets[0].body) != "13001b" {
		t.Fatalf("disable did not apply bit4: %+v", packets)
	}
	saved, _, err := repos.Character.Load(context.Background(), "19")
	if err != nil || saved.Stats["user_state_bits"] != 0x1b {
		t.Fatalf("visibility flags were changed: %+v %v", saved.Stats, err)
	}
	if err = s.handleSaveGameOption1(session, testLengthPrefixedBlob(current)); err != nil {
		t.Fatal(err)
	}
	if c.write.Len() != 0 {
		t.Fatal("identical save redrew actor")
	}
	current[148] = 1
	if err = s.handleSaveGameOption1(session, testLengthPrefixedBlob(current)); err != nil {
		t.Fatal(err)
	}
	if packets = drainUpperPackets(t, c); len(packets) != 1 || packets[0].msgID != 357 || hex.EncodeToString(packets[0].body) != "13000b" {
		t.Fatalf("enable did not clear bit4: %+v", packets)
	}
	saved, _, err = repos.Character.Load(context.Background(), "19")
	if err != nil || saved.Stats["user_state_bits"] != 0x0b {
		t.Fatalf("enable saved bits=%v err=%v", saved.Stats, err)
	}
}

func TestAwakeningAvatarOptionFirstSaveSetsBit4AndPreservesBit3(t *testing.T) {
	s, session, c, repos := testGameOptionFixture(t)
	s.options.currentClientVersion = "official-24.9"
	ctx := context.Background()
	ch, _, err := repos.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	ch.Stats = map[string]int64{"user_state_bits": 0x0b}
	if err = repos.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}

	blob := make([]byte, 186)
	if err = s.handleSaveGameOption1(session, testLengthPrefixedBlob(blob)); err != nil {
		t.Fatal(err)
	}
	packets := drainUpperPackets(t, c)
	if len(packets) != 1 || packets[0].msgID != 357 || hex.EncodeToString(packets[0].body) != "13001b" {
		t.Fatalf("first save did not apply only bit4: %+v", packets)
	}
	saved, _, err := repos.Character.Load(ctx, "19")
	if err != nil || saved.Stats["user_state_bits"] != 0x1b {
		t.Fatalf("first save bits=%v err=%v", saved.Stats, err)
	}
	record, found, err := repos.Settings.Load(ctx, accountGameOptionScope("account-1"))
	if err != nil || !found || record.Values["main_game_option_hex"] != hex.EncodeToString(blob) {
		t.Fatalf("first save option found=%t record=%+v err=%v", found, record, err)
	}
}

type awakeningFailCharacterStore struct {
	dnfrepo.CharacterRepository
	err error
}

func (s awakeningFailCharacterStore) Save(context.Context, dnfrepo.CharacterRecord) error {
	return s.err
}

func (s awakeningFailCharacterStore) SaveFields(context.Context, dnfrepo.CharacterRecord, ...dnfrepo.CharacterField) error {
	return s.err
}

type awakeningFailCharacterCeraShop struct {
	dnfrepo.CeraShopAssetUnitOfWork
	err error
}

func (u awakeningFailCharacterCeraShop) WithinCeraShopAssets(
	ctx context.Context,
	accountID string,
	characterID string,
	settingsScope string,
	apply func(dnfrepo.AccountRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository, dnfrepo.SettingsRepository) error,
) error {
	return u.CeraShopAssetUnitOfWork.WithinCeraShopAssets(
		ctx, accountID, characterID, settingsScope,
		func(accounts dnfrepo.AccountRepository, characters dnfrepo.CharacterRepository, inventories dnfrepo.InventoryRepository, equipment dnfrepo.EquipmentRepository, settings dnfrepo.SettingsRepository) error {
			return apply(
				accounts,
				awakeningFailCharacterStore{CharacterRepository: characters, err: u.err},
				inventories,
				equipment,
				settings,
			)
		},
	)
}

type awakeningEntryTestCeraShop struct {
	characters dnfrepo.CharacterRepository
	settings   dnfrepo.SettingsRepository
}

func (u awakeningEntryTestCeraShop) WithinCeraShopAssets(
	ctx context.Context,
	_ string,
	_ string,
	_ string,
	apply func(dnfrepo.AccountRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository, dnfrepo.SettingsRepository) error,
) error {
	return apply(nil, u.characters, nil, nil, u.settings)
}

func TestAwakeningAvatarOptionFirstSaveRollsBackOnCharacterWriteFault(t *testing.T) {
	s, session, c, repos := testGameOptionFixture(t)
	s.options.currentClientVersion = "official-24.9"
	ctx := context.Background()
	ch, _, err := repos.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	ch.Stats = map[string]int64{"user_state_bits": 0x0b}
	if err = repos.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	repos.CeraShopAssets = awakeningFailCharacterCeraShop{
		CeraShopAssetUnitOfWork: repos.CeraShopAssets,
		err:                     errors.New("injected character bit write failure"),
	}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }

	blob := make([]byte, 186)
	if err = s.handleSaveGameOption1(session, testLengthPrefixedBlob(blob)); err != nil {
		t.Fatal(err)
	}
	if c.write.Len() != 0 {
		t.Fatalf("failed first save emitted packets: %x", c.write.Bytes())
	}
	if _, found, err := repos.Settings.Load(ctx, accountGameOptionScope("account-1")); err != nil || found {
		t.Fatalf("failed first save retained option found=%t err=%v", found, err)
	}
	saved, _, err := repos.Character.Load(ctx, "19")
	if err != nil || saved.Stats["user_state_bits"] != 0x0b {
		t.Fatalf("failed first save changed bits=%v err=%v", saved.Stats, err)
	}
}

func TestAwakeningAvatarOptionEntrySyncsWithoutAccountNotice(t *testing.T) {
	for _, tc := range []struct {
		name          string
		noticeEnv     string
		noticeAlready bool
	}{
		{name: "notice_already_sent", noticeEnv: "1", noticeAlready: true},
		{name: "account_notice_disabled", noticeEnv: "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(currentGameOptionNoticeEnv, tc.noticeEnv)
			service, session := creatorTutorialEntryFixture(t, creatorDefaultGuardRealPVF(t), "10")
			repos, ok := service.repositoryGroup()
			if !ok {
				t.Fatal("repository group unavailable")
			}
			ctx := context.Background()
			character, found, err := repos.Character.Load(ctx, "1")
			if err != nil || !found {
				t.Fatalf("load selected character found=%t err=%v", found, err)
			}
			character.Stats = map[string]int64{"user_state_bits": 0x0b}
			if err = repos.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			repos.CeraShopAssets = awakeningEntryTestCeraShop{
				characters: repos.Character,
				settings:   repos.Settings,
			}
			service.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
			blob := make([]byte, 186)
			if err = service.saveAccountGameOptionValues(session, character.AccountID, map[string]string{
				"main_game_option_hex": hex.EncodeToString(blob),
			}); err != nil {
				t.Fatal(err)
			}
			session.gameOptionNoticeSent = tc.noticeAlready
			if err = service.sendUpperCSharpSelectInit(session, selectFirstSlotRequest()); err != nil {
				t.Fatal(err)
			}
			saved, _, err := repos.Character.Load(ctx, "1")
			if err != nil || saved.Stats["user_state_bits"] != 0x1b {
				t.Fatalf("entry did not sync bit4: bits=%v err=%v", saved.Stats, err)
			}
		})
	}
}

func TestAwakeningAvatarOptionIgnoresEntryAndUnprovenFrames(t *testing.T) {
	for _, tc := range []struct {
		name  string
		size  int
		value byte
		entry bool
	}{
		{"old_blob", 178, 0, false}, {"invalid_bool", 186, 2, false}, {"entry_template", 186, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, session, c, _ := testGameOptionFixture(t)
			s.options.currentClientVersion = "official-24.9"
			if tc.entry {
				session.initialTownRouteStage = 0
				session.townSceneReadyCharacterID = 0
			}
			blob := make([]byte, tc.size)
			blob[148] = tc.value
			if e := s.handleSaveGameOption1(session, testLengthPrefixedBlob(blob)); e != nil {
				t.Fatal(e)
			}
			if c.write.Len() != 0 {
				t.Fatal("unproven or bootstrap frame redrew actor")
			}
		})
	}
}

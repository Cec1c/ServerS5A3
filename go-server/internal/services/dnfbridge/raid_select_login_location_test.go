package dnfbridge

import (
	"context"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRaidColdSelectACKAndCommittedLandingAgree(t *testing.T) {
	for _, kind := range []byte{23, 32} {
		service, session, repos := newTownMoveTest(t)
		loadTownSeriaFixtureService(t, service, townMoveTestSource{
			"town/town.lst":     "19 `anton.twn` 32 `luke.twn` 38 `ordinary.twn`",
			"town/anton.twn":    "[area]\n0 `Town/BattleShip/Gate_BattleShip.map` `[gate]` 474 234\n[/area]\n",
			"town/luke.twn":     "[area]\n0 `Town/Jelva_raid/Gate_Jelva.map` `[gate]` 474 234\n[/area]\n",
			"town/ordinary.twn": townSeriaFixtureElvengard,
		})
		assertRaidColdSelectLanding(t, service, session, repos, kind)
	}
}

func assertRaidColdSelectLanding(t *testing.T, service *Service, session *gameSession, repos dnfrepo.Group, kind byte) {
	t.Helper()
	ctx := context.Background()
	character, found, err := repos.Character.Load(ctx, "29")
	if err != nil || !found {
		t.Fatalf("character: %v %v", found, err)
	}
	character.Level = 90
	character.AccountID = "dnf:1"
	before := dnfrepo.CloneCharacter(character)
	aggregate := dnfrepomemory.NewMemoryGroup()
	if err := aggregate.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	repos.Character, repos.CharacterSettlement = aggregate.Character, aggregate.CharacterSettlement
	repos.CharacterAssets = aggregate.CharacterAssets
	service.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	session.channel = channelcatalog.Channel{ID: int(kind), Type: kind}
	session.residentChannel = session.channel
	session.townSeriaRoomEntrySnapshot = currentTownPositionSnapshot{PositionValid: true, TownID: 38, AreaID: 0}
	session.townPrevVillageSnapshot = session.townSeriaRoomEntrySnapshot
	t.Setenv(currentLoginForcedSeriaFallbackEnv, currentLoginForcedSeriaFallbackValue)
	t.Setenv(currentSelectAckTownEnv, currentSelectAckTownRollbackValue)
	ackTown, _, err := service.currentSessionSelectAckTownID(session, character, 29, 5)
	if err != nil {
		t.Fatal(err)
	}
	want := byte(19)
	if kind == 32 {
		want = 32
	}
	body := buildCurrentSelectCharacterAckBody(character, true, dnfrepo.QuestRecord{}, false, 29, ackTown, 0, []byte{0})
	actualACKTown, ok := currentSelectAckInitialTownIDFromBody(body)
	if !ok || actualACKTown != want {
		t.Fatalf("ACK town=%d ok=%v", actualACKTown, ok)
	}
	projected, town, area, row, mapPath, err := service.currentCharacterListLoginTransition(ctx, session, 29, character)
	if err != nil {
		t.Fatal(err)
	}
	if town != actualACKTown || area != 0 || row.Value1 != 474 || row.Value2 != 234 || !townMapPathLooksLikeSeria(mapPath) || projected.Stats["town_id"] != int64(want) {
		t.Fatalf("ACK/scene disagreement: ack=%d town=%d area=%d row=%+v map=%s", actualACKTown, town, area, row, mapPath)
	}
	stored, found, err := aggregate.Character.Load(ctx, "29")
	if err != nil || !found || stored.Stats["town_id"] != int64(want) || stored.Stats["area_id"] != 0 || stored.Stats["pos_x"] != 474 || stored.Stats["pos_y"] != 234 {
		t.Fatalf("next door request would reload wrong landing: found=%v err=%v stats=%v", found, err, stored.Stats)
	}
	if !reflect.DeepEqual(before, character) {
		t.Fatal("input mutated")
	}
	if session.townSeriaRoomEntrySnapshot.PositionValid {
		t.Fatal("ordinary exit origin leaked into raid room")
	}
}

func TestRaidColdSelectMissingRoomFailsBeforeACKOrLocationWrite(t *testing.T) {
	service, session, repos := newTownMoveTest(t)
	session.channel.Type = 23
	character, _, _ := repos.Character.Load(context.Background(), "29")
	if _, _, err := service.currentSessionSelectAckTownID(session, character, 29, 0); err == nil {
		t.Fatal("missing raid room accepted")
	}
	if _, _, _, _, _, err := service.currentCharacterListLoginTransition(context.Background(), session, 29, character); err == nil {
		t.Fatal("ordinary fallback masked missing raid room")
	}
	stored, _, _ := repos.Character.Load(context.Background(), "29")
	if !reflect.DeepEqual(character, stored) {
		t.Fatal("failed room lookup mutated location")
	}
}

func TestRealPVFRaidColdSelectACKAndLandingAgree(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	table, err := dnftown.Load(context.Background(), archive, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []byte{23, 32} {
		service, session, repos := newTownMoveTest(t)
		service.townCatalog = table
		assertRaidColdSelectLanding(t, service, session, repos, kind)
	}
}

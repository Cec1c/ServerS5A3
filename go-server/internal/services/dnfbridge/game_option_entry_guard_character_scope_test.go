package dnfbridge

import (
	"context"
	"encoding/hex"
	"testing"
)

// Changing characters on one connection must preserve saved account options
// when the new character pushes its factory template during entry.
func TestGameOptionEntryGuardRejectsPreviousCharacterTownReady(t *testing.T) {
	t.Setenv(currentGameOptionEntryGuardEnv, "1")
	service, session, _, repos := testGameOptionFixture(t)
	ctx := context.Background()
	saved := testGameOptionBlob(178, 0x11)
	if _, err := service.handleGameOptionCommand(session, currentSaveGameOption1Opcode, testLengthPrefixedBlob(saved)); err != nil {
		t.Fatal(err)
	}
	session.townSceneReadyCharacterID = session.selectedCharacterID
	service.bindGameSessionCharacter(session, 31)
	service.resetCurrentInitialTownRoute(session)
	if session.townSceneReadyCharacterID != 19 {
		t.Fatalf("fixture no longer reproduces the stale town marker: %d", session.townSceneReadyCharacterID)
	}
	if handled, err := service.handleGameOptionCommand(session, currentSaveGameOption1Opcode, testLengthPrefixedBlob(testGameOptionBlob(178, 0x77))); err != nil || !handled {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
	record, found, err := repos.Settings.Load(ctx, accountGameOptionScope("account-1"))
	if err != nil || !found || record.Values["main_game_option_hex"] != hex.EncodeToString(saved) {
		t.Fatalf("new-character entry overwrote saved account options: found=%t err=%v blob=%s", found, err, record.Values["main_game_option_hex"])
	}
}

func TestGameOptionEntryGuardRejectsPreviousCharacterRouteStage(t *testing.T) {
	t.Setenv(currentGameOptionEntryGuardEnv, "1")
	service, session, _, repos := testGameOptionFixture(t)
	ctx := context.Background()
	saved := testGameOptionBlob(178, 0x11)
	if _, err := service.handleGameOptionCommand(session, currentSaveGameOption1Opcode, testLengthPrefixedBlob(saved)); err != nil {
		t.Fatal(err)
	}
	service.bindGameSessionCharacter(session, 31)
	if session.initialTownRouteCharacterID != 19 || session.initialTownRouteStage != currentInitialTownRoutePlayerStateSent {
		t.Fatal("fixture no longer reproduces the previous character's route")
	}
	if handled, err := service.handleGameOptionCommand(session, currentSaveGameOption1Opcode, testLengthPrefixedBlob(testGameOptionBlob(178, 0x66))); err != nil || !handled {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
	record, found, err := repos.Settings.Load(ctx, accountGameOptionScope("account-1"))
	if err != nil || !found || record.Values["main_game_option_hex"] != hex.EncodeToString(saved) {
		t.Fatalf("previous-character route admitted a new entry template: found=%t err=%v blob=%s", found, err, record.Values["main_game_option_hex"])
	}
}

func TestGameOptionEntryGuardRejectsUnownedRouteStage(t *testing.T) {
	t.Setenv(currentGameOptionEntryGuardEnv, "1")
	_, session, _, _ := testGameOptionFixture(t)
	session.initialTownRouteCharacterID = 0
	if gameOptionSaveWindowOpen(session) {
		t.Fatal("an unowned route stage admitted an entry template")
	}
}

func TestGameOptionEntryGuardAcceptsCurrentCharacterTownReady(t *testing.T) {
	t.Setenv(currentGameOptionEntryGuardEnv, "1")
	service, session, _, repos := testGameOptionFixture(t)
	session.townSceneReadyCharacterID = session.selectedCharacterID
	session.initialTownRouteCharacterID = 0
	session.initialTownRouteStage = currentInitialTownRouteIdle
	blob := testGameOptionBlob(178, 0x11)
	if handled, err := service.handleGameOptionCommand(session, currentSaveGameOption1Opcode, testLengthPrefixedBlob(blob)); err != nil || !handled {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
	record, found, err := repos.Settings.Load(context.Background(), accountGameOptionScope("account-1"))
	if err != nil || !found || record.Values["main_game_option_hex"] != hex.EncodeToString(blob) {
		t.Fatalf("current-character save found=%t err=%v", found, err)
	}
}

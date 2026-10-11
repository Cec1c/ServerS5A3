package dnfbridge

// PREP ONLY: synthetic completed-run/town topology, not actual223/PVF/GUI.
// No test has been executed. This targets the observed stale server tail only.
import (
	"context"
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	"testing"
)

func TestPreparedCompletedTownNoColdCallbackThenAreaDoesNotKeepOldTail(t *testing.T) {
	service, session, runtime := newBackToVillageRuntime(t)
	table, err := dnftown.Load(context.Background(), townMoveTestSource{
		"town/town.lst":     "7 `prepared.twn`",
		"town/prepared.twn": "[area]\n3 `a.map` `[normal]`\n[/area]\n[area]\n4 `b.map` `[normal]`\n[/area]",
	}, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	service.townCatalog = table
	if err := runtime.Session.CompleteCurrentRoom(); err != nil {
		t.Fatal(err)
	}
	transition, err := service.prepareCurrentDungeonTownTransition(context.Background(), session.selectedCharacterID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.sendCurrentCompletedDungeonReturnToTown(session, runtime, transition, uint16(dnfenum.CmdPacketEplpCommand), "prepared_completed_no_cold_callback"); err != nil {
		t.Fatal(err)
	}
	if session.dungeon.runtime != nil || !session.sceneBootstrapTailDeferred || session.initialTownLegacySceneReadyAccepted {
		t.Fatal("fixture did not reach observed detached/unacknowledged completed route")
	}
	connection := session.conn.(*bufferConn)
	connection.write.Reset()
	body := make([]byte, currentTownSetUserAreaBodySize)
	body[0], body[1], body[6] = 7, 4, 5
	binary.LittleEndian.PutUint16(body[2:4], 1550)
	binary.LittleEndian.PutUint16(body[4:6], 396)
	if err := service.handleTownSetUserArea(session, body); err != nil {
		t.Fatal(err)
	}
	if session.initialTownRouteCharacterID != 0 || session.initialTownRouteStage != currentInitialTownRouteIdle || session.townSceneReadyCharacterID != session.selectedCharacterID {
		t.Fatal("normal area transition not committed")
	}
	// Expected baseline RED is this exact business-state assertion, not logging.
	if session.sceneBootstrapTailDeferred {
		t.Fatal("obsolete completed-return cold tail remains armed after owned area commit")
	}
	if session.sceneBootstrapTailSent {
		t.Fatal("unsent obsolete tail falsely marked sent")
	}
	if session.initialTownLegacySceneReadyAccepted {
		t.Fatal("missing1345 falsely accepted")
	}
	if containsClass0Op2ModeForTest(t, connection.write.Bytes(), 0) || containsClass0Op2ModeForTest(t, connection.write.Bytes(), 1) || containsClass0Op2ModeForTest(t, connection.write.Bytes(), 3) {
		t.Fatal("lifecycle cleanup injected an extra actor generation")
	}
	connection.write.Reset()
	if err := service.sendDeferredSelectSceneTail(session, "prepared_heartbeat_after_area"); err != nil {
		t.Fatal(err)
	}
	if connection.write.Len() != 0 {
		t.Fatal("cancelled old scene emitted packets on heartbeat")
	}
}

// These controls model server ownership only; they do not model camera pointers.
func TestPreparedCompletedTownTailOwnerRejectsForeignOrNewRoute(t *testing.T) {
	for _, tc := range []string{"cold_no_receipt", "foreign_character", "other_channel", "other_server", "other_connection", "new_owner_pointer", "callback_already_accepted", "unsuccessful_area_not_committed"} {
		t.Run(tc, func(t *testing.T) {
			_, session, _ := newBackToVillageRuntime(t)
			session.initialTownRouteCharacterID = session.selectedCharacterID
			session.initialTownRouteStage = currentInitialTownRoutePlayerStateSent
			session.sceneBootstrapTailDeferred = true
			session.sceneBootstrapTailSent = false
			session.initialTownLegacySceneReadyAccepted = false
			armCurrentCompletedTownSceneTailOwnerLocked(session, session.selectedCharacterID)
			owner := captureCurrentCompletedTownSceneTailOwnerLocked(session)
			if owner == nil {
				t.Fatal("owner fixture not armed")
			}
			session.initialTownRouteCharacterID = 0
			session.initialTownRouteStage = currentInitialTownRouteIdle
			session.townSceneReadyCharacterID = session.selectedCharacterID
			switch tc {
			case "cold_no_receipt":
				session.completedTownSceneTailOwner = nil
			case "foreign_character":
				session.selectedCharacterID++
			case "other_channel":
				session.channel.ID++
			case "other_server":
				session.channel.ServerID++
			case "other_connection":
				session.connID += "_new"
			case "new_owner_pointer":
				copy := *owner
				session.completedTownSceneTailOwner = &copy
			case "callback_already_accepted":
				session.initialTownLegacySceneReadyAccepted = true
			case "unsuccessful_area_not_committed":
				session.townSceneReadyCharacterID = 0
			}
			finishCurrentCompletedTownSceneTailAfterAreaCommitLocked(session, owner)
			if !session.sceneBootstrapTailDeferred || session.sceneBootstrapTailSent {
				t.Fatal("foreign/new/cold/failed owner retired or falsely sent tail")
			}
		})
	}
}

func TestPreparedCompletedTownRejectedAreaKeepsTail(t *testing.T) {
	service, session, runtime := newBackToVillageRuntime(t)
	table, err := dnftown.Load(context.Background(), townMoveTestSource{
		"town/town.lst":     "7 `prepared.twn`",
		"town/prepared.twn": "[area]\n3 `a.map` `[normal]`\n[/area]",
	}, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	service.townCatalog = table
	if err := runtime.Session.CompleteCurrentRoom(); err != nil {
		t.Fatal(err)
	}
	transition, err := service.prepareCurrentDungeonTownTransition(context.Background(), session.selectedCharacterID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.sendCurrentCompletedDungeonReturnToTown(session, runtime, transition, uint16(dnfenum.CmdPacketEplpCommand), "prepared_failed_area"); err != nil {
		t.Fatal(err)
	}
	owner := session.completedTownSceneTailOwner
	if owner == nil || !session.sceneBootstrapTailDeferred {
		t.Fatal("completed receipt fixture missing")
	}
	body := make([]byte, currentTownSetUserAreaBodySize)
	body[0], body[1] = 7, 255
	if err := service.handleTownSetUserArea(session, body); err != nil {
		t.Fatal(err)
	}
	if session.completedTownSceneTailOwner != owner || !session.sceneBootstrapTailDeferred || session.sceneBootstrapTailSent || session.initialTownLegacySceneReadyAccepted {
		t.Fatal("rejected op36 changed completed tail authority")
	}
}

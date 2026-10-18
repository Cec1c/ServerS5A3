package dnfbridge

import (
	"context"
	"errors"
	"testing"
	"time"

	darkcombo "longheng.io/server/internal/modules/dnf/darkknightcombo"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
)

func TestColdLoginType1345RestoresDarkKnightComboAfterDeferredTailMode1Once(t *testing.T) {
	service, session := newDeferredTailResumeFixture(t)
	repositories, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("repository group unavailable")
	}
	configureDarkKnightSceneReadyTest(t, service, repositories, "29")
	session.initialTownLegacySceneReadyAccepted = false
	connection := session.conn.(*bufferConn)
	connection.write.Reset()

	if err := service.handleLegacyGamePacket(session, buildLegacyTownSceneReadyAckPacket()); err != nil {
		t.Fatal(err)
	}
	_, lastTailCheckpoint, firstCombo, comboCount, skillInfoCount := darkKnightSceneReadyPacketPositions(t, connection.write.Bytes())
	if lastTailCheckpoint < 0 || firstCombo <= lastTailCheckpoint || comboCount != 1 {
		t.Fatalf(
			"cold-login type1345 order last_tail_checkpoint=%d first_op447=%d op447_count=%d",
			lastTailCheckpoint,
			firstCombo,
			comboCount,
		)
	}
	if skillInfoCount != 0 {
		t.Fatalf("cold-login type1345 replayed op19 count=%d", skillInfoCount)
	}
	if session.sceneBootstrapActorGeneration == 0 {
		t.Fatal("cold-login scene actor generation was not allocated")
	}

	firstWriteLen := connection.write.Len()
	if err := service.handleLegacyGamePacket(session, buildLegacyTownSceneReadyAckPacket()); err != nil {
		t.Fatal(err)
	}
	if connection.write.Len() != firstWriteLen {
		t.Fatalf("duplicate type1345 grew stream from %d to %d", firstWriteLen, connection.write.Len())
	}
}

func TestCompletedDungeonReturnType1345RestoresDarkKnightComboWithoutSkillReplay(t *testing.T) {
	service, session, runtime := newBackToVillageRuntime(t)
	repositories, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("repository group unavailable")
	}
	configureDarkKnightSceneReadyTest(t, service, repositories, "99")
	// Model the same selected character's already-restored town generation.
	// Completed return must allocate a new native actor generation instead of
	// letting the prior op447 gate suppress this rebuild.
	const previousActorGeneration = 41
	session.sceneBootstrapActorGeneration = previousActorGeneration
	session.darkKnightCombo.finalTownSceneCharacterID = session.selectedCharacterID
	session.darkKnightCombo.finalTownSceneGeneration = previousActorGeneration
	session.darkKnightCombo.finalTownSceneInitialization = true
	if err := runtime.Session.CompleteCurrentRoom(); err != nil {
		t.Fatal(err)
	}
	transition, err := service.prepareCurrentDungeonTownTransition(context.Background(), session.selectedCharacterID)
	if err != nil {
		t.Fatal(err)
	}
	connection := session.conn.(*bufferConn)
	connection.write.Reset()
	if err := service.sendCurrentCompletedDungeonReturnToTown(
		session,
		runtime,
		transition,
		uint16(dnfenum.CmdPacketEplpCommand),
		"test_dark_knight_completed_return",
	); err != nil {
		t.Fatal(err)
	}
	_, _, _, preReadyComboCount, preReadySkillInfoCount := darkKnightSceneReadyPacketPositions(t, connection.write.Bytes())
	if preReadyComboCount != 0 || preReadySkillInfoCount != 0 {
		t.Fatalf(
			"completed return pre-type1345 op447=%d op19=%d",
			preReadyComboCount,
			preReadySkillInfoCount,
		)
	}
	if session.sceneBootstrapActorGeneration == previousActorGeneration {
		t.Fatal("completed return reused the previous native actor generation")
	}

	if err := service.handleLegacyGamePacket(session, buildLegacyTownSceneReadyAckPacket()); err != nil {
		t.Fatal(err)
	}
	lastMode1, lastTailCheckpoint, firstCombo, comboCount, skillInfoCount := darkKnightSceneReadyPacketPositions(t, connection.write.Bytes())
	if lastMode1 < 0 || lastTailCheckpoint < 0 || firstCombo <= lastMode1 || firstCombo <= lastTailCheckpoint || comboCount != 1 || skillInfoCount != 0 {
		t.Fatalf(
			"completed return type1345 order last_mode1=%d last_tail_checkpoint=%d first_op447=%d op447=%d op19=%d",
			lastMode1,
			lastTailCheckpoint,
			firstCombo,
			comboCount,
			skillInfoCount,
		)
	}
}

func TestFinalTownSceneDarkKnightComboWriteFailureRetriesWithoutDeferredTailReplay(t *testing.T) {
	service, session := newDeferredTailResumeFixture(t)
	repositories, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("repository group unavailable")
	}
	configureDarkKnightSceneReadyTest(t, service, repositories, "29")
	session.sceneBootstrapTailDeferred = false
	session.sceneBootstrapTailSent = true
	wantErr := errors.New("op447 write failed")
	failing := &failNthDungeonWriteConn{failAt: 1, err: wantErr}
	session.conn = failing

	if err := service.sendCurrentSelectSceneReadyBoundary(session, "test_op447_failure"); !errors.Is(err, wantErr) {
		t.Fatalf("first boundary error=%v want=%v", err, wantErr)
	}
	if failing.bufferConn.write.Len() != 0 {
		t.Fatalf("failed op447 retained an accepted prefix: %x", failing.bufferConn.write.Bytes())
	}

	retry := &bufferConn{}
	session.conn = retry
	if err := service.sendCurrentSelectSceneReadyBoundary(session, "test_op447_retry"); err != nil {
		t.Fatal(err)
	}
	lastMode1, lastTailCheckpoint, firstCombo, comboCount, _ := darkKnightSceneReadyPacketPositions(t, retry.write.Bytes())
	if firstCombo != 0 || comboCount != 1 || lastMode1 >= 0 || lastTailCheckpoint >= 0 {
		t.Fatalf(
			"retry replayed tail or misplaced op447 first_op447=%d op447=%d last_mode1=%d last_tail_checkpoint=%d",
			firstCombo,
			comboCount,
			lastMode1,
			lastTailCheckpoint,
		)
	}

	retry.write.Reset()
	if err := service.sendCurrentSelectSceneReadyBoundary(session, "test_op447_duplicate"); err != nil {
		t.Fatal(err)
	}
	_, _, _, duplicateComboCount, _ := darkKnightSceneReadyPacketPositions(t, retry.write.Bytes())
	if duplicateComboCount != 0 {
		t.Fatalf("successful retry did not close the op447 gate: %x", retry.write.Bytes())
	}
}

func TestFinalTownSceneDarkKnightComboGateEmitsNothingForNonDarkKnight(t *testing.T) {
	service, session := newDeferredTailResumeFixture(t)
	session.sceneBootstrapTailDeferred = false
	session.sceneBootstrapTailSent = true
	session.initialTownLegacySceneReadyAccepted = false
	connection := session.conn.(*bufferConn)
	connection.write.Reset()

	if err := service.sendCurrentSelectSceneReadyBoundary(session, "test_non_dark_knight"); err != nil {
		t.Fatal(err)
	}
	if connection.write.Len() != 0 {
		t.Fatalf("non-Dark-Knight final scene emitted packets: %x", connection.write.Bytes())
	}
}

func TestFinalTownSceneDarkKnightComboRepositoryDeferralDoesNotCloseGenerationGate(t *testing.T) {
	service, session := newDeferredTailResumeFixture(t)
	repositories, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("repository group unavailable")
	}
	configureDarkKnightSceneReadyTest(t, service, repositories, "29")
	session.sceneBootstrapTailDeferred = false
	session.sceneBootstrapTailSent = true
	session.initialTownLegacySceneReadyAccepted = false
	connection := session.conn.(*bufferConn)
	connection.write.Reset()

	available := false
	service.repositoryProvider = func() (dnfrepo.Group, bool) {
		if !available {
			return dnfrepo.Group{}, false
		}
		return repositories, true
	}
	if err := service.sendCurrentSelectSceneReadyBoundary(session, "test_repository_deferred"); err != nil {
		t.Fatal(err)
	}
	if connection.write.Len() != 0 || session.darkKnightCombo.finalTownSceneInitialization {
		t.Fatalf("deferred repository closed combo gate: bytes=%x final_town_scene=%t", connection.write.Bytes(), session.darkKnightCombo.finalTownSceneInitialization)
	}

	available = true
	if err := service.sendCurrentSelectSceneReadyBoundary(session, "test_repository_restored"); err != nil {
		t.Fatal(err)
	}
	packets := splitAllCurrentGameServerUpperPackets(t, connection.write.Bytes())
	if len(packets) != 1 || packets[0].Header.Classification != 0 || packets[0].Header.MsgID != 447 {
		t.Fatalf("restored repository did not retry op447: packets=%+v", packets)
	}
}

func configureDarkKnightSceneReadyTest(
	t *testing.T,
	service *Service,
	repositories dnfrepo.Group,
	characterID string,
) {
	t.Helper()
	character, found, err := repositories.Character.Load(context.Background(), characterID)
	if err != nil || !found {
		t.Fatalf("load character %s found=%t err=%v", characterID, found, err)
	}
	character.Job = "9"
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Skill.Save(context.Background(), currentDarkKnightComboNativeTestRecord(characterID)); err != nil {
		t.Fatal(err)
	}
	pages := []darkcombo.Page{{
		Index: 0,
		Roots: []darkcombo.Root{{SkillID: 118, Children: []uint16{46}}},
	}}
	if err := darkcombo.SavePages(
		context.Background(),
		repositories.Settings,
		characterID,
		pages,
		time.Unix(1, 0),
	); err != nil {
		t.Fatal(err)
	}
	service.skillCatalog = &dnfskill.Table{}
	service.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
}

func darkKnightSceneReadyPacketPositions(t *testing.T, stream []byte) (
	lastMode1 int,
	lastTailCheckpoint int,
	firstCombo int,
	comboCount int,
	skillInfoCount int,
) {
	t.Helper()
	lastMode1 = -1
	lastTailCheckpoint = -1
	firstCombo = -1
	packetIndex := 0
	for len(stream) > 0 {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
		if packet.Header.Classification == 0 {
			switch packet.Header.MsgID {
			case uint16(dnfenum.CmdPacketSetUDPIPPort):
				if len(packet.Body) > 0 && packet.Body[0] == 1 {
					lastMode1 = packetIndex
				}
			case currentDarkKnightComboInitializeMsgID:
				if firstCombo < 0 {
					firstCombo = packetIndex
				}
				comboCount++
			case currentSkillInfoMsgID:
				skillInfoCount++
			case currentCharacterOptionNoticeMsgID, currentRentalStateMsgID:
				lastTailCheckpoint = packetIndex
			}
		}
		packetIndex++
		stream = rest
	}
	return lastMode1, lastTailCheckpoint, firstCombo, comboCount, skillInfoCount
}

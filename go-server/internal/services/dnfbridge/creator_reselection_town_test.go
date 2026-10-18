package dnfbridge

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	r62TownHotkeyTextA = "I01\t90\nI02\t88\n"
	r62TownHotkeyTextB = "I01\t38\nI02\t37\n"
)

func r62TownHotkeyFixture(t *testing.T) (*Service, *gameSession, dnfrepo.Group) {
	t.Helper()
	service, session, repositories := scope44Fixture(t)
	ctx := context.Background()
	if skills, ok := service.initialSkillsByJob[0]; ok {
		service.initialSkillsByJob[byte(csharpCreatorMageJob)] = skills
	}
	for id, text := range map[string]string{
		"29": r62TownHotkeyTextA,
		"30": r62TownHotkeyTextB,
	} {
		character, found, err := repositories.Character.Load(ctx, id)
		if err != nil || !found {
			t.Fatalf("load town character %s found=%t err=%v", id, found, err)
		}
		character.Job = "10"
		character.Level = 1
		if character.Stats == nil {
			character.Stats = make(map[string]int64)
		}
		character.Stats["town_id"] = 38
		character.Stats["area_id"] = 1
		character.Stats["pos_x"] = 450
		character.Stats["pos_y"] = 234
		character.Stats["direction"] = 5
		character.Stats["area_state"] = 3
		if err := repositories.Character.Save(ctx, character); err != nil {
			t.Fatalf("save town character %s: %v", id, err)
		}
		if err := repositories.Settings.Save(ctx, dnfrepo.SettingsRecord{
			Scope: characterHotkeyConfigScope(id),
			Values: map[string]string{
				"hotkey_config_hex": hex.EncodeToString([]byte(text)),
			},
		}); err != nil {
			t.Fatalf("save town hotkey %s: %v", id, err)
		}
	}
	// The account option replay was consumed by an earlier character on this
	// same connection. The town route must restore only the newly bound
	// character's saved hotkeys.
	session.gameOptionNoticeSent = true
	session.selectedHotkeyNoticeCharacterID = 0
	session.selectedHotkeyNoticeGeneration = 0
	return service, session, repositories
}

func r62DriveTownActorRoute(t *testing.T, service *Service, session *gameSession, characterID uint16) {
	t.Helper()
	if session.initialTownRouteCharacterID != characterID ||
		session.initialTownRouteStage < currentInitialTownRouteArmed {
		t.Fatalf("town route not armed for character %d: route=%d stage=%d",
			characterID, session.initialTownRouteCharacterID, session.initialTownRouteStage)
	}
	ctx := context.Background()
	charID, charName, character, found := service.selectedCharacterForEnter(ctx, session)
	if charID != characterID || !found {
		t.Fatalf("selected town character=%d found=%t want=%d", charID, found, characterID)
	}
	objectBody := service.buildCurrentSceneObjectListBodyForSession(
		ctx, session, charID, charName, character, found,
	)
	objectKey := currentSceneActorObjectKey(charID)
	mode1Body := buildCurrentActorBindingMode1Body(objectKey, 0)
	row := currentSceneTransitionRow{
		ObjectOrResourceKey: objectKey,
		Value1:              450,
		Value2:              234,
		Value3:              5,
		Value4:              3,
	}
	transitionBody, err := buildCurrentSceneTransitionBody(38, 1, []currentSceneTransitionRow{row})
	if err != nil {
		t.Fatalf("build town transition body: %v", err)
	}
	session.townMu.Lock()
	err = service.sendCurrentInitialTownActorRoutePacketsLocked(
		session, charID, 38, 1, row, objectBody, mode1Body, transitionBody,
	)
	session.townMu.Unlock()
	if err != nil {
		t.Fatalf("drive town actor route for character %d: %v", characterID, err)
	}
}

func r62HotkeyPackets(packets []dnfproto.ChannelPacket) []dnfproto.ChannelPacket {
	out := make([]dnfproto.ChannelPacket, 0, 1)
	for _, packet := range packets {
		if packet.Header.Classification == 0 && packet.Header.MsgID == 454 {
			out = append(out, packet)
		}
	}
	return out
}

func r62AssertTownHotkeyBeforeActorPrerequisites(
	t *testing.T,
	packets []dnfproto.ChannelPacket,
	text string,
) {
	t.Helper()
	notices := r62HotkeyPackets(packets)
	if len(notices) != 1 {
		t.Fatalf("town reselection emitted %d hotkey notices, want 1", len(notices))
	}
	if want := buildAccountHotkeyNoticeBody(1, []byte(text)); !bytes.Equal(notices[0].Body, want) {
		t.Fatalf("town reselection changed saved hotkey bytes: got=%x want=%x", notices[0].Body, want)
	}
	noticeIndex := -1
	prerequisiteIndex := -1
	for index, packet := range packets {
		if packet.Header.Classification == 0 && packet.Header.MsgID == currentAccountGameOptionNoticeMsgID {
			t.Fatalf("town reselection repeated account option notice 173: %+v", packet.Header)
		}
		if packet.Header.Classification == 0 && packet.Header.MsgID == 454 && noticeIndex < 0 {
			noticeIndex = index
		}
		if packet.Header.Classification == 0 &&
			packet.Header.MsgID == uint16(dnfenum.CmdPacketPVPMissionHpPercent) &&
			prerequisiteIndex < 0 {
			prerequisiteIndex = index
		}
	}
	if noticeIndex < 0 || prerequisiteIndex < 0 {
		t.Fatalf("town route order missing hotkey=%d actor_prerequisite=%d", noticeIndex, prerequisiteIndex)
	}
	if noticeIndex >= prerequisiteIndex {
		t.Fatalf("hotkey notice index=%d must precede actor prerequisite index=%d",
			noticeIndex, prerequisiteIndex)
	}
}

func r62AssertNoTownHotkey(t *testing.T, packets []dnfproto.ChannelPacket) {
	t.Helper()
	if notices := r62HotkeyPackets(packets); len(notices) != 0 {
		t.Fatalf("duplicate town route emitted %d hotkey notices", len(notices))
	}
	for _, packet := range packets {
		if packet.Header.Classification == 0 && packet.Header.MsgID == currentAccountGameOptionNoticeMsgID {
			t.Fatal("duplicate town route repeated account option notice 173")
		}
	}
}

func TestCreatorReselectionTownRouteReplaysSavedHotkeyOnce(t *testing.T) {
	t.Setenv(currentGameOptionNoticeEnv, "1")
	service, session, _ := r62TownHotkeyFixture(t)
	connection := session.conn.(*bufferConn)

	scope44Select(t, service, session, 29)
	r62DriveTownActorRoute(t, service, session, 29)
	r62AssertTownHotkeyBeforeActorPrerequisites(
		t,
		scope44Frames(t, session, "r62-town-first-entry"),
		r62TownHotkeyTextA,
	)

	connection.write.Reset()
	r62DriveTownActorRoute(t, service, session, 29)
	r62AssertNoTownHotkey(t, scope44Frames(t, session, "r62-town-repeat-entry"))
}

func TestCreatorReselectionTownRouteDifferentCharacterAfterReturnSelect(t *testing.T) {
	t.Setenv(currentGameOptionNoticeEnv, "1")
	service, session, _ := r62TownHotkeyFixture(t)
	connection := session.conn.(*bufferConn)

	scope44Select(t, service, session, 29)
	r62DriveTownActorRoute(t, service, session, 29)
	r62AssertTownHotkeyBeforeActorPrerequisites(
		t,
		scope44Frames(t, session, "r62-town-seed-character"),
		r62TownHotkeyTextA,
	)

	scope44Upper(t, service, session, 7, nil)
	scope44Upper(t, service, session, 8, nil)
	connection.write.Reset()
	scope44Select(t, service, session, 30)

	// A return-select re-entry resumes the town route during op4. Drive the
	// locked actor-route helper as well, so the assertion covers both the real
	// lifecycle packets and an explicit duplicate route attempt.
	r62DriveTownActorRoute(t, service, session, 30)
	r62AssertTownHotkeyBeforeActorPrerequisites(
		t,
		scope44Frames(t, session, "r62-town-switched-character"),
		r62TownHotkeyTextB,
	)

	connection.write.Reset()
	r62DriveTownActorRoute(t, service, session, 30)
	r62AssertNoTownHotkey(t, scope44Frames(t, session, "r62-town-switched-repeat"))
}

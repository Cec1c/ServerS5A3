package dnfbridge

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestReselectedTutorialCreatorReplays454AfterAccountGate(t *testing.T) {
	t.Setenv(currentGameOptionNoticeEnv, "1")
	service, session := creatorTutorialEntryFixture(t, creatorDefaultGuardRealPVF(t), "10")
	// Earlier character selection on this same TCP connection consumed the
	// account replay; the newly bound character has never received its keys.
	session.gameOptionNoticeSent = true
	conn := session.conn.(*bufferConn)
	if err := service.sendUpperCSharpSelectInit(session, selectFirstSlotRequest()); err != nil {
		t.Fatal(err)
	}
	packets := splitAllCurrentGameServerUpperPackets(t, conn.write.Bytes())
	notices := creatorHotkeyNoticePackets(packets)
	if len(notices) != 1 {
		t.Fatalf("reselected Creator received %d hotkey notices, want 1", len(notices))
	}
	assertCreatorTutorialHotkeyNotice(t, notices[0])
	noticeIndex, previewIndex := -1, -1
	for i, p := range packets {
		if p.Header.MsgID == currentAccountGameOptionNoticeMsgID {
			t.Fatal("character switch replayed connection-scoped account options")
		}
		if p.Header.MsgID == 454 {
			noticeIndex = i
		}
		if p.Header.MsgID == 15 {
			previewIndex = i
		}
	}
	if noticeIndex < 0 || previewIndex <= noticeIndex {
		t.Fatalf("hotkey must precede tutorial preview: %d/%d", noticeIndex, previewIndex)
	}
	conn.write.Reset()
	if err := service.sendUpperCSharpSelectInit(session, selectFirstSlotRequest()); err != nil {
		t.Fatal(err)
	}
	if len(creatorHotkeyNoticePackets(splitAllCurrentGameServerUpperPackets(t, conn.write.Bytes()))) != 0 {
		t.Fatal("duplicate selection reset the same character's keys")
	}
}

func TestReselectedTutorialCreatorRetries454Failure(t *testing.T) {
	t.Setenv(currentGameOptionNoticeEnv, "1")
	path := creatorDefaultGuardRealPVF(t)
	service, session := creatorTutorialEntryFixture(t, path, "10")
	session.gameOptionNoticeSent = true
	counting := &creatorTutorialWriteConn{}
	session.conn = counting
	if err := service.sendUpperCSharpSelectInit(session, selectFirstSlotRequest()); err != nil {
		t.Fatal(err)
	}
	at := creatorTutorialNoticeWriteIndex(t, counting.writes)
	if at == 0 {
		t.Fatal("no reselection 454 to inject failure")
	}
	service, session = creatorTutorialEntryFixture(t, path, "10")
	session.gameOptionNoticeSent = true
	failing := &creatorTutorialFailConn{failAt: at, err: errors.New("reselected 454 write failure")}
	session.conn = failing
	if err := service.sendUpperCSharpSelectInit(session, selectFirstSlotRequest()); err != nil {
		t.Fatal(err)
	}
	if len(creatorHotkeyNoticePackets(splitAllCurrentGameServerUpperPackets(t, failing.write.Bytes()))) != 0 {
		t.Fatal("failed 454 appeared as successful")
	}
	failing.failAt = 0
	failing.write.Reset()
	if err := service.sendUpperCSharpSelectInit(session, selectFirstSlotRequest()); err != nil {
		t.Fatal(err)
	}
	notices := creatorHotkeyNoticePackets(splitAllCurrentGameServerUpperPackets(t, failing.write.Bytes()))
	if len(notices) != 1 {
		t.Fatalf("reselection retry 454 count=%d", len(notices))
	}
	assertCreatorTutorialHotkeyNotice(t, notices[0])
}

func TestSelectedHotkeyReplayPreservesSavedTextAcrossCharacterGenerations(t *testing.T) {
	t.Setenv(currentGameOptionNoticeEnv, "1")
	service, session, conn, repos := testGameOptionFixture(t)
	service.options.currentClientVersion = "official-24.9"
	ctx := context.Background()
	for id, job := range map[string]string{"19": "10", "20": "0"} {
		if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: id, AccountID: "account-1", Job: job, Level: 1}); err != nil {
			t.Fatal(err)
		}
		text := []byte("I01\t90\nI02\t88\n")
		if id == "20" {
			text = []byte("I01\t38\nI02\t37\n")
		}
		if err := repos.Settings.Save(ctx, dnfrepo.SettingsRecord{Scope: characterHotkeyConfigScope(id), Values: map[string]string{"hotkey_config_hex": hex.EncodeToString(text)}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		id         uint16
		generation uint64
		count      int
		keyType    byte
		text       string
	}{
		{19, 1, 1, 1, "I01\t90\nI02\t88\n"},
		{19, 1, 0, 1, ""},
		{20, 2, 1, 0, "I01\t38\nI02\t37\n"},
		{19, 3, 1, 1, "I01\t90\nI02\t88\n"},
	} {
		session.selectedCharacterID, session.characterGeneration = tc.id, tc.generation
		conn.write.Reset()
		if err := service.sendSelectedCharacterHotkeyNotice(session, "test_reselection"); err != nil {
			t.Fatal(err)
		}
		packets := splitAllCurrentGameServerUpperPackets(t, conn.write.Bytes())
		if len(packets) != tc.count {
			t.Fatalf("id%d gen%d packets=%d want%d", tc.id, tc.generation, len(packets), tc.count)
		}
		if tc.count != 0 && (packets[0].Header.MsgID != 454 || !bytes.Equal(packets[0].Body, buildAccountHotkeyNoticeBody(tc.keyType, []byte(tc.text)))) {
			t.Fatalf("id%d keys changed: %+v", tc.id, packets)
		}
	}
}

func TestCreatorReturnSelectStartsNewHotkeyGeneration(t *testing.T) {
	t.Setenv(currentGameOptionNoticeEnv, "1")
	service, session := creatorTutorialEntryFixture(t, creatorDefaultGuardRealPVF(t), "10")
	conn := session.conn.(*bufferConn)
	if err := service.sendUpperCSharpSelectInit(session, selectFirstSlotRequest()); err != nil {
		t.Fatal(err)
	}
	firstGeneration := session.characterGeneration
	if len(creatorHotkeyNoticePackets(splitAllCurrentGameServerUpperPackets(t, conn.write.Bytes()))) != 1 {
		t.Fatal("initial Creator entry did not send keys")
	}
	if err := service.handleUpperReturnSelectCharacter(session, nil); err != nil {
		t.Fatal(err)
	}
	if !session.gameOptionNoticeSent {
		t.Fatal("return-select reset connection account gate")
	}
	conn.write.Reset()
	if err := service.sendUpperCSharpSelectInit(session, selectFirstSlotRequest()); err != nil {
		t.Fatal(err)
	}
	if session.characterGeneration == firstGeneration {
		t.Fatal("return-select did not bind a new generation")
	}
	packets := splitAllCurrentGameServerUpperPackets(t, conn.write.Bytes())
	notices := creatorHotkeyNoticePackets(packets)
	if len(notices) != 1 {
		t.Fatalf("new selected generation received %d 454 notices", len(notices))
	}
	assertCreatorTutorialHotkeyNotice(t, notices[0])
	for _, p := range packets {
		if p.Header.MsgID == currentAccountGameOptionNoticeMsgID {
			t.Fatal("new selection repeated account options")
		}
	}
}

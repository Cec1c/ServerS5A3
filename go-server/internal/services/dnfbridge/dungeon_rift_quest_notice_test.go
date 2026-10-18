package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

func assertRiftUnlockSystemText(t *testing.T, data []byte, want string) {
	t.Helper()
	p, rest := splitGameServerUpperPacket(t, data)
	if len(rest) != 0 || p.Header.MsgID != 12 || p.Header.Classification != 0 || len(p.Body) < 12 || !bytes.Equal(p.Body[:4], []byte{0, 0xff, 0xff, 0}) {
		t.Fatalf("not one native system text: header=%+v body=%x rest=%x", p.Header, p.Body, rest)
	}
	n := int(binary.LittleEndian.Uint32(p.Body[4:8]))
	if n < 0 || n > 240 || len(p.Body) != 12+n || binary.LittleEndian.Uint32(p.Body[8+n:]) != 0 {
		t.Fatalf("invalid system text framing: %x", p.Body)
	}
	text, err := simplifiedchinese.GBK.NewDecoder().Bytes(p.Body[8 : 8+n])
	if err != nil || string(text) != want || strings.Contains(string(text), "上限") {
		t.Fatalf("wrong unlock notice %q: %v", text, err)
	}
}

func TestRiftUnlockNoticePreservesCauseAndReportsSelfOrParty(t *testing.T) {
	for _, test := range []struct {
		name    string
		missing uint16
		want    string
	}{
		{"self", 6, "尚未完成魔界裂缝的入场前置任务，无法进入。"},
		{"other", 72, "队伍中有角色尚未完成魔界裂缝的入场前置任务，无法进入。"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := &Service{options: options{currentClientVersion: "official-24.9", gameUpperBodyCodec: gameUpperBodyCodecPlain}}
			session := &gameSession{selectedCharacterID: 6, conn: &bufferConn{}}
			cause := &dungeonMapQuestRequiredError{characterID: test.missing, dungeonID: 316, questID: 2888}
			err := fmt.Errorf("prepare member: %w", cause)
			if !errors.Is(err, errDungeonMapQuestRequired) {
				t.Fatal("lost admission cause")
			}
			if e := s.refuseDungeonSelectEntryValidation(session, dungeoncmd.SelectDungeonRequest{DungeonID: 316}, err); e != nil {
				t.Fatal(e)
			}
			p, rest := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
			if p.Header.Classification != 1 || p.Header.MsgID != 16 || !bytes.Equal(p.Body, []byte{0, 0x13, 0}) {
				t.Fatalf("old misleading refusal retained: %+v %x", p.Header, p.Body)
			}
			assertRiftUnlockSystemText(t, rest, test.want)
		})
	}
}

func TestRiftUnlockNoticeDoesNotReclassifyOtherFailures(t *testing.T) {
	for _, test := range []struct {
		name    string
		cause   error
		version string
		want    byte
	}{
		{"daily limit", dnfdungeon.ErrDailyEntryLimit, "official-24.9", 0xad},
		{"plain permission", errDungeonMapQuestRequired, "official-24.9", 0xad},
		{"repository", errors.New("quest repository unavailable"), "official-24.9", 1},
		{"legacy", &dungeonMapQuestRequiredError{dungeonID: 316}, "legacy", 0xad},
		{"unrelated map", &dungeonMapQuestRequiredError{dungeonID: 310}, "official-24.9", 0xad},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := &Service{options: options{currentClientVersion: test.version, gameUpperBodyCodec: gameUpperBodyCodecPlain}}
			session := &gameSession{selectedCharacterID: 6, conn: &bufferConn{}}
			if err := s.refuseDungeonSelectEntryValidation(session, dungeoncmd.SelectDungeonRequest{}, test.cause); err != nil {
				t.Fatal(err)
			}
			p, rest := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
			if !bytes.Equal(p.Body, []byte{0, test.want, 0}) || len(rest) != 0 {
				t.Fatalf("other failure changed: %x rest=%x", p.Body, rest)
			}
		})
	}
}

func TestRealPVFRiftUnlockErrorKeepsFailingCharacterThroughPreparation(t *testing.T) {
	f := riftUnlockFixture(t)
	session := &gameSession{selectedCharacterID: 72}
	err := f.service.validateCurrentRiftUnlock(context.Background(), session, f.dungeon)
	var cause *dungeonMapQuestRequiredError
	if !errors.As(err, &cause) || cause.characterID != 72 || cause.dungeonID != 316 || cause.questID != 2888 {
		t.Fatalf("wrong missing unlock identity: %v", err)
	}
}

func TestRiftUnlockNoticeStopsOnWriteFailureAndKeepsActiveRuntimeSilent(t *testing.T) {
	cause := &dungeonMapQuestRequiredError{characterID: 72, dungeonID: 316, questID: 2888}
	for _, failAt := range []int{1, 2} {
		t.Run(fmt.Sprintf("write%d", failAt), func(t *testing.T) {
			writeErr := errors.New("refusal write failed")
			conn := &failNthDungeonWriteConn{failAt: failAt, err: writeErr}
			session := &gameSession{selectedCharacterID: 6, conn: conn}
			s := &Service{options: options{currentClientVersion: "official-24.9", gameUpperBodyCodec: gameUpperBodyCodecPlain}}
			if err := s.refuseDungeonSelectEntryValidation(session, dungeoncmd.SelectDungeonRequest{DungeonID: 316}, cause); !errors.Is(err, writeErr) {
				t.Fatalf("write error lost: %v", err)
			}
			if conn.writes != failAt {
				t.Fatalf("writes after failure: %d", conn.writes)
			}
		})
	}
	s := &Service{options: options{currentClientVersion: "official-24.9", gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	session := &gameSession{selectedCharacterID: 6, conn: &bufferConn{}}
	session.dungeon.runtime = &runtimeDungeonState{}
	if err := s.refuseDungeonSelectEntryValidation(session, dungeoncmd.SelectDungeonRequest{DungeonID: 316}, cause); err != nil {
		t.Fatal(err)
	}
	if data := session.conn.(*bufferConn).write.Bytes(); len(data) != 0 {
		t.Fatalf("active runtime received refusal: %x", data)
	}
}

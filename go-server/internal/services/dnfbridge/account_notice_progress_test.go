package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/accountnotice"
	proto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"os"
	"path/filepath"
	"testing"
)

func noticeFixture(t *testing.T) (*Service, repo.Group, *gameSession) {
	t.Helper()
	s, r := newStoryDigestTestService(t, repo.CharacterRecord{CharacterID: "77", AccountID: "a", Slot: 0, Name: "first", Level: 90, Job: "0", Stats: map[string]int64{currentDungeonTutorialCompletedKey: currentDungeonTutorialCompleteFlag}})
	s.options.currentClientVersion = "official-24.9"
	for _, id := range []string{"a", "b"} {
		if err := r.Account.Save(context.Background(), repo.AccountRecord{AccountID: id, Metadata: map[string]string{"wallet": "keep"}}); err != nil {
			t.Fatal(err)
		}
	}
	p := newStoryDigestTestSession()
	p.accountID = "a"
	p.selectedCharacterID = 77
	return s, r, p
}
func noticeRequest(id uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, id)
	return b
}
func sendNoticeReport(t *testing.T, s *Service, p *gameSession, upper bool, kind byte, body []byte) {
	t.Helper()
	var err error
	if upper {
		var wire []byte
		wire, err = proto.BuildChannelPacket(503, body, 0, kind)
		if err == nil {
			wire[0] = kind // BuildChannelPacket treats zero as its default class.
			err = s.handleGameUpper(p, wire)
		}
	} else {
		err = s.handleGameCommand(p, kind, 503, body)
	}
	if err != nil {
		t.Fatal(err)
	}
}
func readNotice(t *testing.T, s *Service, p *gameSession) []byte {
	t.Helper()
	p.conn.(*bufferConn).write.Reset()
	if err := s.sendCurrentAccountNoticeProgress(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
	if packet.Header.Classification != 0 || packet.Header.MsgID != 440 || len(rest) != 0 {
		t.Fatalf("packet=%+v rest=%x", packet.Header, rest)
	}
	return packet.Body
}
func TestAccountNoticeRoutesReconnectAccountSwitchAndNative(t *testing.T) {
	for _, upper := range []bool{false, true} {
		s, r, p := noticeFixture(t)
		body := noticeRequest(42)
		if dir := os.Getenv("DNF_ACCOUNT_NOTICE_NATIVE_DIR"); dir != "" {
			var err error
			body, err = os.ReadFile(filepath.Join(dir, "request.bin"))
			if err != nil {
				t.Fatal(err)
			}
		}
		if !bytes.Equal(body, noticeRequest(42)) {
			t.Fatalf("native body=%x", body)
		}
		if got := readNotice(t, s, p); !bytes.Equal(got, []byte{0}) {
			t.Fatal(got)
		}
		p.conn.(*bufferConn).write.Reset()
		sendNoticeReport(t, s, p, upper, 1, body)
		sendNoticeReport(t, s, p, upper, 1, body)
		if p.conn.(*bufferConn).write.Len() != 0 {
			t.Fatal("passive report generated ACK")
		}
		if err := r.Character.Save(context.Background(), repo.CharacterRecord{CharacterID: "78", AccountID: "a"}); err != nil {
			t.Fatal(err)
		}
		// Rebuild the Service/session and select a different character on the same account.
		next := &Service{options: s.options, repositoryProvider: s.repositoryProvider}
		q := newStoryDigestTestSession()
		q.accountID = "a"
		q.selectedCharacterID = 78
		viewed := readNotice(t, next, q)
		if !bytes.Equal(viewed, []byte{1, 42}) {
			t.Fatalf("relogin=%x", viewed)
		}
		q.accountID = "b"
		fresh := readNotice(t, next, q)
		if !bytes.Equal(fresh, []byte{0}) {
			t.Fatalf("other account=%x", fresh)
		}
		if dir := os.Getenv("DNF_ACCOUNT_NOTICE_NATIVE_DIR"); dir != "" {
			for name, data := range map[string][]byte{"notice-viewed.bin": viewed, "notice-fresh.bin": fresh} {
				if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}
func TestAccountNoticeRejectsMalformedAndUnownedReports(t *testing.T) {
	for _, upper := range []bool{false, true} {
		for _, body := range [][]byte{nil, {42}, {42, 0, 0}, noticeRequest(128), noticeRequest(256), {42, 0, 0, 0, 0}} {
			s, r, p := noticeFixture(t)
			sendNoticeReport(t, s, p, upper, 1, body)
			ids, err := accountnotice.Load(context.Background(), r.Account, "a")
			if err != nil || len(ids) != 0 {
				t.Fatalf("body=%x ids=%v err=%v", body, ids, err)
			}
		}
		for _, mode := range []string{"class", "owner", "empty", "missing", "deleted", "legacy"} {
			s, r, p := noticeFixture(t)
			kind := byte(1)
			switch mode {
			case "class":
				kind = 0
			case "owner":
				p.accountID = "b"
			case "empty":
				p.accountID = ""
			case "missing":
				p.selectedCharacterID = 999
			case "deleted":
				c, _, _ := r.Character.Load(context.Background(), "77")
				c.Stats["delete_flag"] = 1
				if err := r.Character.Save(context.Background(), c); err != nil {
					t.Fatal(err)
				}
			case "legacy":
				s.options.currentClientVersion = ""
			}
			sendNoticeReport(t, s, p, upper, kind, noticeRequest(42))
			for _, id := range []string{"a", "b"} {
				ids, err := accountnotice.Load(context.Background(), r.Account, id)
				if err != nil || len(ids) != 0 {
					t.Fatalf("%s %s=%v err=%v", mode, id, ids, err)
				}
			}
		}
	}
}
func TestAccountNoticeSelectionBeforeScene(t *testing.T) {
	s, _, p := noticeFixture(t)
	if err := s.sendUpperCSharpSelectInit(p, []byte{77, 0}); err != nil {
		t.Fatal(err)
	}
	data := p.conn.(*bufferConn).write.Bytes()
	selected, restored := false, false
	for len(data) > 0 {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, data)
		data = rest
		if packet.Header.Classification == 1 && packet.Header.MsgID == 4 {
			selected = true
		}
		if packet.Header.Classification == 0 && packet.Header.MsgID == 440 {
			if !selected || restored || !bytes.Equal(packet.Body, []byte{0}) {
				t.Fatalf("restore order/body %x", packet.Body)
			}
			restored = true
		}
		if packet.Header.Classification == 0 && packet.Header.MsgID == 24 && !restored {
			t.Fatal("scene before account progress")
		}
	}
	if !selected || !restored {
		t.Fatalf("selected=%t restored=%t", selected, restored)
	}
}

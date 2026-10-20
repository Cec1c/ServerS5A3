package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/characterfriend"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"os"
	"path/filepath"
	"testing"
)

func officialFriendRequest(op uint16, id uint16, group byte, name string) []byte {
	var w packetWriter
	if op == 290 {
		w.writeUint16(id)
	}
	w.writeByte(group)
	w.writeRawDstr(rosterNameBytes(name))
	return w.bytes()
}

func prepareOfficialFriendTest(t *testing.T) (*Service, *gameSession, *gameSession, *bufferConn, *bufferConn) {
	s, a, b, ac, bc := newCurrentSocialPeerTest(t)
	s.options.currentClientVersion = "official-24.9"
	g, _ := s.repositoryGroup()
	for _, id := range []string{"dnf:1", "dnf:2"} {
		if err := g.Account.Save(context.Background(), dnfrepo.AccountRecord{AccountID: id, Metadata: map[string]string{"keep": "yes"}}); err != nil {
			t.Fatal(err)
		}
	}
	return s, a, b, ac, bc
}

func TestOfficialCharacterFriendNativeRequestFormats(t *testing.T) {
	if dir := os.Getenv("DNF90_CHARACTER_FRIEND_NATIVE_DIR"); dir != "" {
		for _, tc := range []struct {
			file   string
			op, id uint16
			group  byte
		}{{"friend-request-290-65535-1.bin", 290, 65535, 1}, {"friend-request-290-5-0.bin", 290, 5, 0}, {"friend-request-291-0-1.bin", 291, 0, 1}} {
			raw, err := os.ReadFile(filepath.Join(dir, tc.file))
			if err != nil {
				t.Fatal(err)
			}
			got, err := decodeCurrentCharacterFriendMutation(tc.op, raw)
			if err != nil || got.TargetID != tc.id || got.ServerGroup != tc.group || got.Name != "来来来" {
				t.Fatalf("actual native fixture %+v err=%v", got, err)
			}
		}
	}
	for _, op := range []uint16{290, 291} {
		body := officialFriendRequest(op, 0xffff, 0, "来来来")
		got, err := decodeCurrentCharacterFriendMutation(op, body)
		if err != nil || got.Name != "来来来" || got.ServerGroup != 0 || (op == 290 && got.TargetID != 0xffff) {
			t.Fatalf("request=%+v err=%v", got, err)
		}
		for _, bad := range [][]byte{body[:len(body)-1], append(append([]byte(nil), body...), 0), {}} {
			if _, err := decodeCurrentCharacterFriendMutation(op, bad); err == nil {
				t.Fatal("malformed accepted")
			}
		}
	}
	var old packetWriter
	old.writeUint16(5)
	old.writeRawDstr(rosterNameBytes("来来来"))
	if _, err := decodeCurrentCharacterFriendMutation(290, old.bytes()); err == nil {
		t.Fatal("historical layout accepted by current owner")
	}
}

func TestOfficialCharacterFriendsPersistOfflineReloadAndDelete(t *testing.T) {
	s, a, b, ac, bc := prepareOfficialFriendTest(t)
	g, _ := s.repositoryGroup()
	s.unbindGameSession(b)
	if err := s.handleCurrentOfficialCharacterFriendMutation(a, 290, officialFriendRequest(290, 0xffff, 0, "来来来")); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, ac.write.Bytes())
	if ack.Header.MsgID != 290 || !bytes.Equal(ack.Body, []byte{1}) {
		t.Fatal("current ACK")
	}
	list, rest := splitGameServerUpperPacket(t, rest)
	if list.Header.MsgID != 273 || binary.LittleEndian.Uint32(list.Body) != 0 || binary.LittleEndian.Uint32(list.Body[4:]) != 1 || len(rest) != 0 {
		t.Fatal("full list missing")
	}
	if binary.LittleEndian.Uint16(list.Body[9:]) != 0 {
		t.Fatal("offline friend invented online channel")
	}
	if bc.write.Len() != 0 {
		t.Fatal("one-way addition notified target")
	}
	rows, err := characterfriend.NewOwner(g).List(context.Background(), "dnf:1", "1")
	if err != nil || len(rows) != 1 || rows[0].CharacterID != "5" {
		t.Fatalf("persistent relation: %v %v", rows, err)
	}
	ac.write.Reset()
	if err := s.sendCurrentCharacterFriendList(a, "relogin"); err != nil {
		t.Fatal(err)
	}
	reloaded, rest := splitGameServerUpperPacket(t, ac.write.Bytes())
	if !bytes.Equal(list.Body, reloaded.Body) || len(rest) != 0 {
		t.Fatal("reload lost relation")
	}
	ac.write.Reset()
	if err := s.handleCurrentOfficialCharacterFriendMutation(a, 291, officialFriendRequest(291, 0, 0, "来来来")); err != nil {
		t.Fatal(err)
	}
	ack, rest = splitGameServerUpperPacket(t, ac.write.Bytes())
	deletion, rest := splitGameServerUpperPacket(t, rest)
	empty, rest := splitGameServerUpperPacket(t, rest)
	if ack.Header.MsgID != 291 || deletion.Header.MsgID != 273 || binary.LittleEndian.Uint32(deletion.Body) != 2 || !bytes.Equal(empty.Body, make([]byte, 8)) || len(rest) != 0 {
		t.Fatal("deletion must remove native node then refresh")
	}
	rows, err = characterfriend.NewOwner(g).List(context.Background(), "dnf:1", "1")
	if err != nil || len(rows) != 0 {
		t.Fatal("delete not durable")
	}
	account, _, _ := g.Account.Load(context.Background(), "dnf:1")
	if account.Metadata["keep"] != "yes" {
		t.Fatal("account state lost")
	}
}

func TestOfficialCharacterFriendIdentityAndGroupRejections(t *testing.T) {
	for _, tc := range []struct {
		name   string
		id     uint16
		group  byte
		target string
	}{{"wrong_id", 9, 0, "来来来"}, {"foreign_group", 5, 1, "来来来"}, {"self", 1, 0, "pouut"}, {"missing", 0xffff, 0, "absent"}} {
		t.Run(tc.name, func(t *testing.T) {
			s, a, _, ac, _ := prepareOfficialFriendTest(t)
			if err := s.handleCurrentOfficialCharacterFriendMutation(a, 290, officialFriendRequest(290, tc.id, tc.group, tc.target)); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacket(t, ac.write.Bytes())
			if ack.Body[0] != 0 || len(rest) != 0 {
				t.Fatal("rejected identity acknowledged")
			}
			g, _ := s.repositoryGroup()
			rows, _ := characterfriend.NewOwner(g).List(context.Background(), "dnf:1", "1")
			if len(rows) != 0 {
				t.Fatal("rejection created relation")
			}
		})
	}
}

func TestOfficialCharacterFriendExportsRealCodecFixtures(t *testing.T) {
	s, _, _, _, _ := prepareOfficialFriendTest(t)
	g, _ := s.repositoryGroup()
	row, _, _ := g.Character.Load(context.Background(), "5")
	for _, tc := range []struct {
		name string
		rows []dnfrepo.CharacterRecord
	}{{"friend-empty.bin", nil}, {"friend-one.bin", []dnfrepo.CharacterRecord{row}}, {"friend-two.bin", []dnfrepo.CharacterRecord{row, row}}} {
		body, err := s.buildCurrentCharacterFriendListBody(tc.rows)
		if err != nil {
			t.Fatal(err)
		}
		if dir := os.Getenv("DNF90_CHARACTER_FRIEND_NATIVE_DIR"); dir != "" {
			if err := os.WriteFile(filepath.Join(dir, tc.name), body, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

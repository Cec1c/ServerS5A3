package dnfbridge

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestOfficialRepresentNameNotificationCannotPopulateFriendRows(t *testing.T) {
	for _, profile := range []string{"official-24.9", "legacy"} {
		t.Run(profile, func(t *testing.T) {
			conn := &bufferConn{}
			s := &Service{options: options{currentClientVersion: profile, gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain}}
			session := &gameSession{conn: conn, connID: "represent-opcode-fixture"}
			encoded, err := encodeRepresentAccountName("测试冒险团")
			if err != nil {
				t.Fatal(err)
			}
			var w packetWriter
			w.writeRawDstr(encoded)
			want := w.bytes()
			id := uint16(1370)
			if profile != "official-24.9" {
				want = append(want, 0)
				id = 1371
			}
			if err := s.sendRepresentAccountNameState(session, "测试冒险团", false, "fixture"); err != nil {
				t.Fatal(err)
			}
			packet, rest := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
			if packet.Header.MsgID != id || packet.Header.Classification != 0 || !bytes.Equal(packet.Body, want) || len(rest) != 0 {
				t.Fatalf("profile=%s header=%+v body=%x wantID=%d body=%x trailing=%x", profile, packet.Header, packet.Body, id, want, rest)
			}
			if dir := os.Getenv("DNF90_REPRESENT_NATIVE_FIXTURE_DIR"); dir != "" {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "represent-"+profile+".bin"), packet.Body, 0644); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestOfficialRepresentCommandsPersistOnlyCurrentAuthenticatedOwner(t *testing.T) {
	repos := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	for _, id := range []string{"player-a", "fallback"} {
		if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: id, State: "active"}); err != nil {
			t.Fatal(err)
		}
	}
	conn := &bufferConn{}
	s := &Service{options: options{currentClientVersion: "official-24.9", accountID: "fallback", gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain}, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	session := &gameSession{conn: conn, connID: "official-represent", accountID: "player-a", accountFromToken: true, loginPacketSeen: true}
	var w packetWriter
	w.writeRawDstr([]byte("group1"))
	request := w.bytes()
	for _, id := range []uint16{1437, 1444} {
		frame, err := dnfproto.BuildChannelPacket(id, request, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.handleGameUpper(session, frame); err != nil {
			t.Fatal(err)
		}
	}
	account, _, _ := repos.Account.Load(ctx, "player-a")
	if account.RepresentAccountName != "" || conn.write.Len() != 0 {
		t.Fatal("friend/emoticon opcode reached name owner")
	}
	for _, id := range []uint16{1441, 1435} {
		conn.write.Reset()
		frame, err := dnfproto.BuildChannelPacket(id, request, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.handleGameUpper(session, frame); err != nil {
			t.Fatal(err)
		}
		packet, rest := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
		if packet.Header.MsgID != id || packet.Header.Classification != 1 || !bytes.Equal(packet.Body, append([]byte{1}, request...)) || len(rest) != 0 {
			t.Fatalf("official command=%d header=%+v body=%x", id, packet.Header, packet.Body)
		}
	}
	account, _, _ = repos.Account.Load(ctx, "player-a")
	fallback, _, _ := repos.Account.Load(ctx, "fallback")
	if account.RepresentAccountName != "group1" || fallback.RepresentAccountName != "" {
		t.Fatal("name persistence owner mismatch")
	}
	conn.write.Reset()
	session.accountFromToken = false
	handled, err := s.handleOfficialRepresentAccountCommand(session, 1435, 1, request)
	if !handled || err != nil {
		t.Fatalf("unauth handled=%t err=%v", handled, err)
	}
	packet, _ := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Header.MsgID != 1435 || !bytes.Equal(packet.Body, []byte{0, 3}) {
		t.Fatalf("unauth packet=%+v body=%x", packet.Header, packet.Body)
	}
}

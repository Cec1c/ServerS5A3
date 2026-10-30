package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
	"longheng.io/server/internal/modules/dnf/protocol"
	r "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func megaphoneFixture(t *testing.T, mode byte) (*Service, map[uint16]*gameSession, r.Group) {
	t.Helper()
	s, peers, repos := guildHallFixture(t)
	s.options.gameUpperClientBodyCodec = gameUpperClientBodyCodecPlain
	source := townMoveTestSource{
		"monster/monster.lst":     "",
		"stackable/stackable.lst": "36 `cash/megaphone_heartbeat.stk` 37 `cash/megaphone_echo.stk`",
		"equipment/equipment.lst": "",
	}
	for _, path := range []string{"heartbeat", "echo"} {
		source["stackable/cash/megaphone_"+path+".stk"] = "[stackable type]\n`[waste]` 0\n[minimum level]\n50\n[usable job]\n`[all]`\n[action usable place]\n`[village]` `[seria room]`\n[/action usable place]\n"
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog = catalog
	for id, p := range peers {
		c, _, _ := repos.Character.Load(context.Background(), fmt.Sprint(id))
		c.Level, c.Stats["server_group_id"] = 90, 1
		if err := repos.Character.Save(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		p.connectionTownActorOwnerChannel, p.townActorOwnerChannel = 5, 5
	}
	item := int64(36)
	if mode == 13 {
		item = 37
	}
	if err := repos.Inventory.Save(context.Background(), r.InventoryRecord{CharacterID: "29", Slots: map[string]r.ItemStack{"0:68": {ItemID: item, Count: 3}}}); err != nil {
		t.Fatal(err)
	}
	return s, peers, repos
}

func TestMegaphoneTextUsesNativeCharacterLimit(t *testing.T) {
	for _, text := range []string{strings.Repeat("喊", 254), strings.Repeat("a", 254), strings.Repeat("𠀀", 127)} {
		body, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		if !validCurrentMegaphoneText(body) {
			t.Fatal("valid native text rejected")
		}
	}
	for _, text := range []string{strings.Repeat("喊", 255), strings.Repeat("a", 255), strings.Repeat("𠀀", 128)} {
		body, _ := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(text))
		if validCurrentMegaphoneText(body) {
			t.Fatal("overlong text accepted")
		}
	}
	if validCurrentMegaphoneText([]byte{0x81}) || validCurrentMegaphoneText([]byte{'a', 0}) {
		t.Fatal("invalid encoding accepted")
	}
}

func TestMegaphoneFailedSenderStillDeliversCommittedUse(t *testing.T) {
	s, peers, repos := megaphoneFixture(t, 11)
	failed := &guildChatFailedWriteConn{}
	peers[29].conn = failed
	wire, _ := protocol.BuildChannelPacket(17, megaphoneRequest(11), 5, protocol.DefaultChannelClassification)
	if err := s.handleGameUpper(peers[29], wire); err == nil || !failed.closed {
		t.Fatal("partial sender write not closed")
	}
	for _, id := range []uint16{30, 31, 32, 33} {
		p, tail := splitCurrentGameServerUpperPacketAuto(t, peers[id].conn.(*bufferConn).write.Bytes())
		if p.Header.MsgID != 118 || len(tail) != 0 {
			t.Fatal("healthy recipient lost committed message")
		}
	}
	inv, _, _ := repos.Inventory.Load(context.Background(), "29")
	if inv.Slots["0:68"].Count != 2 {
		t.Fatal("wrong debit")
	}
	if len(s.characterAssetReservations) != 0 {
		t.Fatal("reservation leaked")
	}
}

func TestMegaphoneSlowRecipientDoesNotBlockOtherChannels(t *testing.T) {
	s, peers, _ := megaphoneFixture(t, 11)
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	peers[30].conn = server
	startGuildChatTestEvents(t, s, peers)
	wire, _ := protocol.BuildChannelPacket(17, megaphoneRequest(11), 5, protocol.DefaultChannelClassification)
	done := make(chan error, 1)
	go func() { done <- s.handleGameUpper(peers[29], wire) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		client.Close()
		<-done
		t.Fatal("slow recipient blocked sender")
	}
	flushGuildChatTestEvents(t, s, peers[33])
	p, tail := splitCurrentGameServerUpperPacketAuto(t, peers[33].conn.(*bufferConn).write.Bytes())
	if p.Header.MsgID != 118 || len(tail) != 0 {
		t.Fatal("healthy cross-channel recipient blocked")
	}
	client.Close()
}

func TestMegaphoneQueuedChannelChangeDropsPendingMessage(t *testing.T) {
	s, peers, _ := megaphoneFixture(t, 13)
	startGuildChatTestEvents(t, s, peers)
	resume := pauseGuildChatRecipient(t, s, peers[30])
	sendMegaphoneTest(t, s, peers[29], megaphoneRequest(13), 5)
	peers[30].channel.ID++
	resume()
	flushGuildChatTestEvents(t, s, peers[30])
	if peers[30].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("pending horn leaked after channel change")
	}
}

func TestMegaphoneRealPVF(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []byte{11, 13} {
		s, peers, repos := megaphoneFixture(t, mode)
		s.pvfItemCatalog = catalog
		sendMegaphoneTest(t, s, peers[29], megaphoneRequest(mode), 7)
		inv, _, _ := repos.Inventory.Load(context.Background(), "29")
		if inv.Slots["0:68"].Count != 2 {
			t.Fatal("real PVF horn not consumed", mode)
		}
	}
}

func TestMegaphoneRecipientRevalidation(t *testing.T) {
	for _, kind := range []string{"rebind", "foreign_group", "channel_changed", "revoked", "cancelled", "wrong_owner"} {
		t.Run(kind, func(t *testing.T) {
			s, peers, repos := megaphoneFixture(t, 13)
			p := peers[30]
			snapshot, _ := s.boundGameSessionCharacterSnapshot(p)
			channel := p.channel.ID
			switch kind {
			case "rebind":
				s.bindGameSessionCharacter(p, 32)
			case "foreign_group", "wrong_owner":
				c, _, _ := repos.Character.Load(context.Background(), "30")
				if kind == "wrong_owner" {
					c.AccountID = "other"
				} else {
					c.Stats["server_group_id"] = 2
				}
				if err := repos.Character.Save(context.Background(), c); err != nil {
					t.Fatal(err)
				}
			case "channel_changed":
				p.channel.ID++
			case "revoked":
				p.loginRevoked.Store(true)
			case "cancelled":
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				p.ctx = ctx
			}
			_ = s.queueCurrentMegaphone(snapshot, 13, channel, 1, buildCurrentMegaphoneBody(13, 1, 5, 29, []byte("sender"), []byte("hello")))
			if p.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("stale recipient received horn")
			}
		})
	}
}

func TestMegaphoneSenderStateRejection(t *testing.T) {
	for _, kind := range []string{"not_ready", "revoked", "stale", "in_dungeon", "expired"} {
		t.Run(kind, func(t *testing.T) {
			s, peers, repos := megaphoneFixture(t, 11)
			p := peers[29]
			switch kind {
			case "not_ready":
				p.townSceneReadyCharacterID = 0
			case "revoked":
				p.loginRevoked.Store(true)
			case "stale":
				delete(s.gameSessions, 29)
			case "in_dungeon":
				p.dungeon.runtime = &runtimeDungeonState{}
			case "expired":
				inv, _, _ := repos.Inventory.Load(context.Background(), "29")
				stack := inv.Slots["0:68"]
				stack.ExpireAt = time.Unix(1, 0)
				inv.Slots["0:68"] = stack
				if err := repos.Inventory.Save(context.Background(), inv); err != nil {
					t.Fatal(err)
				}
			}
			sendMegaphoneTest(t, s, p, megaphoneRequest(11), 5)
			inv, _, _ := repos.Inventory.Load(context.Background(), "29")
			if inv.Slots["0:68"].Count != 3 {
				t.Fatal("invalid sender debited")
			}
			for id, peer := range peers {
				if id != 29 && peer.conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("invalid sender broadcast")
				}
			}
		})
	}
}

func megaphoneRequest(mode byte) []byte {
	var w packetWriter
	w.writeByte(mode)
	w.writeUint16(0)
	w.writeUint32(68)
	w.writeRawDstr([]byte("hello"))
	return w.bytes()
}

func sendMegaphoneTest(t *testing.T, s *Service, p *gameSession, body []byte, seq uint16) {
	t.Helper()
	wire, err := protocol.BuildChannelPacket(17, body, seq, protocol.DefaultChannelClassification)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.handleGameUpper(p, wire); err != nil {
		t.Fatal(err)
	}
}

func TestMegaphoneNativeRequestScopeAndInventoryOrder(t *testing.T) {
	for _, mode := range []byte{11, 13} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			s, peers, repos := megaphoneFixture(t, mode)
			request := megaphoneRequest(mode)
			if dir := os.Getenv("DNF_MEGAPHONE_NATIVE_DIR"); dir != "" {
				if b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("sender-%d.bin", mode))); err == nil {
					request = b
				}
			}
			sendMegaphoneTest(t, s, peers[29], request, 5)
			ack, rest := splitCurrentGameServerUpperPacketAuto(t, peers[29].conn.(*bufferConn).write.Bytes())
			if ack.Header.MsgID != 17 || !bytes.Equal(ack.Body, []byte{1}) {
				t.Fatalf("horn ACK=%x", ack.Body)
			}
			echo, rest := splitCurrentGameServerUpperPacketAuto(t, rest)
			if echo.Header.MsgID != 118 || echo.Body[0] != mode || echo.Body[1] != 1 {
				t.Fatalf("horn echo=%+v", echo)
			}
			refresh, rest := splitCurrentGameServerUpperPacketAuto(t, rest)
			if refresh.Header.MsgID != 14 || len(rest) != 0 {
				t.Fatalf("inventory must follow prediction: %+v", refresh)
			}
			inv, _, _ := repos.Inventory.Load(context.Background(), "29")
			if inv.Slots["0:68"].Count != 2 {
				t.Fatal("one horn must be consumed")
			}
			for _, id := range []uint16{30, 31, 32, 33} {
				wire := peers[id].conn.(*bufferConn).write.Bytes()
				if mode == 13 && id == 33 {
					if len(wire) != 0 {
						t.Fatal("channel horn leaked")
					}
					continue
				}
				n, tail := splitCurrentGameServerUpperPacketAuto(t, wire)
				if n.Header.MsgID != 118 || !bytes.Equal(n.Body, echo.Body) || len(tail) != 0 {
					t.Fatal("cross-map recipient missing or repeated")
				}
			}
			if dir := os.Getenv("DNF_MEGAPHONE_NATIVE_DIR"); dir != "" {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("notice-%d.bin", mode)), echo.Body, 0644); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestMegaphoneRejectAndReplay(t *testing.T) {
	for _, kind := range []string{"missing", "mismatch", "slot_overflow", "target", "nul", "trailing", "replay", "older"} {
		t.Run(kind, func(t *testing.T) {
			s, peers, repos := megaphoneFixture(t, 11)
			body := megaphoneRequest(11)
			if kind == "replay" || kind == "older" {
				sendMegaphoneTest(t, s, peers[29], body, 5)
				for _, p := range peers {
					p.conn.(*bufferConn).write.Reset()
				}
			}
			switch kind {
			case "missing":
				binary.LittleEndian.PutUint32(body[3:], 58)
			case "mismatch":
				body[0] = 13
			case "slot_overflow":
				binary.LittleEndian.PutUint32(body[3:], 65593)
			case "target":
				body[1] = 29
			case "nul":
				body[len(body)-1] = 0
			case "trailing":
				body = append(body, 0, 0, 0, 0)
			}
			seq := uint16(5)
			if kind == "older" {
				seq = 4
			}
			sendMegaphoneTest(t, s, peers[29], body, seq)
			inv, _, _ := repos.Inventory.Load(context.Background(), "29")
			want := int64(3)
			if kind == "replay" || kind == "older" {
				want = 2
			}
			if inv.Slots["0:68"].Count != want {
				t.Fatal("rejection debited horn")
			}
			for id, p := range peers {
				if id != 29 && p.conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("rejection broadcast")
				}
			}
		})
	}
}

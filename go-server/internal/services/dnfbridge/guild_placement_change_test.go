package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/guild"
)

func guildPlacementChangeBody(id, object, x, y, area uint32) []byte {
	body := bytes.Repeat([]byte{0xa5}, 33)
	for i, v := range []uint32{id, object, x, y, area} {
		binary.LittleEndian.PutUint32(body[13+i*4:], v)
	}
	return body
}

func TestGuildPlacementChangeProductionMoveRemoveReplayReconnect(t *testing.T) {
	s, p, repos := guildPlacementFixture(t)
	sendMerchantRequest(t, s, p, 837, guildPlacementBody(), 77)
	reg, _ := guild.Load(context.Background(), repos.Settings)
	original := reg.Guilds[1].Placements.Rows[0]
	invBefore, _, _ := repos.Inventory.Load(context.Background(), "29")
	body := guildPlacementChangeBody(1, 71021, 938, 276, 0)
	p.conn.(*bufferConn).write.Reset()
	sendMerchantRequest(t, s, p, 839, body, 78)
	if got := guildReply(t, p, 839); !bytes.Equal(got, []byte{1}) {
		t.Fatalf("move ACK %x", got)
	}
	notice := guildReply(t, p, 777)
	if len(notice) != 39 || binary.LittleEndian.Uint32(notice) != 4 || binary.LittleEndian.Uint16(notice[31:]) != 938 {
		t.Fatalf("move notice %x", notice)
	}
	exportGuildPlacement(t, "move-777.bin", notice)
	exportGuildPlacement(t, "ack-839.bin", guildReply(t, p, 839))
	reg, err := guild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	moved := reg.Guilds[1].Placements.Rows[0]
	if moved.X != 938 || !moved.ReadyAt.Equal(original.ReadyAt) || moved.CreatorID != original.CreatorID {
		t.Fatal("move changed identity or construction")
	}
	p.guild.placement.identity = boundGameSessionCharacter{}
	p.conn.(*bufferConn).write.Reset()
	if err = s.writeCurrentGuildEssenceSnapshot(p); err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint16(guildReply(t, p, 191)[21:]) != 938 {
		t.Fatal("reconnect lost move")
	}
	p.conn.(*bufferConn).write.Reset()
	sendMerchantRequest(t, s, p, 838, body, 79)
	if got := guildReply(t, p, 838); !bytes.Equal(got, []byte{1}) {
		t.Fatalf("remove ACK %x", got)
	}
	notice = guildReply(t, p, 777)
	if len(notice) != 39 || binary.LittleEndian.Uint32(notice) != 2 || binary.LittleEndian.Uint32(notice[4:]) != 0 {
		t.Fatalf("remove notice %x", notice)
	}
	exportGuildPlacement(t, "remove-777.bin", notice)
	exportGuildPlacement(t, "ack-838.bin", guildReply(t, p, 838))
	// Old moves and removes only acknowledge their durable receipts.
	for _, op := range []uint16{839, 838} {
		seq := uint16(78)
		if op == 838 {
			seq = 79
		}
		p.conn.(*bufferConn).write.Reset()
		sendMerchantRequest(t, s, p, op, body, seq)
		if !bytes.Equal(guildReply(t, p, op), []byte{1}) {
			t.Fatal("replay ACK")
		}
	}
	reg, _ = guild.Load(context.Background(), repos.Settings)
	if len(reg.Guilds[1].Placements.Rows) != 0 {
		t.Fatal("replayed move resurrected removed object")
	}
	invAfter, _, _ := repos.Inventory.Load(context.Background(), "29")
	if !reflect.DeepEqual(invBefore, invAfter) {
		t.Fatal("move or remove changed book inventory")
	}
	p.guild.placement.identity = boundGameSessionCharacter{}
	p.conn.(*bufferConn).write.Reset()
	if err = s.writeCurrentGuildEssenceSnapshot(p); err != nil {
		t.Fatal(err)
	}
}

func TestGuildPlacementChangeProductionRejectsInvalidRequest(t *testing.T) {
	for _, mode := range []string{"short", "long", "id", "object", "area", "position", "scene", "permission", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			s, p, repos := guildPlacementFixture(t)
			sendMerchantRequest(t, s, p, 837, guildPlacementBody(), 77)
			body := guildPlacementChangeBody(1, 71021, 938, 276, 0)
			switch mode {
			case "short":
				body = body[:32]
			case "long":
				body = append(body, 0)
			case "id":
				binary.LittleEndian.PutUint32(body[13:], 99)
			case "object":
				binary.LittleEndian.PutUint32(body[17:], 71022)
			case "area":
				binary.LittleEndian.PutUint32(body[29:], 1)
			case "position":
				binary.LittleEndian.PutUint32(body[21:], 939)
			case "scene":
				p.townSceneReadyCharacterID = 0
			case "permission":
				reg, _ := guild.Load(context.Background(), repos.Settings)
				reg.Guilds[1].Placements.Rows[0].CreatorID = 99
				reg.Guilds[1].GradePermissions = map[byte]uint32{1: 0}
				if err := guild.Save(context.Background(), repos.Settings, reg, s.gameplayNow()); err != nil {
					t.Fatal(err)
				}
			case "disabled":
				s.options.guildEssenceEnabled = false
			}
			before, _ := guild.Load(context.Background(), repos.Settings)
			p.conn.(*bufferConn).write.Reset()
			sendMerchantRequest(t, s, p, 839, body, 78)
			if got := guildReply(t, p, 839); !bytes.Equal(got, []byte{0, 3}) {
				t.Fatalf("rejection %x", got)
			}
			after, _ := guild.Load(context.Background(), repos.Settings)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("invalid request changed guild")
			}
		})
	}
}

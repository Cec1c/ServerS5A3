package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/protocol"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func cargoTransferFixture(t *testing.T) (*Service, *gameSession, r.Group) {
	t.Helper()
	s, p, repos, src := cargoQueryFixture(t)
	for _, path := range []string{"stackable/cargo.stk", "stackable/feed.stk", "equipment/artifact.equ"} {
		src[path] += "\n[attach type]\n`[trade]`"
	}
	var err error
	s.pvfItemCatalog, err = newPVFDungeonDropCatalog(src)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := guild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	g, _, _ := reg.GuildForCharacter("29")
	g.Cargo = &guild.Cargo{Capacity: 8}
	if err = guild.Save(context.Background(), repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"29", "33"} {
		if err = repos.Inventory.Save(context.Background(), r.InventoryRecord{CharacterID: id, Slots: map[string]r.ItemStack{"0:87": {ItemID: 100, Count: 10, Extra: map[string]string{"item_kind": "stackable", "marker": "keep"}}}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, peer := range []*gameSession{p, s.gameSessions[33]} {
		guildDispatch(t, s, peer, 261, nil)
		if guildReply(t, peer, 261)[0] != 1 {
			t.Fatal("open")
		}
		peer.conn.(*bufferConn).write.Reset()
	}
	return s, p, repos
}

func cargoDepositWire(slot, count uint16) []byte {
	b := make([]byte, 13)
	binary.LittleEndian.PutUint16(b[1:], 87)
	binary.LittleEndian.PutUint32(b[3:], 100)
	binary.LittleEndian.PutUint32(b[7:], uint32(count))
	binary.LittleEndian.PutUint16(b[11:], slot)
	return b
}
func cargoWithdrawWire(slot, count uint16) []byte {
	b := make([]byte, 11)
	binary.LittleEndian.PutUint16(b, slot)
	binary.LittleEndian.PutUint32(b[2:], 100)
	binary.LittleEndian.PutUint32(b[6:], uint32(count))
	return b
}
func sendCargoWire(t *testing.T, s *Service, p *gameSession, upper bool, op, seq uint16, b []byte) {
	t.Helper()
	wire, err := protocol.BuildChannelPacket(op, b, seq, 1)
	if err != nil {
		t.Fatal(err)
	}
	if upper {
		err = s.handleGameUpper(p, wire)
	} else {
		packets, rest, skip, e := protocol.SplitLatestGameStream(wire, 65535)
		if e != nil || len(packets) != 1 || len(rest) != 0 || skip != 0 {
			t.Fatal("wire fixture")
		}
		err = s.handleGameStreamPacket(p, packets[0])
	}
	if err != nil {
		t.Fatal(err)
	}
}
func cargoReplies(t *testing.T, p *gameSession) ([]uint16, [][]byte) {
	t.Helper()
	wire := p.conn.(*bufferConn).write.Bytes()
	var ops []uint16
	var bodies [][]byte
	for len(wire) > 0 {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
		wire = rest
		ops = append(ops, packet.Header.MsgID)
		bodies = append(bodies, append([]byte(nil), packet.Body...))
	}
	p.conn.(*bufferConn).write.Reset()
	return ops, bodies
}

func TestGuildCargoTransferRealStreamAndReplay(t *testing.T) {
	for _, upper := range []bool{false, true} {
		t.Run(fmt.Sprint(upper), func(t *testing.T) {
			s, p, repos := cargoTransferFixture(t)
			sendCargoWire(t, s, p, upper, 247, 101, cargoDepositWire(1, 3))
			ops, b := cargoReplies(t, p)
			if len(ops) < 3 || ops[0] != 247 || ops[1] != 14 || ops[len(ops)-1] != 261 || !bytes.Equal(b[0], []byte{1, 0, 87, 0, 3, 0, 0, 0, 1, 0}) {
				t.Fatalf("deposit ACK/order %v %x", ops, b)
			}
			reg, _ := guild.Load(context.Background(), repos.Settings)
			g, _, _ := reg.GuildForCharacter("29")
			bag, _, _ := repos.Inventory.Load(context.Background(), "29")
			if bag.Slots["0:87"].Count != 7 || g.Cargo.Slots[1].Count != 3 || g.Cargo.Slots[1].Extra["marker"] != "keep" || len(g.Cargo.Transfers) != 1 || len(g.Cargo.History) != 1 {
				t.Fatal("transfer state")
			}
			peerOps, _ := cargoReplies(t, s.gameSessions[33])
			if len(peerOps) != 1 || peerOps[0] != 261 {
				t.Fatalf("peer %v", peerOps)
			}
			sendCargoWire(t, s, p, !upper, 247, 101, cargoDepositWire(1, 3))
			_, b = cargoReplies(t, p)
			if !bytes.Equal(b[0], []byte{0, 0}) {
				t.Fatalf("replay %x", b[0])
			}
			sendCargoWire(t, s, p, upper, 248, 102, cargoWithdrawWire(1, 2))
			ops, b = cargoReplies(t, p)
			if len(ops) < 3 || ops[0] != 248 || ops[1] != 14 || ops[len(ops)-1] != 261 || !bytes.Equal(b[0], []byte{1, 1, 0, 0, 87, 0, 2, 0, 0, 0}) {
				t.Fatalf("withdraw %v %x", ops, b)
			}
			sendCargoWire(t, s, p, upper, 248, 102, cargoWithdrawWire(1, 2))
			_, b = cargoReplies(t, p)
			if !bytes.Equal(b[0], []byte{0, 8}) {
				t.Fatalf("withdraw replay %x", b[0])
			}
			reg, _ = guild.Load(context.Background(), repos.Settings)
			g, _, _ = reg.GuildForCharacter("29")
			bag, _, _ = repos.Inventory.Load(context.Background(), "29")
			if bag.Slots["0:87"].Count != 9 || g.Cargo.Slots[1].Count != 1 || len(g.Cargo.History) != 2 || len(g.Cargo.Transfers) != 2 {
				t.Fatal("duplicate movement")
			}
		})
	}
}

func TestGuildCargoTransferRejectsAndNextRequestWorks(t *testing.T) {
	for _, mode := range []string{"malformed", "count", "capacity", "list", "wrong_scene", "not_loaded", "permission", "locked", "expired", "swap"} {
		t.Run(mode, func(t *testing.T) {
			s, p, repos := cargoTransferFixture(t)
			body := cargoDepositWire(1, 3)
			op := uint16(247)
			bag, _, _ := repos.Inventory.Load(context.Background(), "29")
			reg, _ := guild.Load(context.Background(), repos.Settings)
			g, _, _ := reg.GuildForCharacter("29")
			stack := bag.Slots["0:87"]
			switch mode {
			case "malformed":
				body = body[:12]
			case "count":
				binary.LittleEndian.PutUint32(body[7:], 11)
			case "capacity":
				binary.LittleEndian.PutUint16(body[11:], 8)
			case "list":
				body[0] = 1
			case "wrong_scene":
				p.townPositionSnapshot.TownID = 38
			case "not_loaded":
				p.guild.cargoLoadedGuild = 0
			case "permission":
				g.GradePermissions[1] = 0
			case "locked":
				stack.Extra["equipment_lock_state"] = "1"
			case "expired":
				stack.ExpireAt = s.gameplayNow().Add(-time.Second)
			case "swap":
				op = 249
				body = make([]byte, 12)
			}
			bag.Slots["0:87"] = stack
			repos.Inventory.Save(context.Background(), bag)
			guild.Save(context.Background(), repos.Settings, reg, s.gameplayNow())
			before, _, _ := repos.Settings.Load(context.Background(), r.GuildRegistryScope)
			sendCargoWire(t, s, p, false, op, 1, body)
			ops, b := cargoReplies(t, p)
			if len(ops) == 0 || ops[0] != op || len(b[0]) != 2 || b[0][0] != 0 {
				t.Fatalf("missing failure ACK %v %x", ops, b)
			}
			after, _, _ := repos.Settings.Load(context.Background(), r.GuildRegistryScope)
			afterBag, _, _ := repos.Inventory.Load(context.Background(), "29")
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(bag, afterBag) {
				t.Fatal("failed transfer mutated state")
			}
			p.townPositionSnapshot.TownID = 8
			p.guild.cargoLoadedGuild = g.ID
			g.GradePermissions[1] = guild.CargoPermission
			stack.Extra["equipment_lock_state"] = "0"
			stack.ExpireAt = time.Time{}
			bag.Slots["0:87"] = stack
			repos.Inventory.Save(context.Background(), bag)
			guild.Save(context.Background(), repos.Settings, reg, s.gameplayNow())
			sendCargoWire(t, s, p, false, 247, 2, cargoDepositWire(1, 3))
			ops, b = cargoReplies(t, p)
			if len(ops) == 0 || ops[0] != 247 || b[0][0] != 1 {
				t.Fatalf("failure retained guard %v %x", ops, b)
			}
		})
	}
}

func TestGuildCargoTransferQueuedPeerGetsACKBeforeSnapshot(t *testing.T) {
	s, p, repos := cargoTransferFixture(t)
	peer := s.gameSessions[33]
	s.guildCargoMu.Lock()
	var wg sync.WaitGroup
	for i, actor := range []*gameSession{p, peer} {
		wg.Add(1)
		go func(i int, actor *gameSession) {
			defer wg.Done()
			seq := uint64(i + 1)
			if err := s.handleCurrentGuildCargoTransfer(actor, gameplayRequest{Opcode: 247, Classification: 1, Body: cargoDepositWire(1, 3), Sequence: &seq}); err != nil {
				t.Error(err)
			}
		}(i, actor)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		p.guild.mu.Lock()
		a := p.guild.cargoPending
		p.guild.mu.Unlock()
		peer.guild.mu.Lock()
		b := peer.guild.cargoPending
		peer.guild.mu.Unlock()
		if a == 1 && b == 1 {
			break
		}
		if time.Now().After(deadline) {
			s.guildCargoMu.Unlock()
			t.Fatal("request not queued")
		}
		time.Sleep(time.Millisecond)
	}
	s.guildCargoMu.Unlock()
	wg.Wait()
	for _, actor := range []*gameSession{p, peer} {
		ops, b := cargoReplies(t, actor)
		if len(ops) < 3 || ops[0] != 247 || b[0][0] != 1 || ops[len(ops)-1] != 261 {
			t.Fatalf("snapshot overtook ACK %v %x", ops, b)
		}
	}
	reg, _ := guild.Load(context.Background(), repos.Settings)
	g, _, _ := reg.GuildForCharacter("29")
	if g.Cargo.Slots[1].Count != 6 || len(g.Cargo.History) != 2 {
		t.Fatal("lost concurrent transfer")
	}
}

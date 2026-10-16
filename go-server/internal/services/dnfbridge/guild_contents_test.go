package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"reflect"
	"testing"

	guild "longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/protocol"
	r "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func guildPurchaseFixture(t *testing.T) (*Service, []*gameSession, r.Group) {
	t.Helper()
	s, p, repos := guildTextFixture(t)
	source := cargoQuerySource()
	source["etc/(r)guild.etc"] = "[guild contents list]\n11 4 16 -1 0 10149668 0 10000 `cargo16`\n12 4 8 -1 0 10150915 0 5000 `cargo8`\n[/guild contents list]\n[additional guild contents list]\n22 21 1 30 0 100 0 1000 `contract` 1\n[/additional guild contents list]\n[guild cargo size limit]\n56"
	cat, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog = cat
	reg, err := guild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	reg.Guilds[1].Mileage = 30000
	if err := guild.Save(context.Background(), repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	return s, p, repos
}

func sendGuildPurchase(t *testing.T, s *Service, p *gameSession, id uint32, seq uint16) {
	t.Helper()
	body := make([]byte, 8)
	binary.LittleEndian.PutUint32(body, id)
	wire, err := protocol.BuildChannelPacket(761, body, seq, 1)
	if err != nil {
		t.Fatal(err)
	}
	packets, remaining, skipped, err := protocol.SplitLatestGameStream(wire, 65535)
	if err != nil || len(packets) != 1 || len(remaining) != 0 || skipped != 0 {
		t.Fatalf("purchase split: packets=%d remaining=%x skipped=%d err=%v", len(packets), remaining, skipped, err)
	}
	if err := s.handleGameStreamPacket(p, packets[0]); err != nil {
		t.Fatal(err)
	}
}

func TestGuildCargoPurchaseNativeRequestRefreshAndRetry(t *testing.T) {
	s, p, repos := guildPurchaseFixture(t)
	for _, seq := range []uint16{30, 30, 31} {
		sendGuildPurchase(t, s, p[0], 11, seq)
		if got := guildReply(t, p[0], 761); !bytes.Equal(got, []byte{1}) {
			t.Fatalf("purchase ACK=%x", got)
		}
	}
	reg, err := guild.Load(context.Background(), repos.Settings)
	if err != nil || reg.Guilds[1].Cargo == nil || reg.Guilds[1].Cargo.Capacity != 40 || reg.Guilds[1].Mileage != 10000 || len(reg.CargoPurchases) != 2 {
		t.Fatalf("state=%+v err=%v", reg.Guilds[1], err)
	}
	for _, peer := range p[:2] {
		guildReply(t, peer, 70)
	}
	if p[2].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("foreign guild received purchase")
	}
}

func TestGuildCargoPurchaseBridgeRejectsWithoutCharge(t *testing.T) {
	for _, mode := range []string{"insufficient", "permission", "account", "limit", "text", "trailing", "missing_sequence", "legacy", "unsupported", "save_failure", "reused_sequence"} {
		t.Run(mode, func(t *testing.T) {
			s, p, repos := guildPurchaseFixture(t)
			ctx := context.Background()
			reg, _ := guild.Load(ctx, repos.Settings)
			body := make([]byte, 8)
			binary.LittleEndian.PutUint32(body, 11)
			want := byte(14)
			switch mode {
			case "insufficient":
				reg.Guilds[1].Mileage = 9999
				want = 22
			case "permission":
				reg.Guilds[1].GradePermissions = map[byte]uint32{1: guild.CargoPermission}
				want = 36
			case "account":
				p[0].accountID = "foreign"
			case "limit":
				reg.Guilds[1].Cargo = &guild.Cargo{Capacity: 48}
				want = 13
			case "text":
				binary.LittleEndian.PutUint32(body[4:], 1)
			case "trailing":
				body = append(body, 0)
			case "unsupported":
				binary.LittleEndian.PutUint32(body, 22)
			case "save_failure":
				broken := repos
				broken.Guild = guildTextFailUoW{repos.Guild}
				s.repositoryProvider = func() (r.Group, bool) { return broken, true }
			}
			if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
				t.Fatal(err)
			}
			if mode == "reused_sequence" {
				sendGuildPurchase(t, s, p[0], 12, 30)
				p[0].conn.(*bufferConn).write.Reset()
			}
			before, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			switch mode {
			case "missing_sequence":
				handled, err := currentGameplayModules.DispatchUpper(s, p[0], 761, 1, body)
				if !handled || err != nil {
					t.Fatal(handled, err)
				}
			case "legacy":
				handled, err := currentGameplayModules.DispatchLegacy(s, p[0], 761, body)
				if !handled || err != nil {
					t.Fatal(handled, err)
				}
			default:
				wire, _ := protocol.BuildChannelPacket(761, body, 30, 1)
				if err := s.handleLegacyGamePacket(p[0], wire); err != nil {
					t.Fatal(err)
				}
			}
			if !bytes.Equal(guildReply(t, p[0], 761), []byte{0, want}) {
				t.Fatal("wrong purchase rejection")
			}
			after, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected request charged")
			}
		})
	}
}

func TestGuildCargoPurchaseNativeSequenceWrapAndReconnect(t *testing.T) {
	s, p, repos := guildPurchaseFixture(t)
	sendGuildPurchase(t, s, p[0], 12, 65534)
	session := p[0]
	// Unrelated upper frames still advance the transport sequence.
	for _, seq := range []uint16{65535, 0, 1} {
		wire, _ := protocol.BuildChannelPacket(1276, nil, seq, 1)
		if err := s.handleGameUpper(session, wire); err != nil {
			t.Fatal(err)
		}
	}
	sendGuildPurchase(t, s, session, 12, 65534) // late replay from the previous epoch
	sendGuildPurchase(t, s, session, 12, 2)
	reg, _ := guild.Load(context.Background(), repos.Settings)
	if reg.Guilds[1].Mileage != 20000 || reg.Guilds[1].Cargo.Capacity != 24 || len(reg.CargoPurchases) != 2 {
		t.Fatal("wrap replay charged")
	}
	first, _ := session.guild.purchaseSequence.identity(65536)
	var other guildPurchaseSequence
	other.observe(0)
	second, _ := other.identity(65536)
	if first == second {
		t.Fatal("reconnect reused request nonce")
	}
}

func TestGuildCargoPurchaseRealPVF(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires matching Script.pvf")
	}
	source, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := guild.LoadContentCatalog(source)
	if err != nil || len(c.Contents) != 23 || c.CargoLimit != 56 {
		t.Fatalf("catalog %+v %v", c, err)
	}
	if a, b := c.Contents[11], c.Contents[12]; a.Type != 4 || b.Type != 4 || a.Value != 16 || b.Value != 8 || a.Price != 10000 || b.Price != 5000 || a.Gold || b.Gold {
		t.Fatal("expansion products differ", a, b)
	}
	for _, id := range []uint32{22, 23, 24, 25, 26} {
		if !c.Contents[id].Gold {
			t.Fatal("gold product classified as guild mileage", id)
		}
	}
	for i, want := range []int32{500, 700, 1000} {
		v := c.Contents[uint32(16+i)]
		if v.Type != 6 || v.Value != want || v.Days != -1 || v.Mode != 0 || v.Gold || v.Price != []uint32{100000, 200000, 500000}[i] {
			t.Fatal("member expansion product differs", v)
		}
	}
}

func TestGuildCargoPurchaseRefreshesWarehouseAndPreservesItems(t *testing.T) {
	s, p, repos, source := cargoQueryFixture(t)
	source["etc/(r)guild.etc"] += "\n[guild contents list]\n12 4 8 -1 0 10150915 0 5000 `cargo8`\n[/guild contents list]\n[additional guild contents list]\n[/additional guild contents list]"
	cat, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog = cat
	ctx := context.Background()
	reg, _ := guild.Load(ctx, repos.Settings)
	g, _, _ := reg.GuildForCharacter("29")
	g.Mileage = 10000
	g.Cargo.Capacity = 8
	g.GradePermissions[1] |= guild.PurchasePermission
	if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	sendGuildPurchase(t, s, p, 12, 30)
	if !bytes.Equal(guildReply(t, p, 761), []byte{1}) {
		t.Fatal("purchase rejected")
	}
	for _, id := range []uint16{29, 33} {
		body := guildReply(t, s.gameSessions[id], 261)
		if len(body) != 7+2*119 || body[0] != 1 || binary.LittleEndian.Uint32(body[1:]) != 16 || binary.LittleEndian.Uint16(body[5:]) != 2 {
			t.Fatal("expanded capacity/items not refreshed", id)
		}
	}
	if s.gameSessions[31].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("foreign guild refreshed")
	}
}

package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	guild "longheng.io/server/internal/modules/dnf/guild"
)

func TestGuildMemberExpansionNativeRouteAndRefresh(t *testing.T) {
	s, peers, repos := guildPurchaseFixture(t)
	source := cargoQuerySource()
	source["etc/(r)guild.etc"] = "[guild contents list]\n16 6 500 -1 0 10153531 0 100000 `500`\n17 6 700 -1 0 10153532 0 200000 `700`\n18 6 1000 -1 0 10153533 0 500000 `1000`\n[/guild contents list]\n[additional guild contents list]\n22 21 1 30 0 100 0 1000 `contract` 1\n[/additional guild contents list]\n[guild cargo size limit]\n56"
	cat, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog = cat
	ctx := context.Background()
	reg, _ := guild.Load(ctx, repos.Settings)
	reg.Guilds[1].MemberLimit, reg.Guilds[1].Mileage = 300, 800000
	if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	sendGuildPurchase(t, s, peers[0], 17, 20)
	if got := guildReply(t, peers[0], 761); !bytes.Equal(got, []byte{0, 105}) {
		t.Fatalf("missing prerequisite=%x", got)
	}
	peers[0].conn.(*bufferConn).write.Reset()
	for i, limit := range []uint32{500, 700, 1000} {
		for range 2 {
			sendGuildPurchase(t, s, peers[0], uint32(16+i), uint16(30+i))
			if got := guildReply(t, peers[0], 761); !bytes.Equal(got, []byte{1}) {
				t.Fatalf("ACK=%x", got)
			}
			reg, _ = guild.Load(ctx, repos.Settings)
			if reg.Guilds[1].MemberLimit != int(limit) {
				t.Fatal("capacity did not change")
			}
			for _, peer := range peers[:2] {
				body := guildReply(t, peer, 70)
				// NOTI70 type6 row: type, permanent expiration, mode, capacity.
				row := make([]byte, 10)
				row[0] = 6
				binary.LittleEndian.PutUint32(row[6:], limit)
				if !bytes.Contains(body, row) {
					t.Fatal("benefits did not show real capacity")
				}
				peer.conn.(*bufferConn).write.Reset()
			}
		}
	}
	if reg.Guilds[1].Mileage != 0 || len(reg.MemberExpansions) != 3 || peers[2].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("charge/isolation differs")
	}
	sendGuildPurchase(t, s, peers[0], 16, 40)
	if got := guildReply(t, peers[0], 761); !bytes.Equal(got, []byte{0, 116}) {
		t.Fatalf("repeat expansion=%x", got)
	}
}

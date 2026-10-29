package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/tagtournament"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestTagTournamentUnpaidEntryKeepsAssetsClosed(t *testing.T) {
	for _, id := range []int64{320, 336, 1} {
		s, p, repos := newTownMoveTest(t)
		s.options.currentClientVersion = "official-24.9"
		s.tagTournamentLoadErr = errors.New("测试：配置不可用")
		p.channel = channelcatalog.Channel{Type: 39}
		before, _, err := repos.Character.Load(context.Background(), "29")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.handleCurrentTagTournamentDungeon(p, dungeoncmd.SelectDungeonRequest{DungeonID: uint32(id)}); err != nil {
			t.Fatal(err)
		}
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
		if packet.Header.MsgID != 16 || packet.Header.Classification != 1 || !bytes.Equal(packet.Body, []byte{0, 0x13, 0}) || len(rest) != 0 {
			t.Fatalf("入场拒绝报文边界改变: %+v %x", packet, rest)
		}
		after, _, err := repos.Character.Load(context.Background(), "29")
		if err != nil || !reflect.DeepEqual(before, after) || p.dungeon.runtime != nil {
			t.Fatal("未付费入场仍创建副本或修改资产", err)
		}
	}
}

func TestRealTagTournamentLoadedCatalogStillRejectsUnpaidEntry(t *testing.T) {
	name := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if name == "" {
		t.Skip("未提供真实PVF")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: name})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := tagtournament.LoadCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	s, p, repos := newTownMoveTest(t)
	s.options.currentClientVersion = "official-24.9"
	s.tagTournamentCatalog = catalog
	p.channel = channelcatalog.Channel{Type: 39}
	before, _, err := repos.Character.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{320, 336, 1} {
		p.conn.(*bufferConn).write.Reset()
		if err := s.handleCurrentTagTournamentDungeon(p, dungeoncmd.SelectDungeonRequest{DungeonID: uint32(id)}); err != nil {
			t.Fatal(err)
		}
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
		if packet.Header.MsgID != 16 || packet.Header.Classification != 1 || !bytes.Equal(packet.Body, []byte{0, 0x13, 0}) || len(rest) != 0 {
			t.Fatal("加载配置后误放开进图")
		}
	}
	after, _, err := repos.Character.Load(context.Background(), "29")
	if err != nil || !reflect.DeepEqual(before, after) || p.dungeon.runtime != nil {
		t.Fatal("未付费入场改变玩家状态", err)
	}
}

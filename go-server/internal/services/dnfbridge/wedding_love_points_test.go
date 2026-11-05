package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/protocol"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func weddingLovePointFixture(t *testing.T, balance int64) (*Service, *gameSession, r.Group) {
	t.Helper()
	s, first, second, _ := weddingRoomLeaseFixture(t)
	logger, logPath := newPeerEndpointTestLogger(t)
	s.packetLog = logger
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(string(readPeerEndpointTestLog(t, logger, logPath)))
		} else if err := logger.Close(); err != nil {
			t.Error(err)
		}
	})
	originalRepos, _ := s.repositoryGroup()
	repos := memory.NewMemoryGroup()
	s.repositoryProvider = func() (r.Group, bool) { return repos, true }
	s.wedding.rules.MaxLovePoint, s.wedding.rules.DailyLovePoint = 999, 1
	s.wedding.rules.LovePointItems = map[int64]wedding.LovePointCoupon{10002901: {ItemID: 10002901, Points: 1, MinimumLevel: 1, StackLimit: 999, PVFPath: "stackable/coupon.stk", Village: true, SeriaRoom: true}}
	ctx := context.Background()
	for _, actor := range []*gameSession{first, second} {
		id := "29"
		if actor == second {
			id = "30"
		}
		c, _, _ := originalRepos.Character.Load(ctx, id)
		c.Stats["wedding_v1_love_points"] = balance
		if err := r.SaveCharacterFields(ctx, repos.Character, c, append(r.AllCharacterFields(), r.CharacterFieldWedding)...); err != nil {
			t.Fatal(err)
		}
		inv := r.InventoryRecord{CharacterID: id, Slots: map[string]r.ItemStack{}}
		if actor == first {
			inv.Slots["0:65"] = r.ItemStack{ItemID: 10002901, Count: 2, Extra: map[string]string{"item_kind": "stackable", "stackable_type": "[waste]"}}
		}
		if err := repos.Inventory.Save(ctx, inv); err != nil {
			t.Fatal(err)
		}
	}
	source := cargoQuerySource()
	source["stackable/stackable.lst"] += "\n10002901 `coupon.stk`\n"
	source["stackable/coupon.stk"] = "[minimum level]\n1\n[stack limit]\n999\n[stackable type]\n`[waste]` 0\n[action type]\n`[love point coupon]` 1\n[action usable place]\n`[village]` `[seria room]`\n"
	cat, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog = cat
	return s, first, repos
}

func weddingLovePointBody(slot uint32) []byte {
	b := bytes.Repeat([]byte{0xa5}, 17)
	binary.LittleEndian.PutUint32(b[13:], slot)
	return b
}

func dispatchWeddingLovePoint(t *testing.T, s *Service, p *gameSession, slot uint32, seq uint16) {
	t.Helper()
	wire, err := protocol.BuildChannelPacket(1037, weddingLovePointBody(slot), seq, 1)
	if err != nil {
		t.Fatal(err)
	}
	packets, remaining, skipped, err := protocol.SplitLatestGameStream(wire, 65535)
	if err != nil || len(packets) != 1 || len(remaining) != 0 || skipped != 0 {
		t.Fatalf("split %d %x %d %v", len(packets), remaining, skipped, err)
	}
	if err = s.handleGameStreamPacket(p, packets[0]); err != nil {
		t.Fatal(err)
	}
}

func lovePointStoredState(t *testing.T, repos r.Group) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	c, _, err := repos.Character.Load(ctx, "29")
	if err != nil {
		t.Fatal(err)
	}
	inv, _, err := repos.Inventory.Load(ctx, "29")
	if err != nil {
		t.Fatal(err)
	}
	return c.Stats["wedding_v1_love_points"], inv.Slots["0:65"].Count
}

func TestWeddingLovePointNativeSlotOnlyWireAndReplay(t *testing.T) {
	s, p, repos := weddingLovePointFixture(t, 0)
	dispatchWeddingLovePoint(t, s, p, 65, 100)
	if got := guildReply(t, p, 1037); !bytes.Equal(got, []byte{1, 0, 0, 0, 0}) {
		t.Fatalf("ACK %x", got)
	}
	points, count := lovePointStoredState(t, repos)
	if points != 1 || count != 1 {
		t.Fatalf("points=%d source=%d", points, count)
	}
	if got := guildReply(t, p, 1003); len(got) != 8 || binary.LittleEndian.Uint32(got) != 29 || binary.LittleEndian.Uint32(got[4:]) != 1 {
		t.Fatalf("balance %x", got)
	}
	dispatchWeddingLovePoint(t, s, p, 65, 100)
	points, count = lovePointStoredState(t, repos)
	if points != 1 || count != 1 {
		t.Fatal("wire replay consumed another coupon")
	}
	dispatchWeddingLovePoint(t, s, p, 65, 101)
	points, count = lovePointStoredState(t, repos)
	if points != 2 || count != 0 {
		t.Fatalf("new click points=%d source=%d", points, count)
	}
	dispatchWeddingLovePoint(t, s, p, 65, 100)
	if got := guildReply(t, p, 1037); !bytes.Equal(got, []byte{1, 0xfb, 0xff, 0xff, 0xff}) {
		t.Fatalf("stale ACK %x", got)
	}
	points, count = lovePointStoredState(t, repos)
	if points != 2 || count != 0 {
		t.Fatal("stale wire changed durable assets")
	}
}

func TestWeddingLovePointFailedClickCannotBecomeUseAfterBalanceChanges(t *testing.T) {
	s, p, repos := weddingLovePointFixture(t, 999)
	dispatchWeddingLovePoint(t, s, p, 65, 100)
	if got := guildReply(t, p, 1037); !bytes.Equal(got, []byte{1, 0xfe, 0xff, 0xff, 0xff}) {
		t.Fatalf("overflow ACK %x", got)
	}
	c, _, _ := repos.Character.Load(context.Background(), "29")
	c.Stats["wedding_v1_love_points"] = 0
	if err := r.SaveCharacterFields(context.Background(), repos.Character, c, r.CharacterFieldStats, r.CharacterFieldWedding); err != nil {
		t.Fatal(err)
	}
	dispatchWeddingLovePoint(t, s, p, 65, 100)
	if got := guildReply(t, p, 1037); !bytes.Equal(got, []byte{1, 0xfe, 0xff, 0xff, 0xff}) {
		t.Fatalf("failed replay ACK %x", got)
	}
	points, count := lovePointStoredState(t, repos)
	if points != 0 || count != 2 {
		t.Fatal("failed replay became a new use")
	}
	dispatchWeddingLovePoint(t, s, p, 65, 101)
	points, count = lovePointStoredState(t, repos)
	if points != 1 || count != 1 {
		t.Fatalf("fresh click points=%d count=%d", points, count)
	}
}

func TestWeddingLovePointCodecRejectsMissingHeaderAndInvalidSlot(t *testing.T) {
	for _, length := range []int{0, 4, 13, 16, 18} {
		if _, ok := decodeCurrentWeddingLovePointItem(make([]byte, length)); ok {
			t.Fatalf("length %d accepted", length)
		}
	}
	for _, slot := range []uint32{0, 1, 2, 289, 65536, ^uint32(0)} {
		if _, ok := decodeCurrentWeddingLovePointItem(weddingLovePointBody(slot)); ok {
			t.Fatalf("slot %d accepted", slot)
		}
	}
	if slot, ok := decodeCurrentWeddingLovePointItem(weddingLovePointBody(65)); !ok || slot != 65 {
		t.Fatalf("native body rejected %d %v", slot, ok)
	}
	s, p, repos := weddingLovePointFixture(t, 0)
	if err := currentWeddingLovePointItemHandler(s, p, gameplayRequest{Opcode: 1037, Classification: 1, Body: weddingLovePointBody(65)}); err != nil {
		t.Fatal(err)
	}
	points, count := lovePointStoredState(t, repos)
	if points != 0 || count != 2 {
		t.Fatal("headerless direct dispatch consumed coupon")
	}
}

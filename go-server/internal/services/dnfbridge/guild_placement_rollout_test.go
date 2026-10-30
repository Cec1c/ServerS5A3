package dnfbridge

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/guild"
)

func TestGuildPlacementCompatibilityRejectsNewWritesWithoutConsumption(t *testing.T) {
	s, p, repos := guildPlacementFixture(t)
	s.options.guildPlacementWritesEnabled = false
	ctx := context.Background()
	before, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	beforeInventory, _, err := repos.Inventory.Load(ctx, "29")
	if err != nil {
		t.Fatal(err)
	}
	sendMerchantRequest(t, s, p, 837, guildPlacementBody(), 77)
	if ack := guildReply(t, p, 837); !bytes.Equal(ack, []byte{0, 3}) {
		t.Fatalf("compatibility ACK %x", ack)
	}
	after, err := guild.Load(ctx, repos.Settings)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("compatibility request changed registry: %v", err)
	}
	afterInventory, _, err := repos.Inventory.Load(ctx, "29")
	if err != nil || !reflect.DeepEqual(beforeInventory, afterInventory) {
		t.Fatalf("compatibility request consumed assets: %v", err)
	}
}

func TestGuildPlacementCompatibilityPreservesStateAndReceiptsOnSave(t *testing.T) {
	s, p, repos := guildPlacementFixture(t)
	ctx := context.Background()
	sendMerchantRequest(t, s, p, 837, guildPlacementBody(), 77)
	if ack := guildReply(t, p, 837); !bytes.Equal(ack, []byte{1}) {
		t.Fatalf("seed ACK %x", ack)
	}
	before, err := guild.Load(ctx, repos.Settings)
	if err != nil || len(before.PlacementReceipts) != 1 || len(before.Guilds[1].Placements.Rows) != 1 {
		t.Fatalf("seed placement and receipt missing: %v", err)
	}
	s.options.guildPlacementWritesEnabled = false
	if err = guild.Save(ctx, repos.Settings, before, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	after, err := guild.Load(ctx, repos.Settings)
	if err != nil || !reflect.DeepEqual(before.PlacementReceipts, after.PlacementReceipts) ||
		!reflect.DeepEqual(before.Guilds[1].Placements, after.Guilds[1].Placements) {
		t.Fatalf("compatibility save lost placement state: %v", err)
	}
	p.guild.placement.identity = boundGameSessionCharacter{}
	p.conn.(*bufferConn).write.Reset()
	if err = s.writeCurrentGuildEssenceSnapshot(p); err != nil {
		t.Fatal(err)
	}
	if body := guildReply(t, p, 191); len(body) != 1+0x369a || body[0] != 1 {
		t.Fatalf("compatibility snapshot did not restore placements: %x", body)
	}
}

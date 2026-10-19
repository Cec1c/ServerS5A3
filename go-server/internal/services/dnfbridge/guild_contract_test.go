package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/protocol"
	r "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func guildContractSource() initialEquipmentMemSource {
	s := cargoQuerySource()
	s["etc/(r)guild.etc"] = "[guild contents list]\n[/guild contents list]\n[additional guild contents list]\n23 21 300 1 0 490001373 0 3000000 `` 1\n24 21 500 1 0 490001374 0 5000000 `` 1\n[/additional guild contents list]\n[guild cargo size limit]\n56\n[guild contracts]\n21 490001385 1\n[/guild contracts]"
	s["stackable/stackable.lst"] += " 490001385 `contract.stk`"
	s["stackable/contract.stk"] = "[name] `公会之契约礼盒`\n[stackable type] `[booster random]` 0\n[stack limit] 1\n[daily delete item] 1\n[use action packet] 1"
	return s
}

func guildContractFixture(t *testing.T) (*Service, *gameSession, r.Group) {
	t.Helper()
	s, p, repos := guildMileageFixture(t)
	cat, err := newPVFDungeonDropCatalog(guildContractSource())
	if err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog = cat
	s.gameplayTimers = newFakeCurrentDungeonDeathTimerQueue()
	ctx := context.Background()
	actor, _, _ := repos.Character.Load(ctx, "29")
	actor.Stats["gold"] = 10000000
	if err := repos.Character.Save(ctx, actor); err != nil {
		t.Fatal(err)
	}
	return s, p, repos
}

func sendGuildContract(t *testing.T, s *Service, p *gameSession, body []byte, seq uint16) {
	t.Helper()
	wire, err := protocol.BuildChannelPacket(1244, body, seq, 1)
	if err != nil {
		t.Fatal(err)
	}
	packets, rest, skipped, err := protocol.SplitLatestGameStream(wire, 65535)
	if err != nil || len(packets) != 1 || len(rest) != 0 || skipped != 0 {
		t.Fatal("claim split", err)
	}
	if err := s.handleGameStreamPacket(p, packets[0]); err != nil {
		t.Fatal(err)
	}
}

func guildContractBody() []byte {
	body := bytes.Repeat([]byte{0xa5}, 17)
	binary.LittleEndian.PutUint32(body[13:], 21)
	return body
}

func TestGuildContractNativePurchaseClaimAndReplay(t *testing.T) {
	s, p, repos := guildContractFixture(t)
	ctx := context.Background()
	sendGuildPurchase(t, s, p, 23, 30)
	if !bytes.Equal(guildReply(t, p, 761), []byte{1}) {
		t.Fatal("purchase failed")
	}
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if !reg.Guilds[1].Benefits[21].Active(s.gameplayNow()) || !bytes.Equal(guildReply(t, p, 70), buildCurrentGuildManagementBody(reg.Guilds[1], 3, s.gameplayNow(), 1)) {
		t.Fatal("contract missing from welfare")
	}
	actor, _, _ := repos.Character.Load(ctx, "29")
	if actor.Stats["gold"] != 7000000 {
		t.Fatal("gold not deducted")
	}
	// A changed wallet must be read afresh when the purchase ACK is replayed.
	actor.Stats["gold"] = 6000000
	if err := repos.Character.Save(ctx, actor); err != nil {
		t.Fatal(err)
	}
	p.conn.(*bufferConn).write.Reset()
	sendGuildPurchase(t, s, p, 23, 30)
	want := buildCurrentItemUpdateBody(r.MainInventoryListType, []currentItemListEntry{currentGoldWalletItemListEntry(6000000)})
	if !bytes.Equal(guildReply(t, p, 14), want) {
		t.Fatal("purchase replay projected historical gold")
	}
	p.conn.(*bufferConn).write.Reset()
	sendGuildContract(t, s, p, guildContractBody(), 31)
	if !bytes.Equal(guildReply(t, p, 1244), []byte{1}) {
		t.Fatal("claim failed")
	}
	guildReply(t, p, 14)
	reg, _ = guild.Load(ctx, repos.Settings)
	if len(reg.ContractClaims) != 1 {
		t.Fatal("missing durable claim")
	}
	bag, _, _ := repos.Inventory.Load(ctx, "29")
	count := 0
	for _, v := range bag.Slots {
		if v.ItemID == 490001385 {
			count += int(v.Count)
		}
	}
	if count != 1 {
		t.Fatal("real reward not granted", bag.Slots)
	}
	p.conn.(*bufferConn).write.Reset()
	sendGuildContract(t, s, p, guildContractBody(), 31)
	if !bytes.Equal(guildReply(t, p, 1244), []byte{1}) {
		t.Fatal("same request not reconciled")
	}
	p.conn.(*bufferConn).write.Reset()
	sendGuildContract(t, s, p, guildContractBody(), 32)
	if !bytes.Equal(guildReply(t, p, 1244), []byte{0, 19}) {
		t.Fatal("second daily claim accepted")
	}
	after, _, _ := repos.Inventory.Load(ctx, "29")
	if !reflect.DeepEqual(bag, after) {
		t.Fatal("daily duplicate changed bag")
	}
}

func TestGuildContractPurchaseNativeFailures(t *testing.T) {
	for _, mode := range []string{"gold", "tier", "duration", "reward"} {
		t.Run(mode, func(t *testing.T) {
			s, p, repos := guildContractFixture(t)
			ctx := context.Background()
			id := uint32(23)
			want := byte(14)
			switch mode {
			case "gold":
				actor, _, _ := repos.Character.Load(ctx, "29")
				actor.Stats["gold"] = 2999999
				if err := repos.Character.Save(ctx, actor); err != nil {
					t.Fatal(err)
				}
				want = 10
			case "tier":
				id = 24
				want = 38
			case "duration":
				reg, _ := guild.Load(ctx, repos.Settings)
				reg.Guilds[1].Benefits = map[byte]guild.Benefit{21: {Type: 21, Value: 300, ExpiresAt: s.gameplayNow().Add(30 * 24 * time.Hour)}}
				if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
					t.Fatal(err)
				}
				want = 19
			case "reward":
				source := guildContractSource()
				source["stackable/contract.stk"] = "[stackable type] `[waste]` 0\n[stack limit] 1"
				s.pvfItemCatalog, _ = newPVFDungeonDropCatalog(source)
			}
			before, _, _ := repos.Character.Load(ctx, "29")
			state, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			sendGuildPurchase(t, s, p, id, 30)
			if got := guildReply(t, p, 761); !bytes.Equal(got, []byte{0, want}) {
				t.Fatalf("ack=%x want=%d", got, want)
			}
			after, _, _ := repos.Character.Load(ctx, "29")
			next, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(state, next) {
				t.Fatal("rejected purchase changed assets")
			}
		})
	}
}

func TestGuildContractClaimRejectsBadEnvelopeIdentityAndScene(t *testing.T) {
	for _, mode := range []string{"short", "trailing", "type", "owner", "scene", "token_reuse", "no_contract", "not_member"} {
		t.Run(mode, func(t *testing.T) {
			s, p, repos := guildContractFixture(t)
			ctx := context.Background()
			if mode != "no_contract" {
				sendGuildPurchase(t, s, p, 23, 30)
			}
			body, seq := guildContractBody(), uint16(31)
			switch mode {
			case "short":
				body = body[1:]
			case "trailing":
				body = append(body, 0)
			case "type":
				binary.LittleEndian.PutUint32(body[13:], 20)
			case "owner":
				p.accountID = "foreign"
			case "scene":
				p.townSceneReadyCharacterID = 0
			case "token_reuse":
				seq = 30
			case "not_member":
				if err := guild.Save(ctx, repos.Settings, guild.NewRegistry(), s.gameplayNow()); err != nil {
					t.Fatal(err)
				}
			}
			before, _, _ := repos.Inventory.Load(ctx, "29")
			state, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			p.conn.(*bufferConn).write.Reset()
			sendGuildContract(t, s, p, body, seq)
			if got := guildReply(t, p, 1244); len(got) != 2 || got[0] != 0 {
				t.Fatal("invalid claim accepted", got)
			}
			after, _, _ := repos.Inventory.Load(ctx, "29")
			next, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(state, next) {
				t.Fatal("invalid claim changed assets")
			}
		})
	}
}

func TestGuildContractRealPVF(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("matched PVF not configured")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s, p, repos := guildContractFixture(t)
	s.initialEquipmentArchive = archive
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog = items
	cat, err := guild.LoadContentCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	reward, err := resolveCurrentGuildContractReward(items, cat)
	if err != nil || reward.ItemID != 490001385 || reward.StackLimit != 1 || reward.Kind != "stackable" {
		t.Fatal(reward, err)
	}
	sendGuildPurchase(t, s, p, 23, 30)
	if !bytes.Equal(guildReply(t, p, 761), []byte{1}) {
		t.Fatal("real PVF purchase")
	}
	p.conn.(*bufferConn).write.Reset()
	sendGuildContract(t, s, p, guildContractBody(), 31)
	if !bytes.Equal(guildReply(t, p, 1244), []byte{1}) {
		t.Fatal("real PVF claim")
	}
	reg, err := guild.Load(context.Background(), repos.Settings)
	if err != nil || len(reg.ContractClaims) != 1 {
		t.Fatal("real PVF receipt", err)
	}
}

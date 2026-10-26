package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/cerashop"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/pvf"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func hongbaoBridgeFixture(t *testing.T) (*Service, *gameSession, r.Group) {
	t.Helper()
	s, p, repos := guildMileageFixture(t)
	s.options.guildHongbaoEnabled = true
	ctx := context.Background()
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	base, _, _ := repos.Character.Load(ctx, "29")
	for id := 100; len(reg.Guilds[1].Members) < 30; id++ {
		actor := r.CloneCharacter(base)
		actor.CharacterID, actor.AccountID, actor.Name = fmt.Sprint(id), fmt.Sprintf("hongbao:%d", id), fmt.Sprintf("成员%d", id)
		if err := repos.Character.Save(ctx, actor); err != nil {
			t.Fatal(err)
		}
		if _, err := reg.JoinDirect(1, actor, s.gameplayNow()); err != nil {
			t.Fatal(err)
		}
	}
	for _, member := range reg.Guilds[1].Members {
		seedCeraShopAccountCera(t, ctx, repos, member.AccountID, 200000)
	}
	if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	contributionBridgeCatalog(t, s)
	src := s.pvfItemCatalog.source.(dungeonDropCatalogTestSource)
	src[guild.ContributionCatalogPath] = "[hongbao]\n490003263 103938 50000 100 490003264 103939 20000 95 490003265 103940 10000 90 490003266 103941 5000 80\n[/hongbao]\n[min guild member count]\n30\n[max total cera per day]\n50000\n[max take count per day]\n5\n"
	shop := "[item]\n"
	for i, price := range []int{50000, 20000, 10000, 5000} {
		id, commodity := 490003263+i, 103938+i
		path := fmt.Sprintf("stackable/490003001/chn_%d.stk", id)
		src["stackable/stackable.lst"] += fmt.Sprintf("\n%d `490003001/chn_%d.stk`", id, id)
		src[path] = "[name]\n`公会红包`\n[grade]\n50\n[rarity]\n2\n[attach type]\n`[trade]`\n[minimum level]\n1\n[weight]\n1\n[stack limit]\n1\n[stackable type]\n`[town and dungeon]` 0\n[action type]\n`[guild hongbao]` 0\n[/action type]\n[usable period]\n10\n"
		if root := os.Getenv("GUILD_HONGBAO_EXTRACTED_ROOT"); root != "" {
			b, err := os.ReadFile(filepath.Join(root, path))
			if err != nil {
				t.Fatal(err)
			}
			src[path] = string(b)
		}
		shop += fmt.Sprintf("%d %d 1 0 0 %d `红包` 0 0\n", commodity, id, price)
	}
	if root := os.Getenv("GUILD_HONGBAO_EXTRACTED_ROOT"); root != "" {
		b, err := os.ReadFile(filepath.Join(root, guild.ContributionCatalogPath))
		if err != nil {
			t.Fatal(err)
		}
		src[guild.ContributionCatalogPath] = string(b)
	}
	src["etc/cerashop.etc"] = shop + "[/item]\n"
	for _, section := range []string{"premium", "creature", "coin", "material", "recoveryitem", "package", "visual", "avatar"} {
		src["etc/cerashop.etc"] += fmt.Sprintf("[%s]\n[/%s]\n", section, section)
	}
	var source pvf.Source = src
	if s.initialEquipmentArchive != nil {
		source = s.initialEquipmentArchive
	}
	s.pvfItemCatalog, err = newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	s.ceraShopCatalog, err = newPVFCeraShopCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	return s, p, repos
}

func TestGuildHongbaoProductionSettlementAndMailClaim(t *testing.T) {
	s, p, repos := hongbaoBridgeFixture(t)
	ctx := context.Background()
	now := s.gameplayNow()
	s.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return now }}
	issue := func(seq uint16) uint32 {
		t.Helper()
		ack := hongbaoBridgeSend(t, s, p, 64, currentCeraShopTestRequestBody(103941), seq)
		if ack[0] != 1 {
			t.Fatalf("purchase %x", ack)
		}
		ack = hongbaoBridgeSend(t, s, p, 1303, append(hongbaoBridgeID(490003266), 5), seq+1)
		if len(ack) != 13 || ack[0] != 1 {
			t.Fatalf("issue %x", ack)
		}
		return binary.LittleEndian.Uint32(ack[9:])
	}
	first, second := issue(100), issue(102)
	if first == second {
		t.Fatal("new sequence collapsed two items")
	}
	var total uint32
	for _, id := range []uint16{29, 30, 33, 100, 101} {
		peer := s.gameSessions[id]
		if peer == nil {
			actor, found, err := repos.Character.Load(ctx, strconv.Itoa(int(id)))
			if err != nil || !found {
				t.Fatal(err)
			}
			peer = &gameSession{conn: &bufferConn{}, accountID: actor.AccountID, selectedCharacterID: id, characterGeneration: uint64(id), channel: p.channel, residentChannel: p.channel}
			s.gameSessions[id] = peer
		}
		ack := hongbaoBridgeSend(t, s, peer, 1304, hongbaoBridgeID(first), 110)
		if len(ack) != 13 || ack[0] != 1 {
			t.Fatalf("claim %d: %x", id, ack)
		}
		total += binary.LittleEndian.Uint32(ack[9:])
	}
	if total != 5000 {
		t.Fatal("shares do not conserve value", total)
	}
	if err := s.settleCurrentGuildHongbaos(ctx); err != nil {
		t.Fatal(err)
	}
	now = now.Add(24 * time.Hour)
	if err := s.settleCurrentGuildHongbaos(ctx); err != nil {
		t.Fatal(err)
	}
	box, found, err := repos.Mailbox.Load(ctx, "29")
	if err != nil || !found || len(box.Mails) != 2 {
		t.Fatal("mailbox", box, err)
	}
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Hongbao.Records[first].Settlement.Coins != 40 || reg.Hongbao.Records[second].Settlement.RefundTokenCera != 5000 {
		t.Fatal("settlement amount")
	}
	before := r.CloneMailbox(box)
	if err := s.settleCurrentGuildHongbaos(ctx); err != nil {
		t.Fatal(err)
	}
	box, _, _ = repos.Mailbox.Load(ctx, "29")
	if !reflect.DeepEqual(before, box) {
		t.Fatal("repeated clock wrote mail")
	}
	list := hongbaoBridgeSend(t, s, p, 1300, nil, 120)
	if len(list) != 55 || binary.LittleEndian.Uint32(list[15:]) != second || list[54] != 1 {
		t.Fatalf("expired record remained claimable: %x", list)
	}
	var ids []uint32
	for _, mail := range box.Mails {
		if len(mail.Attachments) != 1 {
			t.Fatal(mail)
		}
		a := mail.Attachments[0]
		switch a.ItemID {
		case guild.ContributionCoinID:
			if a.Count != 40 || !mail.ExpireAt.Equal(mail.CreatedAt.Add(15*24*time.Hour)) {
				t.Fatal(mail)
			}
		case cerashop.TokenCeraVoucherItemID:
			if a.Count != 5000 || !mail.ExpireAt.IsZero() {
				t.Fatal(mail)
			}
		default:
			t.Fatal("unknown mail attachment", a)
		}
		id, err := strconv.ParseUint(mail.MailID, 10, 32)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, uint32(id))
	}
	// Both attachments go through the existing mailbox owner, including its
	// special TokenCera redemption rather than an inventory substitute.
	for _, id := range ids {
		for range 2 {
			p.conn.(*bufferConn).write.Reset()
			handled, err := s.handleAlignedGameCommand(p, 1, uint16(dnfenum.CmdPacketMailboxExtractItem), mailIDListBody(id))
			if err != nil || !handled {
				t.Fatal("mail claim", err)
			}
		}
	}
	account, _, _ := repos.Account.Load(ctx, p.accountID)
	if cerashop.TokenCeraBalance(account) != 5000 {
		t.Fatal("refund wallet", account.Metadata)
	}
	inv, _, _ := repos.Inventory.Load(ctx, "29")
	var coins int64
	for _, item := range inv.Slots {
		if item.ItemID == guild.ContributionCoinID {
			coins += item.Count
		}
	}
	if coins != 40 {
		t.Fatal("contribution coin mail claim", coins)
	}
}

func hongbaoBridgeSend(t *testing.T, s *Service, p *gameSession, opcode uint16, body []byte, sequence uint16) []byte {
	t.Helper()
	p.conn.(*bufferConn).write.Reset()
	wire, err := protocol.BuildChannelPacket(opcode, body, sequence, 1)
	if err != nil {
		t.Fatal(err)
	}
	packets, rest, skipped, err := protocol.SplitLatestGameStream(wire, 65535)
	if err != nil || len(packets) != 1 || len(rest) != 0 || skipped != 0 {
		t.Fatalf("split: %v", err)
	}
	if err := s.handleGameStreamPacket(p, packets[0]); err != nil {
		t.Fatal(err)
	}
	return guildReply(t, p, opcode)
}

func hongbaoBridgeID(id uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, id)
	return b
}

func exportHongbaoProduction(t *testing.T, name string, body []byte) {
	t.Helper()
	if dir := os.Getenv("DNF_GUILD_HONGBAO_WIRE_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".bin"), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGuildHongbaoProductionFourProducts(t *testing.T) {
	for i, price := range []int64{50000, 20000, 10000, 5000} {
		t.Run(fmt.Sprint(price), func(t *testing.T) {
			s, p, repos := hongbaoBridgeFixture(t)
			ctx := context.Background()
			commodity := uint32(103938 + i)
			ack := hongbaoBridgeSend(t, s, p, 64, currentCeraShopTestRequestBody(commodity), 40)
			if !bytes.Equal(ack, buildCurrentCeraShopPurchaseSuccessBodyWithCount(commodity, 1)) {
				t.Fatalf("purchase %x", ack)
			}
			if got := loadCeraShopAccountCera(t, ctx, repos, p.accountID); got != 200000-price {
				t.Fatal(got)
			}
			inv, _, _ := repos.Inventory.Load(ctx, "29")
			var item r.ItemStack
			var slot string
			for k, v := range inv.Slots {
				if v.ItemID == int64(490003263+i) {
					item, slot = v, k
				}
			}
			if item.Count != 1 || item.ExpireAt.Before(s.gameplayNow().Add(239*time.Hour)) || item.ExpireAt.After(s.gameplayNow().Add(241*time.Hour)) {
				t.Fatal(item)
			}
			before := r.CloneInventory(inv)
			hongbaoBridgeSend(t, s, p, 64, currentCeraShopTestRequestBody(commodity), 40)
			inv, _, _ = repos.Inventory.Load(ctx, "29")
			if !reflect.DeepEqual(before, inv) {
				t.Fatal("purchase replay mutated inventory")
			}
			issue := append(hongbaoBridgeID(uint32(item.ItemID)), 5)
			ack = hongbaoBridgeSend(t, s, p, 1303, issue, 41)
			if len(ack) != 13 || ack[0] != 1 || binary.LittleEndian.Uint32(ack[5:]) != uint32(item.ItemID) {
				t.Fatalf("issue %x", ack)
			}
			id := binary.LittleEndian.Uint32(ack[9:])
			if id == 0 {
				t.Fatal("missing record")
			}
			exportHongbaoProduction(t, fmt.Sprintf("handler-%d-issue", price), ack)
			inv, _, _ = repos.Inventory.Load(ctx, "29")
			if inv.Slots[slot].Count != 0 {
				t.Fatal("item not consumed")
			}
			if replay := hongbaoBridgeSend(t, s, p, 1303, issue, 41); !bytes.Equal(replay, ack) {
				t.Fatal("issue replay")
			}
			list := hongbaoBridgeSend(t, s, p, 1300, nil, 42)
			if len(list) != 55 || binary.LittleEndian.Uint32(list[11:]) != 1 || list[53] != 5 {
				t.Fatalf("list %x", list)
			}
			exportHongbaoProduction(t, fmt.Sprintf("handler-%d-list", price), list)
			claim := hongbaoBridgeSend(t, s, p, 1304, hongbaoBridgeID(id), 43)
			if len(claim) != 13 || claim[0] != 1 {
				t.Fatalf("claim %x", claim)
			}
			amount := binary.LittleEndian.Uint32(claim[9:])
			if amount == 0 {
				t.Fatal("empty claim")
			}
			account, _, _ := repos.Account.Load(ctx, p.accountID)
			if cerashop.HappyTokenCeraBalance(account) != int64(amount) {
				t.Fatal("wallet", account.Metadata)
			}
			if replay := hongbaoBridgeSend(t, s, p, 1304, hongbaoBridgeID(id), 43); !bytes.Equal(replay, claim) {
				t.Fatal("claim replay")
			}
			account, _, _ = repos.Account.Load(ctx, p.accountID)
			if cerashop.HappyTokenCeraBalance(account) != int64(amount) {
				t.Fatal("double credit")
			}
			exportHongbaoProduction(t, fmt.Sprintf("handler-%d-claim", price), claim)
			history := hongbaoBridgeSend(t, s, p, 1302, hongbaoBridgeID(id), 44)
			if len(history) != 41 || history[0] != 1 || binary.LittleEndian.Uint32(history[1:]) != 1 || binary.LittleEndian.Uint32(history[36:]) != amount {
				t.Fatalf("history %x", history)
			}
			exportHongbaoProduction(t, fmt.Sprintf("handler-%d-history", price), history)
			dup := hongbaoBridgeSend(t, s, p, 1304, hongbaoBridgeID(id), 45)
			if !bytes.Equal(dup, []byte{0, 34}) {
				t.Fatal("account duplicate", dup)
			}
		})
	}
}

func TestGuildHongbaoProductionPurchaseFailuresPreserveAssets(t *testing.T) {
	for _, kind := range []string{"disabled", "mixed_cart", "wrong_payment", "catalog_mismatch", "insufficient", "inventory_full"} {
		t.Run(kind, func(t *testing.T) {
			s, p, repos := hongbaoBridgeFixture(t)
			ctx := context.Background()
			body := currentCeraShopTestRequestBody(103941)
			switch kind {
			case "disabled":
				s.options.guildHongbaoEnabled = false
			case "mixed_cart":
				body[2] = 2
				body = append(body, body[4:]...)
			case "wrong_payment":
				body[4] = 1
			case "catalog_mismatch":
				v := s.ceraShopCatalog.products[103941]
				v.CeraPrice = 1
				s.ceraShopCatalog.products[103941] = v
			case "insufficient":
				seedCeraShopAccountCera(t, ctx, repos, p.accountID, 4999)
			case "inventory_full":
				inv, _, _ := repos.Inventory.Load(ctx, "29")
				for slot := 0; slot < 500; slot++ {
					inv.Slots[fmt.Sprintf("0:%d", slot)] = r.ItemStack{ItemID: 37, Count: 1}
				}
				if err := repos.Inventory.Save(ctx, inv); err != nil {
					t.Fatal(err)
				}
			}
			a, _, _ := repos.Account.Load(ctx, p.accountID)
			inv, _, _ := repos.Inventory.Load(ctx, "29")
			reg, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			ack := hongbaoBridgeSend(t, s, p, 64, body, 70)
			if len(ack) == 0 || ack[0] != 0 {
				t.Fatalf("unexpected success %x", ack)
			}
			b, _, _ := repos.Account.Load(ctx, p.accountID)
			after, _, _ := repos.Inventory.Load(ctx, "29")
			regAfter, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(inv, after) || !reflect.DeepEqual(reg, regAfter) {
				t.Fatal("failed purchase mutated assets")
			}
		})
	}
}

func TestGuildHongbaoProductionFailureFrames(t *testing.T) {
	s, p, repos := hongbaoBridgeFixture(t)
	ctx := context.Background()
	before, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
	for _, tc := range []struct {
		op         uint16
		body, want []byte
	}{
		{1300, []byte{1}, []byte{0, 34, 34, 0, 0, 0}},
		{1302, []byte{1}, []byte{0, 34, 34, 0, 0, 0}},
		{1303, append(hongbaoBridgeID(490003266), 4), []byte{0, 34}},
		{1303, append(hongbaoBridgeID(490003266), 5), []byte{0, 34}},
		{1304, hongbaoBridgeID(999), []byte{0, 34}},
	} {
		t.Run(fmt.Sprint(tc.op), func(t *testing.T) {
			got := hongbaoBridgeSend(t, s, p, tc.op, tc.body, 200)
			if !bytes.Equal(got, tc.want) {
				t.Fatalf("reply %x want %x", got, tc.want)
			}
		})
	}
	after, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed request changed registry")
	}
}

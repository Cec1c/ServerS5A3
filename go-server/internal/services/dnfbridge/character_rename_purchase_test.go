package dnfbridge

import (
	"context"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/characterdata"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func renameShopFixture(t *testing.T) (*Service, *gameSession, dnfrepo.Group, *bufferConn) {
	t.Helper()
	source := bridgePVFSource{
		"etc/cerashop.etc":    "[item]\n12345 2682290 1 0 0 100 `rename card` 0 0\n[/item]\n[premium]\n[/premium]\n[creature]\n[/creature]\n[coin]\n[/coin]\n[material]\n[/material]\n[recoveryitem]\n[/recoveryitem]\n",
		"monster/monster.lst": "", "equipment/equipment.lst": "",
		"stackable/stackable.lst":                 "2682290 `cash/chn_character_rename.stk`\n",
		"stackable/cash/chn_character_rename.stk": "[stackable type]\n`[etc]` 0\n[minimum level]\n30\n[usable job]\n`[all]`\n",
	}
	catalog, err := newPVFCeraShopCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	seedCeraShopAccountCera(t, ctx, g, "owner", 200)
	if err := g.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "owner", Slot: 3, Name: "旧名字", Level: 90, Job: "0", Stats: map[string]int64{"gold": 9876}}); err != nil {
		t.Fatal(err)
	}
	if err := g.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	conn := &bufferConn{}
	a := &gameSession{conn: conn, connID: "rename-shop", accountID: "owner", accountFromToken: true, loginPacketSeen: true, selectedCharacterID: 19, townSceneReadyCharacterID: 19, channel: channelcatalog.Channel{ID: 1, Type: 1}}
	s := &Service{options: options{currentClientVersion: "official-24.9", gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain}, ceraShopCatalog: catalog, repositoryProvider: func() (dnfrepo.Group, bool) { return g, true }, gameSessions: map[uint16]*gameSession{19: a}}
	return s, a, g, conn
}

func TestCurrentRenameShopPurchaseArmsCreditAndReturnsToSelector(t *testing.T) {
	s, a, g, conn := renameShopFixture(t)
	if err := s.handleCurrentCeraShopPurchase(a, currentCeraShopTestRequestBody(12345)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	c, _, _ := g.Character.Load(ctx, "19")
	bag, _, _ := g.Inventory.Load(ctx, "19")
	if c.Name != "旧名字" || c.Stats[characterdata.RenameEntitlementStat] != 1 || c.Stats[characterdata.RenameMinimumLevelStat] != 30 || c.Stats[characterdata.RenameCooldownSecondsStat] != int64(characterdata.RenameCooldown/time.Second) || c.Stats[characterdata.RenameReservationSecondsStat] != int64(characterdata.PreviousNameReservation/time.Second) || c.Stats["gold"] != 9876 || len(bag.Slots) != 0 {
		t.Fatal(c, bag)
	}
	if balance := loadCeraShopAccountCera(t, ctx, g, "owner"); balance != 100 {
		t.Fatal(balance)
	}
	if a.selectedCharacterID != 0 || len(s.characterAssetReservations) != 0 || s.gameSessions[19] != nil {
		t.Fatal("selector lifecycle or reservation leaked")
	}
	stream := conn.write.Bytes()
	var ops []uint16
	for len(stream) > 0 {
		packet, rest := splitGameServerUpperPacket(t, stream)
		ops = append(ops, packet.Header.MsgID)
		if packet.Header.MsgID == 509 || packet.Header.MsgID == 7 {
			if len(packet.Body) != 1 || packet.Body[0] != 1 {
				t.Fatal(packet)
			}
		}
		stream = rest
	}
	if len(ops) != 4 || ops[0] != 64 || ops[1] != 53 || ops[2] != 509 || ops[3] != 7 {
		t.Fatal("purchase/credit/selector order", ops)
	}
}

func TestCurrentRenameShopPurchaseRejectsWithoutCharging(t *testing.T) {
	for _, kind := range []string{"under_level", "pending", "cooldown", "dungeon", "scene_unready", "raid", "no_canonical_owner"} {
		t.Run(kind, func(t *testing.T) {
			s, a, g, _ := renameShopFixture(t)
			ctx := context.Background()
			c, _, _ := g.Character.Load(ctx, "19")
			switch kind {
			case "under_level":
				c.Level = 29
			case "pending":
				c.Stats[characterdata.RenameEntitlementStat] = 1
			case "cooldown":
				c.Stats[characterdata.RenameNextAllowedUnixStat] = time.Now().Add(time.Hour).Unix()
			case "dungeon":
				c.Location.DungeonID = 123
			case "scene_unready":
				a.townSceneReadyCharacterID = 0
			case "raid":
				s.raids = map[uint32]*runtimeRaidState{1: {RaidKey: 1, Members: []runtimeRaidMemberState{{CharID: 19}}}}
			case "no_canonical_owner":
				delete(s.gameSessions, 19)
			}
			if err := g.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			if err := s.handleCurrentCeraShopPurchase(a, currentCeraShopTestRequestBody(12345)); err != nil {
				t.Fatal(err)
			}
			if balance := loadCeraShopAccountCera(t, ctx, g, "owner"); balance != 200 {
				t.Fatal("charged rejected card", balance)
			}
			if a.selectedCharacterID != 19 || len(s.characterAssetReservations) != 0 {
				t.Fatal("rejection changed binding")
			}
		})
	}
}

func TestCurrentRenameBootstrapRemembersOnlySuccessfulRoster(t *testing.T) {
	s, a, g, _ := renameShopFixture(t)
	s.resetGameSessionForCharacterSelect(a)
	c, _, _ := g.Character.Load(context.Background(), "19")
	c.Stats[characterdata.RenameEntitlementStat] = 1
	c.Stats[characterdata.RenameMinimumLevelStat] = 30
	_ = g.Character.Save(context.Background(), c)
	if err := s.sendUpperGetUserInfoRosterBootstrapEnvelope(a, "upper"); err != nil {
		t.Fatal(err)
	}
	if len(a.currentRenameRoster) != 1 || a.currentRenameRoster[0].CharacterID != "19" || a.currentRenameRoster[0].Slot != 3 || a.currentRenameRoster[0].Name != "旧名字" {
		t.Fatal(a.currentRenameRoster)
	}
	if err := s.handleCurrentCharacterRename(a, bridge135Body(t, 0, "旧名字", "新名字", 1)); err != nil {
		t.Fatal(err)
	}
	updated, _, _ := g.Character.Load(context.Background(), "19")
	if updated.Name != "新名字" {
		t.Fatal("op8 roster did not authorize exact row", updated)
	}
}

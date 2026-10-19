package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func guildMileageFixture(t *testing.T) (*Service, *gameSession, dnfrepo.Group) {
	t.Helper()
	s, peers, seed := guildHallFixture(t)
	// Hall's movement fixture substitutes a character store; paid mutations
	// must use the real memory transaction with its matching stores.
	repos := memory.NewMemoryGroup()
	for id := 29; id <= 33; id++ {
		c, _, err := seed.Character.Load(context.Background(), strconv.Itoa(id))
		if err != nil {
			t.Fatal(err)
		}
		if err := repos.Character.Save(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	settings, _, err := seed.Settings.Load(context.Background(), dnfguild.RegistryScope)
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.Settings.Save(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	logPath := filepath.Join(t.TempDir(), "events.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	s.packetLog = logger
	t.Cleanup(func() {
		logger.Close()
		if t.Failed() {
			data, _ := os.ReadFile(logPath)
			t.Log(string(data))
		}
	})
	catalog, err := newPVFDungeonDropCatalog(dungeonDropCatalogTestSource{
		"monster/monster.lst": "", "equipment/equipment.lst": "",
		"stackable/stackable.lst":         "10153530 `10153001/10153530.stk`",
		"stackable/10153001/10153530.stk": "[name] `公会积分礼袋`\n[stackable type] `[waste]` 0\n[minimum level] 1\n[action type] `[add guildmileage]` 100\n[/action type]\n[action usable condition] `[guildmember]`\n[/action usable condition]\n[action usable place] `[village]`\n[/action usable place]\n[use action packet] 1\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog = catalog
	if def, err := catalog.ResolveItem(10153530); err != nil || def.GuildMileageItem == nil {
		t.Fatalf("definition=%+v err=%v", def, err)
	}
	if path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"); path != "" {
		archive, err := platformpvf.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		s.initialEquipmentArchive = archive
	}
	if err := repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: "29", Slots: map[string]dnfrepo.ItemStack{
		"0:87": {ItemID: 10153530, Count: 1, Extra: map[string]string{"item_kind": "stackable", "pvf_path": "stackable/10153001/10153530.stk", "stackable_type": "[waste]"}},
	}}); err != nil {
		t.Fatal(err)
	}
	return s, peers[29], repos
}

func TestGuildMileageItemAction145CreditsGuild(t *testing.T) {
	s, session, repos := guildMileageFixture(t)
	handled, err := s.handleAlignedGameCommand(session, 1, 515, testQuestClearCouponRequestBody(87, 145))
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	r, err := dnfguild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	g, m, _ := r.GuildForCharacter("29")
	inv, _, _ := repos.Inventory.Load(context.Background(), "29")
	if g.Mileage != 100 || inv.Slots["0:87"].Count != 0 {
		t.Fatalf("guild mileage=%d bag=%+v", g.Mileage, inv.Slots["0:87"])
	}
	if m.Mileage != 0 || m.ContributionPoints != 0 || g.Experience != 0 {
		t.Fatalf("unrelated points changed: guild=%+v member=%+v", g, m)
	}
	packet, rest := splitCurrentGameServerUpperPacketAuto(t, session.conn.(*bufferConn).write.Bytes())
	// The native class1 handler reads slot u16, list u8, action u32 after
	// the dispatcher consumes result. There is no remaining-count field.
	if packet.Header.MsgID != 515 || packet.Header.Classification != 1 || !bytes.Equal(packet.Body, []byte{1, 87, 0, 0, 145, 0, 0, 0}) {
		t.Fatalf("ack=%+v %x", packet.Header, packet.Body)
	}
	var ops []uint16
	var management []byte
	for len(rest) > 0 {
		p, next := splitCurrentGameServerUpperPacketAuto(t, rest)
		rest = next
		ops = append(ops, p.Header.MsgID)
		if p.Header.MsgID == 70 {
			management = p.Body
		}
	}
	if len(ops) < 2 || ops[0] != 14 || !bytes.Equal(management, buildCurrentGuildManagementBody(g, len(s.currentGuildOnlineChannels(g, session)), s.gameplayNow(), m.Grade)) {
		t.Fatalf("missing item/mileage refresh: ops=%v", ops)
	}
}

func TestGuildMileageItemBridgeRejectsInvalidUse(t *testing.T) {
	for _, kind := range []string{"not_member", "wrong_account", "stale_session", "not_ready", "seria", "dungeon", "reserved0", "reserved1", "reserved2", "wrong_list", "negative_slot", "extra_bytes", "truncated_after_action", "missing_slot"} {
		t.Run(kind, func(t *testing.T) {
			s, p, repos := guildMileageFixture(t)
			body := testQuestClearCouponRequestBody(87, 145)
			switch kind {
			case "not_member":
				reg, _ := dnfguild.Load(context.Background(), repos.Settings)
				g, _, _ := reg.GuildForCharacter("29")
				delete(reg.Guilds, g.ID)
				if err := dnfguild.Save(context.Background(), repos.Settings, reg, s.gameplayNow()); err != nil {
					t.Fatal(err)
				}
			case "wrong_account":
				p.accountID = "other"
			case "stale_session":
				s.gameSessions[29] = &gameSession{selectedCharacterID: 29, characterGeneration: 99}
			case "not_ready":
				p.townSceneReadyCharacterID = 0
			case "seria":
				p.townPositionSnapshot.TownID = 38
				p.townPositionSnapshot.AreaID = 1
			case "dungeon":
				p.dungeon.runtime = &runtimeDungeonState{}
			case "reserved0":
				body[3] = 1
			case "reserved1":
				body[11] = 1
			case "reserved2":
				body[15] = 1
			case "wrong_list":
				body[2] = 2
			case "negative_slot":
				binary.LittleEndian.PutUint16(body, 65535)
			case "extra_bytes":
				body = append(body, 0)
			case "truncated_after_action":
				body = body[:11]
			case "missing_slot":
				binary.LittleEndian.PutUint16(body, 86)
			}
			before, _, _ := repos.Inventory.Load(context.Background(), "29")
			regBefore, _, _ := repos.Settings.Load(context.Background(), dnfguild.RegistryScope)
			handled, err := s.handleAlignedGameCommand(p, 1, 515, body)
			if err != nil || !handled {
				t.Fatalf("handled=%v err=%v", handled, err)
			}
			inv, _, _ := repos.Inventory.Load(context.Background(), "29")
			regAfter, _, _ := repos.Settings.Load(context.Background(), dnfguild.RegistryScope)
			if !reflect.DeepEqual(before, inv) || !reflect.DeepEqual(regBefore, regAfter) {
				t.Fatal("invalid use mutated assets")
			}
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
			// Failure dispatcher consumes result/error, then the callback still
			// reads source slot, list and the one-byte action before branching.
			want := []byte{0, 17, body[0], body[1], body[2], 145}
			if packet.Header.MsgID != 515 || packet.Header.Classification != 1 || !bytes.Equal(packet.Body, want) || len(rest) != 0 {
				t.Fatalf("failure response=%x trailing=%x", packet.Body, rest)
			}
		})
	}
}

func TestGuildMileageItemPVFRejectsIncompleteContract(t *testing.T) {
	valid := "[name] `礼袋`\n[stackable type] `[waste]` 0\n[action type] `[add guildmileage]` 100\n[/action type]\n[action usable condition] `[guildmember]`\n[/action usable condition]\n[action usable place] `[village]`\n[/action usable place]\n[use action packet] 1\n"
	for _, pair := range [][2]string{{"[waste]", "[material]"}, {"100\n", "0\n"}, {"100\n", "4294967296\n"}, {"[guildmember]", "[all]"}, {"[village]", "[dungeon]"}, {"packet] 1", "packet] 0"}, {"100\n", "100 200\n"}} {
		catalog, err := newPVFDungeonDropCatalog(dungeonDropCatalogTestSource{"monster/monster.lst": "", "equipment/equipment.lst": "", "stackable/stackable.lst": "10153530 `bag.stk`", "stackable/bag.stk": strings.Replace(valid, pair[0], pair[1], 1)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := catalog.ResolveItem(10153530); err == nil {
			t.Fatalf("invalid contract accepted %v", pair)
		}
	}
}

func TestGuildMileageItemRealPVF(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires matched PVF")
	}
	s, p, repos := guildMileageFixture(t)
	catalog, err := newPVFDungeonDropCatalog(s.initialEquipmentArchive)
	if err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog = catalog
	def, err := catalog.ResolveItem(10153530)
	if err != nil || def.GuildMileageItem == nil || def.GuildMileageItem.Amount != 100 || def.GuildMileageItem.MinimumLevel != 1 || def.PVFPath != "stackable/10153001/10153530.stk" {
		t.Fatalf("real PVF definition=%+v err=%v", def, err)
	}
	handled, err := s.handleAlignedGameCommand(p, 1, 515, testQuestClearCouponRequestBody(87, 145))
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	registry, err := dnfguild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	g, _, _ := registry.GuildForCharacter("29")
	if g.Mileage != 100 {
		t.Fatalf("real PVF mileage=%d", g.Mileage)
	}
}

func TestGuildMileageItemDoesNotWaitOnTransitionWhileReservingCharacter(t *testing.T) {
	for _, kind := range []string{"town", "dungeon"} {
		t.Run(kind, func(t *testing.T) {
			s, p, repos := guildMileageFixture(t)
			unlock := p.townMu.Unlock
			if kind == "town" {
				p.townMu.Lock()
			} else {
				p.dungeon.mu.Lock()
				unlock = p.dungeon.mu.Unlock
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				s.handleCurrentGuildMileageItem(p, testQuestClearCouponRequestBody(87, 145))
			}()
			select {
			case <-done:
				unlock()
			case <-time.After(250 * time.Millisecond):
				unlock()
				<-done
				t.Fatal("item use waited on a transition lock while holding character reservation")
			}
			inv, _, _ := repos.Inventory.Load(context.Background(), "29")
			if inv.Slots["0:87"].Count != 1 {
				t.Fatal("busy scene consumed bag")
			}
		})
	}
}

func TestGuildMileageItemHistoricalActionDoesNotDebit(t *testing.T) {
	for _, action := range []uint32{1, 143, 144, 146} {
		t.Run(strconv.FormatUint(uint64(action), 10), func(t *testing.T) {
			s, session, repos := guildMileageFixture(t)
			before, _, _ := repos.Inventory.Load(context.Background(), "29")
			registry, _, _ := repos.Settings.Load(context.Background(), dnfguild.RegistryScope)
			if handled, err := s.handleCurrentGuildMileageItem(session, testQuestClearCouponRequestBody(87, action)); handled || err != nil {
				t.Fatalf("unrelated action handled=%v err=%v", handled, err)
			}
			s.handleAlignedGameCommand(session, 1, 515, testQuestClearCouponRequestBody(87, action))
			after, _, _ := repos.Inventory.Load(context.Background(), "29")
			afterRegistry, _, _ := repos.Settings.Load(context.Background(), dnfguild.RegistryScope)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(registry, afterRegistry) {
				t.Fatal("unsupported action changed guild mileage or inventory")
			}
		})
	}
}

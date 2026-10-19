package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"reflect"
	"strconv"
	"testing"

	guild "longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func cargoQuerySource() initialEquipmentMemSource {
	return initialEquipmentMemSource{
		"monster/monster.lst": "", "stackable/stackable.lst": "100 `cargo.stk` 101 `feed.stk`", "equipment/equipment.lst": "102 `artifact.equ`",
		"stackable/cargo.stk":    "[stackable type]\n`[waste]` 0\n[stack limit]\n1000",
		"stackable/feed.stk":     "[stackable type]\n`[feed]` 0\n[stack limit]\n1000",
		"equipment/artifact.equ": "[equipment type]\n`[artifact red]` 0",
		"etc/(r)guild.etc":       "[guild cargo size limit]\n56",
		"npc/npc.lst":            "99 `GuildStorage.npc`", "npc/GuildStorage.npc": "[role]\n`[guild storage]` 0\n[/role]",
		"map/Guildagit/GuildAgit_W.map": "[NPC]\n99 `[left]` 1150 165 0\n[/NPC]",
	}
}

func cargoQueryFixture(t *testing.T) (*Service, *gameSession, r.Group, initialEquipmentMemSource) {
	t.Helper()
	s, session, repos := guildMileageFixture(t)
	source := cargoQuerySource()
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog = catalog
	if limit, err := currentGuildCargoSceneLimit(source, "Guildagit/GuildAgit_W.map"); err != nil || limit != 56 {
		t.Fatalf("fixture cargo scene: %d %v", limit, err)
	}
	ctx := context.Background()
	reg, _ := guild.Load(ctx, repos.Settings)
	for _, g := range reg.Guilds {
		g.Cargo = &guild.Cargo{Capacity: 3}
	}
	g, _, _ := reg.GuildForCharacter("29")
	g.GradePermissions = map[byte]uint32{1: guild.CargoPermission, 4: guild.CargoPermission}
	g.Cargo.Slots = map[uint16]r.ItemStack{2: {ItemID: 100, Count: 7, Extra: map[string]string{"item_kind": "stackable", "count": "99"}}, 0: {ItemID: 101, Count: 4, Extra: map[string]string{"item_kind": "stackable", "serial": "999"}}}
	e := cargoHistoryFixture()
	e.Item.ItemID = 100
	if err = g.Cargo.AppendHistory(e); err != nil {
		t.Fatal(err)
	}
	if err = guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	return s, session, repos, source
}

func TestGuildCargoQueriesRouteAndIsolateGuilds(t *testing.T) {
	s, p, repos, _ := cargoQueryFixture(t)
	before, _, _ := repos.Settings.Load(context.Background(), r.GuildRegistryScope)
	for _, op := range []uint16{261, 262} {
		guildDispatch(t, s, p, op, nil)
		body := guildReply(t, p, op)
		if body[0] != 1 {
			t.Fatalf("op%d body %x", op, body)
		}
		if op == 261 {
			if len(body) != 7+2*119 || binary.LittleEndian.Uint32(body[1:]) != 3 || binary.LittleEndian.Uint16(body[5:]) != 2 {
				t.Fatalf("snapshot %x", body)
			}
			first, second := body[7:126], body[126:]
			if binary.LittleEndian.Uint16(first) != 0 || binary.LittleEndian.Uint32(first[2:]) != 101 || binary.LittleEndian.Uint32(first[6:]) != 4 || binary.LittleEndian.Uint16(second) != 2 || binary.LittleEndian.Uint32(second[6:]) != 7 {
				t.Fatal("slot order or authoritative counts")
			}
		} else if len(body) != 62 || binary.LittleEndian.Uint32(body[1:]) != 1 {
			t.Fatal("history envelope")
		}
		peer := s.gameSessions[33]
		guildDispatch(t, s, peer, op, nil)
		if !bytes.Equal(body, guildReply(t, peer, op)) {
			t.Fatal("same guild cross-channel differs")
		}
		other := s.gameSessions[31]
		guildDispatch(t, s, other, op, nil)
		got := guildReply(t, other, op)
		if op == 261 && !bytes.Equal(got, []byte{1, 3, 0, 0, 0, 0, 0}) || op == 262 && !bytes.Equal(got, []byte{1, 0, 0, 0, 0}) {
			t.Fatalf("foreign guild leak: %x", got)
		}
	}
	after, _, _ := repos.Settings.Load(context.Background(), r.GuildRegistryScope)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("query wrote registry")
	}
}

func TestGuildCargoQueriesRejectBeforeDisclosure(t *testing.T) {
	for _, op := range []uint16{261, 262} {
		for _, mode := range []string{"nonmember", "permission", "wrong_account", "stale", "foreign_body", "not_ready", "wrong_area", "no_npc", "bad_role", "bad_limit", "capacity_limit", "unknown_item", "busy_town"} {
			t.Run(strconv.Itoa(int(op))+"/"+mode, func(t *testing.T) {
				s, p, repos, source := cargoQueryFixture(t)
				var request []byte
				want := byte(8)
				reg, _ := guild.Load(context.Background(), repos.Settings)
				g, _, _ := reg.GuildForCharacter("29")
				switch mode {
				case "nonmember":
					p = s.gameSessions[32]
					want = 100
				case "permission":
					g.GradePermissions[1] = 0
					want = 36
				case "wrong_account":
					p.accountID = "other"
				case "stale":
					s.gameSessions[29] = &gameSession{selectedCharacterID: 29, characterGeneration: 99}
				case "foreign_body":
					request = []byte{1, 0, 0, 0}
				case "not_ready":
					p.townSceneReadyCharacterID = 0
				case "wrong_area":
					p.townPositionSnapshot.TownID = 38
				case "no_npc":
					source["map/Guildagit/GuildAgit_W.map"] = "[NPC]\n98 `[left]` 1150 165 0\n[/NPC]"
				case "bad_role":
					source["npc/GuildStorage.npc"] = "[role]\n`[shop]` 0\n[/role]"
				case "bad_limit":
					source["etc/(r)guild.etc"] = "[guild cargo size limit]\n0"
				case "capacity_limit":
					g.Cargo.Capacity = 57
				case "unknown_item":
					if op == 262 {
						g.Cargo.History[0].Item.ItemID = 999
					} else {
						item := g.Cargo.Slots[0]
						item.ItemID = 999
						g.Cargo.Slots[0] = item
					}
				case "busy_town":
					p.townMu.Lock()
					defer p.townMu.Unlock()
				}
				if err := guild.Save(context.Background(), repos.Settings, reg, s.gameplayNow()); err != nil {
					t.Fatal(err)
				}
				before, _, _ := repos.Settings.Load(context.Background(), r.GuildRegistryScope)
				guildDispatch(t, s, p, op, request)
				if got := guildReply(t, p, op); !bytes.Equal(got, []byte{0, want}) {
					t.Fatalf("%x want0,%d", got, want)
				}
				after, _, _ := repos.Settings.Load(context.Background(), r.GuildRegistryScope)
				if !reflect.DeepEqual(before, after) {
					t.Fatal("rejection wrote state")
				}
			})
		}
	}
}

func TestGuildCargoQueriesInitializeEightSlotsOnlyInAuthorizedScene(t *testing.T) {
	for _, op := range []uint16{261, 262} {
		for _, mode := range []string{"success", "permission", "wrong_area", "no_npc", "small_limit"} {
			t.Run(strconv.Itoa(int(op))+"/"+mode, func(t *testing.T) {
				s, p, repos, source := cargoQueryFixture(t)
				ctx := context.Background()
				reg, _ := guild.Load(ctx, repos.Settings)
				g, _, _ := reg.GuildForCharacter("29")
				g.Cargo = nil
				switch mode {
				case "permission":
					g.GradePermissions[1] = 0
				case "wrong_area":
					p.townPositionSnapshot.TownID = 38
				case "no_npc":
					source["map/Guildagit/GuildAgit_W.map"] = ""
				case "small_limit":
					source["etc/(r)guild.etc"] = "[guild cargo size limit]\n7"
				}
				if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
					t.Fatal(err)
				}
				before, _, _ := repos.Settings.Load(ctx, r.GuildRegistryScope)
				guildDispatch(t, s, p, op, nil)
				got := guildReply(t, p, op)
				after, _, _ := repos.Settings.Load(ctx, r.GuildRegistryScope)
				if mode != "success" {
					if len(got) != 2 || got[0] != 0 || !reflect.DeepEqual(before, after) {
						t.Fatalf("rejected initialization: %x", got)
					}
					return
				}
				want := []byte{1, 8, 0, 0, 0, 0, 0}
				if op == 262 {
					want = []byte{1, 0, 0, 0, 0}
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("first query %x want %x", got, want)
				}
				loaded, _ := guild.Load(ctx, repos.Settings)
				if loaded.Guilds[g.ID].Cargo.Capacity != 8 {
					t.Fatal("initial capacity not durable")
				}
				for id, other := range reg.Guilds {
					if id != g.ID && !reflect.DeepEqual(other, loaded.Guilds[id]) {
						t.Fatal("foreign guild changed")
					}
				}
				guildDispatch(t, s, p, op, nil)
				repeat, _, _ := repos.Settings.Load(ctx, r.GuildRegistryScope)
				if !bytes.Equal(guildReply(t, p, op), want) || !reflect.DeepEqual(after, repeat) {
					t.Fatal("repeat open changed state")
				}
			})
		}
	}
}

func TestGuildCargoSnapshotEquipmentKeepsQualityAndRejectsUnsupportedFamily(t *testing.T) {
	s, _, _, _ := cargoQueryFixture(t)
	stack := r.ItemStack{ItemID: 102, Count: 1, Extra: map[string]string{"item_kind": "equipment", "quality_seed": "777"}}
	body, err := buildCurrentGuildCargoSnapshotBody(&guild.Cargo{Capacity: 3, Slots: map[uint16]r.ItemStack{1: stack}}, 56, s.pvfItemCatalog)
	if err != nil || binary.LittleEndian.Uint32(body[12:]) != 777 {
		t.Fatal("artifact quality seed", err)
	}
	for _, kind := range []string{"[creature]", "[hat avatar]"} {
		def := dungeonDropItemDefinition{ItemID: 102, Kind: dungeonDropItemEquipment, EquipmentType: kind}
		if _, err := currentGuildCargoItemEntry(0, stack, def); err == nil {
			t.Fatalf("unsupported %s accepted", kind)
		}
	}
	stack.RawEntry = make([]byte, 119)
	stack.RawEntry[0x3c] = 3
	if _, err := buildCurrentGuildCargoSnapshotBody(&guild.Cargo{Capacity: 3, Slots: map[uint16]r.ItemStack{1: stack}}, 56, s.pvfItemCatalog); err == nil {
		t.Fatal("unsafe native socket array length accepted")
	}
}

func TestGuildCargoSceneLimitRealPVF(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("requires matching Script.pvf")
	}
	archive, err := platformpvf.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	limit, err := currentGuildCargoSceneLimit(archive, "Guildagit/GuildAgit_W.map")
	if err != nil || limit != 56 {
		t.Fatalf("real limit=%d err=%v", limit, err)
	}
	if _, err = currentGuildCargoSceneLimit(archive, "Guildagit/GuildAgit_1.map"); err == nil {
		t.Fatal("warehouse absent from other hall map")
	}
}

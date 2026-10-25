package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/pvf"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/town"
)

const guardianBridgePolicy = `{"first_purchase":"free_once","initial_level":1,"initial_skills":[1,1,1,1,1],"first_select":true}`

func guardianBridgeSource(t *testing.T) dungeonDropCatalogTestSource {
	t.Helper()
	source := dungeonDropCatalogTestSource{"monster/monster.lst": "", "equipment/equipment.lst": "", "stackable/stackable.lst": ""}
	etc := "[guardian npc visible area]\n3\n[attend guardian npc position]\n556 348\n[first guardian]\n[guardian npc]\n1500 430 363 1501 670 363\n[/guardian npc]\n[/first guardian]\n[guardian list]\n"
	list := ""
	for id := 1; id <= 2; id++ {
		base := 1503 + (id-1)*5
		etc += fmt.Sprintf("[guardian]\n%d\n[ap]\n50000\n[gp]\n1000\n[npc index]\n%d %d %d %d %d\n[/guardian]\n", id, base, base+1, base+2, base+3, base+4)
		list += fmt.Sprintf("%d `first%d.npc`\n", 1499+id, id)
		source[fmt.Sprintf("npc/first%d.npc", id)] = fmt.Sprintf("[role]\n`[attend guardian]` %d\n[/role]\n[clickable distance add]\n0 %d", id, 30-id*10)
		for j := range 5 {
			list += fmt.Sprintf("%d `active%d.npc`\n", base+j, base+j)
			source[fmt.Sprintf("npc/active%d.npc", base+j)] = fmt.Sprintf("[role]\n`[agitwar guardian window]` 0\n[/role]\n[clickable distance add]\n0 %d", 30-id*10)
		}
	}
	source["etc/agitwar.etc"] = etc + "[/guardian list]"
	source["npc/npc.lst"] = list
	if root := os.Getenv("GUILD_GUARDIAN_EXTRACTED_ROOT"); root != "" {
		read := func(path string) string {
			b, err := os.ReadFile(filepath.Join(root, path))
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}
		source["etc/agitwar.etc"] = read("pvf/etc/agitwar.etc")
		source["npc/npc.lst"] = read("pvf/npc/npc.lst")
		list, err := pvf.Parse("npc/npc.lst", source["npc/npc.lst"])
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range pvf.ParseList(list) {
			if row.ID == 1500 || row.ID == 1501 || row.ID >= 1503 && row.ID <= 1512 {
				source["npc/"+row.Path] = read("pvf-extra/npc/" + strings.ToLower(row.Path))
			}
		}
	}
	return source
}

func guildGuardianFixture(t *testing.T) (*Service, *gameSession, r.Group) {
	t.Helper()
	s, p, repos := guildMileageFixture(t)
	s.options.guildEssenceEnabled = true
	s.options.guildGuardianPolicy = guardianBridgePolicy
	items, err := newPVFDungeonDropCatalog(guardianBridgeSource(t))
	if err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog = items
	twn := "[area]\n3 `Guildagit/GuildAgit_3.map` `[normal]`\n[/area]"
	if root := os.Getenv("GUILD_GUARDIAN_EXTRACTED_ROOT"); root != "" {
		b, err := os.ReadFile(filepath.Join(root, "pvf/town/guildagit.twn"))
		if err != nil {
			t.Fatal(err)
		}
		twn = string(b)
	}
	table, err := town.Load(context.Background(), townMoveTestSource{"town/town.lst": "8 `GuildAgit.twn`", "town/GuildAgit.twn": twn}, town.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.townCatalog = table
	p.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: p.selectedCharacterID, TownID: 8, AreaID: 3, PositionX: 430, PositionY: 363, PositionValid: true}
	actor, _, _ := repos.Character.Load(context.Background(), "29")
	actor.Stats["town_id"] = 8
	actor.Stats["area_id"] = 3
	if err := repos.Character.Save(context.Background(), actor); err != nil {
		t.Fatal(err)
	}
	reg, err := guild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	reg.Guilds[1].Essence = guild.EssenceBalance{AP: 100000, GP: 2000}
	if err := guild.Save(context.Background(), repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	return s, p, repos
}

func guardianRequestBody(action, id uint32) []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint32(b, action)
	binary.LittleEndian.PutUint32(b[4:], id)
	return b
}
func sendGuardianRequest(t *testing.T, s *Service, p *gameSession, body []byte, seq uint16) {
	t.Helper()
	wire, err := protocol.BuildChannelPacket(1350, body, seq, 1)
	if err != nil {
		t.Fatal(err)
	}
	packets, rest, skipped, err := protocol.SplitLatestGameStream(wire, 65535)
	if err != nil || len(packets) != 1 || len(rest) != 0 || skipped != 0 {
		t.Fatal(err)
	}
	if err := s.handleGameStreamPacket(p, packets[0]); err != nil {
		t.Fatal(err)
	}
}

func TestGuildGuardianProductionPurchaseSelectAndReconnect(t *testing.T) {
	for _, fee := range []string{"free_once", "paid"} {
		for _, first := range []uint32{1, 2} {
			t.Run(fmt.Sprintf("%s/%d", fee, first), func(t *testing.T) {
				s, p, repos := guildGuardianFixture(t)
				s.options.guildGuardianPolicy = fmt.Sprintf(`{"first_purchase":%q,"initial_level":1,"initial_skills":[1,1,1,1,1],"first_select":true}`, fee)
				if first == 2 {
					p.townPositionSnapshot.PositionX = 670
				}
				conn := p.conn.(*bufferConn)
				prefix := fmt.Sprintf("transaction-%s-%d", fee, first)
				step := func(name string, action, id uint32, seq uint16) {
					t.Helper()
					conn.write.Reset()
					sendGuardianRequest(t, s, p, guardianRequestBody(action, id), seq)
					if got := guildReply(t, p, 1350); !bytes.Equal(got, []byte{1}) {
						t.Fatalf("%s ACK %x", name, got)
					}
					body := guildReply(t, p, 1297)
					exportEssenceBody(t, prefix+"-"+name+"-1297", body)
					exportEssenceBody(t, prefix+"-"+name+"-1350", guildReply(t, p, 1350))
					if dir := os.Getenv("GUILD_ESSENCE_NATIVE_STAGE_DIR"); dir != "" {
						state, err := guild.Load(context.Background(), repos.Settings)
						if err != nil {
							t.Fatal(err)
						}
						trace := struct {
							Action, ID uint32
							Sequence   uint16
							Balance    guild.EssenceBalance
							Guardians  *guild.GuardianState
							Receipts   map[string]guild.GuardianReceipt
						}{action, id, seq, state.Guilds[1].Essence, state.Guilds[1].Guardians, state.GuardianReceipts}
						data, err := json.MarshalIndent(trace, "", "  ")
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(dir, prefix+"-"+name+"-state.json"), data, 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
				step("first", 8, first, 30)
				step("replay", 8, first, 30)
				reg, err := guild.Load(context.Background(), repos.Settings)
				if err != nil || len(reg.GuardianReceipts) != 1 || len(reg.Guilds[1].Guardians.Owned) != 1 {
					t.Fatal(err)
				}
				p.townPositionSnapshot.PositionX = 556
				p.townPositionSnapshot.PositionY = 348
				step("second", 8, 3-first, 31)
				step("select", 7, 3-first, 32)
				step("select-same", 7, 3-first, 33)
				step("select-back", 7, first, 34)
				step("old-select-replay", 7, 3-first, 32)
				reg, err = guild.Load(context.Background(), repos.Settings)
				wantAP, wantGP := uint32(50000), uint32(1000)
				if fee == "paid" {
					wantAP, wantGP = 0, 0
				}
				if err != nil || reg.Guilds[1].Essence != (guild.EssenceBalance{AP: wantAP, GP: wantGP}) || reg.Guilds[1].Guardians.ActiveID != first || len(reg.GuardianReceipts) != 5 {
					t.Fatal(reg, err)
				}
				before, _, _ := repos.Settings.Load(context.Background(), guild.RegistryScope)
				conn.write.Reset()
				sendGuardianRequest(t, s, p, guardianRequestBody(8, first), 35)
				if !bytes.Equal(guildReply(t, p, 1350), []byte{0, 3}) {
					t.Fatal("owned purchase accepted")
				}
				// New connection nonce, same owner/scene. Its first action8 is a new
				// request: already-owned failure, followed by current-state reconnect.
				p2 := &gameSession{conn: &bufferConn{}, accountID: p.accountID, selectedCharacterID: p.selectedCharacterID, characterGeneration: p.characterGeneration + 1, channel: p.channel, residentChannel: p.residentChannel, townSceneReadyCharacterID: p.selectedCharacterID, townPositionSnapshot: p.townPositionSnapshot}
				s.gameSessions[p.selectedCharacterID] = p2
				sendGuardianRequest(t, s, p2, guardianRequestBody(8, first), 30)
				if !bytes.Equal(guildReply(t, p2, 1350), []byte{0, 3}) {
					t.Fatal("reconnect granted twice")
				}
				if err := s.writeCurrentGuildEssenceSnapshot(p2); err != nil {
					t.Fatal(err)
				}
				exportEssenceBody(t, prefix+"-reconnect-1297", guildReply(t, p2, 1297))
				after, _, _ := repos.Settings.Load(context.Background(), guild.RegistryScope)
				if !reflect.DeepEqual(before, after) {
					t.Fatal("rejected purchase changed state")
				}
			})
		}
	}
}

type guardianBeforeTransaction struct {
	r.GuildUnitOfWork
	before func()
}

func (u guardianBeforeTransaction) WithinGuild(ctx context.Context, owners []r.GuildOwner, apply func(r.Group) error) error {
	u.before()
	return u.GuildUnitOfWork.WithinGuild(ctx, owners, apply)
}

func TestGuildGuardianProductionRejections(t *testing.T) {
	for _, mode := range []string{"disabled", "missing_policy", "bad_policy", "insufficient_ap", "insufficient_gp", "account", "nonmember", "deputy_bit16", "wrong_town", "wrong_area", "wrong_map", "stale_position", "not_ready", "range_x", "range_y", "persistent_area", "dungeon", "bad_id", "unsupported_action", "trailing", "nonzero_tail", "missing_sequence", "reused_sequence", "session_replaced", "guild_replaced"} {
		t.Run(mode, func(t *testing.T) {
			s, p, repos := guildGuardianFixture(t)
			ctx := context.Background()
			reg, _ := guild.Load(ctx, repos.Settings)
			body := guardianRequestBody(8, 1)
			want := byte(3)
			switch mode {
			case "disabled":
				s.options.guildEssenceEnabled = false
			case "missing_policy":
				s.options.guildGuardianPolicy = ""
			case "bad_policy":
				s.options.guildGuardianPolicy = `{"first_purchase":"free_once"}`
			case "insufficient_ap", "insufficient_gp":
				s.options.guildGuardianPolicy = `{"first_purchase":"paid","initial_level":1,"initial_skills":[1,1,1,1,1],"first_select":true}`
				want = 22
				if mode == "insufficient_ap" {
					reg.Guilds[1].Essence.AP = 49999
				} else {
					reg.Guilds[1].Essence.GP = 999
				}
			case "account":
				p.accountID = "foreign"
				want = 100
			case "nonmember":
				delete(reg.Guilds, 1)
				want = 100
			case "deputy_bit16":
				g := reg.Guilds[1]
				g.MasterCharacterID = "30"
				m := g.Members["30"]
				m.Grade = 1
				g.Members["30"] = m
				m = g.Members["29"]
				m.Grade = 2
				g.Members["29"] = m
				g.GradePermissions = map[byte]uint32{2: 1 << 16}
			case "wrong_town":
				p.townPositionSnapshot.TownID = 1
			case "wrong_area":
				p.townPositionSnapshot.AreaID = 0
			case "wrong_map":
				table, err := town.Load(ctx, townMoveTestSource{"town/town.lst": "8 `fake.twn`", "town/fake.twn": "[area]\n3 `Other.map` `[normal]`\n[/area]"}, town.Options{})
				if err != nil {
					t.Fatal(err)
				}
				s.townCatalog = table
			case "stale_position":
				p.townPositionSnapshot.CharacterID = 30
			case "not_ready":
				p.townSceneReadyCharacterID = 0
			case "range_x":
				p.townPositionSnapshot.PositionX = 471
			case "range_y":
				p.townPositionSnapshot.PositionY = 424
			case "persistent_area":
				a, _, _ := repos.Character.Load(ctx, "29")
				a.Stats["area_id"] = 0
				if err := repos.Character.Save(ctx, a); err != nil {
					t.Fatal(err)
				}
			case "dungeon":
				p.dungeon.runtime = &runtimeDungeonState{}
			case "bad_id":
				body = guardianRequestBody(8, 3)
			case "unsupported_action":
				body = guardianRequestBody(0, 1)
			case "trailing":
				body = append(body, 0)
			case "nonzero_tail":
				body[12] = 1
			case "session_replaced":
				base := repos.Guild
				repos.Guild = guardianBeforeTransaction{base, func() { s.gameSessions[p.selectedCharacterID] = &gameSession{} }}
			case "guild_replaced":
				base := repos.Guild
				repos.Guild = guardianBeforeTransaction{base, func() {
					g := reg.Guilds[1]
					delete(reg.Guilds, 1)
					g.ID = 3
					reg.Guilds[3] = g
					reg.NextID = 4
					if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
						t.Fatal(err)
					}
				}}
				want = 100
			}
			if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
				t.Fatal(err)
			}
			s.repositoryProvider = func() (r.Group, bool) { return repos, true }
			if mode == "reused_sequence" {
				sendGuardianRequest(t, s, p, body, 30)
				body = guardianRequestBody(7, 1)
				p.conn.(*bufferConn).write.Reset()
			}
			before, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			if mode == "missing_sequence" {
				handled, err := currentGameplayModules.DispatchUpper(s, p, 1350, 1, body)
				if !handled || err != nil {
					t.Fatal(handled, err)
				}
			} else {
				sendGuardianRequest(t, s, p, body, 30)
			}
			if got := guildReply(t, p, 1350); !bytes.Equal(got, []byte{0, want}) {
				t.Fatalf("got %x want 00%02x", got, want)
			}
			after, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			if mode != "guild_replaced" && !reflect.DeepEqual(before, after) {
				t.Fatal("rejected mutation")
			}
			loaded, err := guild.Load(ctx, repos.Settings)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "reused_sequence" && len(loaded.GuardianReceipts) != 0 {
				t.Fatal("receipt for failure")
			}
		})
	}
}

func TestGuildGuardianMatchedNPCDistanceBoundaries(t *testing.T) {
	scene, err := loadGuildGuardianScene(guardianBridgeSource(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint32{1, 2} {
		for _, active := range []bool{false, true} {
			g := &guild.Guild{}
			npc := scene.first[id]
			if active {
				g.Guardians = &guild.GuardianState{EverAcquired: true, ActiveID: id, Owned: []guild.Guardian{{ID: id, Level: 1}}}
				npc = scene.active[id][0]
			}
			for _, delta := range [][3]int{{npc.dx, npc.dy, 1}, {-npc.dx, -npc.dy, 1}, {npc.dx + 1, 0, 0}, {0, npc.dy + 1, 0}} {
				if got := scene.near(g, id, uint16(npc.x+delta[0]), uint16(npc.y+delta[1])); got != (delta[2] == 1) {
					t.Fatal(id, active, delta, got)
				}
			}
		}
	}
	for _, kind := range []string{"role", "missing", "wrong_id"} {
		source := guardianBridgeSource(t)
		switch kind {
		case "role":
			for path := range source {
				if filepath.Ext(path) == ".npc" {
					source[path] = "[role]\n`[other]` 0\n[/role]"
				}
			}
		case "missing":
			delete(source, "npc/npc.lst")
		case "wrong_id":
			source["etc/agitwar.etc"] = "[guardian npc visible area]\n3\n[attend guardian npc position]\n556 348\n[first guardian]\n[guardian npc]\n9999 430 363 1501 670 363\n[/guardian npc]\n[/first guardian]"
		}
		if _, err := loadGuildGuardianScene(source); err == nil {
			t.Fatal(kind)
		}
	}
}

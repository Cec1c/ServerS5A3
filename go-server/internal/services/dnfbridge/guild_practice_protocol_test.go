package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Filter lists only; real DGN/MAP/monster/ETC bodies remain unchanged.
type practiceProtocolSource struct{ pvf.Source }

func (s practiceProtocolSource) ReadText(path string) (string, error) {
	text, err := s.Source.ReadText(path)
	if err != nil || (path != worldmap.DefaultDungeonList && path != worldmap.DefaultMapList) {
		return text, err
	}
	doc, err := pvf.Parse(path, text)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, e := range pvf.ParseList(doc) {
		if (path == worldmap.DefaultDungeonList && e.ID >= 8510 && e.ID <= 8520) || (path == worldmap.DefaultMapList && e.ID >= 36600 && e.ID <= 36700) {
			fmt.Fprintf(&out, "%d `%s`\n", e.ID, e.Path)
		}
	}
	return out.String(), nil
}

func practiceProtocolFixture(t *testing.T, source pvf.Source) (*Service, *gameSession) {
	t.Helper()
	s, peers, repos := guildHallFixture(t)
	s.options.guildEssenceEnabled = true
	p := peers[29]
	p.townPositionSnapshot.CharacterID = 29
	c, _, _ := repos.Character.Load(context.Background(), "29")
	c.Level = 90
	c.Stats["fatigue"] = 100
	if err := repos.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), source, worldmap.Options{SkipAreas: true})
	if err != nil {
		t.Fatal(err)
	}
	s.worldMapTable = table
	s.worldMapResolver, err = worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	s.dungeonMonsterTable, err = newPVFDungeonMonsterCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog, err = newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	s.dungeonChoice = func(int) (int, error) { return 0, nil }
	s.dungeonSeed = func() (uint32, error) { return 1, nil }
	reg, err := guild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0)
	reg.Guilds[1].Placements = &guild.PlacementState{NextID: 4}
	reg.Guilds[1].Guardians = &guild.GuardianState{EverAcquired: true, ActiveID: 1, Owned: []guild.Guardian{{ID: 1, Level: 11}}}
	for n := 0; n < 3; n++ {
		reg.Guilds[1].Placements.Rows = append(reg.Guilds[1].Placements.Rows, guild.Placement{ID: uint32(n + 1), ItemID: 1000, ObjectID: uint32(71021 + n), CreatorID: 29, Area: 0, X: int16(800 + n*40), Y: 400, Maintenance: 300, Cells: []guild.PlacementCell{{X: n, Y: 0}}, CreatedAt: now, ReadyAt: now})
	}
	if err := guild.Save(context.Background(), repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	logger, _ := newPeerEndpointTestLogger(t)
	s.packetLog = logger
	t.Cleanup(func() { _ = logger.Close() })
	return s, p
}

func practiceProtocolEnter(t *testing.T, s *Service, p *gameSession, id uint32) *runtimeDungeonState {
	t.Helper()
	body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
	binary.LittleEndian.PutUint32(body, id)
	body[4] = 3
	body[8] = 1
	binary.LittleEndian.PutUint16(body[9:], 65535)
	if err := s.handleDungeonSelectUpper(p, body); err != nil {
		t.Fatal(err)
	}
	r := p.dungeon.runtime
	if r == nil || r.guildPractice == nil || r.Room.guildPractice == nil {
		t.Fatalf("entry did not commit: bytes=%x", p.conn.(*bufferConn).write.Bytes())
	}
	seen191, seen28, seen29 := false, false, false
	for wire := p.conn.(*bufferConn).write.Bytes(); len(wire) > 0; {
		packet, tail := splitCurrentGameServerUpperPacketAuto(t, wire)
		wire = tail
		switch packet.Header.MsgID {
		case 191:
			seen191 = true
		case 28:
			if !seen191 {
				t.Fatal("entry before frozen191")
			}
			seen28 = true
		case 29:
			seen29 = true
		}
	}
	if !seen28 || !seen29 {
		t.Fatal("missing native entry/start-map packets")
	}
	return r
}

func TestGuildPracticeProtocolEntryCompletionAndGiveup(t *testing.T) {
	for _, finish := range []bool{false, true} {
		t.Run(fmt.Sprint(finish), func(t *testing.T) {
			source := bridgeDungeonPVF(true)
			for k, v := range source {
				source[k] = strings.ReplaceAll(v, "700", "8510")
			}
			source["dungeon/test.dgn"] = "[agit war dungeon]\n1\n[friendly agit dungeon]\n" + source["dungeon/test.dgn"]
			source["map/dungeon/test/start.map"] += "\n[agitwar area index]\n0\n[agit obejcts xpos reverse]\n1330\n"
			source["etc/agitwar.etc"] = guildPracticeBridgeETC
			source["etc/agitwarpassivemonsters.etc"] = "[agitwar monster build]\n71021 70641 71022 70642 71023 70643\n[/agitwar monster build]"
			source["equipment/equipment.lst"] = ""
			source["stackable/stackable.lst"] = ""
			s, p := practiceProtocolFixture(t, source)
			r := practiceProtocolEnter(t, s, p, 8510)
			practiceProtocolExit(t, s, p, r, finish)
		})
	}
}

func practiceProtocolExit(t *testing.T, s *Service, p *gameSession, r *runtimeDungeonState, finish bool) {
	t.Helper()
	repos, _ := s.repositoryProvider()
	before, _, _ := repos.Character.Load(context.Background(), "29")
	beforeInventory, _, _ := repos.Inventory.Load(context.Background(), "29")
	beforeGuild, err := guild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if finish {
		for key, target := range r.Room.guildPractice.targets {
			if target.Local {
				if err := s.handleDungeonMonsterDeath(p, doc94Official249DeathBody(key)); err != nil {
					t.Fatal(err)
				}
			}
		}
		for _, monster := range r.Room.Snapshot().Monsters {
			if err := s.handleDungeonMonsterDeath(p, doc94Official249DeathBody(monster.ObjectKey)); err != nil {
				t.Fatal(err)
			}
		}
		if !r.guildPractice.Finished || r.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted {
			t.Fatalf("no practice completion: %+v", r.Session.Snapshot())
		}
		p.conn.(*bufferConn).write.Reset()
		playResult := make([]byte, currentDungeonPlayResultBaseSize)
		playResult[currentDungeonPlayResultClientRankPointOffset] = 73
		for n := 0; n < 2; n++ {
			for _, call := range []func() error{
				func() error { return s.handleDungeonSetPlayResult(p, playResult) },
				func() error {
					return s.handleDungeonCharacterStatistic(p, make([]byte, currentDungeonCharacterStatisticBodySize))
				},
				func() error { return s.handleDungeonScoreScrollState(p, nil) },
				func() error { return s.handleDungeonCardSelectRightState(p, nil) },
				func() error { return s.handleDungeonSelectCard(p, []byte{0, 0}) },
			} {
				if err := call(); err != nil {
					t.Fatal(err)
				}
			}
		}
		if p.conn.(*bufferConn).write.Len() != 0 || r.settlementPlayResultReceived || r.settlementStatisticReceived || r.settlementCardScrollStateSent || r.settlementCardLayoutSent {
			t.Fatal("ordinary settlement replay accepted")
		}
	}
	if r.settlementEntrySent || r.settlementCardRewardCommitted || r.bossDieCheckAccepted {
		t.Fatal("ordinary rewards/settlement acquired")
	}
	after, _, _ := repos.Character.Load(context.Background(), "29")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("death/completion changed character state")
	}
	afterInventory, _, _ := repos.Inventory.Load(context.Background(), "29")
	afterGuild, err := guild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeInventory, afterInventory) || !reflect.DeepEqual(beforeGuild, afterGuild) {
		t.Fatal("practice changed inventory or guild progression")
	}
	p.conn.(*bufferConn).write.Reset()
	reg, err := guild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	reg.Guilds[1].Placements.Rows[0].Maintenance--
	if err := guild.Save(context.Background(), repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	wantProjection, err := buildCurrentGuildPlacementSnapshot(reg.Guilds[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := s.handleDungeonGiveupGame(p, nil); err != nil {
		t.Fatal(err)
	}
	if !r.townReturnOp24Sent {
		t.Fatalf("op42 did not send town transition, finished=%t status=%v", finish, r.Session.Snapshot().Run.Status)
	}
	if r.townReturnTransition.TownID != 8 || r.townReturnTransition.AreaID != 0 || r.townReturnTransition.PositionX != 500 {
		t.Fatalf("wrong frozen origin: %+v", r.townReturnTransition)
	}
	if !finish {
		if p.dungeon.runtime != r {
			t.Fatal("giveup detached before town evidence")
		}
		if err := s.commitPendingDungeonReturnForSceneRequest(p, "guild_practice_protocol_town_confirmation"); err != nil {
			t.Fatal(err)
		}
	}
	if p.dungeon.runtime != nil || p.guild.placement.frozenPractice.session != nil {
		t.Fatal("return retained runtime or frozen guild snapshot")
	}
	if r.settlementCardRewardCommitted {
		t.Fatal("return invented card settlement")
	}
	fresh := false
	for wire := p.conn.(*bufferConn).write.Bytes(); len(wire) > 0; {
		packet, tail := splitCurrentGameServerUpperPacketAuto(t, wire)
		wire = tail
		if packet.Header.MsgID == 191 && bytes.Equal(packet.Body, wantProjection) {
			fresh = true
		}
	}
	if !fresh {
		t.Fatal("town return did not restore the latest guild placement snapshot")
	}
}

func TestGuildPracticeRealPVFProtocolMoveCompleteReturn(t *testing.T) {
	path := os.Getenv("GUILD_PRACTICE_TEST_PVF")
	if path == "" {
		t.Skip("matching PVF required")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint32{8510, 8511} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			s, p := practiceProtocolFixture(t, practiceProtocolSource{a})
			r := practiceProtocolEnter(t, s, p, id)
			start := r.Session.Snapshot().Scene
			for _, m := range r.Room.Snapshot().Monsters {
				if err := s.handleDungeonMonsterDeath(p, doc94Official249DeathBody(m.ObjectKey)); err != nil {
					t.Fatal(err)
				}
			}
			body := make([]byte, dungeoncmd.MoveMapRequestSize)
			body[0] = 1
			p.conn.(*bufferConn).write.Reset()
			if err := s.handleDungeonMoveMap(p, body); err != nil {
				t.Fatal(err)
			}
			if r.Session.Snapshot().Scene.Coordinate == start.Coordinate {
				t.Fatal("real room move did not commit")
			}
			if r.Room.guildPractice == nil {
				t.Fatal("target room practice table missing")
			}
			if id == 8510 && len(r.Room.guildPractice.targets) != 4 {
				t.Fatalf("real boss guardian+local targets=%+v", r.Room.guildPractice.targets)
			}
			if id == 8510 {
				monster := r.Room.Snapshot().Monsters[0]
				if r.Room.guildPractice.targets[monster.ObjectKey].MonsterID != 71022 {
					t.Fatal("active guardian level11 not frozen into server target")
				}
				found := false
				for wire := p.conn.(*bufferConn).write.Bytes(); len(wire) > 0; {
					packet, tail := splitCurrentGameServerUpperPacketAuto(t, wire)
					wire = tail
					if packet.Header.MsgID == 29 {
						if len(packet.Body) < 31 || packet.Body[18] != 1 || binary.LittleEndian.Uint16(packet.Body[25:27]) != uint16(monster.ObjectKey) || binary.LittleEndian.Uint32(packet.Body[27:31]) != 9 {
							t.Fatalf("guardian op29 must remain one dummy9 row: %x", packet.Body)
						}
						found = true
					}
				}
				if !found {
					t.Fatal("no real guardian room op29")
				}
			}
			if bytes.Equal(nil, p.conn.(*bufferConn).write.Bytes()) {
				t.Fatal("move wrote no packets")
			}
			practiceProtocolExit(t, s, p, r, true)
		})
	}
}

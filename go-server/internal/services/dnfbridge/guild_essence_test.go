package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func guildEssenceBridgeCatalog(t *testing.T, s *Service) {
	t.Helper()
	text := "[daily max ap]\n5000\n[ap reward]\n10 15 15 30 0\n[/ap reward]"
	if path := os.Getenv("GUILD_ESSENCE_EXTRACTED_ETC"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text = string(raw)
	}
	// Use the real production catalog loader, alongside the existing item fixture.
	items, err := newPVFDungeonDropCatalog(dungeonDropCatalogTestSource{"monster/monster.lst": "", "equipment/equipment.lst": "", "stackable/stackable.lst": "", guild.EssenceCatalogPath: text})
	if err != nil {
		t.Fatal(err)
	}
	prior := s.pvfItemCatalog
	s.pvfItemCatalog = items
	s.guildEssenceCatalog = nil
	if _, err = s.currentGuildEssenceCatalog(); err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog = prior
	s.options.guildEssenceEnabled = true
}

func exportEssenceBody(t *testing.T, name string, body []byte) {
	t.Helper()
	if dir := os.Getenv("GUILD_ESSENCE_NATIVE_STAGE_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".bin"), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGuildEssenceLoginProductionProjectionAndReconnect(t *testing.T) {
	s, p, repos := guildMileageFixture(t)
	guildEssenceBridgeCatalog(t, s)
	conn := p.conn.(*bufferConn)
	ctx := context.Background()
	p.initialTownLegacySceneReadyAccepted = true
	if err := s.sendCurrentInitialTownGuildStateProjectionLocked(p, "essence-login"); err != nil {
		t.Fatal(err)
	}
	seenMembership := false
	for wire := conn.write.Bytes(); len(wire) > 0; {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
		wire = rest
		if packet.Header.MsgID == 70 {
			seenMembership = true
		}
		if (packet.Header.MsgID == 1297 || packet.Header.MsgID == 1284) && !seenMembership {
			t.Fatal("essence before native guild creation")
		}
	}
	snapshot := append([]byte(nil), guildReply(t, p, 1297)...)
	notice := append([]byte(nil), guildReply(t, p, 1284)...)
	if len(snapshot) != 0x895 || binary.LittleEndian.Uint32(snapshot[0x17:]) != 10 || binary.LittleEndian.Uint32(snapshot[0x1b:]) != 0 || !bytes.Equal(notice, []byte{10, 0, 0, 0, 0, 0, 0, 0}) {
		t.Fatal("login packets")
	}
	exportEssenceBody(t, "login-1297", snapshot)
	exportEssenceBody(t, "login-1284", notice)
	conn.write.Reset()
	if err := s.grantCurrentGuildEssenceLogin(p); err != nil {
		t.Fatal(err)
	}
	assertNoEssenceNotice(t, conn.write.Bytes())
	// No extra grant on reconnect; constructor defaults remain exact.
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil || reg.Guilds[1].Essence.AP != 10 {
		t.Fatal(err)
	}
	restarted := &Service{repositoryProvider: s.repositoryProvider, options: s.options, gameplayTimers: s.gameplayTimers}
	conn.write.Reset()
	if err := restarted.writeCurrentGuildEssenceSnapshot(p); err != nil {
		t.Fatal(err)
	}
	if got := guildReply(t, p, 1297); !bytes.Equal(got, snapshot) {
		t.Fatal("reconnect snapshot")
	}
	exportEssenceBody(t, "reconnect-1297", snapshot)
	p.selectedCharacterID = 32
	conn.write.Reset()
	if err := s.writeCurrentGuildEssenceSnapshot(p); err != nil {
		t.Fatal(err)
	}
	empty := append([]byte(nil), guildReply(t, p, 1297)...)
	if !bytes.Equal(empty, buildCurrentGuildEssenceSnapshot(guild.EssenceBalance{})) {
		t.Fatal("nonmember stale balance")
	}
	exportEssenceBody(t, "nonmember-1297", empty)
}

func TestGuildEssenceProductionSettlementPackets(t *testing.T) {
	for _, mode := range []string{"recommended", "guild-solo", "guild-party"} {
		t.Run(mode, func(t *testing.T) {
			s, runtime, p, conn, _ := prepareCompletedSettlementRuntime(t)
			ctx := context.Background()
			repos, _ := s.repositoryGroup()
			guildDungeonBridgeCatalog(t, s)
			guildEssenceBridgeCatalog(t, s)
			c, _, _ := repos.Character.Load(ctx, "99")
			c.Name = "EssenceClear"
			c.Level = 1
			c.Stats["exp"] = 90
			for _, err := range []error{repos.Character.Save(ctx, c), repos.Skill.Save(ctx, r.SkillRecord{CharacterID: "99", Points: r.SkillPointState{SyncedLevel: 1, TotalSP: 10, RemainingSP: 10}}), repos.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "99"})} {
				if err != nil {
					t.Fatal(err)
				}
			}
			reg := guild.NewRegistry()
			if _, err := reg.Create(c, "EssenceGuild", s.gameplayNow()); err != nil {
				t.Fatal(err)
			}
			if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
				t.Fatal(err)
			}
			// Real canonical selection is required by the income reservation.
			p.accountID = c.AccountID
			p.characterGeneration = 1
			s.gameSessions = map[uint16]*gameSession{99: p}
			runtime.Character = c
			runtime.AccountID = c.AccountID
			runtime.entryCharacterLevel = 1
			runtime.ordinaryFinalRoomClearAccepted = true
			runtime.tutorialFinalRoomClearAccepted = false
			runtime.clearMapCompletionKey = "essence-" + mode
			runtime.Dungeon.Metadata.TutorialDungeon = worldmap.OptionalInt{}
			runtime.Dungeon.Metadata.RecommendedLevels = []int64{1, 2}
			want := uint32(15)
			if mode != "recommended" {
				runtime.Dungeon.ID = 8500
				runtime.Dungeon.Metadata.IntegerValues = map[string]int64{"guild dungeon": 1}
				runtime.Request.DungeonID = 8500
				runtime.Request.Difficulty = 2
				var followers []*runtimePartyPreparedDungeonEntry
				if mode == "guild-party" {
					other := r.CloneCharacter(c)
					other.CharacterID = "100"
					other.Name = "PartyMember"
					other.AccountID = "party-account"
					if err := repos.Character.Save(ctx, other); err != nil {
						t.Fatal(err)
					}
					if _, err := reg.JoinDirect(1, other, s.gameplayNow()); err != nil {
						t.Fatal(err)
					}
					if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
						t.Fatal(err)
					}
					q := &gameSession{accountID: other.AccountID, selectedCharacterID: 100}
					rr := &runtimeDungeonState{Character: other, Dungeon: runtime.Dungeon, Request: runtime.Request}
					followers = []*runtimePartyPreparedDungeonEntry{{session: q, runtime: rr}}
					want = 30
				}
				if err := s.freezeCurrentGuildDungeonGroup(ctx, p, runtime, followers); err != nil {
					t.Fatal(err)
				}
			}
			resources := currentDungeonSettlementTestResources(t)
			first, err := s.commitCurrentDungeonSettlement(ctx, p, runtime, resources.Progression, 20)
			if err != nil || first.Replayed {
				t.Fatal("commit", err)
			}
			snapshot := append([]byte(nil), guildReply(t, p, 1297)...)
			notice := append([]byte(nil), guildReply(t, p, 1284)...)
			if binary.LittleEndian.Uint32(snapshot[0x17:]) != want || binary.LittleEndian.Uint32(notice) != want || binary.LittleEndian.Uint32(notice[4:]) != 0 {
				t.Fatal("wire reward")
			}
			exportEssenceBody(t, mode+"-1297", snapshot)
			exportEssenceBody(t, mode+"-1284", notice)
			conn.write.Reset()
			second, err := s.commitCurrentDungeonSettlement(ctx, p, runtime, resources.Progression, 20)
			if err != nil || !second.Replayed {
				t.Fatal("replay", err)
			}
			reg, err = guild.Load(ctx, repos.Settings)
			if err != nil || reg.Guilds[1].Essence.AP != want {
				t.Fatal("double income", err)
			}
			if !bytes.Equal(guildReply(t, p, 1297), snapshot) {
				t.Fatal("replay snapshot")
			}
			assertNoEssenceNotice(t, conn.write.Bytes())
		})
	}
}

func TestGuildEssenceDefaultOffAndStaleBinding(t *testing.T) {
	s, p, repos := guildMileageFixture(t)
	ctx := context.Background()
	before, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
	if err := s.grantCurrentGuildEssenceLogin(p); err != nil {
		t.Fatal(err)
	}
	if err := s.writeCurrentGuildEssenceSnapshot(p); err != nil {
		t.Fatal(err)
	}
	if p.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("default-off sends")
	}
	guildEssenceBridgeCatalog(t, s)
	delete(s.gameSessions, p.selectedCharacterID)
	if err := s.grantCurrentGuildEssenceLogin(p); err == nil {
		t.Fatal("stale binding granted")
	}
	after, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
	if fmt.Sprint(before.Values) != fmt.Sprint(after.Values) {
		t.Fatal("disabled/stale mutation")
	}
}

func assertNoEssenceNotice(t *testing.T, wire []byte) {
	t.Helper()
	for len(wire) > 0 {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
		wire = rest
		if packet.Header.MsgID == 1284 {
			t.Fatal("duplicate income notice")
		}
	}
}

func TestGuildEssencePeerRefreshUsesCanonicalMembers(t *testing.T) {
	s, peers, repos := guildHallFixture(t)
	s.options.guildEssenceEnabled = true
	reg, err := guild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	reg.Guilds[1].Essence.AP = 31
	if err := guild.Save(context.Background(), repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, id := range []uint16{30, 31, 32, 33} {
		p := peers[id]
		if err := s.startGameSessionEvents(ctx, p); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			stop, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := s.stopGameSessionEvents(stop, p); err != nil {
				t.Error(err)
			}
		})
	}
	s.queueCurrentGuildEssenceRefresh(1, 29)
	for _, id := range []uint16{30, 31, 32, 33} {
		p := peers[id]
		if err := s.callGameSession(ctx, p, "drain-guild-refresh", func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		if id == 30 || id == 33 {
			if body := guildReply(t, p, 1297); len(body) != 0x895 || binary.LittleEndian.Uint32(body[0x17:]) != 31 {
				t.Fatalf("peer %d snapshot=%x", id, body)
			}
		} else if p.conn.(*bufferConn).write.Len() != 0 {
			t.Fatalf("cross-guild snapshot sent to %d", id)
		}
	}
}

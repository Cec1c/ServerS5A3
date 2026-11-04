package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func repeatRewardBridgeFixture(t *testing.T, progress int64) (*Service, *runtimeDungeonState, *gameSession, *bufferConn, r.Group) {
	t.Helper()
	s, rt, p, conn, _ := prepareCompletedSettlementRuntime(t)
	ctx := context.Background()
	repos, _ := s.repositoryGroup()
	guildDungeonBridgeCatalog(t, s)
	catalog, err := s.currentGuildDungeonCatalog()
	if err != nil {
		t.Fatal(err)
	}
	// Keep the normal tests independent of external PVF. Real ETC already has this section.
	if os.Getenv("GUILD_DUNGEON_EXTRACTED_ETC") == "" {
		text, err := s.pvfItemCatalog.source.ReadText(guild.DungeonCatalogPath)
		if err != nil {
			t.Fatal(err)
		}
		catalog, err = guild.ParseDungeonCatalog(text + "\n[repeat clear bonus reward]\n10156320 1\n")
		if err != nil {
			t.Fatal(err)
		}
		s.guildDungeonCatalog = catalog
	}
	c, _, _ := repos.Character.Load(ctx, "99")
	c.Name = "RepeatReward"
	c.Level = 1
	c.Stats["exp"] = 90
	c.Stats[guild.DungeonRepeatRewardProgressStat] = progress
	for _, err := range []error{repos.Character.Save(ctx, c), repos.Skill.Save(ctx, r.SkillRecord{CharacterID: "99", Points: r.SkillPointState{SyncedLevel: 1, TotalSP: 10, RemainingSP: 10}}), repos.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "99"})} {
		if err != nil {
			t.Fatal(err)
		}
	}
	reg := guild.NewRegistry()
	if _, err := reg.Create(c, "RepeatGuild", s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	rt.Dungeon.ID = 8500
	rt.Character = c
	rt.AccountID = c.AccountID
	rt.Request.DungeonID = 8500
	rt.Request.Difficulty = 2
	rt.Dungeon.Metadata.IntegerValues = map[string]int64{"guild dungeon": 1}
	rt.Dungeon.Metadata.BasisLevel.Set = true
	rt.Dungeon.Metadata.BasisLevel.Value = 1
	rt.Dungeon.Metadata.ExperienceIncreasingPoint.Set = true
	rt.Dungeon.Metadata.ExperienceIncreasingPoint.Value = 1
	rt.ordinaryFinalRoomClearAccepted = true
	rt.tutorialFinalRoomClearAccepted = false
	rt.clearMapCompletionKey = "guild-repeat"
	rt.entryCharacterLevel = 1
	rt.settlementPlayResultReceived = true
	rt.settlementPlayResultBody = make([]byte, currentDungeonPlayResultBaseSize)
	if err := s.freezeCurrentGuildDungeonGroup(ctx, p, rt, nil); err != nil {
		t.Fatal(err)
	}
	conn.write.Reset()
	return s, rt, p, conn, repos
}

func assertRepeatPacket(t *testing.T, wire []byte, want uint32) []byte {
	t.Helper()
	var body []byte
	for len(wire) > 0 {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
		wire = rest
		if packet.Header.MsgID == 742 {
			if body != nil || packet.Header.Classification != 0 || len(packet.Body) != 4 || binary.LittleEndian.Uint32(packet.Body) != want {
				t.Fatalf("742 class=%d body=%x want=%d", packet.Header.Classification, packet.Body, want)
			}
			body = append([]byte(nil), packet.Body...)
		}
	}
	if body == nil {
		t.Fatal("missing class0/742")
	}
	return body
}

func saveRepeatNativeBody(t *testing.T, name string, body []byte) {
	t.Helper()
	if out := os.Getenv("GUILD_REPEAT_NATIVE_DIR"); out != "" {
		if err := os.MkdirAll(out, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, name+".bin"), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGuildDungeonRepeatRewardEntryOwnershipAndOrdinaryDungeon(t *testing.T) {
	s, rt, p, conn, _ := repeatRewardBridgeFixture(t, 30)
	p.selectedCharacterID++
	if err := s.sendCurrentGuildDungeonRepeatRewardOnEntry(p, rt); err == nil || conn.write.Len() != 0 {
		t.Fatal("wrong character accepted")
	}
	p.selectedCharacterID--
	rt.Character.AccountID = "wrong"
	if err := s.sendCurrentGuildDungeonRepeatRewardOnEntry(p, rt); err == nil || conn.write.Len() != 0 {
		t.Fatal("wrong account accepted")
	}
	rt.Dungeon = worldmap.Dungeon{ID: 1}
	rt.guildDungeonClear = nil
	if err := s.sendCurrentGuildDungeonRepeatRewardOnEntry(p, rt); err != nil || conn.write.Len() != 0 {
		t.Fatal("ordinary dungeon received 742", err)
	}
	if _, err := buildCurrentGuildDungeonRepeatRewardBody(101); err == nil {
		t.Fatal("invalid percent accepted")
	}
}

func repeatKillBridgeFixture(t *testing.T, progress int64) (*Service, *runtimeDungeonState, *gameSession, *bufferConn, r.Group) {
	s, rt, p, conn, repos := repeatRewardBridgeFixture(t, progress)
	fresh := cardKillLedgerRuntimeForTest(t)
	rt.Session, rt.Room, rt.Rooms = fresh.Session, fresh.Room, fresh.Rooms
	if err := s.freezeCurrentGuildDungeonGroup(context.Background(), p, rt, nil); err != nil {
		t.Fatal(err)
	}
	return s, rt, p, conn, repos
}
func TestGuildDungeonRepeatRewardKillsAndReentry(t *testing.T) {
	s, rt, p, conn, repos := repeatKillBridgeFixture(t, 0)
	if err := s.sendCurrentGuildDungeonRepeatRewardOnEntry(p, rt); err != nil {
		t.Fatal(err)
	}
	saveRepeatNativeBody(t, "kill-entry", assertRepeatPacket(t, conn.write.Bytes(), 0))
	conn.write.Reset()
	normal := cardKillLedgerMonsterByActorType(t, rt, 0)
	if _, _, err := s.commitGuildDungeonRewardDeath(p, rt, normal.ObjectKey, nil); err != nil {
		t.Fatal(err)
	}
	expected := uint32(10 / len(rt.guildDungeonKills.targets))
	saveRepeatNativeBody(t, "kill-partial", assertRepeatPacket(t, conn.write.Bytes(), expected))
	conn.write.Reset()
	before, _, _ := repos.Character.Load(context.Background(), "99")
	if _, _, err := s.commitGuildDungeonRewardDeath(p, rt, normal.ObjectKey, nil); !errors.Is(err, errDungeonMonsterAlreadyDefeated) {
		t.Fatal(err)
	}
	assertRepeatPacket(t, conn.write.Bytes(), expected)
	conn.write.Reset()
	after, _, _ := repos.Character.Load(context.Background(), "99")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("duplicate increment")
	}
	// Reentry recovers the committed balance without completing the dungeon.
	rt.Character = after
	if err := s.freezeCurrentGuildDungeonGroup(context.Background(), p, rt, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.sendCurrentGuildDungeonRepeatRewardOnEntry(p, rt); err != nil {
		t.Fatal(err)
	}
	saveRepeatNativeBody(t, "kill-reentry", assertRepeatPacket(t, conn.write.Bytes(), expected))
}
func TestGuildDungeonRepeatRewardRejectsUnknownAndCleanup(t *testing.T) {
	s, rt, p, conn, repos := repeatKillBridgeFixture(t, 0)
	before, _, _ := repos.Character.Load(context.Background(), "99")
	if _, _, err := s.commitGuildDungeonRewardDeath(p, rt, 65535, nil); err == nil {
		t.Fatal("unknown object accepted")
	}
	normal := cardKillLedgerMonsterByActorType(t, rt, 0)
	if _, _, err := rt.Room.commitActorDeathReport(normal.ObjectKey, rt.Session, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.commitGuildDungeonRewardDeath(p, rt, normal.ObjectKey, nil); !errors.Is(err, errDungeonMonsterAlreadyDefeated) {
		t.Fatal(err)
	}
	after, _, _ := repos.Character.Load(context.Background(), "99")
	if !reflect.DeepEqual(before, after) || conn.write.Len() != 0 {
		t.Fatal("cleanup generated progress")
	}
	// A dynamically added monster with no static identity has no reward share.
	s, rt, p, _, _ = repeatKillBridgeFixture(t, 0)
	normal = cardKillLedgerMonsterByActorType(t, rt, 0)
	delete(rt.guildDungeonKills.targets, guildDungeonMonsterKey(rt.Room.coordinate, rt.Room.mapID, normal.Reference.Index))
	apply, _, err := s.prepareCurrentGuildDungeonKill(p, rt, normal.ObjectKey)
	if err != nil || apply != nil {
		t.Fatal("non-layout monster counted", err)
	}
}
func TestGuildDungeonRepeatRewardWriteFailureRetry(t *testing.T) {
	s, rt, p, _, repos := repeatKillBridgeFixture(t, 99)
	normal := cardKillLedgerMonsterByActorType(t, rt, 0)
	fault := errors.New("742 write failure")
	conn := &failNthDungeonWriteConn{err: fault, failWhen: func(data []byte) bool {
		packet, _ := splitCurrentGameServerUpperPacketAuto(t, data)
		return packet.Header.MsgID == 742
	}}
	p.conn = conn
	if _, _, err := s.commitGuildDungeonRewardDeath(p, rt, normal.ObjectKey, nil); !errors.Is(err, fault) {
		t.Fatal(err)
	}
	before, _, _ := repos.Inventory.Load(context.Background(), "99")
	conn.failWhen = nil
	conn.write.Reset()
	if _, _, err := s.commitGuildDungeonRewardDeath(p, rt, normal.ObjectKey, nil); !errors.Is(err, errDungeonMonsterAlreadyDefeated) {
		t.Fatal(err)
	}
	after, _, _ := repos.Inventory.Load(context.Background(), "99")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("retry minted second box")
	}
	c, _, _ := repos.Character.Load(context.Background(), "99")
	assertRepeatPacket(t, conn.write.Bytes(), uint32(c.Stats[guild.DungeonRepeatRewardProgressStat]))
}

type repeatRewardSQLRollback struct {
	r.CharacterSettlementUnitOfWork
	kill  r.GuildDungeonKillUnitOfWork
	fault error
}

func (f repeatRewardSQLRollback) WithinGuildDungeonKill(ctx context.Context, owner r.GuildOwner, key string, apply func(r.Group) error) error {
	return f.kill.WithinGuildDungeonKill(ctx, owner, key, func(tx r.Group) error {
		if err := apply(tx); err != nil {
			return err
		}
		return f.fault
	})
}
func TestGuildDungeonRepeatRewardMySQLRollbackAndReplay(t *testing.T) {
	g := clearSettlementMySQLFixture(t)
	s, rt, _, _, mem := repeatRewardBridgeFixture(t, 90)
	ctx := context.Background()
	c, _, _ := mem.Character.Load(ctx, "99")
	skill, _, _ := mem.Skill.Load(ctx, "99")
	inv, _, _ := mem.Inventory.Load(ctx, "99")
	for _, err := range []error{g.Account.Save(ctx, r.AccountRecord{AccountID: c.AccountID}), g.Character.Save(ctx, c), g.Skill.Save(ctx, skill), g.Inventory.Save(ctx, inv)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.resolveCurrentGuildDungeonRewardItems(rt); err != nil {
		t.Fatal(err)
	}
	cmd := dungeon.GuildDungeonKillCommand{Clear: rt.guildDungeonClear, Target: "boss", Total: 1, UpdatedAt: s.gameplayNow()}
	original := g.CharacterSettlement
	fault := errors.New("late SQL failure")
	g.CharacterSettlement = repeatRewardSQLRollback{original, original.(r.GuildDungeonKillUnitOfWork), fault}
	before, _, _ := g.Character.Load(ctx, "99")
	invBefore, _, _ := g.Inventory.Load(ctx, "99")
	owner, _ := dungeon.NewOwner(g)
	if _, err := owner.CommitGuildDungeonKill(ctx, cmd); !errors.Is(err, fault) {
		t.Fatal(err)
	}
	after, _, _ := g.Character.Load(ctx, "99")
	invAfter, _, _ := g.Inventory.Load(ctx, "99")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(invBefore, invAfter) {
		t.Fatal("partial SQL reward")
	}
	g.CharacterSettlement = original
	owner, _ = dungeon.NewOwner(g)
	got, err := owner.CommitGuildDungeonKill(ctx, cmd)
	if err != nil || !got.Reward.Awarded {
		t.Fatal(got, err)
	}
	invBefore, _, _ = g.Inventory.Load(ctx, "99")
	got, err = owner.CommitGuildDungeonKill(ctx, cmd)
	if err != nil || !got.Replayed {
		t.Fatal(got, err)
	}
	invAfter, _, _ = g.Inventory.Load(ctx, "99")
	if !reflect.DeepEqual(invBefore, invAfter) {
		t.Fatal("SQL duplicate reward")
	}
	// Exact scope rejects the global registry, a different run and another owner.
	if err := original.(r.GuildDungeonKillUnitOfWork).WithinGuildDungeonKill(ctx, cmd.Clear.Entry.Owner, cmd.Clear.Entry.CompletionKey(), func(tx r.Group) error {
		for _, scope := range []string{guild.RegistryScope, r.GuildDungeonKillReceiptScope(cmd.Clear.Entry.Owner, "other")} {
			if _, _, err := tx.Settings.Load(ctx, scope); err == nil {
				t.Fatal("unrelated scope exposed")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestGuildDungeonRepeatRewardRealPVFSixMaps(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("matched PVF required")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := guild.LoadDungeonCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	ids := []uint32{8500, 8504, 8507, 8505, 8503, 8508}
	for _, id := range ids {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			d, ok := table.FindDungeon(int64(id))
			if !ok {
				t.Fatal(id)
			}
			percent, err := catalog.RepeatRewardRunPercent(id)
			if err != nil {
				t.Fatal(err)
			}
			for maze := range d.Mazes {
				topology, err := worldmap.BuildDungeonLayout(resolver, int64(id), maze, func(choice worldmap.DungeonMapChoice) (int64, error) { return choice.Candidates[0].ID, nil })
				if err != nil {
					t.Fatal(err)
				}
				targets, err := guildDungeonKillTargets(topology.Rooms())
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("id=%d name=%s maze=%d rooms=%d monsters=%d full_run=%d%%", id, d.Metadata.Name, maze, len(topology.Rooms()), len(targets), percent)
				s, rt, p, _, repos := repeatRewardBridgeFixture(t, 90)
				s.pvfItemCatalog = items
				s.guildDungeonCatalog = catalog
				run, err := worldmap.NewDungeonRun(topology)
				if err != nil {
					t.Fatal(err)
				}
				rt.Session, err = worldmap.NewDungeonSession(run)
				if err != nil {
					t.Fatal(err)
				}
				rt.Dungeon = d
				rt.Request.DungeonID = id
				reg, _ := guild.Load(context.Background(), repos.Settings)
				cycle, _ := reg.DungeonPolicy.Cycle(s.gameplayNow())
				counts := map[uint32]uint32{}
				for _, other := range ids {
					counts[other] = 1000
				}
				reg.Guilds[1].Dungeon = &guild.DungeonProgress{CycleKey: cycle.Key, StartUnix: cycle.StartUnix, EndUnix: cycle.EndUnix, Counts: counts}
				if err := guild.Save(context.Background(), repos.Settings, reg, s.gameplayNow()); err != nil {
					t.Fatal(err)
				}
				if err := s.freezeCurrentGuildDungeonGroup(context.Background(), p, rt, nil); err != nil {
					t.Fatal(err)
				}
				if err := s.resolveCurrentGuildDungeonRewardItems(rt); err != nil {
					t.Fatal(err)
				}
				owner, _ := dungeon.NewOwner(repos)
				for token := range targets {
					cmd := dungeon.GuildDungeonKillCommand{Clear: rt.guildDungeonClear, Target: token, Total: uint32(len(targets)), UpdatedAt: s.gameplayNow()}
					if _, err := owner.CommitGuildDungeonKill(context.Background(), cmd); err != nil {
						t.Fatal(err)
					}
				}
				c, _, _ := repos.Character.Load(context.Background(), "99")
				inv, _, _ := repos.Inventory.Load(context.Background(), "99")
				boxes := int64(0)
				for _, item := range inv.Slots {
					if item.ItemID == 10156320 {
						boxes += item.Count
					}
				}
				if c.Stats[guild.DungeonRepeatRewardProgressStat] != int64((90+percent)%100) || c.Stats[guild.DungeonRepeatRewardFractionStat] != 0 || boxes != 1 {
					t.Fatal(c.Stats, boxes)
				}
			}
		})
	}
}
func TestGuildDungeonRepeatRewardPartyKillSharesAndDeduplicates(t *testing.T) {
	for n := 2; n <= 4; n++ {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s, ps, repos, key := partyMaterialFixture(t, n, 0, false, false, 100)
			ctx := context.Background()
			guildDungeonBridgeCatalog(t, s)
			text, _ := s.pvfItemCatalog.source.ReadText(guild.DungeonCatalogPath)
			catalog, err := guild.ParseDungeonCatalog(text + "\n[repeat clear bonus reward]\n10156320 1\n")
			if err != nil {
				t.Fatal(err)
			}
			s.guildDungeonCatalog = catalog
			reg := guild.NewRegistry()
			c, _, _ := repos.Character.Load(ctx, "99")
			c.Name = "Leader"
			if _, err := reg.Create(c, "KillParty", s.gameplayNow()); err != nil {
				t.Fatal(err)
			}
			followers := []*runtimePartyPreparedDungeonEntry{}
			for i, p := range ps {
				id := fmt.Sprint(p.selectedCharacterID)
				ch, _, _ := repos.Character.Load(ctx, id)
				ch.Name = id
				ch.Stats[guild.DungeonRepeatRewardProgressStat] = int64(i * 10)
				if err := repos.Character.Save(ctx, ch); err != nil {
					t.Fatal(err)
				}
				if err := repos.Quest.Save(ctx, r.QuestRecord{CharacterID: id}); err != nil {
					t.Fatal(err)
				}
				if i > 0 {
					m := reg.Guilds[1].Members["99"]
					m.CharacterID = id
					m.Name = id
					m.Grade = 4
					reg.Guilds[1].Members[id] = m
				}
				rt := p.dungeon.runtime
				rt.Dungeon.ID = 8500
				rt.Request.DungeonID = 8500
				rt.Character = ch
				if i > 0 {
					followers = append(followers, &runtimePartyPreparedDungeonEntry{session: p, runtime: rt})
				}
			}
			if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
				t.Fatal(err)
			}
			if err := s.freezeCurrentGuildDungeonGroup(ctx, ps[0], ps[0].dungeon.runtime, followers); err != nil {
				t.Fatal(err)
			}
			killer := ps[n-1]
			rt := killer.dungeon.runtime
			if _, _, err := s.commitQuestMaterialDeath(killer, rt, key); err != nil {
				t.Fatal(err)
			}
			delta := int64(10 / len(rt.guildDungeonKills.targets))
			for i, p := range ps {
				ch, _, _ := repos.Character.Load(ctx, fmt.Sprint(p.selectedCharacterID))
				if ch.Stats[guild.DungeonRepeatRewardProgressStat] != int64(i*10)+delta {
					t.Fatal("peer missed kill", i, ch.Stats)
				}
			}
			// Another reporter for the same shared object cannot mint a second share.
			if _, _, err := s.commitQuestMaterialDeath(ps[0], ps[0].dungeon.runtime, key); !errors.Is(err, errDungeonMonsterAlreadyDefeated) {
				t.Fatal(err)
			}
			for i, p := range ps {
				ch, _, _ := repos.Character.Load(ctx, fmt.Sprint(p.selectedCharacterID))
				if ch.Stats[guild.DungeonRepeatRewardProgressStat] != int64(i*10)+delta {
					t.Fatal("peer replay gained", i, ch.Stats)
				}
			}
		})
	}
}
func TestGuildDungeonRepeatRewardProjectionRevision(t *testing.T) {
	s, rt, p, conn, _ := repeatKillBridgeFixture(t, 0)
	newer := dungeon.GuildDungeonKillResult{Revision: 12, Reward: guild.DungeonRepeatRewardResult{Enabled: true, Progress: 22, Display: 22}}
	if err := s.sendCurrentGuildDungeonKillProgress(p, rt, newer); err != nil {
		t.Fatal(err)
	}
	assertRepeatPacket(t, conn.write.Bytes(), 22)
	conn.write.Reset()
	older := dungeon.GuildDungeonKillResult{Revision: 11, Reward: guild.DungeonRepeatRewardResult{Enabled: true, Progress: 10, Display: 10}}
	if err := s.sendCurrentGuildDungeonKillProgress(p, rt, older); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 || rt.Character.Stats[guild.DungeonRepeatRewardProgressStat] != 22 {
		t.Fatal("stale message rewound bar")
	}
}
func TestGuildDungeonRepeatRewardMySQLFullBagMail(t *testing.T) {
	g := clearSettlementMySQLFixture(t)
	s, rt, _, _, mem := repeatRewardBridgeFixture(t, 90)
	ctx := context.Background()
	c, _, _ := mem.Character.Load(ctx, "99")
	if err := g.Account.Save(ctx, r.AccountRecord{AccountID: c.AccountID}); err != nil {
		t.Fatal(err)
	}
	if err := g.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := s.resolveCurrentGuildDungeonRewardItems(rt); err != nil {
		t.Fatal(err)
	}
	definition := rt.guildDungeonClear.Items[10156320]
	inv := r.InventoryRecord{CharacterID: "99", Slots: map[string]r.ItemStack{}}
	for slot := int16(3); slot <= 8; slot++ {
		inv.Slots[fmt.Sprintf("0:%d", slot)] = r.ItemStack{ItemID: 222, Count: 1}
	}
	for i := definition.SlotStart; i <= definition.SlotEnd; i++ {
		inv.Slots[fmt.Sprintf("0:%d", i)] = r.ItemStack{ItemID: 222, Count: 1}
	}
	if err := g.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	cmd := dungeon.GuildDungeonKillCommand{Clear: rt.guildDungeonClear, Target: "boss", Total: 1, UpdatedAt: s.gameplayNow()}
	original := g.CharacterSettlement
	fault := errors.New("rollback mail and reward")
	g.CharacterSettlement = repeatRewardSQLRollback{original, original.(r.GuildDungeonKillUnitOfWork), fault}
	before, _, _ := g.Mailbox.Load(ctx, "99")
	owner, _ := dungeon.NewOwner(g)
	if _, err := owner.CommitGuildDungeonKill(ctx, cmd); !errors.Is(err, fault) {
		t.Fatal(err)
	}
	after, _, _ := g.Mailbox.Load(ctx, "99")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("mail survived rollback")
	}
	g.CharacterSettlement = original
	owner, _ = dungeon.NewOwner(g)
	got, err := owner.CommitGuildDungeonKill(ctx, cmd)
	if err != nil || got.MailID == "" || !got.Reward.Awarded {
		t.Fatal(got, err)
	}
	mailbox, _, err := g.Mailbox.Load(ctx, "99")
	if err != nil {
		t.Fatal(err)
	}
	message, found := mailbox.Mails[got.MailID]
	if !found || len(message.Attachments) != 1 || message.Attachments[0].ItemID != 10156320 || message.Attachments[0].Count != 1 {
		t.Fatal(message)
	}
	got, err = owner.CommitGuildDungeonKill(ctx, cmd)
	if err != nil || !got.Replayed {
		t.Fatal(got, err)
	}
	after, _, _ = g.Mailbox.Load(ctx, "99")
	if !reflect.DeepEqual(mailbox, after) {
		t.Fatal("replayed mail")
	}
}

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

func TestGuildDungeonRepeatRewardMySQLRollbackAndReplay(t *testing.T) {
	g := clearSettlementMySQLFixture(t)
	s, rt, p, _, mem := repeatRewardBridgeFixture(t, 90)
	ctx := context.Background()
	c, _, _ := mem.Character.Load(ctx, "99")
	skill, _, _ := mem.Skill.Load(ctx, "99")
	inv, _, _ := mem.Inventory.Load(ctx, "99")
	reg, _, _ := mem.Settings.Load(ctx, guild.RegistryScope)
	for _, err := range []error{g.Account.Save(ctx, r.AccountRecord{AccountID: c.AccountID}), g.Character.Save(ctx, c), g.Skill.Save(ctx, skill), g.Inventory.Save(ctx, inv), g.Settings.Save(ctx, reg)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	s.repositoryProvider = func() (r.Group, bool) { return g, true }
	if err := s.freezeCurrentGuildDungeonRewards(rt); err != nil {
		t.Fatal(err)
	}
	original := g.CharacterSettlement
	fault := errors.New("late guild reward transaction failure")
	g.CharacterSettlement = repeatRewardSQLRollback{original, original.(r.GuildSettlementUnitOfWork), fault}
	before, _, _ := g.Character.Load(ctx, "99")
	invBefore, _, _ := g.Inventory.Load(ctx, "99")
	regBefore, _, _ := g.Settings.Load(ctx, guild.RegistryScope)
	resources := currentDungeonSettlementTestResources(t)
	if _, err := s.commitCurrentDungeonSettlement(ctx, p, rt, resources.Progression, 20); !errors.Is(err, fault) {
		t.Fatal("expected late rollback", err)
	}
	after, _, _ := g.Character.Load(ctx, "99")
	invAfter, _, _ := g.Inventory.Load(ctx, "99")
	regAfter, _, _ := g.Settings.Load(ctx, guild.RegistryScope)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(invBefore, invAfter) || !reflect.DeepEqual(regBefore, regAfter) {
		t.Fatal("MySQL rollback left partial state")
	}
	g.CharacterSettlement = original
	first, err := s.commitCurrentDungeonSettlement(ctx, p, rt, resources.Progression, 20)
	if err != nil || !first.GuildDungeon.RepeatReward.Awarded {
		t.Fatal(first.GuildDungeon, err)
	}
	// Compare persisted snapshots; the SQL timestamp precision differs from the
	// in-memory return value of the first commit.
	committedInventory, _, err := g.Inventory.Load(ctx, "99")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.commitCurrentDungeonSettlement(ctx, p, rt, resources.Progression, 20)
	if err != nil || !replay.Replayed || !reflect.DeepEqual(committedInventory, replay.Inventory) || replay.Character.Stats[guild.DungeonRepeatRewardProgressStat] != 0 {
		t.Fatalf("MySQL replay err=%v replayed=%t progress=%d inventory_equal=%t", err, replay.Replayed, replay.Character.Stats[guild.DungeonRepeatRewardProgressStat], reflect.DeepEqual(committedInventory, replay.Inventory))
	}
	boxes := int64(0)
	for _, item := range replay.Inventory.Slots {
		if item.ItemID == 10156320 {
			boxes += item.Count
		}
	}
	if boxes != 1 {
		t.Fatalf("MySQL reward boxes=%d want 1", boxes)
	}
}

type repeatRewardSQLRollback struct {
	r.CharacterSettlementUnitOfWork
	guild r.GuildSettlementUnitOfWork
	fault error
}

func (f repeatRewardSQLRollback) WithinGuildSettlement(ctx context.Context, owner r.GuildOwner, key string, apply func(r.Group) error) error {
	return f.guild.WithinGuildSettlement(ctx, owner, key, func(tx r.Group) error {
		if err := apply(tx); err != nil {
			return err
		}
		return f.fault
	})
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

func TestGuildDungeonRepeatRewardCommittedPlanAndReentry(t *testing.T) {
	for _, initial := range []int64{0, 90} {
		t.Run(fmt.Sprint(initial), func(t *testing.T) {
			s, rt, p, conn, repos := repeatRewardBridgeFixture(t, initial)
			if err := s.sendCurrentGuildDungeonRepeatRewardOnEntry(p, rt); err != nil {
				t.Fatal(err)
			}
			saveRepeatNativeBody(t, fmt.Sprintf("entry-%d", initial), assertRepeatPacket(t, conn.write.Bytes(), uint32(initial)))
			conn.write.Reset()
			if err := s.produceCurrentDungeonSettlementPlanWithResourcesLocked(context.Background(), p, rt, currentDungeonSettlementTestResources(t)); err != nil {
				t.Fatal(err)
			}
			plan := rt.settlementResultPlan
			if plan == nil {
				t.Fatal("no committed plan")
			}
			if err := s.sendCurrentGuildDungeonSettlement(p, rt, plan); err != nil {
				t.Fatal(err)
			}
			want := uint32(initial + 10)
			saveRepeatNativeBody(t, fmt.Sprintf("clear-%d", want), assertRepeatPacket(t, conn.write.Bytes(), want))
			c, _, _ := repos.Character.Load(context.Background(), "99")
			if c.Stats[guild.DungeonRepeatRewardProgressStat] != int64(want%100) {
				t.Fatal(c.Stats)
			}
			inv, _, _ := repos.Inventory.Load(context.Background(), "99")
			boxes := int64(0)
			for _, item := range inv.Slots {
				if item.ItemID == 10156320 {
					boxes += item.Count
				}
			}
			if boxes != int64(want/100) {
				t.Fatal("box grant mismatch", boxes)
			}
			written := conn.write.Len()
			if err := s.sendCurrentGuildDungeonSettlement(p, rt, plan); err != nil || conn.write.Len() != written {
				t.Fatal("resend", err)
			}
			// New admission consumes a freshly loaded persisted character, including after reconnect.
			rt.Character = c
			if err := s.freezeCurrentGuildDungeonGroup(context.Background(), p, rt, nil); err != nil {
				t.Fatal(err)
			}
			conn.write.Reset()
			if err := s.sendCurrentGuildDungeonRepeatRewardOnEntry(p, rt); err != nil {
				t.Fatal(err)
			}
			saveRepeatNativeBody(t, fmt.Sprintf("reentry-%d", want%100), assertRepeatPacket(t, conn.write.Bytes(), want%100))
		})
	}
}

func TestGuildDungeonRepeatRewardWriteFailureRetainsCommittedPlan(t *testing.T) {
	s, rt, p, _, repos := repeatRewardBridgeFixture(t, 90)
	if err := s.produceCurrentDungeonSettlementPlanWithResourcesLocked(context.Background(), p, rt, currentDungeonSettlementTestResources(t)); err != nil {
		t.Fatal(err)
	}
	plan := rt.settlementResultPlan
	fault := errors.New("742 write failure")
	conn := &failNthDungeonWriteConn{err: fault, failWhen: func(data []byte) bool {
		packet, _ := splitCurrentGameServerUpperPacketAuto(t, data)
		return packet.Header.MsgID == 742
	}}
	p.conn = conn
	if err := s.sendCurrentGuildDungeonSettlement(p, rt, plan); !errors.Is(err, fault) || rt.settlementGuildDungeonSent {
		t.Fatal(err)
	}
	conn.failWhen = nil
	conn.write.Reset()
	if err := s.sendCurrentGuildDungeonSettlement(p, rt, plan); err != nil {
		t.Fatal(err)
	}
	assertRepeatPacket(t, conn.write.Bytes(), 100)
	inv, _, _ := repos.Inventory.Load(context.Background(), "99")
	boxes := int64(0)
	for _, item := range inv.Slots {
		if item.ItemID == 10156320 {
			boxes += item.Count
		}
	}
	if boxes != 1 {
		t.Fatal("retry awarded again", boxes)
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

func TestGuildDungeonRepeatRewardRealPVFSixMaps(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("matched real PVF required")
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
	ids := []uint32{8500, 8504, 8507, 8505, 8503, 8508}
	for _, id := range ids {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			s, rt, p, _, repos := repeatRewardBridgeFixture(t, 90)
			s.pvfItemCatalog = items
			s.guildDungeonCatalog = catalog
			rt.Dungeon.ID = int64(id)
			rt.Request.DungeonID = id
			reg, err := guild.Load(context.Background(), repos.Settings)
			if err != nil {
				t.Fatal(err)
			}
			cycle, err := reg.DungeonPolicy.Cycle(s.gameplayNow())
			if err != nil {
				t.Fatal(err)
			}
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
			result, err := s.commitCurrentDungeonSettlement(context.Background(), p, rt, currentDungeonSettlementTestResources(t).Progression, 20)
			if err != nil {
				t.Fatal(err)
			}
			boxes := int64(0)
			for _, item := range result.Inventory.Slots {
				if item.ItemID == 10156320 {
					boxes += item.Count
				}
			}
			if boxes != 1 || !result.GuildDungeon.RepeatReward.Awarded {
				t.Fatal("real reward mismatch", boxes, result.GuildDungeon)
			}
		})
	}
}

package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/progression"
	pvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

var levelUpTicketArchiveOnce sync.Once
var levelUpTicketArchive *platformpvf.Archive
var levelUpTicketArchiveError error
var levelUpTicketSkillCatalog *dnfskill.Table

func levelUpTicketRealArchive(t *testing.T) *platformpvf.Archive {
	t.Helper()
	levelUpTicketArchiveOnce.Do(func() {
		levelUpTicketArchive, levelUpTicketArchiveError = platformpvf.Open(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
		if levelUpTicketArchiveError == nil {
			levelUpTicketSkillCatalog, levelUpTicketArchiveError = buildSkillCatalogFromSource(context.Background(), levelUpTicketArchive)
		}
	})
	if levelUpTicketArchiveError != nil {
		t.Fatal(levelUpTicketArchiveError)
	}
	return levelUpTicketArchive
}

func TestLevelUpTicketDefinitionRejectsUnverifiedRules(t *testing.T) {
	valid := "[stackable type] `[etc]` 0\n[action type] `[level up ticket]`\n[levelup range] 1\n[minimum level] 1\n[maximum level] 89\n[usable job] `[all]`\n[cool time] 1000\n"
	for _, tail := range []string{"", "[need material] 77 2\n", "[impossible contents] `ban redeemitem`\n[impossible contents] `hardcore mode`\n", "[using limit info] 1 1\n", "[usable game server type] 0\n", "[total usable count] 1\n", "[cooltime group] 1\n", "[action usable condition] `[guildmember]`\n", "[impossible contents] `gift`\n[impossible contents] `unknown restriction`\n"} {
		doc, err := pvf.Parse("stackable/ticket.stk", valid+tail)
		if err != nil {
			t.Fatal(err)
		}
		d, err := currentLevelUpTicketDefinition(77, doc.Path, doc)
		want := tail == "" || strings.Contains(tail, "need material") || strings.Contains(tail, "hardcore mode")
		if (err == nil) != want {
			t.Fatalf("额外规则边界错误：%s / %v", tail, err)
		}
		if strings.Contains(tail, "hardcore mode") && !d.DisallowHardcore {
			t.Fatal("漏读重复段中的硬核限制")
		}
	}
	for _, pair := range [][2]string{{"[etc]", "[waste]"}, {"ticket]`", "ticket]` 85"}, {"range] 1", "range] 0"}, {"level] 89", "level] 90"}, {"time] 1000", "time] -1"}} {
		doc, _ := pvf.Parse("stackable/ticket.stk", strings.Replace(valid, pair[0], pair[1], 1))
		if _, err := currentLevelUpTicketDefinition(77, doc.Path, doc); err == nil {
			t.Fatal("错误配置被接受", pair)
		}
	}
}

func TestLevelUpTicketDefinitionsRealPVF(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("需要当前真实PVF")
	}
	a := levelUpTicketRealArchive(t)
	text, err := a.ReadText("stackable/stackable.lst")
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := pvf.Parse("stackable/stackable.lst", text)
	accepted, rejected, multi, random := 0, 0, 0, 0
	var exclusions []int64
	for _, e := range pvf.ParseList(doc) {
		p := e.Path
		if !strings.HasPrefix(p, "stackable/") {
			p = "stackable/" + p
		}
		x, err := a.ReadText(p)
		if err != nil || !strings.Contains(x, "`[level up ticket]`") {
			continue
		}
		t.Run(strconv.FormatInt(e.ID, 10), func(t *testing.T) {
			doc, err := pvf.Parse(p, x)
			if err != nil {
				t.Fatal(err)
			}
			d, err := currentLevelUpTicketDefinition(e.ID, p, doc)
			if err != nil {
				rejected++
				exclusions = append(exclusions, e.ID)
				t.Log(err)
				return
			}
			accepted++
			if d.MaximumGain > 1 {
				multi++
			}
			if len(d.Outcomes) > 1 {
				random++
			}
		})
	}
	if accepted+rejected != 578 || rejected != 3 || multi != 6 {
		t.Fatalf("真实PVF范围变化：通过%d 拒绝%d 多级%d 随机表%d，拒绝%v", accepted, rejected, multi, random, exclusions)
	}
	t.Logf("实际索引578件：接入%d，额外规则拒绝%v，多级%d，含权重表%d", accepted, exclusions, multi, random)
}

func levelUpTicketBridgeFixture(t *testing.T, id int64, level int) (*Service, *gameSession, repo.Group, *progression.Tables) {
	t.Helper()
	a := levelUpTicketRealArchive(t)
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	t.Setenv("DNFBRIDGE_REAL_PVF_SMOKE", "")
	s, p, g := guildMileageFixture(t)
	t.Setenv("DNFBRIDGE_REAL_PVF_SMOKE", path)
	s.initialEquipmentArchive = a
	s.skillCatalog = levelUpTicketSkillCatalog
	s.initialSkillsByJob = nil
	s.initialSPTable = nil
	if a == nil {
		t.Fatal("缺少配套PVF")
	}
	catalog, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog = catalog
	tables, err := progression.Load(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	c, _, _ := g.Character.Load(context.Background(), "29")
	c.Level = level
	c.Job = "2"
	c.Stats["grow_type"] = 0
	exp, err := tables.ThresholdToNext(level - 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Stats["exp"] = int64(exp) + 3
	sp, err := tables.TotalSkillPoints(level)
	if err != nil {
		t.Fatal(err)
	}
	tp, err := tables.TotalTechniquePoints(level)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	skill, _, err := s.loadOrBackfillCurrentSceneSkillRecord(context.Background(), g, c, 2)
	if err != nil {
		t.Fatal(err)
	}
	skill.CharacterID = "29"
	skill.Points = repo.SkillPointState{SyncedLevel: level, TotalSP: sp, RemainingSP: sp - 10, TotalTP: tp, RemainingTP: tp}
	if err := g.Skill.Save(context.Background(), skill); err != nil {
		t.Fatal(err)
	}
	if err := g.Inventory.Save(context.Background(), repo.InventoryRecord{CharacterID: "29", Slots: map[string]repo.ItemStack{"0:4": {ItemID: id, Count: 1, Extra: map[string]string{"item_kind": "stackable"}}, "0:5": {ItemID: 10002567, Count: 8}}}); err != nil {
		t.Fatal(err)
	}
	// 先加载只读目录，冷加载不计入短写入事务；生产也会在角色选取时加载。
	if _, err := s.loadQuestCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s, p, g, tables
}

func levelUpTicketBridgeSnapshot(t *testing.T, g repo.Group) []any {
	t.Helper()
	ctx := context.Background()
	c, _, e := g.Character.Load(ctx, "29")
	if e != nil {
		t.Fatal(e)
	}
	i, _, e := g.Inventory.Load(ctx, "29")
	if e != nil {
		t.Fatal(e)
	}
	s, _, e := g.Skill.Load(ctx, "29")
	if e != nil {
		t.Fatal(e)
	}
	q, _, e := g.Quest.Load(ctx, "29")
	if e != nil {
		t.Fatal(e)
	}
	return []any{c, i, s, q}
}

func TestLevelUpTicketDispatchAndRefreshRealPVF(t *testing.T) {
	if os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE") == "" {
		t.Skip("需要当前真实PVF")
	}
	for _, row := range []struct {
		id          int64
		level, gain int
	}{{10151463, 50, 1}, {10151466, 50, 10}, {10002583, 50, 1}, {10151463, 89, 1}} {
		t.Run(strconv.FormatInt(row.id, 10)+"_lv"+strconv.Itoa(row.level), func(t *testing.T) {
			s, p, g, tables := levelUpTicketBridgeFixture(t, row.id, row.level)
			before, _, _ := g.Character.Load(context.Background(), "29")
			handled, err := currentGameplayModules.DispatchUpper(s, p, 418, 1, []byte{4, 0, 0})
			if err != nil || !handled {
				t.Fatalf("处理=%v/%v", handled, err)
			}
			c, _, _ := g.Character.Load(context.Background(), "29")
			bag, _, _ := g.Inventory.Load(context.Background(), "29")
			skill, _, _ := g.Skill.Load(context.Background(), "29")
			if c.Level != row.level+row.gain || bag.Slots["0:4"].Count != 0 || bag.Slots["0:5"].Count != 8 || skill.Points.TotalSP-skill.Points.RemainingSP != 10 {
				t.Fatalf("升级入库不正确：角色%+v 点数%+v 背包%+v", c, skill.Points, bag)
			}
			target, _ := tables.ThresholdToNext(c.Level - 1)
			ack, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
			if ack.Header.MsgID != 418 || ack.Header.Classification != 1 || len(ack.Body) != 12 || ack.Body[0] != 1 || binary.LittleEndian.Uint16(ack.Body[1:]) != 4 || ack.Body[3] != 0 || binary.LittleEndian.Uint32(ack.Body[4:]) != target-uint32(before.Stats["exp"]) {
				t.Fatalf("成功正文错误：%+v %x", ack.Header, ack.Body)
			}
			var ops []uint16
			var actorBodies [][]byte
			var stateBody []byte
			for len(rest) > 0 {
				packet, next := splitCurrentGameServerUpperPacketAuto(t, rest)
				rest = next
				ops = append(ops, packet.Header.MsgID)
				if packet.Header.MsgID == 2 {
					actorBodies = append(actorBodies, packet.Body)
				}
				if packet.Header.MsgID == 37 {
					stateBody = packet.Body
				}
			}
			if len(ops) < 8 || !reflect.DeepEqual(ops[:5], []uint16{14, 2, 2, 37, 19}) {
				t.Fatalf("刷新序列错误：%v", ops)
			}
			assertLevelUpTicketActorSnapshots(t, s, p, c, actorBodies)
			if dst := os.Getenv("DNF90_LEVEL_TICKET_FIXTURES"); dst != "" {
				if err := os.MkdirAll(dst, 0755); err != nil {
					t.Fatal(err)
				}
				b, _ := json.MarshalIndent(map[string]any{"item_id": row.id, "previous_level": row.level, "new_level": c.Level, "ack": ack.Body, "ops": ops, "mode0": actorBodies[0], "mode1": actorBodies[1], "state": stateBody}, "", "  ")
				if err := os.WriteFile(filepath.Join(dst, strconv.FormatInt(row.id, 10)+"-"+strconv.Itoa(row.level)+".json"), b, 0644); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestLevelUpTicketGuardianHistoricalExperienceRealPVF(t *testing.T) {
	if os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE") == "" {
		t.Skip("需要当前真实PVF")
	}
	for _, id := range []int64{10004243, 10004024, 10005030} {
		t.Run(strconv.FormatInt(id, 10), func(t *testing.T) {
			s, p, g, tables := levelUpTicketBridgeFixture(t, id, 20)
			ctx := context.Background()
			c, _, _ := g.Character.Load(ctx, "29")
			c.Job = "12"
			c.Stats["grow_type"] = 2
			c.Stats["exp"] = 36574
			if err := g.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			skill, _, _ := g.Skill.Load(ctx, "29")
			// 各职业只加载自己的初始技能，不借用夹具中的男枪技能。
			if err := g.Skill.Save(ctx, repo.SkillRecord{CharacterID: "29", Points: skill.Points}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.loadOrBackfillCurrentSceneSkillRecord(ctx, g, c, 12); err != nil {
				t.Fatal(err)
			}
			guardian, _, _ := g.Skill.Load(ctx, "29")
			guardian.Points = skill.Points
			if err := g.Skill.Save(ctx, guardian); err != nil {
				t.Fatal(err)
			}
			if _, err := currentGameplayModules.DispatchUpper(s, p, 418, 1, []byte{4, 0, 0}); err != nil {
				t.Fatal(err)
			}
			after, _, _ := g.Character.Load(ctx, "29")
			bag, _, _ := g.Inventory.Load(ctx, "29")
			points, _, _ := g.Skill.Load(ctx, "29")
			target, _ := tables.ThresholdToNext(20)
			if after.Level != 21 || after.Stats["exp"] != int64(target) || bag.Slots["0:4"].Count != 0 || points.Points.TotalSP-points.Points.RemainingSP != 10 || after.Stats[repo.LevelUpEventKey(20)] != 0 || after.Stats[repo.LevelUpEventKey(21)] <= 0 {
				t.Fatal("守护者旧存档升级结算错误", after, points.Points, bag)
			}
			ack, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
			if ack.Header.MsgID != 418 || len(ack.Body) != 12 || ack.Body[0] != 1 || binary.LittleEndian.Uint32(ack.Body[4:]) != target-36574 {
				t.Fatal("守护者没有收到正确成功应答", ack)
			}
			var state, skills bool
			var actors [][]byte
			for len(rest) > 0 {
				packet, next := splitCurrentGameServerUpperPacketAuto(t, rest)
				rest = next
				if packet.Header.MsgID == 2 {
					actors = append(actors, packet.Body)
				}
				if packet.Header.MsgID == 37 {
					state = packet.Body[0] == 21 && binary.LittleEndian.Uint32(packet.Body[1:]) == target
				}
				if packet.Header.MsgID == 19 {
					skills = true
				}
			}
			if !state || !skills {
				t.Fatal("缺少等级或技能刷新")
			}
			assertLevelUpTicketActorSnapshots(t, s, p, after, actors)
		})
	}
}

func assertLevelUpTicketActorSnapshots(t *testing.T, s *Service, p *gameSession, c repo.CharacterRecord, bodies [][]byte) {
	t.Helper()
	if len(bodies) != 2 || len(bodies[0]) <= 5+0x47+2 || len(bodies[1]) <= currentMode1ObjectTailOffset || bodies[0][0] != 0 || bodies[1][0] != 1 {
		t.Fatal("缺少完整城镇角色资料与本体属性")
	}
	level, _, valid := currentSceneObjectLevelForLog(bodies[0])
	if !valid || int(level) != c.Level || int(bodies[0][5+0x0a]) != c.Level {
		t.Fatal("mode0的原生raw47和角色资料等级未同步", level, c.Level)
	}
	owner := currentSelectedActorAppearanceOwnerContext(p)
	for _, body := range bodies {
		if body[4] != owner || body[3] != s.currentSceneActorRoute() {
			t.Fatal("角色刷新进入了错误的场景owner", body[:5], owner)
		}
	}
	if binary.LittleEndian.Uint16(bodies[0][5+0x47:]) != p.selectedCharacterID || binary.LittleEndian.Uint16(bodies[1][0x15:]) != p.selectedCharacterID || binary.LittleEndian.Uint32(bodies[1][currentMode1ExperienceOffset:]) != uint32(c.Stats["exp"]) {
		t.Fatal("角色对象身份或本体经验与已提交状态不一致")
	}
}

func TestLevelUpTicketBridgeRejectsWithoutDebitRealPVF(t *testing.T) {
	if os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE") == "" {
		t.Skip("需要当前真实PVF")
	}
	for _, kind := range []string{"wrong_class", "extra", "short", "wrong_list", "negative_slot", "missing_slot", "expired", "locked", "wrong_account", "not_ready", "bad_position", "dungeon", "town_busy", "dungeon_busy", "pvp_room", "pvp_busy", "stale_session"} {
		t.Run(kind, func(t *testing.T) {
			s, p, g, _ := levelUpTicketBridgeFixture(t, 10151463, 50)
			body := []byte{4, 0, 0}
			class := byte(1)
			switch kind {
			case "wrong_class":
				class = 0
			case "extra":
				body = append(body, 0)
			case "short":
				body = body[:2]
			case "wrong_list":
				body[2] = 1
			case "negative_slot":
				body[0] = 255
				body[1] = 255
			case "missing_slot":
				body[0] = 5
			case "expired", "locked":
				bag, _, _ := g.Inventory.Load(context.Background(), "29")
				item := bag.Slots["0:4"]
				if kind == "expired" {
					item.ExpireAt = s.gameplayNow().Add(-time.Second)
				} else {
					item.Extra["equipment_lock_state"] = "1"
				}
				bag.Slots["0:4"] = item
				if err := g.Inventory.Save(context.Background(), bag); err != nil {
					t.Fatal(err)
				}
			case "wrong_account":
				p.accountID = "other"
			case "not_ready":
				p.townSceneReadyCharacterID = 0
			case "bad_position":
				p.townPositionSnapshot.PositionValid = false
			case "dungeon":
				p.dungeon.runtime = &runtimeDungeonState{}
			case "pvp_room":
				p.pvpRoom = &currentPVPRoom{}
			case "pvp_busy":
				s.pvpMu.Lock()
				defer s.pvpMu.Unlock()
			case "town_busy":
				p.townMu.Lock()
				defer p.townMu.Unlock()
			case "dungeon_busy":
				p.dungeon.mu.Lock()
				defer p.dungeon.mu.Unlock()
			case "stale_session":
				s.gameSessions[29] = &gameSession{selectedCharacterID: 29, characterGeneration: 99}
			}
			before := levelUpTicketBridgeSnapshot(t, g)
			handled, err := currentGameplayModules.DispatchUpper(s, p, 418, class, body)
			if err != nil || !handled {
				t.Fatalf("处理=%v/%v", handled, err)
			}
			if !reflect.DeepEqual(before, levelUpTicketBridgeSnapshot(t, g)) {
				t.Fatal("拒绝时仍修改资产")
			}
			if kind == "wrong_class" {
				if p.conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("错误分类不应发送成功或失败正文")
				}
				return
			}
			ack, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
			if ack.Header.MsgID != 418 || !bytes.Equal(ack.Body, []byte{0, 99}) || len(rest) != 0 {
				t.Fatalf("失败正文错误：%x 后续%x", ack.Body, rest)
			}
		})
	}
}

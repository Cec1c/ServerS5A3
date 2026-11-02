package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/progression"
	pvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const privateCapsule012CandidateID int64 = 990029201
const privateCapsule012CandidatePath = "stackable/private_beta012/990029201.stk"

var privateCapsule012ArchiveOnce sync.Once
var privateCapsule012Archive *platformpvf.Archive
var privateCapsule012ArchiveErr error

func privateCapsule012FullArchive(t *testing.T) *platformpvf.Archive {
	t.Helper()
	privateCapsule012ArchiveOnce.Do(func() {
		privateCapsule012Archive, privateCapsule012ArchiveErr = platformpvf.Open(os.Getenv("PRIVATE_CAPSULE012_CANDIDATE_PVF"))
	})
	if privateCapsule012ArchiveErr != nil {
		t.Fatal(privateCapsule012ArchiveErr)
	}
	return privateCapsule012Archive
}
func privateCapsule012CandidateText(t *testing.T) string {
	t.Helper()
	b, e := os.ReadFile(os.Getenv("PRIVATE_CAPSULE012_CANDIDATE_STK"))
	if e != nil {
		t.Fatal(e)
	}
	return strings.ReplaceAll(string(b), "\r", "")
}
func privateCapsule012CandidateFixture(t *testing.T, level int, progress string) (*Service, *gameSession, repo.Group, *progression.Tables) {
	t.Helper()
	ctx := context.Background()
	s, p, g, tables := levelUpTicketBridgeFixture(t, privateCapsule012CandidateID, 30)
	a := privateCapsule012FullArchive(t)
	list, e := a.ReadText("stackable/stackable.lst")
	if e != nil {
		t.Fatal(e)
	}
	doc, e := pvf.Parse("stackable/stackable.lst", list)
	if e != nil {
		t.Fatal(e)
	}
	count := 0
	for _, entry := range pvf.ParseList(doc) {
		if entry.ID == privateCapsule012CandidateID {
			if strings.ReplaceAll(entry.Path, "\\", "/") != strings.TrimPrefix(privateCapsule012CandidatePath, "stackable/") {
				t.Fatal("wrong candidate PVF path")
			}
			count++
		}
	}
	if count != 1 {
		t.Fatal("candidate PVF must contain exactly one capsule ID", count)
	}
	actual, e := a.ReadText(privateCapsule012CandidatePath)
	if e != nil || strings.TrimSpace(strings.ReplaceAll(actual, "\r", "")) != strings.TrimSpace(privateCapsule012CandidateText(t)) {
		t.Fatal("candidate STK export differs from actual PVF", e)
	}
	catalog, e := newPVFDungeonDropCatalog(a)
	if e != nil {
		t.Fatal(e)
	}
	s.pvfItemCatalog = catalog
	s.initialEquipmentArchive = a
	itemDefinition, e := catalog.ResolveItem(uint32(privateCapsule012CandidateID))
	if e != nil || itemDefinition.AttachType != "[account]" {
		t.Fatal("catalog did not prove account binding", itemDefinition.AttachType, e)
	}
	tables, e = progression.Load(ctx, a)
	if e != nil {
		t.Fatal(e)
	}
	c, _, _ := g.Character.Load(ctx, "29")
	c.Level = level
	floor := uint32(0)
	if level > 1 {
		floor, e = tables.ThresholdToNext(level - 1)
		if e != nil {
			t.Fatal(e)
		}
	}
	next := floor + 1
	if level < 90 {
		next, e = tables.ThresholdToNext(level)
		if e != nil {
			t.Fatal(e)
		}
	}
	c.Stats["exp"] = int64(floor)
	switch progress {
	case "middle":
		c.Stats["exp"] += int64((next - floor) / 2)
	case "last":
		c.Stats["exp"] = int64(next - 1)
	case "historical_low":
		c.Stats["exp"] = 0
	}
	c.Stats["bonus_sp"] = 7
	c.Stats["bonus_tp"] = 2
	c.Stats["gold"] = 98765
	sp, e := tables.TotalSkillPoints(level)
	if e != nil {
		t.Fatal(e)
	}
	tp, e := tables.TotalTechniquePoints(level)
	if e != nil {
		t.Fatal(e)
	}
	skill, _, _ := g.Skill.Load(ctx, "29")
	spentSP := 10
	if sp+7 < spentSP {
		spentSP = 3
	}
	skill.Points = repo.SkillPointState{SyncedLevel: level, TotalSP: sp + 7, RemainingSP: sp + 7 - spentSP, TotalTP: tp + 2, RemainingTP: tp + 2 - 1}
	bag, _, _ := g.Inventory.Load(ctx, "29")
	item := bag.Slots["0:4"]
	item.Count = 2
	item.Extra["pvf_path"] = privateCapsule012CandidatePath
	item.Extra["attach_type"] = "[account]"
	bag.Slots["0:4"] = item
	for _, e := range []error{g.Character.Save(ctx, c), g.Skill.Save(ctx, skill), g.Inventory.Save(ctx, bag)} {
		if e != nil {
			t.Fatal(e)
		}
	}
	return s, p, g, tables
}
func TestPrivateCapsule012CandidateDefinition(t *testing.T) {
	if os.Getenv("PRIVATE_CAPSULE012_CANDIDATE_PVF") == "" {
		t.Skip("012 full PVF fixture required")
	}
	text := privateCapsule012CandidateText(t)
	doc, e := pvf.Parse(privateCapsule012CandidatePath, text)
	if e != nil {
		t.Fatal(e)
	}
	def, e := currentLevelUpTicketDefinition(privateCapsule012CandidateID, doc.Path, doc)
	if e != nil || def.TargetLevel != 90 || def.MinimumLevel != 1 || def.MaximumLevel != 89 || def.MaximumGain != 1 || len(def.Outcomes) != 0 {
		t.Fatal(def, e)
	}
	if attach, _ := doc.Text("attach type"); attach != "[account]" {
		t.Fatal("candidate is not account bound")
	}
	for _, nativeRange := range []string{"1", "89"} {
		if strings.Count(text, "[levelup range]\n1") != 1 {
			t.Fatal("native range variant did not match v2 input exactly once")
		}
		variant := strings.Replace(text, "[levelup range]\n1", "[levelup range]\n"+nativeRange, 1)
		parsed, err := pvf.Parse(doc.Path, variant)
		if err != nil {
			t.Fatal(err)
		}
		definition, err := currentLevelUpTicketDefinition(privateCapsule012CandidateID, parsed.Path, parsed)
		if err != nil || definition.TargetLevel != 90 {
			t.Fatal("native range changed private target", nativeRange, definition, err)
		}
	}
	var mutations []map[string]any
	for _, row := range []struct{ name, old, new string }{
		{"zero", "[private level target]\n90", "[private level target]\n0"},
		{"negative", "[private level target]\n90", "[private level target]\n-1"},
		{"above_cap", "[private level target]\n90", "[private level target]\n91"},
		{"not_above_maximum", "[private level target]\n90", "[private level target]\n89"},
		{"fraction", "[private level target]\n90", "[private level target]\n90.0"},
		{"string", "[private level target]\n90", "[private level target]\n`90`"},
		{"two_values", "[private level target]\n90", "[private level target]\n90 90"},
		{"empty", "[private level target]\n90", "[private level target]"},
		{"duplicate", "[private level target]\n90", "[private level target]\n90\n[private level target]\n90"},
		{"duplicate_case", "[private level target]\n90", "[private level target]\n90\n[PRIVATE LEVEL TARGET]\n90"},
		{"duplicate_max", "[maximum level]\n89", "[maximum level]\n89\n[maximum level]\n89"},
		{"duplicate_max_case", "[maximum level]\n89", "[maximum level]\n89\n[MAXIMUM LEVEL]\n89"},
		{"duplicate_action", "[action type]\n`[level up ticket]`", "[action type]\n`[level up ticket]`\n[action type]\n`[level up ticket]`"},
		{"free", "[attach type]\n`[account]`", "[attach type]\n`[free]`"},
		{"trade", "[attach type]\n`[account]`", "[attach type]\n`[trade]`"},
		{"minimum_zero", "[minimum level]\n1", "[minimum level]\n0"},
		{"gain_zero", "[levelup range]\n1", "[levelup range]\n0"},
		{"random_table", "[private level target]\n90", "[private level target]\n90\n[booster info]\n[etc]\n1 1 1 10000\n[/etc]\n[/booster info]"},
	} {
		t.Run(row.name, func(t *testing.T) {
			if strings.Count(text, row.old) != 1 {
				t.Fatal("invalid test mutation")
			}
			mutated := strings.Replace(text, row.old, row.new, 1)
			if mutated == text {
				t.Fatal("negative case did not mutate text")
			}
			bad, e := pvf.Parse(doc.Path, mutated)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = currentLevelUpTicketDefinition(privateCapsule012CandidateID, bad.Path, bad); e == nil {
				t.Fatal("invalid target accepted")
			}
			var changedSections []map[string]any
			for _, section := range bad.Sections {
				if section.Name == "private level target" || section.Name == "PRIVATE LEVEL TARGET" || section.Name == "levelup range" || section.Name == "minimum level" || section.Name == "maximum level" || section.Name == "MAXIMUM LEVEL" || section.Name == "attach type" || section.Name == "action type" || section.Name == "booster info" {
					changedSections = append(changedSections, map[string]any{"name": section.Name, "tokens": bad.Tokens[section.Start:section.End]})
				}
			}
			mutations = append(mutations, map[string]any{"case": row.name, "matches_before": 1, "text_changed": mutated != text, "mutated_text": mutated, "parser_rejected": true, "error": e.Error(), "sections_after": changedSections})
		})
	}
	raw, e := json.MarshalIndent(map[string]any{"passed": !t.Failed(), "mutations": mutations}, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(os.Getenv("PRIVATE_CAPSULE012_MUTATIONS_REPORT"), append(raw, '\n'), 0644); e != nil {
		t.Fatal(e)
	}
}
func TestPrivateCapsule012CandidateRealPVFDispatch(t *testing.T) {
	if os.Getenv("PRIVATE_CAPSULE012_CANDIDATE_PVF") == "" {
		t.Skip("012 full PVF fixture required")
	}
	if os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE") == "" {
		t.Fatal("real PVF is required")
	}
	ctx := context.Background()
	var results []map[string]any
	for _, level := range []int{1, 30, 89} {
		for _, progress := range []string{"floor", "middle", "last", "historical_low"} {
			t.Run(fmt.Sprintf("lv%d_%s", level, progress), func(t *testing.T) {
				s, p, g, tables := privateCapsule012CandidateFixture(t, level, progress)
				before, _, _ := g.Character.Load(ctx, "29")
				beforeSkill, _, _ := g.Skill.Load(ctx, "29")
				beforeBag, _, _ := g.Inventory.Load(ctx, "29")
				handled, e := currentGameplayModules.DispatchUpper(s, p, 418, 1, []byte{4, 0, 0})
				if e != nil || !handled {
					t.Fatal(handled, e)
				}
				c, _, _ := g.Character.Load(ctx, "29")
				skill, _, _ := g.Skill.Load(ctx, "29")
				bag, _, _ := g.Inventory.Load(ctx, "29")
				target, e := tables.ThresholdToNext(89)
				if e != nil {
					t.Fatal(e)
				}
				sp90, _ := tables.TotalSkillPoints(90)
				spBefore, _ := tables.TotalSkillPoints(level)
				tp90, _ := tables.TotalTechniquePoints(90)
				tpBefore, _ := tables.TotalTechniquePoints(level)
				if c.Level != 90 || c.Stats["exp"] != int64(target) || bag.Slots["0:4"].Count != 1 || skill.Points.SyncedLevel != 90 || skill.Points.TotalSP != beforeSkill.Points.TotalSP+sp90-spBefore || skill.Points.RemainingSP != beforeSkill.Points.RemainingSP+sp90-spBefore || skill.Points.TotalTP != beforeSkill.Points.TotalTP+tp90-tpBefore || skill.Points.RemainingTP != beforeSkill.Points.RemainingTP+tp90-tpBefore {
					t.Fatal("level/EXP/SP/TP/debit mismatch", c, skill.Points, bag)
				}
				if c.Job != before.Job || c.Stats["grow_type"] != before.Stats["grow_type"] || c.Stats["bonus_sp"] != 7 || c.Stats["bonus_tp"] != 2 || c.Stats["gold"] != 98765 || !reflect.DeepEqual(beforeSkill.Skills, skill.Skills) || bag.Slots["0:5"].Count != beforeBag.Slots["0:5"].Count {
					t.Fatal("unrelated assets changed")
				}
				for crossed := level + 1; crossed <= 90; crossed++ {
					if c.Stats[repo.LevelUpEventKey(crossed)] <= 0 {
						t.Fatal("missing crossed-level event", crossed)
					}
				}
				ack, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
				if ack.Header.MsgID != 418 || ack.Header.Classification != 1 || len(ack.Body) != 12 || ack.Body[0] != 1 || binary.LittleEndian.Uint32(ack.Body[4:]) != target-uint32(before.Stats["exp"]) || int(binary.LittleEndian.Uint16(ack.Body[8:])) != sp90-spBefore || int(binary.LittleEndian.Uint16(ack.Body[10:])) != tp90-tpBefore {
					t.Fatal("native op418 acknowledgement mismatch", ack.Body)
				}
				var ops []uint16
				var actors [][]byte
				var state []byte
				var buffs [][]byte
				for len(rest) > 0 {
					packet, next := splitCurrentGameServerUpperPacketAuto(t, rest)
					rest = next
					ops = append(ops, packet.Header.MsgID)
					if packet.Header.MsgID == 2 {
						actors = append(actors, packet.Body)
					}
					if packet.Header.MsgID == 1368 {
						if packet.Header.Classification != 0 {
							t.Fatal("Buff snapshot must be a notification")
						}
						buffs = append(buffs, packet.Body)
					}
					if packet.Header.MsgID == 37 {
						state = packet.Body
					}
				}
				if len(ops) < 6 || !reflect.DeepEqual(ops[:6], []uint16{14, 2, 2, 1368, 37, 19}) {
					t.Fatal("native refresh sequence mismatch", ops)
				}
				assertLevelUpTicketEmptyBuffSnapshot(t, buffs)
				assertLevelUpTicketActorSnapshots(t, s, p, c, actors)
				if len(state) < 17 || state[0] != 90 || binary.LittleEndian.Uint32(state[1:]) != target || int(binary.LittleEndian.Uint16(state[9:])) != skill.Points.RemainingSP || int(binary.LittleEndian.Uint16(state[11:])) != skill.Points.RemainingSP || int(binary.LittleEndian.Uint16(state[13:])) != skill.Points.RemainingTP || int(binary.LittleEndian.Uint16(state[15:])) != skill.Points.RemainingTP {
					t.Fatal("op37 EXP/level mismatch", state)
				}
				once := levelUpTicketBridgeSnapshot(t, g)
				p.conn.(*bufferConn).write.Reset()
				if _, e = currentGameplayModules.DispatchUpper(s, p, 418, 1, []byte{4, 0, 0}); e != nil {
					t.Fatal(e)
				}
				if !reflect.DeepEqual(once, levelUpTicketBridgeSnapshot(t, g)) {
					t.Fatal("replay at level90 changed assets")
				}
				rejected, tail := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
				if !reflect.DeepEqual(rejected.Body, []byte{0, 99}) || len(tail) != 0 {
					t.Fatal("replay did not reject")
				}
				results = append(results, map[string]any{"level_before": level, "exp_position": progress, "level_after": 90, "exp_after": target, "remaining": 1, "sp_gain": sp90 - spBefore, "tp_gain": tp90 - tpBefore, "spent_sp_preserved": true, "spent_tp_preserved": true, "native_mode0_mode1_op37_synchronized": true, "repeat_rejected_without_debit": true, "ops": ops})
			})
		}
	}
	for _, level := range []int{90, 91, 100} {
		t.Run(fmt.Sprintf("reject_lv%d", level), func(t *testing.T) {
			s, p, g, _ := privateCapsule012CandidateFixture(t, level, "floor")
			before := levelUpTicketBridgeSnapshot(t, g)
			if _, e := currentGameplayModules.DispatchUpper(s, p, 418, 1, []byte{4, 0, 0}); e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(before, levelUpTicketBridgeSnapshot(t, g)) {
				t.Fatal("90+ rejection changed assets")
			}
			ack, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
			if !reflect.DeepEqual(ack.Body, []byte{0, 99}) || len(rest) != 0 {
				t.Fatal("90+ failure response mismatch")
			}
			results = append(results, map[string]any{"level_before": level, "level_after": level, "remaining": 2, "rejected_without_any_asset_change": true})
		})
	}
	raw, e := json.MarshalIndent(map[string]any{"passed": !t.Failed(), "item_id": privateCapsule012CandidateID, "private_field": "private level target", "target": 90, "pvf": "actual full PVF012 candidate archive; no PVF source overlay", "results": results}, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(os.Getenv("PRIVATE_CAPSULE012_CANDIDATE_REPORT"), append(raw, '\n'), 0644); e != nil {
		t.Fatal(e)
	}
}
func TestPrivateCapsule012CandidateBridgeRestrictions(t *testing.T) {
	if os.Getenv("PRIVATE_CAPSULE012_CANDIDATE_PVF") == "" {
		t.Skip("012 full PVF fixture required")
	}
	ctx := context.Background()
	for _, kind := range []string{"expired", "locked", "wrong_account", "not_ready", "dungeon", "pvp", "cooldown", "stale_session", "bad_class", "extra_body", "wrong_list"} {
		t.Run(kind, func(t *testing.T) {
			s, p, g, _ := privateCapsule012CandidateFixture(t, 30, "middle")
			body := []byte{4, 0, 0}
			class := byte(1)
			switch kind {
			case "expired", "locked":
				bag, _, _ := g.Inventory.Load(ctx, "29")
				v := bag.Slots["0:4"]
				if kind == "expired" {
					v.ExpireAt = s.gameplayNow().Add(-time.Second)
				} else {
					v.Extra["equipment_lock_state"] = "1"
				}
				bag.Slots["0:4"] = v
				if e := g.Inventory.Save(ctx, bag); e != nil {
					t.Fatal(e)
				}
			case "wrong_account":
				p.accountID = "wrong"
			case "not_ready":
				p.townSceneReadyCharacterID = 0
			case "dungeon":
				p.dungeon.runtime = &runtimeDungeonState{}
			case "pvp":
				p.pvpRoom = &currentPVPRoom{}
			case "cooldown":
				c, _, _ := g.Character.Load(ctx, "29")
				c.Stats["level_up_ticket_next_use_ms_990029201"] = s.gameplayNow().Add(time.Hour).UnixMilli()
				if e := g.Character.Save(ctx, c); e != nil {
					t.Fatal(e)
				}
			case "stale_session":
				s.gameSessions[29] = &gameSession{selectedCharacterID: 29, characterGeneration: 99}
			case "bad_class":
				class = 0
			case "extra_body":
				body = append(body, 90)
			case "wrong_list":
				body[2] = 1
			}
			before := levelUpTicketBridgeSnapshot(t, g)
			if _, e := currentGameplayModules.DispatchUpper(s, p, 418, class, body); e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(before, levelUpTicketBridgeSnapshot(t, g)) {
				t.Fatal("restriction changed assets")
			}
			if kind == "bad_class" {
				if p.conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("bad class wrote response")
				}
			} else {
				ack, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
				if !reflect.DeepEqual(ack.Body, []byte{0, 99}) || len(rest) != 0 {
					t.Fatal("restriction response mismatch")
				}
			}
		})
	}
}

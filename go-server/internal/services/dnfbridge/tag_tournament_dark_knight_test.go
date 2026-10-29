package dnfbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	darkcombo "longheng.io/server/internal/modules/dnf/darkknightcombo"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/skillcmd"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func tagDarkKnightSource() townMoveTestSource {
	return townMoveTestSource{
		"skill/skilllist.lst":                 "9 `demonicswordman/skilllist.lst`\n",
		"skill/demonicswordman/skilllist.lst": "10 `a.skl`\n20 `b.skl`\n30 `c.skl`\n118 `comboset0.skl`\n119 `comboset1.skl`\n",
		"skill/demonicswordman/a.skl":         "[name] `甲`\n[type] `active`",
		"skill/demonicswordman/b.skl":         "[name] `乙`\n[type] `active`",
		"skill/demonicswordman/c.skl":         "[name] `丙`\n[type] `active`",
		"skill/demonicswordman/comboset0.skl": "[name] `连招甲`\n[combo set]\n10 20\n[/combo set]",
		"skill/demonicswordman/comboset1.skl": "[name] `连招乙`\n[combo set]\n30\n[/combo set]",
	}
}

func tagDarkKnightFixture(t *testing.T, both bool) (*Service, *gameSession, repo.Group) {
	t.Helper()
	s, p, g := tagPlayFixture(t)
	var err error
	s.skillCatalog, err = buildSkillCatalogFromSource(context.Background(), tagDarkKnightSource())
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"29", "60"} {
		if i == 0 && !both {
			continue
		}
		ch, _, _ := g.Character.Load(context.Background(), id)
		ch.Job = "9"
		if err = g.Character.Save(context.Background(), ch); err != nil {
			t.Fatal(err)
		}
		r := repo.SkillRecord{CharacterID: id,
			Skills:  map[int64]repo.SkillState{10: {Enabled: true, Level: 2}, 20: {Enabled: true, Level: 3}, 118: {Enabled: true, Level: 1}, 119: {Enabled: true, Level: 1}},
			Layouts: map[int]repo.SkillLayout{i: {0: 10, 198: 20, 6: 118, 7: 119}},
			Trees:   repo.SkillTreeState{Active: byte(i), Expanded: i == 1, Other: repo.SkillProfile{Skills: map[int64]repo.SkillState{30: {Enabled: true, Level: 99}}}},
		}
		if i == 0 {
			r.Layouts[0] = repo.SkillLayout{0: 118, 1: 119, 198: 20, 199: 10}
		}
		if err = g.Skill.Save(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		pages := []darkcombo.Page{{Index: byte(i), Roots: []darkcombo.Root{{SkillID: 118, Children: []uint16{20, 10}}, {SkillID: 119}}}}
		if i == 0 {
			pages[0].Roots[0].Children = []uint16{10, 20}
		}
		if i == 1 {
			pages = append(pages, darkcombo.Page{Index: 0, Roots: []darkcombo.Root{{SkillID: 118, Children: []uint16{30}}}})
		}
		if err = darkcombo.SavePages(context.Background(), g.Settings, id, pages, time.Unix(1, 0)); err != nil {
			t.Fatal(err)
		}
		if err = g.Settings.Save(context.Background(), repo.SettingsRecord{Scope: skillcmd.SkillCommandScope(id), Values: map[string]string{"command_118": "0102", "command_10": "0304"}}); err != nil {
			t.Fatal(err)
		}
	}
	return s, p, g
}

func TestTagDarkKnightProductionBothRolesCurrentPageAndFinalCreation(t *testing.T) {
	for _, both := range []bool{false, true} {
		t.Run(map[bool]string{false: "仅备用黑武", true: "双黑武"}[both], func(t *testing.T) {
			s, p, g := tagDarkKnightFixture(t, both)
			ctx := context.Background()
			before, _, _ := g.Skill.Load(ctx, "60")
			saved, _, _ := g.Settings.Load(ctx, darkcombo.SettingsScope("60"))
			if err := s.handleCurrentTagTournamentEnter(p, []byte{1}); err != nil {
				t.Fatal(err)
			}
			if p.tagTournamentBattle == nil {
				t.Fatal("生产1458未接纳黑武")
			}
			prepared := p.tagTournamentBattle.prepared
			want := []byte{1, 1, 0, 1, 1, 2, 118, 0, 2, 20, 0, 10, 0, 119, 0, 0}
			if both {
				want = append([]byte{2, 0, 0, 1, 0, 2, 118, 0, 2, 10, 0, 20, 0, 119, 0, 0}, want[1:]...)
			}
			if !bytes.Equal(prepared.skillState.darkKnight, want) {
				t.Fatal("连招队员/页/独立正文错误", prepared.skillState.darkKnight, want)
			}
			wantCommands := []byte{2, 0, 0, 1, 1, 7, 10, 0, 2, 2, 3, 4}
			if both {
				wantCommands = []byte{2, 0, 1, 7, 10, 0, 2, 2, 3, 4, 1, 1, 7, 10, 0, 2, 2, 3, 4}
			}
			if !bytes.Equal(prepared.skillState.commands, wantCommands) {
				t.Fatal("连招根混入普通指令或普通技能槽错位", prepared.skillState.commands)
			}
			var order []uint16
			for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
				packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
				if packet.Header.MsgID >= 1389 && packet.Header.MsgID <= 1393 {
					order = append(order, packet.Header.MsgID)
				}
				rest = tail
			}
			if !reflect.DeepEqual(order, []uint16{1389, 1390, 1391, 1392, 1393}) {
				t.Fatal("原生初始化顺序错误", order)
			}
			row := prepared.characters[1]
			for _, skill := range row.skills {
				if skill.id == 118 && skill.slot != 0 || skill.id == 119 && skill.slot != 1 || skill.id == 10 && skill.slot != 7 || skill.id == 20 && skill.slot != 6 {
					t.Fatal("真实技能和固定连招槽错位", skill)
				}
			}
			after, _, _ := g.Skill.Load(ctx, "60")
			stored, _, _ := g.Settings.Load(ctx, darkcombo.SettingsScope("60"))
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(saved, stored) || p.selectedCharacterID != 29 || p.characterGeneration != 1 {
				t.Fatal("投影改写了原技能、连招或身份")
			}
			if target := os.Getenv("DNFBRIDGE_TAG_DARK_KNIGHT_FIXTURES"); target != "" && both {
				raw, _ := json.Marshal(map[string]any{"body": want, "selectors": []byte{0, 1}, "pages": []byte{0, 1}})
				if err := os.WriteFile(target, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			p.conn.(*bufferConn).write.Reset()
			if err := s.handleDungeonSelectUpper(p, encodeCurrentDungeonSelectRequest(prepared.runtime.Request)); err != nil {
				t.Fatal(err)
			}
			seen := 0
			for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
				packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
				if packet.Header.MsgID == 1392 {
					seen++
					if !bytes.Equal(packet.Body, want) {
						t.Fatal("进图连招变了")
					}
				}
				rest = tail
			}
			if seen != 0 {
				t.Fatal("最终本体落位重置了已创建的连招", seen)
			}
			tagPlayMoveNext(t, s, p)
			p.conn.(*bufferConn).write.Reset()
			if err := s.handleCurrentTagTournamentTag(p, []byte{1}); err != nil {
				t.Fatal(err)
			}
			if p.tagTournamentBattle.run.Snapshot().Active != 1 {
				t.Fatal("连招测试未实际切至备用角色")
			}
			for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
				packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
				if packet.Header.MsgID == 1392 {
					t.Fatal("切人重置了连招")
				}
				rest = tail
			}
		})
	}
}

func TestTagDarkKnightReadOnlyDefaultsAndStoredEmptyColumns(t *testing.T) {
	s, _, g := tagDarkKnightFixture(t, false)
	record, _, _ := g.Skill.Load(context.Background(), "60")
	before := repo.CloneSkill(record)
	for _, empty := range []bool{false, true} {
		var saved *darkcombo.Page
		if empty {
			saved = &darkcombo.Page{Index: 1}
		}
		_, body, _, err := projectCurrentTagTournamentDarkKnight(record, s.skillCatalog, saved, tagDarkKnightSource())
		if err != nil {
			t.Fatal(err)
		}
		page, err := darkcombo.ParsePageBlock(body[2:])
		if err != nil || page.Index != 1 || len(page.Roots) != 2 {
			t.Fatal(page, err)
		}
		want := []uint16{10, 20}
		if empty {
			want = nil
		}
		if !reflect.DeepEqual(page.Roots[0].Children, want) && !(empty && len(page.Roots[0].Children) == 0) {
			t.Fatal("空保存被默认连招覆盖或默认引用了另一页", page)
		}
		if len(page.Roots[1].Children) != 0 || !reflect.DeepEqual(record, before) {
			t.Fatal("默认连招引用另一页或改写源")
		}
	}
}

func TestTagDarkKnightInvalidOrChangedStorageRejectsBeforePayment(t *testing.T) {
	for _, scenario := range []string{"格式损坏", "伪根技能", "根作子技能", "重复根", "读取中变化", "地图准备后变化", "主角色槽位未同步"} {
		t.Run(scenario, func(t *testing.T) {
			s, p, g := tagDarkKnightFixture(t, scenario == "主角色槽位未同步")
			ctx := context.Background()
			scope := darkcombo.SettingsScope("60")
			saved, _, _ := g.Settings.Load(ctx, scope)
			before, _, _ := g.Character.Load(ctx, "29")
			switch scenario {
			case "主角色槽位未同步":
				r, _, _ := g.Skill.Load(ctx, "29")
				r.Layouts[0] = repo.SkillLayout{0: 10, 198: 20, 6: 118, 7: 119}
				if err := g.Skill.Save(ctx, r); err != nil {
					t.Fatal(err)
				}
			case "格式损坏":
				saved.Values["page_1"] = "!!"
				if err := g.Settings.Save(ctx, saved); err != nil {
					t.Fatal(err)
				}
			case "读取中变化", "地图准备后变化":
				threshold := 1
				if scenario == "地图准备后变化" {
					threshold = 4
				}
				g.Settings = &tagDarkKnightChangingRepository{SettingsRepository: g.Settings, scope: scope, threshold: threshold}
				s.repositoryProvider = func() (repo.Group, bool) { return g, true }
			default:
				page := darkcombo.Page{Index: 1, Roots: []darkcombo.Root{{SkillID: 118, Children: []uint16{10}}}}
				if scenario == "伪根技能" {
					page.Roots[0].SkillID = 20
				}
				if scenario == "根作子技能" {
					page.Roots[0].Children = []uint16{119}
				}
				if scenario == "重复根" {
					page.Roots = append(page.Roots, page.Roots[0])
				}
				if err := darkcombo.SavePages(ctx, g.Settings, "60", []darkcombo.Page{page}, time.Unix(2, 0)); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.handleCurrentTagTournamentEnter(p, []byte{1}); err != nil {
				t.Fatal(err)
			}
			after, _, _ := g.Character.Load(ctx, "29")
			if p.tagTournamentBattle != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("无效或变化连招仍扣费/创建")
			}
			for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
				packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
				if packet.Header.MsgID == 1389 || packet.Header.MsgID == 1392 {
					t.Fatal("失败仍创建角色")
				}
				rest = tail
			}
		})
	}
}

type tagDarkKnightChangingRepository struct {
	repo.SettingsRepository
	scope            string
	calls, threshold int
}

func (r *tagDarkKnightChangingRepository) Load(ctx context.Context, scope string) (repo.SettingsRecord, bool, error) {
	value, found, err := r.SettingsRepository.Load(ctx, scope)
	if scope == r.scope {
		r.calls++
		if r.calls > r.threshold {
			value.UpdatedAt = value.UpdatedAt.Add(time.Second)
		}
	}
	return value, found, err
}

func TestRealPVFTagDarkKnightDefaultsUseActualLearnedRoots(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("需指定真实PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := buildSkillCatalogFromSource(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	// 由现用PVF初始表提取已有根和子技能，不给玩家角色添加技能。
	entries, err := parseInitialCharacterSkillsWithCatalog(archive, 9, catalog)
	if err != nil {
		t.Fatal(err)
	}
	r := repo.SkillRecord{CharacterID: "隔离黑武", Skills: map[int64]repo.SkillState{}}
	for _, entry := range entries {
		r.Skills[entry.SkillID] = repo.SkillState{Enabled: true, Level: entry.Level}
	}
	_, body, roots, err := projectCurrentTagTournamentDarkKnight(r, catalog, nil, archive)
	if err != nil || len(roots) != 6 || len(body) < 4 {
		t.Fatal("真实PVF连招根/默认页缺失", len(roots), err)
	}
	s, p, g := tagPlayFixture(t)
	s.skillCatalog, s.initialEquipmentArchive = catalog, archive
	ctx := context.Background()
	ch, _, _ := g.Character.Load(ctx, "60")
	ch.Job = "9"
	if err = g.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	r.CharacterID = "60"
	if err = g.Skill.Save(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err = s.handleCurrentTagTournamentEnter(p, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if p.tagTournamentBattle == nil || len(p.tagTournamentBattle.prepared.skillState.darkKnight) == 0 {
		t.Fatal("无存档黑武未走生产只读默认页")
	}
	if _, found, err := g.Settings.Load(ctx, darkcombo.SettingsScope("60")); found || err != nil {
		t.Fatal("预检写入了缺省连招存档", found, err)
	}
	after, _, _ := g.Skill.Load(ctx, "60")
	if !reflect.DeepEqual(r, after) {
		t.Fatal("真实PVF准备改写了原技能")
	}
}

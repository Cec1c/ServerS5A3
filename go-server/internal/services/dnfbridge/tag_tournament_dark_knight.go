package dnfbridge

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	darkcombo "longheng.io/server/internal/modules/dnf/darkknightcombo"
	repo "longheng.io/server/internal/modules/dnf/repository"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
)

const currentTagTournamentDarkKnightNotification uint16 = 1392

type currentTagTournamentDarkKnightProjection struct {
	source repo.SettingsRecord
	found  bool
	body   []byte
	roots  map[uint16]bool
}

// 145CF10逐队员读取selector及普通连招通知正文，临时选择页、清理该页
// 连招、写入各根技能的子技能，再还原原页。1389只创建当前已学技能页，
// 因此这里只投影当前页；不能向尚未创建的另一页写入连招。
func (s *Service) prepareCurrentTagTournamentDarkKnight(ctx context.Context, g repo.Group, row *currentTagTournamentCharacterProjection) error {
	if row.job != darkcombo.Job {
		return nil
	}
	if g.Settings == nil {
		return fmt.Errorf("帝国竞技场缺少黑暗武士连招仓储")
	}
	raw, found, err := g.Settings.Load(ctx, darkcombo.SettingsScope(row.character.CharacterID))
	if err != nil {
		return err
	}
	if found && raw.Scope != darkcombo.SettingsScope(row.character.CharacterID) {
		return fmt.Errorf("帝国竞技场连招存档不属于该队员")
	}
	var pages []darkcombo.Page
	if found {
		pages, _, err = darkcombo.DecodeSettingsRecord(raw)
		if err != nil {
			return err
		}
	}
	s.initialSkillsMu.Lock()
	catalog := s.skillCatalog
	s.initialSkillsMu.Unlock()
	if catalog == nil {
		return fmt.Errorf("帝国竞技场黑暗武士技能目录尚未就绪")
	}
	var active *darkcombo.Page
	for i := range pages {
		if pages[i].Index == row.skillTree {
			active = &pages[i]
			break
		}
	}
	var source darkcombo.TextSource
	if active == nil {
		// 仅在无当前页存档时按真实PVF推导初值。不能调用普通城镇的懒
		// 初始化保存入口，否则一次未付费预检会改写玩家连招或另一页。
		s.initialEquipmentMu.Lock()
		archive, e := s.initialEquipmentArchiveLocked()
		s.initialEquipmentMu.Unlock()
		if e != nil {
			return e
		}
		source = archive
	}
	skills, body, roots, err := projectCurrentTagTournamentDarkKnight(row.skillRecord, catalog, active, source)
	if err != nil {
		return err
	}
	row.skills = skills
	row.darkKnight = &currentTagTournamentDarkKnightProjection{source: repo.CloneSettings(raw), found: found, body: body, roots: roots}
	return nil
}

func projectCurrentTagTournamentDarkKnight(record repo.SkillRecord, catalog *dnfskill.Table, saved *darkcombo.Page, source darkcombo.TextSource) ([]currentTagTournamentSkillWire, []byte, map[uint16]bool, error) {
	tree := repo.ActiveSkillTree(record)
	layout, _, err := darkcombo.NormalizeFixedComboRootSlots(catalog, record.Skills, record.Layouts[tree])
	if err != nil {
		return nil, nil, nil, err
	}
	projection := repo.CloneSkill(record)
	if projection.Layouts == nil {
		projection.Layouts = make(map[int]repo.SkillLayout)
	}
	projection.Layouts[tree] = layout
	skills, _, err := currentTagTournamentCharacterSkills(projection)
	if err != nil {
		return nil, nil, nil, err
	}
	page := darkcombo.Page{Index: byte(tree)}
	roots := make(map[uint16]bool)
	for slot := 0; slot < 6; slot++ {
		id, exists := layout[slot]
		definition, known := catalog.Find(darkcombo.Job, id)
		path := "/" + strings.ReplaceAll(definition.Path, `\`, "/")
		if exists && known && strings.HasSuffix(strings.ToLower(path), fmt.Sprintf("/comboset%d.skl", slot)) {
			page.Roots = append(page.Roots, darkcombo.Root{SkillID: id})
			roots[id] = true
		}
	}
	if len(roots) == 0 {
		return nil, nil, nil, fmt.Errorf("帝国竞技场黑暗武士没有已学的真实连招根技能")
	}
	if saved == nil {
		// 默认页只读取本次已经创建的当前技能，另一页的旧技能与PVF缺口
		// 不应影响当前页；整个过程不修改原SkillRecord。
		defaults := repo.CloneSkill(projection)
		defaults.Trees = repo.SkillTreeState{}
		defaults.Layouts = map[int]repo.SkillLayout{0: layout}
		pages, e := darkcombo.BuildDefaultPages(source, catalog, defaults)
		if e != nil || len(pages) != 1 {
			return nil, nil, nil, fmt.Errorf("帝国竞技场连招默认页缺失: %v", e)
		}
		candidate := pages[0]
		candidate.Index = byte(tree)
		saved = &candidate
	}
	if saved.Index != byte(tree) {
		return nil, nil, nil, fmt.Errorf("帝国竞技场连招技能页不符")
	}
	if _, err := darkcombo.NormalizePage(*saved); err != nil {
		return nil, nil, nil, err
	}
	filtered := darkcombo.FilterLearnedPages(record, []darkcombo.Page{*saved})
	if len(filtered) != 1 {
		return nil, nil, nil, fmt.Errorf("帝国竞技场当前连招页未解锁")
	}
	seen := make(map[uint16]bool)
	for _, root := range filtered[0].Roots {
		if !roots[root.SkillID] || seen[root.SkillID] {
			return nil, nil, nil, fmt.Errorf("帝国竞技场连招根技能无效或重复")
		}
		seen[root.SkillID] = true
		for _, child := range root.Children {
			if _, known := catalog.Find(darkcombo.Job, child); !known || roots[child] || child == 0 || child >= 512 {
				return nil, nil, nil, fmt.Errorf("帝国竞技场连招子技能无效")
			}
		}
		for i := range page.Roots {
			if page.Roots[i].SkillID == root.SkillID {
				page.Roots[i].Children = append([]uint16(nil), root.Children...)
			}
		}
	}
	body, err := darkcombo.EncodeNotificationBody([]darkcombo.Page{page})
	return skills, body, roots, err
}

func currentTagTournamentDarkKnightSourceCurrent(ctx context.Context, g repo.Group, row currentTagTournamentCharacterProjection) error {
	if row.darkKnight == nil {
		if row.job == darkcombo.Job {
			return fmt.Errorf("帝国竞技场缺少黑暗武士连招投影")
		}
		return nil
	}
	raw, found, err := g.Settings.Load(ctx, darkcombo.SettingsScope(row.character.CharacterID))
	if err != nil {
		return err
	}
	if found != row.darkKnight.found || !reflect.DeepEqual(raw, row.darkKnight.source) {
		return fmt.Errorf("帝国竞技场准备期间连招存档变化，请重试")
	}
	return nil
}

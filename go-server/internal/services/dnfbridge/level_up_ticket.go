package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/inventory"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

func levelUpTicketGameplayModule() gameplayModuleDefinition {
	op := uint16(dnfenum.CmdPacketRequestEventServerLevelUp)
	handler := func(s *Service, p *gameSession, body []byte) error { return s.handleCurrentLevelUpTicket(p, body) }
	return gameplayModuleDefinition{Name: "level-up-ticket", LegacyHandlers: map[uint16]gameplayHandler{op: func(s *Service, p *gameSession, r gameplayRequest) error { return handler(s, p, r.Body) }}, UpperHandlers: map[uint16]gameplayHandler{op: defaultClassGameplayHandler("升级券请求分类被拒绝", "op418_requires_class1", handler)}}
}

// 现用418失败读取器把99映射到“无法使用该物品”；22专指预约角色。
const currentLevelUpTicketFailureCode byte = 99

func currentLevelUpTicketDefinition(id int64, path string, doc *dnfpvf.Document) (inventory.LevelUpTicketDefinition, error) {
	d := inventory.LevelUpTicketDefinition{ItemID: id, PVFPath: path}
	if doc == nil {
		return d, inventory.ErrLevelUpTicket
	}
	kind, _ := doc.Text("stackable type")
	action, found := doc.Section("action type")
	if kind != "[etc]" || !found || len(action) != 1 || action[0].Value != "[level up ticket]" {
		return d, inventory.ErrLevelUpTicket
	}
	for _, field := range []string{"levelup range", "maximum level", "minimum level", "cool time"} {
		tokens, found := doc.Section(field)
		if !found {
			if field == "minimum level" || field == "cool time" {
				continue
			}
			return d, inventory.ErrLevelUpTicket
		}
		if len(tokens) != 1 || tokens[0].Kind != dnfpvf.TokenInt || tokens[0].Int < 0 || tokens[0].Int > math.MaxUint32 {
			return d, inventory.ErrLevelUpTicket
		}
		switch field {
		case "levelup range":
			d.MaximumGain = int(tokens[0].Int)
		case "maximum level":
			d.MaximumLevel = int(tokens[0].Int)
		case "minimum level":
			d.MinimumLevel = int(tokens[0].Int)
		case "cool time":
			d.CoolTimeMS = tokens[0].Int
		}
	}
	if d.MaximumGain < 1 || d.MaximumGain > currentAdventureCharacterLevelCap || d.MaximumLevel < d.MinimumLevel || d.MaximumLevel >= currentAdventureCharacterLevelCap {
		return d, inventory.ErrLevelUpTicket
	}
	d.Jobs = doc.Texts("usable job")
	if len(d.Jobs) == 0 {
		return d, inventory.ErrLevelUpTicket
	}
	for _, place := range doc.Texts("action usable place") {
		if place != "[village]" && place != "[seria room]" {
			return d, inventory.ErrLevelUpTicket
		}
		d.Places = append(d.Places, place)
	}
	for _, field := range []string{"using limit info", "total usable count", "cooltime group", "action usable condition"} {
		if tokens, found := doc.Section(field); found && len(tokens) > 0 {
			return d, fmt.Errorf("升级券额外条件尚未核验：%s", field)
		}
	}
	if types, found := doc.Section("usable game server type"); found && (len(types) != 1 || types[0].Kind != dnfpvf.TokenInt || types[0].Int != -1) {
		return d, fmt.Errorf("升级券限定服务器类型尚未核验")
	}
	// 同一配置可重复列出内容限制，不能只读取第一段而漏掉后面的禁用规则。
	var contents []string
	for _, section := range doc.Sections {
		if section.Name == "impossible contents" && section.Start >= 0 && section.End <= len(doc.Tokens) {
			for _, token := range doc.Tokens[section.Start:section.End] {
				contents = append(contents, token.Value)
			}
		}
	}
	for _, content := range contents {
		switch content {
		case "hardcore mode":
			d.DisallowHardcore = true
		case "ban redeemitem", "gift":
		default:
			return d, fmt.Errorf("升级券内容限制尚未核验：%s", content)
		}
	}
	// 顶层need material是商店兑换价格，两套90参考在使用升级券时都不再次扣除。
	if _, found := doc.Section("booster info"); found {
		groups, err := parseMagicBoxBoosterGroups(doc)
		if err != nil || len(groups) != 1 || groups[0].DrawCount != 1 || len(groups[0].Entries) == 0 {
			return d, inventory.ErrLevelUpTicket
		}
		var total int64
		for _, entry := range groups[0].Entries {
			if entry.ItemID != 1 || entry.Count < 1 || entry.Count > int64(d.MaximumGain) || entry.Weight < 0 || entry.Weight > math.MaxInt64-total {
				return d, inventory.ErrLevelUpTicket
			}
			total += entry.Weight
			d.Outcomes = append(d.Outcomes, inventory.LevelUpTicketOutcome{Levels: int(entry.Count), Weight: entry.Weight})
		}
		if total == 0 {
			return d, inventory.ErrLevelUpTicket
		}
	}
	return d, nil
}

func buildCurrentLevelUpTicketSuccess(slot int16, list byte, r inventory.LevelUpTicketResult) []byte {
	b := make([]byte, 12)
	b[0] = 1
	binary.LittleEndian.PutUint16(b[1:], uint16(slot))
	b[3] = list
	binary.LittleEndian.PutUint32(b[4:], r.Progression.Experience.Gain)
	binary.LittleEndian.PutUint16(b[8:], uint16(r.Progression.SkillPoints.SPGain))
	binary.LittleEndian.PutUint16(b[10:], uint16(r.Progression.SkillPoints.TPGain))
	return b
}

func (s *Service) handleCurrentLevelUpTicket(p *gameSession, body []byte) error {
	if p == nil {
		return fmt.Errorf("升级券会话缺失")
	}
	op := uint16(dnfenum.CmdPacketRequestEventServerLevelUp)
	slot, list, err := inventory.DecodeLevelUpTicketRequest(body)
	reject := func(reason any) error {
		s.logGameEvent(p, "升级券使用被拒绝", "slot", slot, "list_type", list, "reason", reason)
		return s.sendGameUpperFailure(p, op, currentLevelUpTicketFailureCode)
	}
	if err != nil {
		return reject(err)
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(p)
	if !bound || identity.generation == 0 {
		return reject("角色未绑定")
	}
	g, ok := s.repositoryGroup()
	if !ok || g.CharacterSettlement == nil || g.Inventory == nil {
		return reject("角色结算仓储不可用")
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{identity})
	if err != nil {
		return reject(err)
	}
	defer release()
	id := strconv.Itoa(int(identity.character))
	bag, found, err := g.Inventory.Load(ctx, id)
	if err != nil || !found {
		return reject("来源背包不可用")
	}
	stack, found := bag.Slots["0:"+strconv.Itoa(int(slot))]
	if !found || stack.ItemID <= 0 || stack.ItemID > math.MaxUint32 || stack.Count <= 0 {
		return reject("来源升级券不存在")
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil || items == nil || items.source == nil {
		return reject("当前PVF不可用")
	}
	item, err := items.ResolveItem(uint32(stack.ItemID))
	if err != nil || item.Kind != dungeonDropItemStackable {
		return reject("来源不是当前PVF堆叠物品")
	}
	text, err := items.source.ReadText(item.PVFPath)
	if err != nil {
		return reject(err)
	}
	doc, err := dnfpvf.Parse(item.PVFPath, text)
	if err != nil {
		return reject(err)
	}
	definition, err := currentLevelUpTicketDefinition(stack.ItemID, item.PVFPath, doc)
	if err != nil {
		return reject(err)
	}
	quests, err := s.loadQuestCatalog(ctx)
	if err != nil {
		return reject(err)
	}
	tables, err := progression.Load(ctx, items.source)
	if err != nil {
		return reject(err)
	}
	if !p.townMu.TryLock() {
		return reject("城镇场景正在变更")
	}
	if !p.dungeon.mu.TryLock() {
		p.townMu.Unlock()
		return reject("场景正在变更")
	}
	if !s.pvpMu.TryLock() {
		p.dungeon.mu.Unlock()
		p.townMu.Unlock()
		return reject("决斗场状态正在变更")
	}
	r, err := func() (inventory.LevelUpTicketResult, error) {
		defer s.pvpMu.Unlock()
		defer p.dungeon.mu.Unlock()
		defer p.townMu.Unlock()
		if p.pvpRoom != nil {
			return inventory.LevelUpTicketResult{}, fmt.Errorf("升级券不能在决斗房间内使用")
		}
		for _, queued := range s.pvpMatching {
			if queued != nil && queued.member != nil && queued.member.session == p {
				return inventory.LevelUpTicketResult{}, fmt.Errorf("升级券不能在决斗匹配期间使用")
			}
		}
		// 本次以已核对的城镇使用与本体刷新契约接入；地下城/比赛不推进持久角色等级。
		area, known := s.townArea(int64(p.townPositionSnapshot.TownID), int64(p.townPositionSnapshot.AreaID))
		if p.dungeon.runtime != nil || p.townSceneReadyCharacterID != identity.character || !p.townPositionSnapshot.PositionValid || !known || area.MapPath == "" {
			return inventory.LevelUpTicketResult{}, fmt.Errorf("升级券需要先返回城镇")
		}
		place := "[village]"
		if townMapPathLooksLikeSeria(area.MapPath) {
			place = "[seria room]"
		}
		if len(definition.Places) > 0 {
			allowed := false
			for _, value := range definition.Places {
				allowed = allowed || value == place
			}
			if !allowed {
				return inventory.LevelUpTicketResult{}, fmt.Errorf("升级券不允许在当前城镇区域使用")
			}
		}
		return inventory.ApplyLevelUpTicket(ctx, g, tables, quests, inventory.LevelUpTicketRequest{AccountID: s.accountIDForSession(p), CharacterID: id, SlotIndex: slot, LevelCap: currentAdventureCharacterLevelCap, Definition: definition, Roll: secureCurrentBoosterRoll, Revalidate: func() bool { return s.boundGameSessionCharacterCurrent(identity) && p.dungeon.runtime == nil }}, s.gameplayNow())
	}()
	if err != nil {
		return reject(err)
	}
	s.logGameEvent(p, "升级券使用已提交", "character_id", identity.character, "item_id", r.ItemID, "previous_level", r.Progression.Experience.PreviousLevel, "new_level", r.Character.Level, "gain", r.Progression.Experience.Gain, "sp_gain", r.Progression.SkillPoints.SPGain, "tp_gain", r.Progression.SkillPoints.TPGain, "remaining", r.Remaining, "cleared_quests", len(r.QuestPlan.ChangedQuestIDs), "blocked_question_ids", int64ListText(r.QuestPlan.BlockedQuestionIDs), "damaged_question_ids", int64ListText(r.QuestPlan.DamagedQuestionIDs))
	if err := s.sendGameUpperRawClass(p, op, buildCurrentLevelUpTicketSuccess(slot, list, r), 1); err != nil {
		return err
	}
	if err := s.sendSelectedIncrementalItemSlotRefreshes(p, "level_up_ticket_after_ack", []alignedcmd.ItemSlotRefresh{{ListType: 0, SlotIndex: slot}}); err != nil {
		return err
	}
	if err := s.sendCurrentLevelUpTicketActorRefresh(ctx, p, identity, r); err != nil {
		return err
	}
	if err := s.sendGameUpperRawClass(p, currentDungeonCharacterStateMsgID, buildCurrentFinishLoadingCharacterStateBody(r.Character, r.Skill.Points), 0); err != nil {
		return err
	}
	if !s.boundGameSessionCharacterCurrent(identity) {
		return fmt.Errorf("升级券提交后的角色绑定已变化")
	}
	// 与S4A21的城镇经验道具流程一致，先更新角色资料与本体，再发布绝对
	// 经验及本次已提交技能；37只更新经验时不能替代场景角色的等级更新。
	if err := s.sendCommittedDarkKnightSkillRefresh(p, r.Character, r.Skill, "level_up_ticket_after_state"); err != nil {
		return err
	}
	return s.sendCommittedStorySweepSnapshots(p, r.Character, r.QuestPlan.Record,
		append(append([]int64(nil), r.QuestPlan.ChangedQuestIDs...), r.QuestPlan.ChangedLinkedSubIDs...), "level_up_ticket")
}

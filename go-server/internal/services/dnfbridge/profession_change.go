package dnfbridge

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/inventory"
	"longheng.io/server/internal/modules/dnf/itemplacement"
	"longheng.io/server/internal/modules/dnf/profession"
	doc "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/quest"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

const currentProfessionChangeInfoMsgID uint16 = 1150

// 现用502的1进入本操作的0x6A1C失败提示；19专指未转职，不能承载其它拒绝。
const currentProfessionChangeBookFailureCode byte = 1

func professionChangeGameplayModule() gameplayModuleDefinition {
	book, system := uint16(dnfenum.CmdPacketUseRightOfChangeGrowType), uint16(dnfenum.CmdPacketReGrowupChange)
	h := func(op uint16) func(*Service, *gameSession, []byte) error {
		return func(s *Service, p *gameSession, b []byte) error { return s.handleCurrentProfessionChange(p, op, b) }
	}
	return gameplayModuleDefinition{Name: "profession-change", LegacyHandlers: map[uint16]gameplayHandler{
		book:   func(s *Service, p *gameSession, r gameplayRequest) error { return h(book)(s, p, r.Body) },
		system: func(s *Service, p *gameSession, r gameplayRequest) error { return h(system)(s, p, r.Body) },
	}, UpperHandlers: map[uint16]gameplayHandler{
		book:   defaultClassGameplayHandler("转职书请求分类被拒绝", "op502_requires_class1", h(book)),
		system: defaultClassGameplayHandler("技能栏转职请求分类被拒绝", "op785_requires_class1", h(system)),
	}}
}

func decodeCurrentProfessionChangeRequest(op uint16, body []byte) (int16, byte, error) {
	switch op {
	case uint16(dnfenum.CmdPacketUseRightOfChangeGrowType):
		if len(body) == 3 {
			slot := int16(binary.LittleEndian.Uint16(body))
			if slot >= 0 && body[2] > 0 && body[2] <= 15 {
				return slot, body[2], nil
			}
		}
	case uint16(dnfenum.CmdPacketReGrowupChange):
		// 现用EXE按原生结构写14字节，目标在第14字节；不把旧86的15字节请求混入。
		if len(body) == 14 && body[13] <= 15 {
			return -1, body[13], nil
		}
	}
	return 0, 0, inventory.ErrProfessionChange
}

func currentProfessionChangeConfig(source profession.Source) (inventory.ProfessionChangeConfig, error) {
	var c inventory.ProfessionChangeConfig
	if source == nil {
		return c, inventory.ErrProfessionChange
	}
	text, err := source.ReadText("character/growup.etc")
	if err != nil {
		return c, err
	}
	d, err := doc.Parse("character/growup.etc", text)
	if err != nil {
		return c, err
	}
	lv, found := d.Section("grow up change lv")
	if !found || len(lv) != 2 || lv[0].Kind != doc.TokenInt || lv[1].Kind != doc.TokenInt || lv[0].Int < 1 || lv[1].Int > 90 || lv[0].Int > lv[1].Int {
		return c, inventory.ErrProfessionChange
	}
	c.MinimumLevel, c.MaximumLevel = int(lv[0].Int), int(lv[1].Int)
	fees, found := d.Section("grow up change gold")
	if !found {
		return c, inventory.ErrProfessionChange
	}
	for _, v := range fees {
		if v.Kind != doc.TokenInt {
			return c, inventory.ErrProfessionChange
		}
		c.GoldCosts = append(c.GoldCosts, v.Int)
	}
	_, err = c.GoldCost(0)
	return c, err
}

// 这些图标是当前54份[class change]配置及原生选择器使用的书族，未知书族拒绝。
func currentProfessionChangeBook(id int64, path string, d *doc.Document) (inventory.ProfessionChangeBook, error) {
	b := inventory.ProfessionChangeBook{ItemID: id, PVFPath: path}
	if d == nil {
		return b, inventory.ErrProfessionChange
	}
	kind, _ := d.Text("stackable type")
	action, found := d.Section("action type")
	if kind != "[waste]" || !found || len(action) < 1 || len(action) > 2 || action[0].Value != "[class change]" {
		return b, inventory.ErrProfessionChange
	}
	icon, found := d.Section("icon")
	if !found || len(icon) != 2 || icon[1].Kind != doc.TokenInt {
		return b, inventory.ErrProfessionChange
	}
	switch icon[1].Int {
	case 788:
	case 789:
		b.PreserveAwakening = true
		b.RequireAwakening = true
		b.ClearMain = true
	case 780, 1323, 1946:
		b.PreserveAwakening = true
	case 728:
		if len(action) != 2 || action[1].Kind != doc.TokenInt || action[1].Int < 1 || action[1].Int > 4 || id != 10002875+action[1].Int {
			return b, inventory.ErrProfessionChange
		}
		b.PreserveAwakening = true
		b.ReturnBranch = byte(action[1].Int)
		b.ReturnGroup = 10002875
	default:
		return b, inventory.ErrProfessionChange
	}
	if b.ReturnBranch == 0 && len(action) != 1 {
		return b, inventory.ErrProfessionChange
	}
	if id == 10002875 {
		b.RecordReturnGroup = 10002875
	}
	if id == 10150901 {
		b.ClearMain = true
		b.BlockActiveAwakening = true
	}
	for field, target := range map[string]*int{"minimum level": &b.MinimumLevel, "maximum level": &b.MaximumLevel} {
		v, ok := d.Section(field)
		if !ok || len(v) != 1 || v[0].Kind != doc.TokenInt || v[0].Int < 1 || v[0].Int > 90 {
			return b, inventory.ErrProfessionChange
		}
		*target = int(v[0].Int)
	}
	if b.MinimumLevel > b.MaximumLevel {
		return b, inventory.ErrProfessionChange
	}
	b.Jobs = d.Texts("usable job")
	if len(b.Jobs) == 0 {
		return b, inventory.ErrProfessionChange
	}
	if v, found := d.Section("usable first growtype"); found {
		for _, x := range v {
			if x.Kind != doc.TokenInt || x.Int < 1 || x.Int > 15 {
				return b, inventory.ErrProfessionChange
			}
			b.AllowedBranches = append(b.AllowedBranches, byte(x.Int))
		}
	}
	for _, place := range d.Texts("action usable place") {
		switch place {
		case "[village]", "[seria room]":
			b.Places = append(b.Places, place)
		case "[dont use assult]":
		default:
			return b, inventory.ErrProfessionChange
		}
	}
	if len(b.Places) == 0 {
		return b, inventory.ErrProfessionChange
	}
	servers := d.Texts("action usable server type")
	if len(servers) == 0 {
		return b, inventory.ErrProfessionChange
	}
	allowsCurrentServer := false
	for _, v := range servers {
		if v != "[general server]" && v != "[event server]" && v != "[starter server]" {
			return b, inventory.ErrProfessionChange
		}
		allowsCurrentServer = allowsCurrentServer || v == "[general server]" || v == "[event server]"
	}
	if !allowsCurrentServer {
		return b, inventory.ErrProfessionChange
	}
	if v, found := d.Section("cool time"); found {
		if len(v) != 1 || v[0].Kind != doc.TokenInt || v[0].Int < 0 || v[0].Int > math.MaxUint32 {
			return b, inventory.ErrProfessionChange
		}
		b.CoolTimeMS = v[0].Int
	}
	for _, key := range []string{"total usable count", "using limit info", "cooltime group", "action usable condition"} {
		if v, found := d.Section(key); found && len(v) > 0 {
			return b, fmt.Errorf("转职书额外条件尚未核验：%s", key)
		}
	}
	// need material是购买/兑换价格，不在使用时重复收费。
	return b, nil
}

type currentProfessionChangePending struct {
	identity boundGameSessionCharacter
	op       uint16
	body     string
	result   inventory.ProfessionChangeResult
}

func currentProfessionChangeWireCount(count int64) byte {
	if count < 0 {
		return 0
	}
	if count > 127 {
		return 127
	}
	return byte(count)
}

func buildCurrentProfessionChangeAck(code uint32, count int64) []byte {
	b := make([]byte, 5)
	binary.LittleEndian.PutUint32(b, code)
	b[4] = currentProfessionChangeWireCount(count)
	return b
}

func (s *Service) sendCurrentProfessionChangeInfo(p *gameSession, c repo.CharacterRecord) error {
	count := c.Stats[inventory.ProfessionChangeCountKey]
	if count < 0 || count > math.MaxInt32 || c.Level < 1 || c.Level > 90 {
		return inventory.ErrProfessionChange
	}
	body := make([]byte, 5)
	body[0] = currentProfessionChangeWireCount(count)
	// 现用10AB7D0读u8+i32，后者在10B6DE7与50比较以阻止技能栏转职。
	// 使用已提交的真实等级；不借零值打开50级及以上入口。
	binary.LittleEndian.PutUint32(body[1:], uint32(c.Level))
	return s.sendGameUpperRawClass(p, currentProfessionChangeInfoMsgID, body, 0)
}

func (s *Service) handleCurrentProfessionChange(p *gameSession, op uint16, body []byte) error {
	if p == nil {
		return inventory.ErrProfessionChange
	}
	p.professionChangeMu.Lock()
	defer p.professionChangeMu.Unlock()
	slot, target, err := decodeCurrentProfessionChangeRequest(op, body)
	var count int64
	reject := func(reason any) error {
		s.logGameEvent(p, "转职变更被拒绝", "opcode", op, "source_slot", slot, "target", target, "reason", reason)
		if op == uint16(dnfenum.CmdPacketReGrowupChange) {
			// 现用785的19专指“转职后可以使用”；其它条件拒绝使用通用提示。
			code := uint32(99)
			if e, ok := reason.(error); ok && errors.Is(e, inventory.ErrProfessionChangeGold) {
				code = 22
			}
			return s.sendGameUpperSuccess(p, op, buildCurrentProfessionChangeAck(code, count))
		}
		return s.sendGameUpperFailure(p, op, currentProfessionChangeBookFailureCode)
	}
	if err != nil {
		return reject(err)
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(p)
	if !bound || identity.generation == 0 {
		return reject("角色未绑定")
	}
	// 现用体验退出先发42并收到24，再发送502或785提交。两条入口均需
	// 消费同一个已确认返回；仍活动的体验图和其它副本由原判据保护。
	if err := s.confirmCurrentProfessionTrialTownReturn(p); err != nil {
		return err
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{identity})
	if err != nil {
		return reject(err)
	}
	defer release()
	if pending := p.professionChangePending; pending != nil {
		if pending.identity.character != identity.character || pending.identity.generation != identity.generation {
			p.professionChangePending = nil
		} else {
			if err := s.finishCurrentProfessionChange(p, pending); err != nil {
				return err
			}
			p.professionChangePending = nil
			if pending.op == op && pending.body == string(body) {
				return nil
			}
		}
	}
	g, ok := s.repositoryGroup()
	if !ok || g.Character == nil || g.CharacterSettlement == nil {
		return reject("角色结算仓储不可用")
	}
	id := strconv.Itoa(int(identity.character))
	c, found, err := g.Character.Load(ctx, id)
	if err != nil || !found || c.AccountID != s.accountIDForSession(p) {
		return reject("当前角色不可用")
	}
	count = c.Stats[inventory.ProfessionChangeCountKey]
	items, err := s.currentPVFItemCatalog()
	if err != nil || items == nil || items.source == nil {
		return reject("当前PVF不可用")
	}
	profiles, skills, err := s.currentProfessionResources(ctx)
	if err != nil {
		return reject(err)
	}
	quests, err := s.loadQuestCatalog(ctx)
	if err != nil {
		return reject(err)
	}
	req := inventory.ProfessionChangeRequest{AccountID: c.AccountID, CharacterID: id, Target: target, Slot: slot, Profiles: profiles, Skills: skills, Quests: quests, PointBaseline: func(c repo.CharacterRecord) (repo.SkillPointState, error) {
		return s.currentPVFSkillPointTarget(ctx, c)
	}}
	if op == uint16(dnfenum.CmdPacketReGrowupChange) {
		req.Config, err = currentProfessionChangeConfig(items.source)
		if err != nil {
			return reject(err)
		}
	} else {
		if g.Inventory == nil {
			return reject("背包仓储不可用")
		}
		bag, found, err := g.Inventory.Load(ctx, id)
		if err != nil || !found {
			return reject("来源背包不可用")
		}
		stack, found := bag.Slots["0:"+strconv.Itoa(int(slot))]
		if !found || stack.ItemID <= 0 || stack.ItemID > math.MaxUint32 {
			return reject("来源转职书不存在")
		}
		def, err := items.ResolveItem(uint32(stack.ItemID))
		if err != nil || def.Kind != dungeonDropItemStackable {
			return reject("来源不是堆叠道具")
		}
		d, err := parseDungeonCardPVFDocument(items.source, def.PVFPath)
		if err != nil {
			return reject(err)
		}
		book, err := currentProfessionChangeBook(stack.ItemID, def.PVFPath, d)
		if err != nil {
			return reject(err)
		}
		book.SlotPolicy = itemplacement.SlotPolicy{ListType: 0, Start: def.SlotStart, End: def.SlotEnd, AllowQuickSlots: true}
		book.ProjectSource = func(st repo.ItemStack, at time.Time) (repo.ItemStack, error) {
			st, _ = applyCurrentPVFItemExpirationAt(st, def, at)
			if currentDungeonDiscardStackLocked(st) || currentDungeonDiscardStackExpired(st, at) {
				return st, inventory.ErrProfessionChange
			}
			return st, nil
		}
		if stack.ItemID == 2683469 || stack.ItemID == 2683470 {
			// 现用PVF特别版说明对应15天书，两个真实物品定义也必须配套存在。
			bonus, err := items.ResolveItem(uint32(stack.ItemID + 2))
			if err != nil || bonus.Kind != dungeonDropItemStackable || bonus.UsablePeriodDays != 15 || bonus.ActionType != "[class change]" {
				return reject("特别版的15天转职书配套缺失")
			}
			book.GrantBonus = func(bag *repo.InventoryRecord, at time.Time) ([]int16, error) {
				grant, err := currentPVFItemDefinitionForGrantAt(bonus, at)
				if err != nil {
					return nil, err
				}
				slots, err := grantCurrentCeraShopProductAt(bag, grant, 1, at)
				if err != nil {
					return nil, err
				}
				out := make([]int16, len(slots))
				for i, slot := range slots {
					out[i] = int16(slot)
				}
				return out, nil
			}
		}
		req.Book = &book
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
		return reject("决斗状态正在变更")
	}
	r, err := func() (inventory.ProfessionChangeResult, error) {
		defer s.pvpMu.Unlock()
		defer p.dungeon.mu.Unlock()
		defer p.townMu.Unlock()
		if p.dungeon.runtime != nil || p.pvpRoom != nil || p.townSceneReadyCharacterID != identity.character || !p.townPositionSnapshot.PositionValid {
			return inventory.ProfessionChangeResult{}, fmt.Errorf("转职需要城镇就绪")
		}
		for _, q := range s.pvpMatching {
			if q != nil && q.member != nil && q.member.session == p {
				return inventory.ProfessionChangeResult{}, fmt.Errorf("决斗匹配期间不能转职")
			}
		}
		area, known := s.townArea(int64(p.townPositionSnapshot.TownID), int64(p.townPositionSnapshot.AreaID))
		if !known || area.MapPath == "" {
			return inventory.ProfessionChangeResult{}, inventory.ErrProfessionChange
		}
		if req.Book != nil {
			if c.Stats["is_hardcore_mode"] != 0 {
				return inventory.ProfessionChangeResult{}, inventory.ErrProfessionChange
			}
			place := "[village]"
			if townMapPathLooksLikeSeria(area.MapPath) {
				place = "[seria room]"
			}
			allowed := false
			for _, v := range req.Book.Places {
				allowed = allowed || v == place
			}
			if !allowed {
				return inventory.ProfessionChangeResult{}, inventory.ErrProfessionChange
			}
		}
		req.Revalidate = func() bool { return s.boundGameSessionCharacterCurrent(identity) && p.dungeon.runtime == nil }
		return inventory.ApplyProfessionChange(ctx, g, req, s.gameplayNow())
	}()
	if err != nil {
		return reject(err)
	}
	pending := &currentProfessionChangePending{identity: identity, op: op, body: string(body), result: r}
	p.professionChangePending = pending
	s.logGameEvent(p, "转职变更已提交", "opcode", op, "character_id", identity.character, "grow_before", r.Transition.PreviousGrowType, "grow_after", r.Transition.NewGrowType, "gold_cost", r.GoldCost, "count_before", r.PreviousCount, "count_after", r.Count, "changed_slots", r.ChangedSlots)
	if err := s.finishCurrentProfessionChange(p, pending); err != nil {
		return err
	}
	p.professionChangePending = nil
	return nil
}

func (s *Service) finishCurrentProfessionChange(p *gameSession, pending *currentProfessionChangePending) error {
	if pending == nil || !s.boundGameSessionCharacterCurrent(pending.identity) {
		return inventory.ErrProfessionChange
	}
	r := pending.result
	// 复用已验证的任务转职投影：提交后的mode0、绝对状态、双页技能和任务。
	result := quest.FinishCommitResult{AtomicCommitted: true, HasProfession: true, CharacterID: r.Character.CharacterID, Profession: r.Transition, PostCommitCharacter: r.Character, PostCommitSkill: r.Skill, PostCommitQuest: r.Quest}
	if err := s.sendCurrentProfessionFinishPostCommitSnapshots(p, result); err != nil {
		return err
	}
	if len(r.ChangedSlots) > 0 {
		var slots []alignedcmd.ItemSlotRefresh
		seen := map[int16]bool{}
		for _, slot := range r.ChangedSlots {
			if !seen[slot] {
				slots = append(slots, alignedcmd.ItemSlotRefresh{ListType: 0, SlotIndex: slot})
				seen[slot] = true
			}
		}
		if err := s.sendSelectedIncrementalItemSlotRefreshes(p, "profession_change_after_commit", slots); err != nil {
			return err
		}
	}
	if r.GoldCost > 0 {
		if err := s.sendCarryGoldWallet(p, r.Character.Stats["gold"]); err != nil {
			return err
		}
	}
	if err := s.sendCurrentProfessionChangeInfo(p, r.Character); err != nil {
		return err
	}
	if pending.op == uint16(dnfenum.CmdPacketReGrowupChange) {
		return s.sendGameUpperSuccess(p, pending.op, buildCurrentProfessionChangeAck(0, r.Count))
	}
	return s.sendGameUpperSuccess(p, pending.op, nil)
}

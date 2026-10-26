package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/inventory"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// 当前客户端op515动作99在24f1e72读取PVF的形态和持续毫秒，
// 24f1fe4..24f2036建立本地变身效果；成功包仍是通用8字节动作ACK。
const currentRidingPotionAction uint32 = 99

func currentRidingPotionDefinition(itemID int64, path string, doc *dnfpvf.Document) (inventory.RidingPotionDefinition, error) {
	d := inventory.RidingPotionDefinition{ItemID: itemID, PVFPath: path, Materials: map[int64]int64{}}
	if doc == nil {
		return d, inventory.ErrRidingPotion
	}
	stack, _ := doc.Text("stackable type")
	action, found := doc.Section("action type")
	if stack != "[waste]" || !found || len(action) != 3 || action[0].Value != "[riding]" || action[1].Kind != dnfpvf.TokenInt || action[2].Kind != dnfpvf.TokenInt ||
		action[1].Int <= 0 || action[1].Int > math.MaxUint32 || action[2].Int <= 0 || action[2].Int > math.MaxUint32 {
		return d, inventory.ErrRidingPotion
	}
	d.FormID, d.DurationMS = action[1].Int, action[2].Int
	places := doc.Texts("action usable place")
	if len(places) != 1 || places[0] != "[dungeon]" {
		return d, inventory.ErrRidingPotion
	}
	for _, field := range []string{"action usable condition", "limit dungeon", "cooltime group", "total usable count"} {
		if values, found := doc.Section(field); found && len(values) > 0 {
			return d, fmt.Errorf("变身药水含本轮未支持的额外条件：%s", field)
		}
	}
	for _, field := range []string{"minimum level", "maximum level", "cool time"} {
		values, found := doc.Section(field)
		if !found {
			continue
		}
		if len(values) != 1 || values[0].Kind != dnfpvf.TokenInt || values[0].Int < 0 || values[0].Int > math.MaxUint32 {
			return d, inventory.ErrRidingPotion
		}
		switch field {
		case "minimum level":
			d.MinimumLevel = values[0].Int
		case "maximum level":
			d.MaximumLevel = values[0].Int
		case "cool time":
			d.CoolTimeMS = values[0].Int
		}
	}
	ids, _ := doc.Section("action usable dungeon")
	for _, token := range ids {
		if token.Kind != dnfpvf.TokenInt || token.Int <= 0 || token.Int > math.MaxUint32 {
			return d, inventory.ErrRidingPotion
		}
		d.DungeonIDs = append(d.DungeonIDs, token.Int)
	}
	if len(d.DungeonIDs) == 0 {
		return d, inventory.ErrRidingPotion
	}
	materials, _ := doc.Section("need material")
	if len(materials)%2 != 0 {
		return d, inventory.ErrRidingPotion
	}
	for i := 0; i < len(materials); i += 2 {
		id, count := materials[i], materials[i+1]
		if id.Kind != dnfpvf.TokenInt || count.Kind != dnfpvf.TokenInt || id.Int <= 0 || id.Int > math.MaxUint32 || id.Int == itemID || count.Int <= 0 || count.Int > math.MaxInt32-d.Materials[id.Int] {
			return d, inventory.ErrRidingPotion
		}
		d.Materials[id.Int] += count.Int
	}
	for _, content := range doc.Texts("impossible contents") {
		if content != "hardcore mode" {
			return d, inventory.ErrRidingPotion
		}
		d.DisallowHardcore = true
	}
	if unlimited, found := doc.Int("unlimited waste"); found && unlimited != 0 {
		return d, inventory.ErrRidingPotion
	}
	return d, nil
}

func (s *Service) handleCurrentRidingPotionUse(session *gameSession, body []byte) (bool, error) {
	if len(body) < 11 || binary.LittleEndian.Uint32(body[7:11]) != currentRidingPotionAction {
		return false, nil
	}
	if session == nil {
		return true, fmt.Errorf("变身药水会话缺失")
	}
	slot := int16(binary.LittleEndian.Uint16(body))
	reject := func(reason any) (bool, error) {
		s.logGameEvent(session, "变身药水使用被拒绝", "slot", slot, "action", currentRidingPotionAction, "reason", reason)
		return true, s.sendGameUpperRawClass(session, 515, inventory.BuildStackableActionFailure(slot, body[2], byte(currentRidingPotionAction), 1), 1)
	}
	if len(body) != 19 || slot < 0 || body[2] != 0 || binary.LittleEndian.Uint32(body[3:7]) != 0 || binary.LittleEndian.Uint32(body[11:15]) != 0 || binary.LittleEndian.Uint32(body[15:19]) != 0 {
		return reject("请求字段不合法")
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || identity.generation == 0 {
		return reject("角色未绑定")
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Inventory == nil {
		return reject("背包仓储不可用")
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{identity})
	if err != nil {
		return reject(err)
	}
	defer release()
	characterID := strconv.Itoa(int(identity.character))
	accountID := s.accountIDForSession(session)
	bag, found, err := repos.Inventory.Load(ctx, characterID)
	if err != nil {
		return reject(err)
	}
	stack, exists := bag.Slots["0:"+strconv.Itoa(int(slot))]
	if !found || !exists || stack.Count <= 0 || stack.ItemID <= 0 || stack.ItemID > math.MaxUint32 {
		return reject("源物品不存在")
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil || catalog == nil {
		return reject("当前PVF目录不可用")
	}
	item, err := catalog.ResolveItem(uint32(stack.ItemID))
	if err != nil {
		return reject(err)
	}
	if item.Kind != dungeonDropItemStackable || !strings.EqualFold(strings.TrimSpace(item.ActionType), "[riding]") {
		return reject("物品不属于动作99变身药水")
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return reject(err)
	}
	text, err := archive.ReadText(item.PVFPath)
	if err != nil {
		return reject(err)
	}
	doc, err := dnfpvf.Parse(item.PVFPath, text)
	if err != nil {
		return reject(err)
	}
	definition, err := currentRidingPotionDefinition(stack.ItemID, item.PVFPath, doc)
	if err != nil {
		return reject(err)
	}
	owner, err := inventory.NewOwner(repos)
	if err != nil {
		return reject(err)
	}
	if !session.dungeon.mu.TryLock() {
		return reject("副本场景正在变更")
	}
	result, err := func() (inventory.RidingPotionResult, error) {
		defer session.dungeon.mu.Unlock()
		runtime := session.dungeon.runtime
		if runtime == nil || runtime.Session == nil || runtime.Room == nil || runtime.Character.CharacterID != characterID || runtime.AccountID != accountID {
			return inventory.RidingPotionResult{}, fmt.Errorf("药水需要当前角色的有效地下城场景")
		}
		return owner.UseRidingPotion(ctx, inventory.RidingPotionRequest{CharacterID: characterID, AccountID: accountID, SlotIndex: slot, DungeonID: int64(runtime.Dungeon.ID), Definition: definition,
			Revalidate: func() bool { return s.boundGameSessionCharacterCurrent(identity) && session.dungeon.runtime == runtime }}, s.gameplayNow())
	}()
	if err != nil {
		return reject(err)
	}
	s.logGameEvent(session, "变身药水使用已提交", "character_id", identity.character, "item_id", result.ItemID, "form_id", definition.FormID, "duration_ms", definition.DurationMS, "remaining", result.Remaining)
	if err := s.sendGameUpperRawClass(session, 515, inventory.BuildStackableActionAck(slot, 0, currentRidingPotionAction), 1); err != nil {
		return true, err
	}
	refreshes := make([]alignedcmd.ItemSlotRefresh, 0, len(result.ChangedSlots))
	for _, index := range result.ChangedSlots {
		refreshes = append(refreshes, alignedcmd.ItemSlotRefresh{ListType: 0, SlotIndex: index})
	}
	return true, s.sendSelectedIncrementalItemSlotRefreshes(session, "riding_potion_after_ack", refreshes)
}

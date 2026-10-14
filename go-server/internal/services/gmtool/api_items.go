package gmtool

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// ---------------------------------------------------------------------------
// 物品索引接口
// ---------------------------------------------------------------------------

func (s *Server) handleItemCategories(w http.ResponseWriter, r *http.Request) {
	payload := map[string]any{
		"ready":     s.items.isReady(),
		"equipment": []any{},
		"stackable": []any{},
	}
	if err := s.items.failure(); err != nil {
		writeFailure(w, "索引构建失败: "+err.Error())
		return
	}
	if s.items.isReady() {
		equipmentCounts := make(map[string]int)
		segmentCounts := make(map[string]int)
		for _, entry := range s.items.snapshot() {
			if entry.Kind == "equipment" {
				tag := entry.Tag
				if tag == "" {
					tag = "(无标签)"
				}
				equipmentCounts[tag]++
				continue
			}
			segment := entry.Segment
			if segment == "" {
				segment = "消耗品"
			}
			segmentCounts[segment]++
		}
		equipment := make([]map[string]any, 0, len(equipmentCounts))
		for tag, count := range equipmentCounts {
			equipment = append(equipment, map[string]any{"tag": tag, "count": count})
		}
		sort.Slice(equipment, func(i, j int) bool {
			return equipment[i]["count"].(int) > equipment[j]["count"].(int)
		})
		stackable := make([]map[string]any, 0, len(segmentCounts))
		for segment, count := range segmentCounts {
			stackable = append(stackable, map[string]any{"segment": segment, "count": count})
		}
		sort.Slice(stackable, func(i, j int) bool {
			return stackable[i]["count"].(int) > stackable[j]["count"].(int)
		})
		payload["equipment"] = equipment
		payload["stackable"] = stackable
	}
	if profiles, _, err := s.pvf.professionResources(s); err == nil {
		payload["jobs"] = s.jobOptions(profiles)
	}
	writeJSON(w, payload)
}

func splitPipe(value string) []string {
	parts := strings.Split(value, "|")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func (s *Server) handleItemBrowse(w http.ResponseWriter, r *http.Request) {
	if err := s.items.failure(); err != nil {
		writeFailure(w, "索引构建失败: "+err.Error())
		return
	}
	if !s.items.isReady() {
		writeFailure(w, "物品索引还在构建中, 稍等几秒再搜")
		return
	}
	query := r.URL.Query()
	limit := queryInt(r, "limit", 100)
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	offset := queryInt(r, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	search := itemSearchQuery{
		Q:          strings.TrimSpace(query.Get("q")),
		Kind:       strings.TrimSpace(query.Get("kind")),
		Tags:       splitPipe(query.Get("tag")),
		Segments:   splitPipe(query.Get("segment")),
		Special:    strings.TrimSpace(query.Get("special")),
		MinLevel:   queryInt(r, "minLevel", 0),
		MaxLevel:   queryInt(r, "maxLevel", 0),
		Rarity:     queryInt(r, "rarity", -1),
		Limit:      limit,
		Offset:     offset,
		UsableJob:  queryInt(r, "usableJob", -1),
		Expiration: strings.TrimSpace(query.Get("expiration")),
		// 上游 GMSearchItemPage 的 group 分组过滤（stackable/pet/avatar/equipment/other）。
		// 不带该参数 = 不过滤，既有调用者的响应逐字节不变。
		Group: strings.TrimSpace(query.Get("group")),
	}
	entries, total := s.items.search(search)
	results := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		results = append(results, browseItemPayload(entry))
	}
	writeOK(w, map[string]any{
		"total":   total,
		"offset":  offset,
		"count":   len(results),
		"results": results,
	})
}

func browseItemPayload(entry itemEntry) map[string]any {
	var segment any
	if entry.Segment != "" {
		segment = entry.Segment
	}
	var special any
	if entry.Special != "" {
		special = entry.Special
	}
	usableJobLabel := ""
	labels := entry.UsableJobLabels
	if len(labels) > 0 {
		usableJobLabel = labels[0]
	}
	return map[string]any{
		"itemId":                  entry.ID,
		"name":                    entry.Name,
		"kind":                    entry.Kind,
		"tag":                     entry.Tag,
		"segment":                 segment,
		"special":                 special,
		"rarity":                  entry.Rarity,
		"minLevel":                entry.MinLevel,
		"grade":                   entry.Grade,
		"usableJob":               entry.UsableJob,
		"usableJobLabel":          usableJobLabel,
		"usableJobLabels":         labels,
		"requiresManualGrantType": false,
		"requiresConfiguration":   false,
		"supportsQuality":         false,
		"templateExpiration":      templateExpirationPayload(entry),
	}
}

func templateExpirationPayload(entry itemEntry) map[string]any {
	return map[string]any{
		"known":              true,
		"absoluteExpireTime": entry.AbsoluteExpireUnix,
		"usablePeriodDays":   entry.UsablePeriodDays,
		"dailyDeleteItem":    false,
		"invalid":            entry.Invalid,
	}
}

func (s *Server) handleItemSearch(w http.ResponseWriter, r *http.Request) {
	rawQuery := strings.TrimSpace(r.URL.Query().Get("q"))
	if rawQuery == "" {
		writeFailure(w, "query 不能为空")
		return
	}
	if err := s.items.failure(); err != nil {
		writeFailure(w, "索引构建失败: "+err.Error())
		return
	}
	if !s.items.isReady() {
		writeFailure(w, "物品索引还在构建中, 稍等几秒再搜")
		return
	}
	limit := queryInt(r, "limit", 30)
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	entries, _ := s.items.search(itemSearchQuery{
		Q:         rawQuery,
		Rarity:    -1,
		Limit:     limit,
		UsableJob: -1,
	})
	results := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		results = append(results, map[string]any{
			"itemId": entry.ID,
			"name":   entry.Name,
			"kind":   entry.Kind,
		})
	}
	writeJSON(w, map[string]any{"query": rawQuery, "count": len(results), "results": results})
}

func (s *Server) handleGrantOptions(w http.ResponseWriter, r *http.Request) {
	panelID, err := strconv.Atoi(strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeFailure(w, "角色编号无效")
		return
	}
	templateID, err := strconv.ParseUint(strings.TrimSpace(r.PathValue("templateId")), 10, 32)
	if err != nil || templateID == 0 {
		writeFailure(w, "itemTemplateId 无效")
		return
	}
	ctx := r.Context()
	group, repoErr := s.repositories()
	if repoErr != nil {
		writeFailure(w, repoErr.Error())
		return
	}
	if _, loadErr := s.loadCharacter(ctx, group, panelID); loadErr != nil {
		writeFailure(w, fmt.Sprintf("角色不存在: %d", panelID))
		return
	}
	entry, found := s.items.lookup(uint32(templateID))
	if !found {
		writeFailure(w, fmt.Sprintf("物品 ID %d 在 PVF 中不存在(装备/堆叠表都没有)", templateID))
		return
	}
	payload := map[string]any{
		"characterId":           panelID,
		"itemTemplateId":        entry.ID,
		"name":                  entry.Name,
		"kind":                  entry.Kind,
		"pvfTypeTag":            entry.Tag,
		"requiresConfiguration": false,
		"equipment": map[string]any{
			"type":            entry.Tag,
			"rarity":          entry.Rarity,
			"minimumLevel":    entry.MinLevel,
			"canUpgrade":      false,
			"canAmplify":      false,
			"canForge":        false,
			"supportsQuality": false,
			"maxUpgradeLevel": 31,
			"maxForgingLevel": 8,
			"qualityOptions": []map[string]any{
				{"value": 0, "label": "随机品级"},
				{"value": 1, "label": "100% 最上级"},
			},
			"amplifyTypes": []map[string]any{
				{"value": 0, "label": "无红字（强化）"},
				{"value": 1, "label": "体力"},
				{"value": 2, "label": "精神"},
				{"value": 3, "label": "力量"},
				{"value": 4, "label": "智力"},
			},
		},
		"avatar": nil,
		"manual": nil,
		"expiration": map[string]any{
			"limited":           entry.UsablePeriodDays > 0 || entry.AbsoluteExpireUnix > 0,
			"canOverride":       false,
			"expired":           entry.AbsoluteExpireUnix > 0 && entry.AbsoluteExpireUnix <= time.Now().Unix(),
			"defaultExpireTime": entry.AbsoluteExpireUnix,
			"maxDays":           3650,
		},
	}
	// J13：只有 DNFBRIDGE_GM_MAIL_OPTIONS=on 时才把"这 5 个 option 可用"报给前端
	// （含锻造：canForge/maxForgingLevel 由武器的 PVF 能力如实算出，give.js:545 靠它
	// 决定渲不渲染那个「锻造」输入框）。
	// off 档（默认）能力块逐字节与今天相同（supportsQuality / canUpgrade / canAmplify /
	// canForge 都是 false、maxForgingLevel 是历史常量 8、expiration.canOverride 是 false），
	// 前端因此渲染出与今天完全一样的控件。
	if GMMailOptionsFromEnvironment() {
		s.gmGrantOptionsCapability(entry, payload)
	}
	writeOK(w, payload)
}

// ---------------------------------------------------------------------------
// 发放物品（系统邮件）
// ---------------------------------------------------------------------------

const (
	grantRequestMinLength = 8
	grantRequestMaxLength = 128
	grantMaxCount         = 1000000
	grantIdempotencyTTL   = 30 * time.Minute
)

type itemGrantRequest struct {
	TemplateID   int             `json:"templateId"`
	Count        int             `json:"count"`
	Options      json.RawMessage `json:"options"`
	RequestID    string          `json:"requestId"`
	DeliveryMode string          `json:"deliveryMode"`
}

type grantRecord struct {
	fingerprint string
	mailID      string
	name        string
	count       int
	at          time.Time
}

func (s *Server) handleItemGrant(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody[itemGrantRequest](w, r)
	if !ok {
		return
	}
	if body.TemplateID <= 0 {
		writeFailure(w, "itemTemplateId 无效")
		return
	}
	if body.Count <= 0 {
		writeFailure(w, "数量必须大于 0")
		return
	}
	if body.Count > grantMaxCount {
		writeFailure(w, fmt.Sprintf("单次发放数量不能超过 %d", grantMaxCount))
		return
	}
	requestID := strings.TrimSpace(body.RequestID)
	if len(requestID) < grantRequestMinLength || len(requestID) > grantRequestMaxLength {
		writeFailure(w, "发放请求编号无效，请刷新页面后重试")
		return
	}
	if strings.EqualFold(strings.TrimSpace(body.DeliveryMode), "inventory") {
		writeFailure(w, "90CN 面板只支持邮件发放：直写背包会与在线会话的存档互相覆盖。")
		return
	}
	panelID, err := strconv.Atoi(strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeFailure(w, "角色编号无效")
		return
	}
	ctx := r.Context()
	group, repoErr := s.repositories()
	if repoErr != nil {
		writeFailure(w, repoErr.Error())
		return
	}
	character, err := s.loadCharacter(ctx, group, panelID)
	if err != nil {
		writeFailure(w, fmt.Sprintf("角色不存在: %d", panelID))
		return
	}
	entry, found := s.items.lookup(uint32(body.TemplateID))
	if !found {
		writeFailure(w, fmt.Sprintf("物品 ID %d 在 PVF 中不存在(装备/堆叠表都没有)", body.TemplateID))
		return
	}

	// J13：发放 option（DNFBRIDGE_GM_MAIL_OPTIONS，**空 = off**）。
	//
	// off 档这段主体**一行都不执行**：options 不解析、不校验、不投影，只继续参与下面
	// 的幂等指纹 —— 邮件与改动前逐字节相同（连"装备数量必须为 1"这条上游门禁也只在
	// on 档生效，因为 off 档今天就允许装备多发）。
	now := time.Now().UTC()
	grantOptions := gmMailGrantOptions{}
	if GMMailOptionsFromEnvironment() {
		parsed, parseErr := parseGMMailGrantOptions(body.Options)
		if parseErr != nil {
			s.rejectGMMailGrantOptions(w, panelID, body.TemplateID, "options_unparsable", parseErr.Error())
			return
		}
		if rejection := s.validateGMMailGrantOptions(entry, body.Count, parsed); rejection != nil {
			s.rejectGMMailGrantOptions(w, panelID, body.TemplateID, rejection.reason, rejection.message)
			return
		}
		grantOptions = parsed
	}

	fingerprint := grantFingerprint(body)
	if existing, ok := s.lookupGrant(requestID); ok && s.deps.Runtime == nil {
		if existing.fingerprint != fingerprint {
			writeFailure(w, "同一请求编号已用于不同的发放内容，请刷新页面后重试")
			return
		}
		writeOK(w, grantMailPayload(panelID, entry, existing.count, existing.mailID, true))
		return
	}

	// 附件证据（类型 + 装备耐久）必须在发放时就落库：只写 {ItemID, Count} 的话，
	// 装备类道具在邮箱与背包里都显示 0 耐久，而且缺 durability/max_durability
	// 会让服务端修理路径失败关闭 —— 就是"GM 发的装备修不了"。
	attachmentExtra := grantAttachmentExtra(entry, s.grantPVFDurability(entry))
	attachmentExpireAt := time.Time{}
	if GMMailOptionsFromEnvironment() {
		projection, applyErr := s.applyGMMailGrantOptions(entry, attachmentExtra, grantOptions, now)
		if applyErr != nil {
			// 投影是第二道闸（校验已经跑过同一条规则），reason 必须同名 ——
			// 例如锻造在两次之间 PVF 变得不可读时仍旧报 forging_table_unavailable。
			s.rejectGMMailGrantOptions(w, panelID, body.TemplateID,
				gmGrantOptionRejectionReason(applyErr), applyErr.Error())
			return
		}
		attachmentExtra = projection.extra
		attachmentExpireAt = projection.expireAt
		if len(projection.applied) > 0 {
			s.logEvent("gm-item-grant-options",
				"panel_id", panelID, "item_id", entry.ID, "count", body.Count,
				"applied", strings.Join(projection.applied, ","),
				"expire_days", grantOptions.ExpireDays,
				"reinforce", grantOptions.Reinforce,
				"amplify_type", grantOptions.AmplifyType,
				"amplify_value", projection.amplifyValue,
				"equipment_quality", strings.ToLower(strings.TrimSpace(grantOptions.EquipmentQuality)),
				"quality_seed", projection.qualitySeed,
				"forging_level", projection.forgingLevel,
				"forging_row_offset", itemcfgForgingRowOffset,
				"result", "applied")
		}
	}
	delivery := dnfrepo.SystemMailDelivery{
		RecipientCharacterID: character.CharacterID,
		Title:                "GM物品发放",
		Body:                 fmt.Sprintf("GM 已发放「%s」×%d，请在邮件中领取。", entry.Name, body.Count),
		Source:               "dnf90-gm-tool",
		Attachments: []dnfrepo.MailAttachment{
			{ItemID: int64(entry.ID), Count: int64(body.Count), ExpireAt: attachmentExpireAt, Extra: attachmentExtra},
		},
		CreatedAt: now,
	}
	var mailID string
	var replayed bool
	if s.deps.Runtime != nil {
		mailID, replayed, err = sendItemMailOnce(ctx, group, character, requestID, fingerprint, delivery)
	} else {
		mailID, err = dnfrepo.AppendSystemMail(ctx, group.Mailbox, delivery)
	}
	if err != nil {
		writeFailure(w, "邮件发放失败: "+err.Error())
		return
	}
	s.rememberGrant(requestID, grantRecord{
		fingerprint: fingerprint,
		mailID:      mailID,
		name:        entry.Name,
		count:       body.Count,
		at:          time.Now(),
	})
	writeOK(w, grantMailPayload(panelID, entry, body.Count, mailID, replayed))
}

func grantMailPayload(panelID int, entry itemEntry, count int, mailID string, replayed bool) map[string]any {
	return map[string]any{
		"characterId":      panelID,
		"itemTemplateId":   entry.ID,
		"name":             entry.Name,
		"count":            count,
		"delivery":         "mail",
		"messageId":        mailID,
		"messageIds":       []string{mailID},
		"messageCount":     1,
		"attachmentCount":  1,
		"replayed":         replayed,
		"notification":     "mailbox_reopen_required",
		"requiresReselect": false,
		"deliveryHint":     "邮件已发送，请在游戏内邮箱领取。",
	}
}

func grantFingerprint(body itemGrantRequest) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%d|%s", body.TemplateID, body.Count, string(body.Options))))
	return hex.EncodeToString(sum[:])
}

func (s *Server) lookupGrant(requestID string) (grantRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.grants[requestID]
	if !ok {
		return grantRecord{}, false
	}
	if time.Since(record.at) > grantIdempotencyTTL {
		delete(s.grants, requestID)
		return grantRecord{}, false
	}
	return record, true
}

func (s *Server) rememberGrant(requestID string, record grantRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.grants == nil {
		s.grants = make(map[string]grantRecord)
	}
	for key, value := range s.grants {
		if time.Since(value.at) > grantIdempotencyTTL {
			delete(s.grants, key)
		}
	}
	s.grants[requestID] = record
}

// ---------------------------------------------------------------------------
// 角色背包（只读）
// ---------------------------------------------------------------------------

func (s *Server) handleItemList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	group, err := s.repositories()
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	panelID, err := strconv.Atoi(strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeFailure(w, "角色编号无效")
		return
	}
	character, err := s.loadCharacter(ctx, group, panelID)
	if err != nil {
		writeFailure(w, fmt.Sprintf("角色不存在: %d", panelID))
		return
	}

	inventory := dnfrepo.InventoryRecord{CharacterID: character.CharacterID}
	if loaded, found, loadErr := group.Inventory.Load(ctx, character.CharacterID); loadErr == nil && found {
		inventory = loaded
	}
	if inventory.Slots == nil {
		inventory.Slots = make(map[string]dnfrepo.ItemStack)
	}
	if group.AccountInventory != nil {
		accountID := strings.TrimSpace(character.AccountID)
		if account, found, loadErr := group.AccountInventory.Load(ctx, accountID); loadErr == nil && found {
			inventory = dnfrepo.MergeAccountSharedInventory(inventory, account)
		}
	}

	rows := make([]map[string]any, 0, len(inventory.Slots)+3)
	rows = append(rows, walletRowPayload(0, "金币", statInt(character, "gold")))
	for slot := int16(1); slot <= 2; slot++ {
		key := fmt.Sprintf("%d:%d", listTypeMain, slot)
		name := "复活币"
		if slot == 2 {
			name = "技能点"
		}
		count := int64(0)
		if stack, ok := inventory.Slots[key]; ok {
			count = stack.Count
		}
		rows = append(rows, walletRowPayload(int(slot), name, count))
	}

	for key, stack := range inventory.Slots {
		listType, slot, ok := parseInventoryKey(key)
		if !ok {
			continue
		}
		if listType == listTypeMain && slot <= 2 {
			continue
		}
		if stack.ItemID <= 0 && stack.Count <= 0 {
			continue
		}
		rows = append(rows, s.inventoryRowPayload(listType, slot, stack))
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left, _ := rows[i]["listType"].(int)
		right, _ := rows[j]["listType"].(int)
		if left != right {
			return left < right
		}
		leftSlot, _ := rows[i]["slot"].(int)
		rightSlot, _ := rows[j]["slot"].(int)
		return leftSlot < rightSlot
	})
	writeJSON(w, map[string]any{
		"characterId": panelIDOfCharacter(character),
		"count":       len(rows),
		"items":       rows,
	})
}

func walletRowPayload(slot int, name string, count int64) map[string]any {
	return map[string]any{
		"container":              "角色物品栏",
		"category":               "货币",
		"listType":               listTypeMain,
		"slot":                   slot,
		"templateId":             0,
		"name":                   name,
		"kind":                   "stackable",
		"rarity":                 -1,
		"count":                  count,
		"instanceValue":          0,
		"durability":             0,
		"serial":                 0,
		"expireTime":             0,
		"supplementalExpiration": nil,
		"templateExpiration": map[string]any{
			"known": false, "absoluteExpireTime": 0, "usablePeriodDays": 0,
			"dailyDeleteItem": false, "invalid": false,
		},
		"seal":                   0,
		"deletable":              false,
		"configurable":           false,
		"expirationConfigurable": false,
		"configKind":             nil,
	}
}

func (s *Server) inventoryRowPayload(listType int, slot int16, stack dnfrepo.ItemStack) map[string]any {
	entry, found := s.items.lookup(uint32(stack.ItemID))
	name := fmt.Sprintf("未知物品 %d", stack.ItemID)
	kind := "stackable"
	rarity := -1
	category := containerLabel(listType)
	if found {
		name = entry.Name
		kind = entry.Kind
		rarity = entry.Rarity
		if listType == listTypeMain || listType == listTypeAccountCargo {
			switch kind {
			case "equipment":
				if strings.Contains(entry.Tag, "avatar") {
					category = "装扮"
				} else {
					category = "装备"
				}
			default:
				category = stackableCategory(entry.Segment)
			}
		}
	}
	durability := int64(0)
	if stack.Extra != nil {
		durability, _ = strconv.ParseInt(strings.TrimSpace(stack.Extra["durability"]), 10, 64)
	}
	expireTime := int64(0)
	if !stack.ExpireAt.IsZero() {
		expireTime = stack.ExpireAt.Unix()
	}
	deletable := listType != listTypeAccountCargo && !dnfrepo.IsAccountSharedInventorySlot(byte(listType), slot)
	// instanceValue = 该装备真实的品级种子（0 = 无/未记录）。
	// 以前这里硬编码 0，面板「品质」列恒显示 0；种子的唯一读取实现是
	// `itemcfgQualitySeed`（先看 Extra["quality_seed"]，再退回 RawEntry[6:10]），
	// 与 config-options 报的 currentQualitySeed 同一口径，不另写一份解析。
	instanceValue := uint32(0)
	if found && entry.Kind == grantItemKindEquipment {
		instanceValue = itemcfgQualitySeed(stack, entry)
	}
	return map[string]any{
		"container":              containerLabel(listType),
		"category":               category,
		"listType":               listType,
		"slot":                   int(slot),
		"templateId":             stack.ItemID,
		"name":                   name,
		"kind":                   kind,
		"rarity":                 rarity,
		"count":                  stack.Count,
		"instanceValue":          instanceValue,
		"durability":             durability,
		"serial":                 0,
		"expireTime":             expireTime,
		"supplementalExpiration": nil,
		"templateExpiration": map[string]any{
			"known":              true,
			"absoluteExpireTime": entry.AbsoluteExpireUnix,
			"usablePeriodDays":   entry.UsablePeriodDays,
			"dailyDeleteItem":    false,
			"invalid":            false,
		},
		"seal":                   0,
		"deletable":              deletable,
		"configurable":           false,
		"expirationConfigurable": false,
		"configKind":             nil,
	}
}

func stackableCategory(segment string) string {
	switch segment {
	case "材料", "任务品", "副职业材料", "徽章":
		return segment
	case "宠物消耗品":
		return "宠物消耗品"
	default:
		return "消耗品"
	}
}

func containerLabel(listType int) string {
	switch listType {
	case listTypeMain:
		return "角色物品栏"
	case listTypeAvatar:
		return "装扮背包"
	case listTypePersonalCargo:
		return "个人仓库"
	case listTypeEquipment:
		return "身上穿戴"
	case listTypePet:
		return "宠物背包"
	case listTypeAccountCargo:
		return "账号金库"
	case listTypeGuildMedal:
		return "勋章背包"
	default:
		return "其他容器"
	}
}

func parseInventoryKey(key string) (int, int16, bool) {
	parts := strings.Split(strings.TrimSpace(key), ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	listType, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, false
	}
	slot, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, false
	}
	return listType, int16(slot), true
}

// ---------------------------------------------------------------------------
// 钱包 / 邮箱
// ---------------------------------------------------------------------------

type walletRequest struct {
	Type  string `json:"type"`
	Value int64  `json:"value"`
}

func (s *Server) handleWallet(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody[walletRequest](w, r)
	if !ok {
		return
	}
	walletType := strings.ToLower(strings.TrimSpace(body.Type))
	switch walletType {
	case "gold":
	case "cera":
	default:
		writeFailure(w, fmt.Sprintf("不支持的类型: %s (90CN 面板支持 gold / cera)", body.Type))
		return
	}
	if body.Value < 0 {
		writeFailure(w, "数值不能为负")
		return
	}
	ctx, group, panelID, character, ok := s.characterRequest(w, r)
	if !ok {
		return
	}
	if walletType == "cera" {
		// 点券是账号级的：写 account_cera，账号下所有角色共用。
		if err := s.setAccountCera(ctx, group, character.AccountID, body.Value); err != nil {
			writeFailure(w, err.Error())
			return
		}
		writeOK(w, map[string]any{
			"characterId": panelID,
			"type":        "cera",
			"value":       body.Value,
			"hint":        "点券存在账号上；客户端会在进入场景或重开点券商店时刷新，请让玩家换个地图或重新登录一次。",
		})
		return
	}
	updated := dnfrepo.CloneCharacter(character)
	if updated.Stats == nil {
		updated.Stats = make(map[string]int64)
	}
	updated.Stats["gold"] = body.Value
	if err := s.saveCharacterStats(ctx, group, updated); err != nil {
		writeFailure(w, err.Error())
		return
	}
	writeOK(w, map[string]any{
		"characterId": panelID,
		"type":        "gold",
		"value":       body.Value,
	})
}

func (s *Server) handleMailboxClear(w http.ResponseWriter, r *http.Request) {
	ctx, group, panelID, character, ok := s.characterRequest(w, r)
	if !ok {
		return
	}
	record, found, err := group.Mailbox.Load(ctx, character.CharacterID)
	if err != nil {
		writeFailure(w, "清空邮箱失败: "+err.Error())
		return
	}
	messageCount := 0
	attachmentCount := 0
	if found {
		messageCount = len(record.Mails)
		for _, mail := range record.Mails {
			attachmentCount += len(mail.Attachments)
		}
	}
	cleared := dnfrepo.MailboxRecord{CharacterID: character.CharacterID, Mails: map[string]dnfrepo.MailRecord{}}
	if err := dnfrepo.SaveMailboxFields(ctx, group.Mailbox, cleared, dnfrepo.MailboxFieldMails); err != nil {
		writeFailure(w, "清空邮箱失败: "+err.Error())
		return
	}
	writeOK(w, map[string]any{
		"characterId":     panelID,
		"folder":          0,
		"recipientCount":  messageCount,
		"messageCount":    messageCount,
		"attachmentCount": attachmentCount,
		"auditCount":      0,
	})
}

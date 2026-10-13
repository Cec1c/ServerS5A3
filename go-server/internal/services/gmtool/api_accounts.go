package gmtool

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 面板里的 accountId 是启动器账号表的自增主键；90CN 仓储侧的账号 ID 形如
// "dnf:10001"。两者用 dnf90_web_accounts.account_id 对应起来。

func (s *Server) handleAccountList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	group, err := s.repositories()
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	// J14（剩余部分）：只有 DNFBRIDGE_GM_ACCOUNT_SEARCH=on **且**本次请求确实带了
	// q / limit 时才走跨账号检索（api_accounts_search.go）。其余情况 —— 包括开关
	// 未开、以及开关开着但请求没带参数 —— 都走下面这条**与今天逐字节相同**的全量路径。
	if s.gmAccountSearchEnabled() {
		if query, limit, ok := gmAccountSearchRequest(r); ok {
			s.handleAccountSearch(w, r, ctx, group, query, limit)
			return
		}
	}
	accounts, err := s.webAccounts(ctx)
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	writeJSON(w, map[string]any{"accounts": s.accountListPayload(ctx, group, accounts)})
}

// accountListPayload 是账号列表的载荷构建（原 handleAccountList 的内层循环，逐字搬移：
// 同一顺序、同一字段、同一 map 字面量）。默认档与检索档共用它，所以两档里"同一条账号行"
// 的 JSON 必然逐字节相同 —— 差别只在于喂进来的是全量还是检索命中的子集。
func (s *Server) accountListPayload(ctx context.Context, group dnfrepo.Group, accounts []webAccount) []map[string]any {
	payload := make([]map[string]any, 0, len(accounts))
	for _, account := range accounts {
		characters, charErr := s.charactersForAccount(ctx, group, account.AccountID)
		if charErr != nil {
			characters = nil
		}
		names := make([]string, 0, len(characters))
		for _, character := range characters {
			names = append(names, character.Name)
		}
		cera := s.accountCera(ctx, group, account.AccountID)
		value, starErr := accountLuckyStars(ctx, group, account.AccountID)
		var luckyStars any = value
		// Keep the established zero display for a launcher account whose
		// gameplay wallet has not been created. Other read failures are unknown.
		if starErr != nil && starErr != errAccountMissing {
			luckyStars = nil
		}
		payload = append(payload, map[string]any{
			"accountId":      account.PanelID,
			"name":           account.Account,
			"cera":           cera,
			"tokenCera":      0,
			"luckyStar":      luckyStars,
			"characterCount": len(characters),
			"characterNames": names,
		})
	}
	return payload
}

func (s *Server) handleAccountDetail(w http.ResponseWriter, r *http.Request) {
	panelID, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil {
		writeFailure(w, "账号编号无效")
		return
	}
	ctx := r.Context()
	group, repoErr := s.repositories()
	if repoErr != nil {
		writeFailure(w, repoErr.Error())
		return
	}
	account, err := s.webAccountByPanelID(ctx, panelID)
	if err != nil {
		writeFailure(w, fmt.Sprintf("账号不存在: %d", panelID))
		return
	}
	characters, _ := s.charactersForAccount(ctx, group, account.AccountID)

	luckyStars, starErr := accountLuckyStars(ctx, group, account.AccountID)
	if starErr != nil {
		writeFailure(w, "读取幸运星失败: "+starErr.Error())
		return
	}
	currencies := map[string]any{
		"name":           account.Account,
		"cera":           s.accountCera(ctx, group, account.AccountID),
		"tokenCera":      int64(0),
		"happyTokenCera": int64(0),
		"luckyStar":      luckyStars,
		"seriaLuck":      int64(0),
	}

	progress, progressErr := s.accountProgress(ctx, group, account, characters)
	if progressErr != nil {
		writeFailure(w, progressErr.Error())
		return
	}

	// 账号金库列表（list type 12）：真实读取，不再硬编码空表 —— 面板的「账号金库」
	// 表格、每行「删除」按钮与「清空」按钮都依赖它（sidebar.js:198-236）。
	// 只读；读不到时 cargoState.available=false + 具名 reason，不伪装成"已清空"。
	cargoRows, cargoState := s.accountCargoProjection(ctx, group, account)

	writeJSON(w, map[string]any{
		"accountId":      account.PanelID,
		"currencies":     currencies,
		"cubes":          []any{},
		"souls":          []any{},
		"epicPieces":     []any{},
		"cargo":          cargoRows,
		"cargoState":     cargoState,
		"characterCount": len(characters),
		"lastLoginAt":    account.LastLoginAt.Format("2006-01-02 15:04:05"),
		"lastLoginIp":    account.LastLoginIP,
		"progress":       progress,
	})
}

// accountProgress 把 90CN 的账号荣誉经验映射成 A21 的 progress 结构。
// 成长胶囊在 90CN 服务端没有对应系统，固定返回 0（前端只做展示）。
func (s *Server) accountProgress(
	ctx context.Context,
	group dnfrepo.Group,
	account webAccount,
	characters []dnfrepo.CharacterRecord,
) (map[string]any, error) {
	honor := map[string]any{
		"totalExp":           int64(0),
		"maxTotalExp":        int64(0),
		"level":              1,
		"maxLevel":           1,
		"currentLevelExp":    int64(0),
		"currentLevelExpCap": int64(0),
	}
	tables, err := s.pvf.honorTables(s)
	if err == nil && tables != nil {
		record, found, loadErr := group.Account.Load(ctx, account.AccountID)
		if loadErr == nil && found {
			maxLevel := tables.MaxOrdinaryLevel()
			maxTotal := tables.MaxTotalExperience()
			progress, resolveErr := tables.Resolve(record.HonorExp)
			if resolveErr == nil {
				level := progress.Level
				if level <= 0 {
					level = 1
				}
				entryExp, _ := tables.RequiredExperienceToEnter(level)
				nextExp, ok := tables.RequiredExperienceToEnter(level + 1)
				if !ok {
					nextExp = maxTotal
				}
				honor = map[string]any{
					"totalExp":           int64(progress.TotalExperience),
					"maxTotalExp":        int64(maxTotal),
					"level":              level,
					"maxLevel":           maxLevel,
					"currentLevelExp":    int64(progress.TotalExperience) - int64(entryExp),
					"currentLevelExpCap": int64(nextExp) - int64(entryExp),
				}
			}
		}
	}
	return map[string]any{
		"honor":         honor,
		"growthCapsule": map[string]any{"totalExp": int64(0), "requiredExp": int64(0)},
	}, nil
}

type accountCurrencyRequest struct {
	Type   string `json:"type"`
	Value  *int64 `json:"value"`
	Amount *int64 `json:"amount"`
}

// handleAccountCurrency 覆写/增减账号级点券。
// 90CN 把点券存在角色数值里，所以这里对该账号的所有角色统一取值；
// 幸运星走账号租赁钱包；赛丽亚幸运仍无对应字段。
func (s *Server) handleAccountCurrency(w http.ResponseWriter, r *http.Request) {
	panelID, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil {
		writeFailure(w, "账号编号无效")
		return
	}
	body, ok := decodeBody[accountCurrencyRequest](w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	group, repoErr := s.repositories()
	if repoErr != nil {
		writeFailure(w, repoErr.Error())
		return
	}
	account, err := s.webAccountByPanelID(ctx, panelID)
	if err != nil {
		writeFailure(w, fmt.Sprintf("账号不存在: %d", panelID))
		return
	}
	s.applyAccountCurrency(w, ctx, group, account, body)
}

// applyAccountCurrency 是 currency 路由在**账号已解析**之后的全部行为：type 分派 +
// 目标值计算 + 写库 + 响应。
//
// 抽出来只有一个理由：`handleAccountCurrency` 的前半段要查启动器账号表（s.db），
// 单测里没有数据库；把不依赖 db 的这一段单独放，才能对每条 type 分支做逐字节断言。
// 行为与拆分前**完全一致**（同一段代码、同一顺序、同一响应）。
func (s *Server) applyAccountCurrency(
	w http.ResponseWriter,
	ctx context.Context,
	group dnfrepo.Group,
	account webAccount,
	body accountCurrencyRequest,
) {
	currencyType := strings.ToLower(strings.TrimSpace(body.Type))
	switch currencyType {
	case "cera", "celain":
	case "cargo_gold", "cargogold":
		s.handleAccountCargoGold(w, ctx, group, account, body)
		return
	case "token", "tokencera":
		writeFailure(w, "90CN 的点券只有一个账号级池子（account_cera），没有独立的代币券数值；请用 cera。")
		return
	case "luckystar":
		s.handleAccountLuckyStars(w, ctx, group, account, body)
		return
	case "serialuck":
		writeFailure(w, "90CN 服务端没有赛丽亚幸运数值，无法设置。")
		return
	default:
		writeFailure(w, fmt.Sprintf("不支持的类型: %s (可用: cera)", body.Type))
		return
	}

	current := s.accountCera(ctx, group, account.AccountID)
	target := current
	switch {
	case body.Value != nil:
		target = *body.Value
		if target < 0 {
			writeFailure(w, "数值不能为负")
			return
		}
	case body.Amount != nil:
		if *body.Amount == 0 {
			writeFailure(w, "amount 不能为 0")
			return
		}
		target = current + *body.Amount
		if target < 0 {
			writeFailure(w, "扣减失败(余额不足)")
			return
		}
	default:
		writeFailure(w, "缺少 value 或 amount")
		return
	}

	if target != current {
		if err := s.setAccountCera(ctx, group, account.AccountID, target); err != nil {
			writeFailure(w, err.Error())
			return
		}
	}

	characters, _ := s.charactersForAccount(ctx, group, account.AccountID)
	panelCharacterID := 0
	if len(characters) > 0 {
		panelCharacterID = panelIDOfCharacter(characters[0])
	}
	writeOK(w, map[string]any{
		"accountId":   account.PanelID,
		"characterId": panelCharacterID,
		"type":        "cera",
		"amount":      target - current,
		"cera":        target,
		"tokenCera":   0,
		"hint":        "点券存在账号上；客户端会在进入场景或重开点券商店时刷新，请让玩家换个地图或重新登录一次。",
	})
}

// J16：账号金库金币（`account_cargo_gold`）设置。
//
// 上游参考：`gm_extended.go:167-227` GMSetAccountValue 的 `cargo_gold` 分支
//
//	:174-178  账号级在线守卫（本仓 J0 的 requireAccountOffline；默认档不检查）
//	:179-185  Load 账号；不存在 -> 拒绝
//	:193-197  value > MaxInt64 -> 拒绝；SaveAccountMetadataEntry("account_cargo_gold")
//
// 本仓的三处**有意差异**（前两条由计划书 §4.3 明文要求，第三条是这道上界的真凭据）：
//
//	J16-1 走 `dnfrepo.SaveAccountMetadataEntry`（键级写）：MySQL 档只 UPSERT
//	      `dnf_account_metadata` 的那一行 + 碰一下 `dnf_accounts.updated_at`
//	      （mysql_account.go:120-160），**不**照抄上游的 `Account.Save` 全记录写
//	      （那对并发写入是 last-write-wins，会覆盖别的 GM 刚改的荣誉/点券）。
//	J16-2 沿用本路由既有的 value（绝对值）/amount（增量）两种参数；上游只有绝对值。
//	J16-3 上界取 **MaxUint32** 而不是上游的 MaxInt64。依据：本仓把金库金币发给客户端时
//	      明确按 u32 截顶（dnfbridge/current_item_list_account.go:81-83
//	      `if gold > math.MaxUint32 { gold = math.MaxUint32 }`），写更大的值只会让
//	      客户端的金库显示与实际存量不一致。越界一律拒绝、零写入。
func (s *Server) handleAccountCargoGold(
	w http.ResponseWriter,
	ctx context.Context,
	group dnfrepo.Group,
	account webAccount,
	body accountCurrencyRequest,
) {
	// 账号级在线守卫：金库金币被在线会话的容器投影缓存着
	// （current_item_list_account.go:69-88），只写库会让在线客户端继续看到旧值。
	if !s.requireAccountOffline(ctx, w, "account-cargo-gold", group, account.AccountID, int(account.PanelID)) {
		return
	}
	if group.Account == nil {
		writeFailure(w, "账号仓储未就绪")
		return
	}
	record, found, err := group.Account.Load(ctx, account.AccountID)
	if err != nil {
		writeFailure(w, "读取账号失败: "+err.Error())
		return
	}
	if !found {
		writeFailure(w, "账号不存在。")
		return
	}
	current := accountCargoGold(record)
	target := current
	switch {
	case body.Value != nil:
		target = *body.Value
	case body.Amount != nil:
		if *body.Amount == 0 {
			writeFailure(w, "amount 不能为 0")
			return
		}
		target = current + *body.Amount
	default:
		writeFailure(w, "缺少 value 或 amount")
		return
	}
	if target < 0 {
		s.logEvent("gm-account-cargo-gold-rejected",
			"account_id", account.AccountID, "panel_id", account.PanelID,
			"old_value", current, "value", target, "reason", "negative",
			"result", "rejected")
		writeFailure(w, fmt.Sprintf("金库金币不能为负（当前 %d，目标 %d，未做任何写入）。", current, target))
		return
	}
	if target > accountCargoGoldMax {
		s.logEvent("gm-account-cargo-gold-rejected",
			"account_id", account.AccountID, "panel_id", account.PanelID,
			"old_value", current, "value", target, "reason", "exceeds_uint32",
			"result", "rejected")
		writeFailure(w, fmt.Sprintf("金库金币超出客户端可表示范围 0..%d（目标 %d，未做任何写入）。",
			uint64(accountCargoGoldMax), target))
		return
	}
	if target == current {
		s.logEvent("gm-account-cargo-gold",
			"account_id", account.AccountID, "panel_id", account.PanelID,
			"old_value", current, "value", target, "delta", int64(0), "result", "noop")
		writeOK(w, map[string]any{
			"accountId": account.PanelID,
			"type":      "cargo_gold",
			"amount":    int64(0),
			"cargoGold": current,
		})
		return
	}
	if err := dnfrepo.SaveAccountMetadataEntry(ctx, group.Account, record,
		accountCargoGoldMetadataKey, strconv.FormatInt(target, 10), time.Now().UTC()); err != nil {
		s.logEvent("gm-account-cargo-gold-failed",
			"account_id", account.AccountID, "panel_id", account.PanelID,
			"old_value", current, "value", target, "reason", "write_failed",
			"error", err.Error(), "result", "failed")
		writeFailure(w, "写入金库金币失败: "+err.Error())
		return
	}
	s.logEvent("gm-account-cargo-gold",
		"account_id", account.AccountID, "panel_id", account.PanelID,
		"old_value", current, "value", target, "delta", target-current, "result", "applied")
	writeOK(w, map[string]any{
		"accountId": account.PanelID,
		"type":      "cargo_gold",
		"amount":    target - current,
		"cargoGold": target,
		"hint":      "金库金币存在账号元数据上；客户端会在重新打开金库或换场景时刷新。",
	})
}

// accountCargoGoldMetadataKey 是账号金库金币的元数据键（与上游 gm_extended.go:197
// 和本仓 cargo/inventory 模块读的键完全一致：modules/dnf/cargo/owner.go:99、
// modules/dnf/inventory/account_cargo.go:95-101、dnfbridge/current_item_list_account.go:71）。
const accountCargoGoldMetadataKey = "account_cargo_gold"

// accountCargoGoldMax 与 dnfbridge 发给客户端的 u32 截顶保持一致
// （current_item_list_account.go:81-83）。
const accountCargoGoldMax int64 = 4294967295

// accountCargoGold 读取账号金库金币；缺键或解析失败都按 0（与
// dnfbridge/current_item_list_account.go:90-96 的 metadataInt 同口径）。
func accountCargoGold(record dnfrepo.AccountRecord) int64 {
	raw := strings.TrimSpace(record.Metadata[accountCargoGoldMetadataKey])
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

type accountHonorRequest struct {
	Level int `json:"level"`
}

func (s *Server) handleAccountHonorLevel(w http.ResponseWriter, r *http.Request) {
	panelID, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil {
		writeFailure(w, "账号编号无效")
		return
	}
	body, ok := decodeBody[accountHonorRequest](w, r)
	if !ok {
		return
	}
	tables, err := s.pvf.honorTables(s)
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	if body.Level < 1 || body.Level > tables.MaxOrdinaryLevel() {
		writeFailure(w, "目标荣誉等级不在当前 PVF 定义中。")
		return
	}
	exp, found := tables.RequiredExperienceToEnter(body.Level)
	if !found {
		writeFailure(w, "目标荣誉等级不在当前 PVF 定义中。")
		return
	}
	s.writeAccountHonor(w, r, panelID, exp)
}

func (s *Server) handleAccountHonorLevelMax(w http.ResponseWriter, r *http.Request) {
	panelID, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil {
		writeFailure(w, "账号编号无效")
		return
	}
	tables, err := s.pvf.honorTables(s)
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	s.writeAccountHonor(w, r, panelID, tables.MaxTotalExperience())
}

func (s *Server) writeAccountHonor(w http.ResponseWriter, r *http.Request, panelID int64, honorExp uint64) {
	ctx := r.Context()
	group, repoErr := s.repositories()
	if repoErr != nil {
		writeFailure(w, repoErr.Error())
		return
	}
	account, err := s.webAccountByPanelID(ctx, panelID)
	if err != nil {
		writeFailure(w, "账号不存在。")
		return
	}
	record, found, err := group.Account.Load(ctx, account.AccountID)
	if err != nil || !found {
		writeFailure(w, "账号不存在。")
		return
	}
	record = dnfrepo.CloneAccount(record)
	record.HonorExp = honorExp
	if err := group.Account.Save(ctx, record); err != nil {
		writeFailure(w, "账号写入后无法重新读取。")
		return
	}
	characters, _ := s.charactersForAccount(ctx, group, account.AccountID)
	progress, progressErr := s.accountProgress(ctx, group, account, characters)
	if progressErr != nil {
		writeFailure(w, progressErr.Error())
		return
	}
	writeOK(w, map[string]any{"accountId": account.PanelID, "progress": progress})
}

func (s *Server) handleAccountGrowthCapsule(w http.ResponseWriter, r *http.Request) {
	writeFailure(w, "90CN 服务端没有成长胶囊系统，无法设置。")
}

func panelIDOfCharacter(record dnfrepo.CharacterRecord) int {
	value, err := strconv.Atoi(strings.TrimSpace(record.CharacterID))
	if err != nil {
		return 0
	}
	return value
}

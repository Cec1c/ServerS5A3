package gmtool

// J14（剩余部分）：GET /api/accounts 的跨账号检索。
//
// 上游依据（UP = `H:\86JP启动器\90CN\90原神开源0.1.8\dnf90-source-oneclick-main\go-server`）：
//
//	UP internal/modules/dnf/repository/account_repository.go:120-142
//	   ListAccounts(ctx, repo, query, limit)：repo 未实现 AccountLister -> ErrAccountListUnavailable；
//	   query TrimSpace；limit <=0 取 50、>200 截到 200
//	UP internal/services/dnfbridge/gm.go:335-354  GMListAccounts：仓储 -> 汇总
//	UP internal/app/dnf90/gm_admin.go:75-87       GET /dnf/gm/accounts?q=&limit=（gmLimit 默认 50）
//
// 本仓的仓储侧（J14 已交付、本批未改）：
//
//	FO internal/modules/dnf/repository/account_repository.go:43-80  AccountLister + 包级 ListAccounts
//	FO internal/modules/dnf/repository/memory/memory_account.go:177-213
//	FO internal/modules/dnf/repository/mysql/mysql_account.go      （同一份契约的 SQL 实现）
//
// 开关：`DNFBRIDGE_GM_ACCOUNT_SEARCH`。**空 = off，只认显式 `on`**（与
// online_guard.go:61-63 / api_quest_mutations.go:117-119 同一约定）。
//
// 开关 off 时 handleAccountList 走 api_accounts.go 原来那条全量查询路径，响应逐字节不变；
// **开关 on 但请求没带 q/limit 时同样走原路径** —— 打开开关不会改变任何既有调用者的
// 可观察行为（前端 sidebar.js:21/32 调的正是无参数的 /api/accounts）。
//
// 检索语义（本仓与上游的唯一结构差异）：90CN 面板的账号列表来自启动器账号表
// （`dnf90_web_accounts`，deps.go:290-319 的 webAccounts），跨账号检索能力在 DNF 仓储侧
// （`dnf_accounts`，两者用 account_id 形如 "dnf:10001" 对应，见 api_accounts.go:14-15）。
// 因此这里的分工是：**DNF 仓储是检索索引，启动器账号表只负责渲染**——
//   - ListAccounts(q, limit) 决定"命中的是哪些账号"（按 account_id / 冒险团代表名匹配，
//     大小写不敏感，排序 updated_at DESC, account_id ASC，窗口由 limit 界定）；
//   - 命中的 account_id 用作启动器账号表的过滤器；
//   - 响应里每一行的字段仍全部来自启动器账号表 + 既有仓储（绝不凭空造名字/点券）。
//
// 由此产生的两个**已知限制**（交付报告 §5 有记录，不是静默行为）：
//   1. 检索只认 DNF 侧的词表；启动器登录名与角色名不参与匹配（前端 sidebar.js:52-64
//      仍会在返回的这一屏里按 登录名/面板 ID/角色名 做客户端过滤）。
//   2. 启动器账号表里 account_id 不在 DNF 账号表里的行，检索档查不到（全量档仍能看到）。
// 拿不到检索能力时**明确失败**，绝不退化成空列表（那会让面板把"查不到"显示成"没有这个账号"）。

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// gmAccountSearchEnv 是"账号列表是否接受 q/limit 并走跨账号检索"的开关名。
const gmAccountSearchEnv = "DNFBRIDGE_GM_ACCOUNT_SEARCH"

// GMAccountSearchFromEnv 解析 DNFBRIDGE_GM_ACCOUNT_SEARCH。**空 = off**，且只有显式
// `on`（两端裁剪、大小写不敏感）才启用 —— 与 RequireOfflineFromEnv 同一约定。
func GMAccountSearchFromEnv(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "on")
}

// GMAccountSearchFromEnvironment 读取进程环境里的同一个开关（理由同
// GMMailOptionsFromEnvironment：不进改动密集的 dnfbridge 包）。
func GMAccountSearchFromEnvironment() bool {
	return GMAccountSearchFromEnv(os.Getenv(gmAccountSearchEnv))
}

func (s *Server) gmAccountSearchEnabled() bool {
	return GMAccountSearchFromEnvironment()
}

// gmAccountSearchRequest 报告本次请求要不要走检索档：URL 里必须**显式出现** q 或 limit。
//
// 两个参数都不带 = 走原路径（哪怕开关是 on）：这样"打开开关"对既有调用者是零影响，
// 也不会让只有 30 个账号的本地档突然被 limit 默认值截断。
// limit 的原样透传给 dnfrepo.ListAccounts，钳制规则（<=0→50 / >200→200）只有仓储那一处实现。
func gmAccountSearchRequest(r *http.Request) (string, int, bool) {
	values := r.URL.Query()
	if !values.Has("q") && !values.Has("limit") {
		return "", 0, false
	}
	return values.Get("q"), queryInt(r, "limit", 0), true
}

// handleAccountSearch 是检索档的全部行为。
func (s *Server) handleAccountSearch(
	w http.ResponseWriter,
	r *http.Request,
	ctx context.Context,
	group dnfrepo.Group,
	query string,
	limit int,
) {
	records, err := dnfrepo.ListAccounts(ctx, group.Account, query, limit)
	if err != nil {
		reason := "account_list_failed"
		if errors.Is(err, dnfrepo.ErrAccountListUnavailable) {
			reason = "account_list_unavailable"
		}
		s.logEvent("gm-account-search-rejected",
			"query", strings.TrimSpace(query), "limit", limit,
			"reason", reason, "error", err.Error(), "result", "rejected")
		// 明确失败：绝不返回 {"accounts":[]}（那等于把"这个能力没接上"伪装成"查不到账号"）。
		writeFailure(w, "账号检索失败: "+err.Error())
		return
	}

	matched := make([]string, 0, len(records))
	keep := make(map[string]struct{}, len(records))
	for _, record := range records {
		accountID := strings.TrimSpace(record.AccountID)
		if accountID == "" {
			continue
		}
		if _, duplicate := keep[accountID]; duplicate {
			continue
		}
		keep[accountID] = struct{}{}
		matched = append(matched, accountID)
	}

	accounts, err := s.webAccounts(ctx)
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	filtered := make([]webAccount, 0, len(accounts))
	for _, account := range accounts {
		if _, hit := keep[strings.TrimSpace(account.AccountID)]; hit {
			filtered = append(filtered, account)
		}
	}
	s.logEvent("gm-account-search",
		"query", strings.TrimSpace(query), "limit", limit,
		"matched", len(matched), "returned", len(filtered), "result", "ok")
	writeJSON(w, map[string]any{"accounts": s.accountListPayload(ctx, group, filtered)})
}

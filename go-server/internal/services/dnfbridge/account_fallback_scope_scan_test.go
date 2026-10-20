package dnfbridge

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// sharedAccountFallbackProductionCallSites 是全包（生产代码）**允许**直接调用
// 共享兜底账号 `.accountID()` 的完整清单。
//
// 这份白名单是 2026-09-25「自制史诗选武器 → 客户端弹用户信息错误」那类缺陷的全仓
// 防线（当时 epic_production.go 有 8 处拿 `s.accountID()`＝共享兜底账号 `dnf:1`
// 去比对角色归属，而角色表是用会话账号 `dnf:10001` 建的 ⇒ 恒不匹配）。口径规则：
//
//   - **玩家请求路径**（任何由某条会话驱动的命令、包、持久化）必须用
//     `accountIDForSession(session)` 或同口径的 per-session 访问器；
//   - `s.accountID()` 只允许出现在**没有会话上下文**的场合（启动期、日志）或
//     **显式要求旧口径**的一键回退档里。
//
// 新增一处就要在这里显式加一行并说明理由 —— 这正是"下一批改动不许悄悄退回兜底
// 账号"的闸门。
var sharedAccountFallbackProductionCallSites = map[string]int{
	// accountIDForSession 自己的兜底出口 + warnSharedAccountFallback 的 WARN 字段。
	// 两处都在 repository_access.go：前者是设计本身，后者只把兜底账号写进日志。
	"repository_access.go": 2,
	// 启动期开关日志 "shared_account_id"：进程起来时没有任何会话上下文。
	"service.go": 1,
	// DNFBRIDGE_EPIC_PRODUCTION_ACCOUNT_SCOPE=shared 的一键回退档：
	// 显式回到"用兜底账号比对"的现场症状。
	"epic_production_account_scope.go": 1,
}

// TestSharedAccountFallbackCallSitesStayAllowlisted 把上面那份口径规则钉成可执行
// 的：生产代码里 `.accountID()` 的**每一处**都必须在白名单里，且数量精确匹配
// （多一处、少一处都红）。测试文件不受约束 —— 测试里大量需要"故意造一个兜底账号
// 与会话账号不同"的夹具。
func TestSharedAccountFallbackCallSitesStayAllowlisted(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	actual := make(map[string][]string)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		for lineNumber, line := range strings.Split(string(source), "\n") {
			trimmed := strings.TrimSpace(line)
			// 注释里提到 `s.accountID()` 是在解释口径，不是调用点。
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if strings.Contains(line, ".accountID()") {
				actual[name] = append(actual[name], name+":"+strconv.Itoa(lineNumber+1))
			}
		}
	}
	var problems []string
	for name, sites := range actual {
		want, allowed := sharedAccountFallbackProductionCallSites[name]
		if !allowed {
			problems = append(problems, "unexpected shared-fallback account use in "+strings.Join(sites, ", "))
			continue
		}
		if len(sites) != want {
			problems = append(problems, "shared-fallback account use count changed in "+name+
				": got "+strconv.Itoa(len(sites))+" want "+strconv.Itoa(want)+" ("+strings.Join(sites, ", ")+")")
		}
	}
	names := make([]string, 0, len(sharedAccountFallbackProductionCallSites))
	for name := range sharedAccountFallbackProductionCallSites {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if len(actual[name]) == 0 {
			problems = append(problems, "allowlisted shared-fallback account use is gone from "+name+
				" (update the allowlist if that is deliberate)")
		}
	}
	if len(problems) != 0 {
		t.Fatalf("shared fallback account (accountID()) scope changed:\n  %s\n"+
			"player request paths must use accountIDForSession(session); see "+
			"repository_access.go and the epic-production fix report",
			strings.Join(problems, "\n  "))
	}
}

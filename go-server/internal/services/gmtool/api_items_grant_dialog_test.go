package gmtool

// ---------------------------------------------------------------------------
// J13-G：邮件发放**对话框**（DNFBRIDGE_GM_MAIL_GRANT_DIALOG）
//
// 现场报障（操作者原话 + 截图）：「物品列表每一行的『邮件发放』按钮，点下去直接就把
// 邮件发了；应该像 A21 GM 工具那样先弹一个对话框，里面有能填的 强化 / 红字 / 锻造」。
//
// 本文件的断言口径（全部先能"红"，见交付报告的证伪记录）：
//
//	#1 开关：默认档 = 对话框，只有显式 off/direct 等字面量才回退（逐条列字面量）
//	#2 广告：GET /api/environment 的 switches[] 必须报这个开关（前端唯一真源）
//	#3 字段集：能力块 × 物品 → 三个框渲染不渲染（武器全有 / 上衣无锻造 /
//	   装扮与堆叠物一个都没有）—— 前端只看这四个布尔位，所以这里钉的就是"框出不出来"
//	#4 越界零写入：三个值越界一律 HTTP 200 + success:false + 具名 reason，
//	   并且**写次数 = 0**（grantMailboxWriteCounter，与 renameSettlementCounter 同手法）
//	#5 回退逐字节：开关 = 回退字面量时发出去的邮件与默认档逐字节相同
//	#6 前端接线：行内按钮改走对话框、回退档真的直接发、三个框仍由能力位驱动
//	#7 红字数值：类型与数值成对写入（复用在线配置接口那份初值实现），
//	   读不到红字表时**明确拒绝 + 零写入**（vendor ItemGrantOptions.cs:150-163 同款）
//	#8 投递链：发放侧写的键 == 领取侧逐字保留 == 客户端行读的键 == itemcfg 读器
// ---------------------------------------------------------------------------

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// grantDialogAvatarID 是"三项都不支持"的物品（装扮/光环）：能力块必须一个框都不给。
// A21 工具的对话框在这些物品上也只显示数量 —— 前端渲染条件就是这四个能力位。
const grantDialogAvatarID = 2101

// grantDialogEntries 是对话框矩阵用到的四类物品（武器 / 上衣 / 装扮 / 堆叠物）。
func grantDialogEntries(source readonlyTestSource) []itemEntry {
	raw := []itemEntry{
		{ID: p7SwordID, Kind: "equipment", PVFPath: "weapon/sword.equ"},
		{ID: p7CoatID, Kind: "equipment", PVFPath: "coat/coat.equ"},
		{ID: grantDialogAvatarID, Kind: "equipment", PVFPath: "avatar/aura.equ"},
		{ID: p7PotionID, Kind: "stackable", PVFPath: "material/potion.stk"},
	}
	entries := make([]itemEntry, 0, len(raw))
	for _, entry := range raw {
		item := entry
		parseItemEntry(source, &item)
		entries = append(entries, item)
	}
	return entries
}

// grantDialogSource 在 p7Source 上补两件东西：
//   - 装扮（[aurora avatar]）：不在 UP:1948-1955 的支持类型表里 ⇒ 三项全 false；
//   - 上衣的 [rarity]：红字初值 = int(rarityWeight × optionBase)，没有稀有度就算不出
//     初值（能力块会如实把 canAmplify 报成 false），所以矩阵里的上衣必须带稀有度。
func grantDialogSource() readonlyTestSource {
	source := p7Source()
	source["avatar/aura.equ"] = "[name]\n`测试光环` \n[equipment type]\n`[aurora avatar]` \n[rarity]\n" +
		strconv.Itoa(p7SwordRarity) + " \n"
	source["equipment/coat/coat.equ"] = "[name]\n`测试上衣` \n[equipment type]\n`[coat]` \n[rarity]\n" +
		strconv.Itoa(p7SwordRarity) + " \n"
	return source
}

// grantDialogHarness 与 p7HarnessWithSource 同形，只是物品表换成上面那四件。
func grantDialogHarness(t *testing.T) *gmtoolWriteHarness {
	t.Helper()
	return grantDialogHarnessWithSource(t, grantDialogSource())
}

func grantDialogHarnessWithSource(t *testing.T, source readonlyTestSource) *gmtoolWriteHarness {
	t.Helper()
	harness := newGmtoolWriteHarness(t, nil)
	harness.server.deps.PVF = func() (dnfpvf.Source, error) { return source, nil }
	harness.source = source
	index := newItemIndex("", harness.server, harness.server.cfg.Logger)
	index.mu.Lock()
	index.setEntriesLocked(grantDialogEntries(source))
	index.mu.Unlock()
	harness.server.items = index
	return harness
}

// grantMailboxWriteCounter 包住群组里的 Mailbox 仓储，只数"写了几次"。
//
// 与 renameSettlementCounter（api_character_rename_test.go:31-45）同一手法：本面板的
// 发放路径没有单事务边界，AppendSystemMail（repository/mail_system.go:79）落到
// Mailbox 仓储的 SaveFields 就是"有没有写入"的唯一可观测点（内存仓储 Load/Save 都深拷贝）。
type grantMailboxWriteCounter struct {
	inner  dnfrepo.MailboxRepository
	writes int32
}

func (c *grantMailboxWriteCounter) Load(ctx context.Context, key string) (dnfrepo.MailboxRecord, bool, error) {
	return c.inner.Load(ctx, key)
}

func (c *grantMailboxWriteCounter) Save(ctx context.Context, record dnfrepo.MailboxRecord) error {
	atomic.AddInt32(&c.writes, 1)
	return c.inner.Save(ctx, record)
}

func (c *grantMailboxWriteCounter) SaveFields(
	ctx context.Context,
	record dnfrepo.MailboxRecord,
	fields ...dnfrepo.MailboxField,
) error {
	atomic.AddInt32(&c.writes, 1)
	if store, ok := c.inner.(interface {
		SaveFields(context.Context, dnfrepo.MailboxRecord, ...dnfrepo.MailboxField) error
	}); ok {
		return store.SaveFields(ctx, record, fields...)
	}
	return c.inner.Save(ctx, record)
}

func (c *grantMailboxWriteCounter) count() int32 { return atomic.LoadInt32(&c.writes) }

// installGrantWriteCounter 把计数仓储装进测试台（Deps.Repos 返回的那个组）。
func installGrantWriteCounter(t *testing.T, harness *gmtoolWriteHarness) *grantMailboxWriteCounter {
	t.Helper()
	group, ok := harness.server.deps.Repos()
	if !ok {
		t.Fatal("repositories are not ready")
	}
	if group.Mailbox == nil {
		t.Fatal("内存仓储组没有 Mailbox：发放路径的写入点无法计数")
	}
	counter := &grantMailboxWriteCounter{inner: group.Mailbox}
	group.Mailbox = counter
	harness.server.deps.Repos = func() (dnfrepo.Group, bool) { return group, true }
	return counter
}

// grantDialogFail 断言"明确失败"（HTTP 200 + success:false），返回服务端原文。
func grantDialogFail(t *testing.T, status int, response map[string]any) string {
	t.Helper()
	message := requireFailure(t, status, response)
	if strings.TrimSpace(message) == "" {
		t.Fatal("失败响应没有给出任何原因（本仓不许静默失败）")
	}
	return message
}

// ---------------------------------------------------------------------------
// #1 开关
// ---------------------------------------------------------------------------

func TestGMMailGrantDialogFromEnvDefaultsToEnabled(t *testing.T) {
	// 未设置/空/未知值（含笔误）= 默认档：先弹对话框。
	for _, value := range []string{"", " ", "on", "ON", "On", "true", "1", "yes",
		"enable", "enabled", "dialog", "fix", "offf"} {
		if !GMMailGrantDialogFromEnv(value) {
			t.Fatalf("GMMailGrantDialogFromEnv(%q) = false, want true（默认档 = 对话框）", value)
		}
	}
	// 回退必须写成显式字面量：这些值 = 加这条开关之前的行为（行内按钮直接发）。
	for _, value := range []string{"off", "OFF", " off ", "\toff\n", "0", "false", "no",
		"disable", "disabled", "none", "legacy", "direct", " direct "} {
		if GMMailGrantDialogFromEnv(value) {
			t.Fatalf("GMMailGrantDialogFromEnv(%q) = true, want false（显式回退字面量）", value)
		}
	}
	if gmMailGrantDialogMode(true) != gmMailGrantDialogModeDialog ||
		gmMailGrantDialogMode(false) != gmMailGrantDialogModeDirect {
		t.Fatalf("模式字面量不对：%q / %q",
			gmMailGrantDialogMode(true), gmMailGrantDialogMode(false))
	}
}

func TestGMMailGrantDialogFromEnvironmentReadsProcessEnv(t *testing.T) {
	t.Setenv(gmMailGrantDialogEnv, "direct")
	if GMMailGrantDialogFromEnvironment() {
		t.Fatal("环境变量 = direct 时开关仍为真（回退字面量没生效）")
	}
	t.Setenv(gmMailGrantDialogEnv, "on")
	if !GMMailGrantDialogFromEnvironment() {
		t.Fatal("环境变量 = on 时开关为假")
	}
}

// ---------------------------------------------------------------------------
// #2 广告（前端唯一真源）
// ---------------------------------------------------------------------------

func TestMailGrantDialogSwitchIsReportedInEnvironment(t *testing.T) {
	harness := newGmtoolWriteHarness(t, nil)
	find := func() map[string]any {
		t.Helper()
		for _, entry := range harness.server.envSwitchReport() {
			if entry.Name == gmMailGrantDialogEnv {
				return map[string]any{
					"value": entry.Value, "enabled": entry.Enabled,
					"defaultWhenUnset": entry.DefaultWhenUnset, "effect": entry.Effect,
				}
			}
		}
		t.Fatalf("环境报告里没有 %s（前端读不到这个开关，回退档就没法生效）", gmMailGrantDialogEnv)
		return nil
	}

	entry := find()
	if entry["defaultWhenUnset"] != "on" || entry["enabled"] != true {
		t.Fatalf("默认档应当是 on：%+v", entry)
	}
	if !strings.Contains(fmt.Sprint(entry["effect"]), gmMailGrantDialogModeDialog) {
		t.Fatalf("effect 没有点明对话框档：%v", entry["effect"])
	}

	t.Setenv(gmMailGrantDialogEnv, "direct")
	entry = find()
	if entry["enabled"] != false || entry["value"] != "direct" {
		t.Fatalf("回退档没有如实上报：%+v", entry)
	}
	if !strings.Contains(fmt.Sprint(entry["effect"]), gmMailGrantDialogModeDirect) {
		t.Fatalf("effect 没有点明回退档：%v", entry["effect"])
	}

	// 前端还要靠 DNFBRIDGE_GM_MAIL_OPTIONS 解释"三个框为什么没出来"，
	// 所以那一条必须继续在同一个报告里。
	found := false
	for _, item := range harness.server.envSwitchReport() {
		if item.Name == gmMailOptionsEnv {
			found = true
		}
	}
	if !found {
		t.Fatalf("环境报告里没有 %s：对话框无法解释能力位为什么全 false", gmMailOptionsEnv)
	}
}

// ---------------------------------------------------------------------------
// #3 字段集：能力块 × 物品
// ---------------------------------------------------------------------------

func TestMailGrantDialogFieldsFollowGrantOptions(t *testing.T) {
	t.Setenv(gmMailOptionsEnv, "on")
	harness := grantDialogHarness(t)

	// 武器：强化 / 红字 / 锻造 三个框**都要出来**（A21 工具那张对话框的字段集）。
	status, response := harness.p7GrantOptions(t, strconv.Itoa(p7SwordID))
	requireOK(t, status, response)
	sword := responseMap(t, response, "equipment")
	for _, key := range []string{"supportsQuality", "canUpgrade", "canAmplify", "canForge"} {
		if sword[key] != true {
			t.Fatalf("武器的 %s = %v, want true（否则那个框根本不渲染）", key, sword[key])
		}
	}
	if sword["maxUpgradeLevel"] != float64(gmMailReinforceMax) {
		t.Fatalf("maxUpgradeLevel = %v, want %d（前端 max 属性的真源）",
			sword["maxUpgradeLevel"], gmMailReinforceMax)
	}
	if sword["maxForgingLevel"] != float64(7) {
		t.Fatalf("maxForgingLevel = %v, want 7（PVF [separate upgrade max] 的测试档）",
			sword["maxForgingLevel"])
	}
	// 红字不是自由数字：vendor ItemGrantOptions.cs:122/167-177 只认 0..4 五个词，
	// 前端渲染的正是这五个选项（0 = 无红字）。
	amplify := responseList(t, sword, "amplifyTypes")
	if len(amplify) != gmMailAmplifyTypeMax+1 {
		t.Fatalf("amplifyTypes 有 %d 项，want %d（0..4）", len(amplify), gmMailAmplifyTypeMax+1)
	}
	for index, want := range []string{"无红字（强化）", "体力", "精神", "力量", "智力"} {
		option := amplify[index].(map[string]any)
		if option["value"] != float64(index) || option["label"] != want {
			t.Fatalf("amplifyTypes[%d] = %+v, want value=%d label=%q", index, option, index, want)
		}
	}

	// 上衣：强化 / 红字 有，锻造**没有**（只有武器可锻）—— 前端于是不渲染那个框。
	status, response = harness.p7GrantOptions(t, strconv.Itoa(p7CoatID))
	requireOK(t, status, response)
	coat := responseMap(t, response, "equipment")
	if coat["canUpgrade"] != true || coat["canAmplify"] != true {
		t.Fatalf("上衣的强化/红字能力 = %v/%v, want true/true", coat["canUpgrade"], coat["canAmplify"])
	}
	if coat["canForge"] != false || coat["maxForgingLevel"] != float64(0) {
		t.Fatalf("上衣的锻造能力 = %v/%v, want false/0", coat["canForge"], coat["maxForgingLevel"])
	}

	// 装扮（光环）与堆叠物：三项一个都不支持 —— 一个框都不许出来（不许有死控件）。
	for _, templateID := range []int{grantDialogAvatarID, p7PotionID} {
		status, response = harness.p7GrantOptions(t, strconv.Itoa(templateID))
		requireOK(t, status, response)
		equipment := responseMap(t, response, "equipment")
		for _, key := range []string{"supportsQuality", "canUpgrade", "canAmplify", "canForge"} {
			if equipment[key] != false {
				t.Fatalf("物品 %d 的 %s = %v, want false", templateID, key, equipment[key])
			}
		}
		if equipment["maxForgingLevel"] != float64(0) {
			t.Fatalf("物品 %d 的 maxForgingLevel = %v, want 0", templateID, equipment["maxForgingLevel"])
		}
	}
}

// ---------------------------------------------------------------------------
// #4 越界 = 明确失败 + 零写入
// ---------------------------------------------------------------------------

func TestMailGrantDialogRejectsOutOfRangeWithZeroWrites(t *testing.T) {
	t.Setenv(gmMailOptionsEnv, "on")
	cases := []struct {
		name    string
		options map[string]any
		reason  string
		want    string
	}{
		{"强化超上限", map[string]any{"reinforce": gmMailReinforceMax + 1}, "out_of_range", "reinforce"},
		{"强化负数", map[string]any{"reinforce": -1}, "out_of_range", "reinforce"},
		{"红字超词表", map[string]any{"amplify_type": gmMailAmplifyTypeMax + 1}, "out_of_range", "amplify_type"},
		{"红字负数", map[string]any{"amplify_type": -1}, "out_of_range", "amplify_type"},
		{"锻造超 PVF 上限", map[string]any{"forging_level": 8}, "forging_level_out_of_range", "7"},
		{"锻造负数", map[string]any{"forging_level": -1}, "forging_level_out_of_range", "7"},
	}
	for index, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			harness := grantDialogHarness(t)
			counter := installGrantWriteCounter(t, harness)
			before := harness.mailbox(t)
			status, response := harness.p7Grant(t, p7GrantBody(p7SwordID, 1,
				fmt.Sprintf("j13g-range-%02d", index), testCase.options))
			message := grantDialogFail(t, status, response)
			if !strings.Contains(message, testCase.want) {
				t.Fatalf("拒绝文案没有点名 %q：%q", testCase.want, message)
			}
			fields := harness.eventFields(t, "gm-item-grant-options-rejected")
			if fields["reason"] != testCase.reason {
				t.Fatalf("结构化 reason = %v, want %s（全部 %+v）",
					fields["reason"], testCase.reason, fields)
			}
			if got := counter.count(); got != 0 {
				t.Fatalf("拒绝路径写了 %d 次邮箱，want 0（零写入）", got)
			}
			harness.requireUnchanged(t, before)
			if harness.hasEvent("gm-item-grant-options") {
				t.Fatalf("拒绝路径记了一条已生效事件：%v", harness.eventKinds())
			}
		})
	}

	// 对照：同一批值都在范围内时必须成功（否则上面的"零写入"可能只是因为压根没写）。
	harness := grantDialogHarness(t)
	counter := installGrantWriteCounter(t, harness)
	status, response := harness.p7Grant(t, p7GrantBody(p7SwordID, 1, "j13g-range-ok", map[string]any{
		"reinforce":     gmMailReinforceMax,
		"amplify_type":  gmMailAmplifyTypeMax,
		"forging_level": 7,
	}))
	requireOK(t, status, response)
	if counter.count() == 0 {
		t.Fatal("合法值一次都没写：计数器没接上，上面的零写入断言是假绿")
	}
	if len(harness.mailboxMails(t)) != 1 {
		t.Fatalf("合法值没有发出邮件：%+v", harness.mailboxMails(t))
	}
}

func TestMailGrantDialogRejectsUnsupportedCombinationsWithZeroWrites(t *testing.T) {
	t.Setenv(gmMailOptionsEnv, "on")
	cases := []struct {
		name       string
		templateID int
		options    map[string]any
		want       string
	}{
		{"堆叠物带强化", p7PotionID, map[string]any{"reinforce": 5}, "只能发给装备"},
		{"装扮带锻造", grantDialogAvatarID, map[string]any{"forging_level": 3}, "只有武器"},
		{"上衣带锻造", p7CoatID, map[string]any{"forging_level": 3}, "只有武器"},
	}
	for index, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			harness := grantDialogHarness(t)
			counter := installGrantWriteCounter(t, harness)
			before := harness.mailbox(t)
			status, response := harness.p7Grant(t, p7GrantBody(testCase.templateID, 1,
				fmt.Sprintf("j13g-unsupported-%02d", index), testCase.options))
			message := grantDialogFail(t, status, response)
			if !strings.Contains(message, testCase.want) {
				t.Fatalf("拒绝文案没有点名 %q：%q", testCase.want, message)
			}
			if got := counter.count(); got != 0 {
				t.Fatalf("拒绝路径写了 %d 次邮箱，want 0", got)
			}
			harness.requireUnchanged(t, before)
		})
	}
}

// ---------------------------------------------------------------------------
// #5 回退档：邮件逐字节相同
// ---------------------------------------------------------------------------

func TestMailGrantDialogRollbackLiteralKeepsMailByteIdentical(t *testing.T) {
	t.Setenv(gmMailOptionsEnv, "on")
	// 控制组：默认档（对话框）。
	control := grantDialogHarness(t)
	status, response := control.p7Grant(t, p7GrantBody(p7SwordID, 1, "j13g-rollback-control", nil))
	requireOK(t, status, response)
	controlHash := p7MailHash(t, p7SingleMail(t, control))

	// 回退档：DNFBRIDGE_GM_MAIL_GRANT_DIALOG=off —— 前端直接发（请求体不带 options），
	// 服务端这条路径上**没有任何按本开关分叉的代码**，所以邮件必须逐字节相同。
	t.Setenv(gmMailGrantDialogEnv, "off")
	rollback := grantDialogHarness(t)
	counter := installGrantWriteCounter(t, rollback)
	status, response = rollback.p7Grant(t, p7GrantBody(p7SwordID, 1, "j13g-rollback-direct", nil))
	requireOK(t, status, response)
	if counter.count() == 0 {
		t.Fatal("回退档没有写邮箱（对照组失效）")
	}
	if rollbackHash := p7MailHash(t, p7SingleMail(t, rollback)); rollbackHash != controlHash {
		t.Fatalf("回退档的邮件与控制组不同：\nrollback=%s\ncontrol =%s", rollbackHash, controlHash)
	}
	if GMMailGrantDialogFromEnvironment() {
		t.Fatal("回退字面量没有让 GMMailGrantDialogFromEnvironment 变成 false")
	}
}

// ---------------------------------------------------------------------------
// #6 前端接线
// ---------------------------------------------------------------------------

func TestMailGrantDialogFrontendIsWired(t *testing.T) {
	script := p7GiveScript(t)
	code := p7StripJSComments(script)

	// 开关真源：服务端 GET /api/environment 的 switches[]（本仓前端唯一能读到环境的地方）。
	for _, marker := range []string{
		"giveDeliveryDialogMode",
		"'/api/environment'",
		"'DNFBRIDGE_GM_MAIL_GRANT_DIALOG'",
		"'DNFBRIDGE_GM_MAIL_OPTIONS'",
		"applyGiveDialogSwitches(report && report.switches)",
	} {
		if !strings.Contains(code, marker) {
			t.Fatalf("give.js 的**代码**里缺少 %q（注释里的不算）", marker)
		}
	}
	// 探测失败必须保持默认档（对话框），绝不在读不到开关时悄悄回退成直发。
	modeBody := p7FunctionBody(t, code, "async function giveDeliveryDialogMode")
	if !strings.Contains(modeBody, "catch (_)") {
		t.Fatalf("giveDeliveryDialogMode 没有兜住探测失败：\n%s", modeBody)
	}

	dialog := p7FunctionBody(t, code, "async function openGrantDialogForRow")
	for _, marker := range []string{
		"await giveDeliveryDialogMode()",
		"if (!mode.dialog)",
		"await giveItem(item.itemId, seedCount)",
		"await configureGrantItem(item, row, seedCount)",
	} {
		if !strings.Contains(dialog, marker) {
			t.Fatalf("openGrantDialogForRow 缺少 %q：\n%s", marker, dialog)
		}
	}
	// 回退档那一支必须在对话框之前返回（顺序错了就变成"先弹框再直接发"）。
	if strings.Index(dialog, "if (!mode.dialog)") > strings.Index(dialog, "await configureGrantItem(item, row, seedCount)") {
		t.Fatal("回退档没有先于对话框返回")
	}

	// 行内按钮：直发行的按钮必须改走对话框入口，那条"直接发"的老写法必须消失。
	if !strings.Contains(code, "openGrantDialogForRow(r, tr);") {
		t.Fatal("发放列表的行内按钮没有接到 openGrantDialogForRow（点了仍旧直接发）")
	}
	if strings.Contains(code, "giveItem(r.itemId, parseInt(") {
		t.Fatal("give.js 里仍留着'点行内按钮直接发'的老写法（就是本次报障的那一行）")
	}

	// 对话框字段仍由能力位驱动（三个框的渲染条件）+ 数量带入行内值。
	render := p7FunctionBody(t, code, "function renderGrantConfiguration")
	for _, marker := range []string{
		"equipment.supportsQuality", "equipment.canUpgrade", "equipment.canAmplify", "equipment.canForge",
		"maxUpgradeLevel", "maxForgingLevel",
		`id="give-config-count"`,
	} {
		if !strings.Contains(render, marker) {
			t.Fatalf("对话框渲染缺少 %q（字段集不再由能力块驱动）", marker)
		}
	}
	// 对话框自己的"数量"是提交时的权威值。
	if !strings.Contains(p7FunctionBody(t, code, "async function submitConfiguredGrant"), "$('#give-config-count')") {
		t.Fatal("submitConfiguredGrant 没有读对话框里的数量")
	}
	// 对话框打开时把行内数量带进去（configureGrantItem 的第三个参数）。
	configure := p7FunctionBody(t, code, "async function configureGrantItem")
	if !strings.Contains(configure, "seedCount") || !strings.Contains(configure, "count }") {
		t.Fatalf("configureGrantItem 没有接住行内数量：\n%s", configure)
	}

	// 前端打的路径必须真的注册过（gmtool.go 是唯一真源）。
	if routes := p4bRegisteredRoutes(t); !routes["POST /api/characters/*/items"] {
		t.Fatal("gmtool.go 里没有注册 POST /api/characters/{id}/items")
	}
}

// ---------------------------------------------------------------------------
// #7 红字数值：类型与数值成对写入（复用既有的初值实现）
// ---------------------------------------------------------------------------

func TestMailGrantRedWordValueComesFromExistingAmplifyResolver(t *testing.T) {
	t.Setenv(gmMailOptionsEnv, "on")
	// fixture：[option data] 类型 1..4 = 10/20/30/40，[rarity weight] epic(4) = 2.00
	// ⇒ 初值 20/40/60/80（api_items_configure_test.go:29-43 的同一份档）。
	for _, testCase := range []struct {
		amplifyType int
		wantValue   string
	}{{1, "20"}, {3, "60"}, {4, "80"}} {
		harness := grantDialogHarness(t)
		status, response := harness.p7Grant(t, p7GrantBody(p7SwordID, 1,
			fmt.Sprintf("j13g-amplify-%d", testCase.amplifyType),
			map[string]any{"amplify_type": testCase.amplifyType}))
		requireOK(t, status, response)
		attachment := p7Attachment(t, p7SingleMail(t, harness))
		for _, key := range []string{"amplify_type", "byte_13"} {
			if attachment.Extra[key] != strconv.Itoa(testCase.amplifyType) {
				t.Fatalf("红字类型没落到 %q：%+v", key, attachment.Extra)
			}
		}
		// 数值必须一起写（否则操作者拿到的是一件"有红字、数值 +0"的装备：
		// 客户端行 0x14..0x15 = Extra 的 marker_16/value_d，inventory/body.go:454）。
		for _, key := range []string{"amplify_value", "marker_16"} {
			if attachment.Extra[key] != testCase.wantValue {
				t.Fatalf("红字数值没落到 %q=%q（want %s）：%+v",
					key, attachment.Extra[key], testCase.wantValue, attachment.Extra)
			}
		}
		claimed := dnfrepo.ItemStack{ItemID: int64(p7SwordID), Count: 1, Extra: cloneGrantExtra(attachment.Extra)}
		if got := itemcfgAmplifyTypeOf(claimed); got != testCase.amplifyType {
			t.Fatalf("按游戏自己的读器读红字类型 = %d, want %d", got, testCase.amplifyType)
		}
		if got := itemcfgAmplifyValueOf(claimed); got != int(mustAtoi(t, testCase.wantValue)) {
			t.Fatalf("按游戏自己的读器读红字数值 = %d, want %s", got, testCase.wantValue)
		}
		fields := harness.eventFields(t, "gm-item-grant-options")
		if fields["amplify_value"] != int(mustAtoi(t, testCase.wantValue)) {
			t.Fatalf("审计事件里的 amplify_value = %v, want %s（全部 %+v）",
				fields["amplify_value"], testCase.wantValue, fields)
		}
	}
}

func TestMailGrantRedWordRejectsWhenAmplifyTableUnreadable(t *testing.T) {
	t.Setenv(gmMailOptionsEnv, "on")
	source := grantDialogSource()
	delete(source, itemcfgAmplifyItemPVFPath)
	harness := grantDialogHarnessWithSource(t, source)
	counter := installGrantWriteCounter(t, harness)
	before := harness.mailbox(t)
	status, response := harness.p7Grant(t, p7GrantBody(p7SwordID, 1, "j13g-amplify-no-table",
		map[string]any{"amplify_type": 3}))
	message := grantDialogFail(t, status, response)
	if !strings.Contains(message, "红字") {
		t.Fatalf("拒绝文案没有点名红字表：%q", message)
	}
	if fields := harness.eventFields(t, "gm-item-grant-options-rejected"); fields["reason"] != "amplify_table_unavailable" {
		t.Fatalf("结构化 reason = %v, want amplify_table_unavailable", fields["reason"])
	}
	if got := counter.count(); got != 0 {
		t.Fatalf("拒绝路径写了 %d 次邮箱，want 0", got)
	}
	harness.requireUnchanged(t, before)

	// 能力块同一条降级：读不到表就不许渲染那个下拉（canAmplify=false）。
	status, response = harness.p7GrantOptions(t, strconv.Itoa(p7SwordID))
	requireOK(t, status, response)
	equipment := responseMap(t, response, "equipment")
	if equipment["canAmplify"] != false || equipment["canUpgrade"] != true {
		t.Fatalf("读不到红字表时的能力 = canAmplify:%v canUpgrade:%v, want false/true",
			equipment["canAmplify"], equipment["canUpgrade"])
	}
}

func mustAtoi(t *testing.T, value string) int64 {
	t.Helper()
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		t.Fatalf("ParseInt(%q): %v", value, err)
	}
	return parsed
}

// ---------------------------------------------------------------------------
// #8 投递链：发放侧写的键 == 领取侧逐字保留 == 客户端行读的键 == itemcfg 读器
// ---------------------------------------------------------------------------

func TestMailGrantReinforceAndAmplifySurviveMailClaimProjection(t *testing.T) {
	readSource := func(relative string) string {
		t.Helper()
		path := filepath.Join(p7GmtoolDir(t), "..", "..", relative)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读 %s: %v", relative, err)
		}
		return string(raw)
	}

	// 1. 发放侧：强化用既有的两键写法，红字走同一个移植自游戏自身的写入器。
	grant := readSource(filepath.Join("services", "gmtool", "api_items_grant_options.go"))
	for _, marker := range []string{
		`stack.Extra["reinforce"] = value`,
		`stack.Extra["ext_data0"] = value`,
		"itemcfgSetAmplifyState(&stack, byte(amplifyType), uint16(amplifyValue))",
	} {
		if !strings.Contains(grant, marker) {
			t.Fatalf("发放投影不再写 %q（强化/红字的投递链第一环断了）", marker)
		}
	}
	writer := readSource(filepath.Join("services", "gmtool", "api_items_configure.go"))
	for _, marker := range []string{
		`"amplify_type", "amplification_type", "byte_13", "value_13", "value_c"`,
		`"amplify_value", "amplification_value", "marker_16", "marker16", "value_d"`,
	} {
		if !strings.Contains(writer, marker) {
			t.Fatalf("itemcfgSetAmplifyState 不再写 %q", marker)
		}
	}

	// 2. 领取侧：附件 Extra 只删三个 mailbox_* 信封键，其余逐字保留。
	claim := readSource(filepath.Join("modules", "dnf", "mail", "owner.go"))
	for _, marker := range []string{
		"func mailboxAttachmentStack(",
		"extra := cloneStringMap(attachment.Extra)",
		`delete(extra, "mailbox_pvf_kind")`,
		`delete(extra, "mailbox_equipment_type")`,
		`delete(extra, "mailbox_pvf_path")`,
	} {
		if !strings.Contains(claim, marker) {
			t.Fatalf("mailboxAttachmentStack 不再逐字保留附件 Extra（缺 %q）", marker)
		}
	}

	// 3. 客户端行：三个属性各自的行内偏移（0x0A 强化 / 0x13 红字类型 / 0x14..0x15 红字数值）。
	row := readSource(filepath.Join("modules", "dnf", "inventory", "body.go"))
	for _, marker := range []string{
		`entry[0x0A] = clampByte(firstExtraInt(`,
		`entry[0x13] = clampByte(firstExtraInt(`,
		`entry[0x14:0x16], clampUint16(firstExtraInt(extra, 0, "marker_16", "marker16", "value_d"))`,
	} {
		if !strings.Contains(row, marker) {
			t.Fatalf("inventory/body.go 不再把 Extra 写进行内偏移（缺 %q）：客户端读不到", marker)
		}
	}

	// 4. 端到端对照：发放投影写出来的键，用游戏自己的读器必须读回同样的值。
	entry := p7Entry(t, p7SwordID)
	projection, err := grantDialogHarness(t).server.applyGMMailGrantOptions(entry,
		map[string]string{grantExtraKeyPVFKind: "equipment"},
		gmMailGrantOptions{Reinforce: 12, AmplifyType: 3}, time.Now().UTC())
	if err != nil {
		t.Fatalf("applyGMMailGrantOptions: %v", err)
	}
	claimed := dnfrepo.ItemStack{ItemID: int64(entry.ID), Count: 1, Extra: cloneGrantExtra(projection.extra)}
	if got := itemcfgUpgradeLevelOf(claimed); got != 12 {
		t.Fatalf("领取后按游戏自己的读器读强化 = %d, want 12", got)
	}
	if got := itemcfgAmplifyTypeOf(claimed); got != 3 {
		t.Fatalf("领取后按游戏自己的读器读红字类型 = %d, want 3", got)
	}
	if got := itemcfgAmplifyValueOf(claimed); got != 60 {
		t.Fatalf("领取后按游戏自己的读器读红字数值 = %d, want 60", got)
	}
}

package gmtool

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/itemquality"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// ---------------------------------------------------------------------------
// P7 / J13：发放 option（expire_days / reinforce / amplify_type / equipment_quality）
//
// 计划书 §4.7 测试清单 4/5/6/7/8/9 条全部落在这个文件里：
//
//	#4 TestP7MailOptionsDefaultIgnored            默认档传 4 个 option -> 邮件逐字节不变
//	#5 TestP7EquipmentMailCountMustBeOne          装备 + count=2 -> 拒绝 + 零写入
//	#6 TestP7MailOptionsBounds                    四个 option 的边界/词表
//	#7 TestP7MailOptionsEquipmentOnlyAttributes   非装备（与不支持的类型）带装备专属 option -> 拒绝
//	#8 TestP7QualitySeedProjectionMatchesFork     品级种子投影与 dnfbridge 用的是同一份实现
//	#9 TestP7GMExpirationOverrideIsPureDate       applyCurrentGMExpirationOverrideAt 是纯日期计算
//
// 另加三条本批自己的回归：
//
//	TestP7MailOptionsDefaultIgnoresInvalidRequests off 档连"装备 count=2 / 越界 / 未知字段"都不拒绝
//	TestP7MailOptionsProjection                    on 档四个 option 真正落到附件 Extra/ExpireAt
//	TestP7GrantOptionsCapabilityBlock*            能力块：off 档逐字节不变，on 档如实报能力
//	TestP7GiveScript*                              give.js 只发服务端认识的字段、失败即原文报错
// ---------------------------------------------------------------------------

const (
	p7SwordID  = 2001 // equipment/[weapon]：支持强化/增幅/品级，且**只有它能锻造**
	p7TitleID  = 2002 // equipment/[title name]：UP:1948-1955 的支持类型表里没有它
	p7PotionID = 2003 // stackable：只能设 expire_days
	p7CoatID   = 2004 // equipment/[coat]：支持强化/增幅/品级，但**不能锻造**（只有武器可以）
	p7SwordDur = 100  // [durability] 的值（发放端已有证据，option 不许把它弄丢）
	// p7SwordRarity 是 fixture 短剑的 [rarity]（4 = epic）。红字初值 = int(2.00 × base)，
	// 于是 type 1..4 的初值分别是 20/40/60/80（见 api_items_configure_test.go:29-43）。
	p7SwordRarity = 4
)

// p7Source 是四个道具 + 锻造表 + 红字数值表的最小 PVF 替身（只保留物品索引、耐久、
// 锻造上限与红字初值真正读的段）。
// 锻造上限取 api_items_configure_test.go 的测试档（故意 7 而不是真档 8）：任何"写死 8"
// 的实现都会在发放路径上同样露馅。红字数值表（J13-G）同样取配置侧的测试档：
// 四个红字类型的基础值 10/20/30/40，[rarity weight] 覆盖 common..legendary。
// 物品带上 [rarity] 是**必须**的：红字初值 = int(rarityWeight × optionBase)，
// 没有稀有度的物品（Rarity = -1）算不出初值，能力块会如实报 canAmplify=false。
func p7Source() readonlyTestSource {
	return readonlyTestSource{
		itemcfgSeparateUpgradePVFPath: itemcfgTestSeparateUpgradeText,
		itemcfgAmplifyItemPVFPath:     itemcfgTestAmplifyItemText,
		"equipment/weapon/sword.equ": "[name]\n`测试短剑` \n[equipment type]\n`[weapon]` \n[rarity]\n" +
			strconv.Itoa(p7SwordRarity) + " \n[durability]\n" +
			strconv.Itoa(p7SwordDur) + " \n",
		"equipment/title/title.equ":     "[name]\n`测试称号` \n[equipment type]\n`[title name]` \n",
		"equipment/coat/coat.equ":       "[name]\n`测试上衣` \n[equipment type]\n`[coat]` \n",
		"stackable/material/potion.stk": "[name]\n`红色药水` \n[stackable type]\n`[consumable]` 4 \n",
	}
}

func p7Entries(source readonlyTestSource) []itemEntry {
	raw := []itemEntry{
		{ID: p7SwordID, Kind: "equipment", PVFPath: "weapon/sword.equ"},
		{ID: p7TitleID, Kind: "equipment", PVFPath: "title/title.equ"},
		{ID: p7PotionID, Kind: "stackable", PVFPath: "material/potion.stk"},
		{ID: p7CoatID, Kind: "equipment", PVFPath: "coat/coat.equ"},
	}
	entries := make([]itemEntry, 0, len(raw))
	for _, entry := range raw {
		item := entry
		parseItemEntry(source, &item)
		entries = append(entries, item)
	}
	return entries
}

func p7Entry(t *testing.T, id uint32) itemEntry {
	t.Helper()
	for _, entry := range p7Entries(p7Source()) {
		if entry.ID == id {
			return entry
		}
	}
	t.Fatalf("fixture 里没有 item %d", id)
	return itemEntry{}
}

// p7Harness 在 P2/P4 的写测试台上换掉 PVF 与物品索引（邮箱为空）。
func p7Harness(t *testing.T) *gmtoolWriteHarness {
	t.Helper()
	return p7HarnessWithSource(t, p7Source())
}

// p7HarnessWithSource 与 p7Harness 相同，但用调用方给的 PVF 替身 —— 用来构造
// "读不到 etc/upgrade_separate.etc" 这类降级场景（junk 源里没有那个文件）。
func p7HarnessWithSource(t *testing.T, source readonlyTestSource) *gmtoolWriteHarness {
	t.Helper()
	harness := newGmtoolWriteHarness(t, nil)
	harness.server.deps.PVF = func() (dnfpvf.Source, error) { return source, nil }
	harness.source = source
	index := newItemIndex("", harness.server, harness.server.cfg.Logger)
	index.mu.Lock()
	index.setEntriesLocked(p7Entries(source))
	index.mu.Unlock()
	harness.server.items = index
	return harness
}

func p7GrantBody(templateID int, count int, requestID string, options any) map[string]any {
	body := map[string]any{
		"templateId":   templateID,
		"count":        count,
		"requestId":    requestID,
		"deliveryMode": "mail",
	}
	if options != nil {
		body["options"] = options
	}
	return body
}

func (h *gmtoolWriteHarness) p7Grant(t *testing.T, body map[string]any) (int, map[string]any) {
	t.Helper()
	return h.post(t, "/api/characters/1/items", map[string]string{"id": "1"}, body, h.server.handleItemGrant)
}

// p7SingleMail 取出邮箱里唯一那封邮件。
func p7SingleMail(t *testing.T, h *gmtoolWriteHarness) dnfrepo.MailRecord {
	t.Helper()
	mails := h.mailboxMails(t)
	if len(mails) != 1 {
		t.Fatalf("邮箱里有 %d 封邮件，期望 1 封：%+v", len(mails), mails)
	}
	for _, mail := range mails {
		return mail
	}
	return dnfrepo.MailRecord{}
}

func p7Attachment(t *testing.T, mail dnfrepo.MailRecord) dnfrepo.MailAttachment {
	t.Helper()
	if len(mail.Attachments) != 1 {
		t.Fatalf("邮件附件数 = %d，期望 1：%+v", len(mail.Attachments), mail)
	}
	return mail.Attachments[0]
}

// p7NormalizedMail 把 CreatedAt 归零：两次调用之间唯一必然不同的字段就是墙钟时间，
// 其余（标题/正文/附件 ItemID/Count/ExpireAt/Extra/RawEntry/来源元数据）必须逐字节相同。
func p7NormalizedMail(mail dnfrepo.MailRecord) dnfrepo.MailRecord {
	mail.CreatedAt = time.Time{}
	return mail
}

// p7MailHash 是"邮件行内容哈希"：拒绝路径要求它不变，默认档要求它与不带 option 时相同。
func p7MailHash(t *testing.T, mail dnfrepo.MailRecord) string {
	t.Helper()
	raw, err := json.Marshal(p7NormalizedMail(mail))
	if err != nil {
		t.Fatalf("marshal mail: %v", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func p7Marshal(t *testing.T, payload any) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return string(raw)
}

// 所有 option 真正会写出来的键（默认档一个都不许出现）。
var p7OptionExtraKeys = []string{
	"expire_time", "expire_unix", "expiration_source", "gm_expire_days",
	"reinforce", "ext_data0", "amplify_type", "byte_13", "quality_seed",
	"byte_57", "value_57",
}

// ---------------------------------------------------------------------------
// 开关
// ---------------------------------------------------------------------------

func TestP7MailOptionsSwitchOnlyAcceptsExplicitOn(t *testing.T) {
	for _, value := range []string{"on", "ON", "On", " on ", "\ton\n"} {
		if !GMMailOptionsFromEnv(value) {
			t.Fatalf("GMMailOptionsFromEnv(%q) = false, want true", value)
		}
	}
	for _, value := range []string{"", " ", "true", "1", "yes", "enable", "enabled", "off", "0", "no"} {
		if GMMailOptionsFromEnv(value) {
			t.Fatalf("GMMailOptionsFromEnv(%q) = true, want false（本仓开关只认字面 on）", value)
		}
	}
	t.Setenv(gmMailOptionsEnv, "")
	if GMMailOptionsFromEnvironment() {
		t.Fatal("空值必须是 off")
	}
	t.Setenv(gmMailOptionsEnv, "true")
	if GMMailOptionsFromEnvironment() {
		t.Fatal("`true` 不算 on")
	}
	t.Setenv(gmMailOptionsEnv, "on")
	if !GMMailOptionsFromEnvironment() {
		t.Fatal("显式 on 必须生效")
	}
}

// ---------------------------------------------------------------------------
// #4 默认档：4 个 option 被完全忽略
// ---------------------------------------------------------------------------

func TestP7MailOptionsDefaultIgnored(t *testing.T) {
	t.Setenv(gmMailOptionsEnv, "")

	baseline := p7Harness(t)
	status, response := baseline.p7Grant(t, p7GrantBody(p7SwordID, 1, "p7-off-baseline-0001", nil))
	requireOK(t, status, response)
	baseMail := p7SingleMail(t, baseline)

	withOptions := p7Harness(t)
	status, response = withOptions.p7Grant(t, p7GrantBody(p7SwordID, 1, "p7-off-options-0001", map[string]any{
		"expire_days":       30,
		"reinforce":         12,
		"amplify_type":      3,
		"equipment_quality": "top",
		// 锻造也是 option 之一（J13-F）：off 档同样一个字节都不许写。
		"forging_level": 5,
	}))
	requireOK(t, status, response)
	optionMail := p7SingleMail(t, withOptions)

	if got, want := p7MailHash(t, optionMail), p7MailHash(t, baseMail); got != want {
		t.Fatalf("默认档带了 option 的邮件哈希 %s != 不带 option 的 %s", got, want)
	}
	if !reflect.DeepEqual(p7NormalizedMail(baseMail), p7NormalizedMail(optionMail)) {
		t.Fatalf("默认档邮件被 option 改动了:\nbase=%+v\noptions=%+v", baseMail, optionMail)
	}
	attachment := p7Attachment(t, optionMail)
	for _, key := range p7OptionExtraKeys {
		if _, has := attachment.Extra[key]; has {
			t.Fatalf("默认档不该写 option 键 %q：%+v", key, attachment.Extra)
		}
	}
	if !attachment.ExpireAt.IsZero() {
		t.Fatalf("默认档不该改到期时间：%s", attachment.ExpireAt)
	}
	if attachment.RawEntry != nil {
		t.Fatalf("默认档不该造 0x77 行：%d 字节", len(attachment.RawEntry))
	}
	// 发放端既有证据（PVF 耐久）必须还在。
	if attachment.Extra["durability"] != strconv.Itoa(p7SwordDur) {
		t.Fatalf("耐久证据丢了：%+v", attachment.Extra)
	}
}

// 默认档连"装备 count=2 / 越界 / 旧字段名"都必须照发 —— 新门禁只在 on 档生效，
// 否则 off 档就不是"今天的字节"了。
func TestP7MailOptionsDefaultIgnoresInvalidRequests(t *testing.T) {
	t.Setenv(gmMailOptionsEnv, "")

	baseline := p7Harness(t)
	status, response := baseline.p7Grant(t, p7GrantBody(p7SwordID, 2, "p7-off-two-baseline-0001", nil))
	requireOK(t, status, response)
	baseMail := p7SingleMail(t, baseline)

	junk := p7Harness(t)
	status, response = junk.p7Grant(t, p7GrantBody(p7SwordID, 2, "p7-off-two-junk-0001", map[string]any{
		"expire_days":       99999,
		"reinforce":         99,
		"amplify_type":      9,
		"equipment_quality": "legend",
		// 改动前的 give.js 就是发这些字段名的（服务端一直忽略它们）。
		"qualityMode":    1,
		"upgradeLevel":   5,
		"forgingLevel":   3,
		"expirationDays": 7,
	}))
	requireOK(t, status, response)
	junkMail := p7SingleMail(t, junk)

	if p7MailHash(t, junkMail) != p7MailHash(t, baseMail) {
		t.Fatalf("默认档对越界/未知字段必须一个字节都不改:\nbase=%+v\njunk=%+v", baseMail, junkMail)
	}
	if got := p7Attachment(t, junkMail).Count; got != 2 {
		t.Fatalf("默认档装备 count=2 必须照发（今天的字节），实际 count=%d", got)
	}
}

// ---------------------------------------------------------------------------
// #5 装备 + count != 1 -> 拒绝（UP gm.go:1805）
// ---------------------------------------------------------------------------

func TestP7EquipmentMailCountMustBeOne(t *testing.T) {
	t.Setenv(gmMailOptionsEnv, "on")
	harness := p7Harness(t)
	before := harness.mailbox(t)

	status, response := harness.p7Grant(t, p7GrantBody(p7SwordID, 2, "p7-on-count-two-0001", nil))
	message := requireFailure(t, status, response)
	if !strings.Contains(message, "一封邮件只能发 1 件") {
		t.Fatalf("拒绝文案没有点名原因：%q", message)
	}
	harness.requireUnchanged(t, before) // 零写入
	fields := harness.eventFields(t, "gm-item-grant-options-rejected")
	if fields["reason"] != "equipment_count_not_one" {
		t.Fatalf("结构化 reason = %v, want equipment_count_not_one（全部字段 %+v）", fields["reason"], fields)
	}
	if fields["result"] != "rejected" {
		t.Fatalf("审计事件 result = %v, want rejected", fields["result"])
	}

	// 堆叠物不受这条门禁约束（UP:1805 只判装备）。
	stackable := p7Harness(t)
	status, response = stackable.p7Grant(t, p7GrantBody(p7PotionID, 3, "p7-on-potion-three-0001", nil))
	requireOK(t, status, response)
	if got := p7Attachment(t, p7SingleMail(t, stackable)).Count; got != 3 {
		t.Fatalf("堆叠物 count=3 应当放行，实际 %d", got)
	}
}

// ---------------------------------------------------------------------------
// #6 边界与词表（UP gm.go:1809-1812）
// ---------------------------------------------------------------------------

func TestP7MailOptionsBounds(t *testing.T) {
	sword := p7Entry(t, p7SwordID)
	// 门禁是 *Server 上的方法（锻造那一条要读 PVF 的 [separate upgrade max]）。
	server := p7Harness(t).server
	cases := []struct {
		name       string
		options    gmMailGrantOptions
		wantReason string
	}{
		{"全零值", gmMailGrantOptions{}, ""},
		{"expire_days 下界", gmMailGrantOptions{ExpireDays: 0}, ""},
		{"expire_days 上界", gmMailGrantOptions{ExpireDays: gmMailExpireDaysMax}, ""},
		{"reinforce 上界", gmMailGrantOptions{Reinforce: gmMailReinforceMax}, ""},
		{"amplify_type 上界", gmMailGrantOptions{AmplifyType: gmMailAmplifyTypeMax}, ""},
		{"品级 default", gmMailGrantOptions{EquipmentQuality: "DEFAULT"}, ""},
		{"品级 random", gmMailGrantOptions{EquipmentQuality: "Random"}, ""},
		{"品级 top 带空白", gmMailGrantOptions{EquipmentQuality: " top "}, ""},
		{"锻造 0（不介入）", gmMailGrantOptions{ForgingLevel: 0}, ""},
		{"锻造上界（测试档 7）", gmMailGrantOptions{ForgingLevel: 7}, ""},
		{"expire_days 负数", gmMailGrantOptions{ExpireDays: -1}, "out_of_range"},
		{"expire_days 超上界", gmMailGrantOptions{ExpireDays: gmMailExpireDaysMax + 1}, "out_of_range"},
		{"reinforce 负数", gmMailGrantOptions{Reinforce: -1}, "out_of_range"},
		{"reinforce 超上界", gmMailGrantOptions{Reinforce: gmMailReinforceMax + 1}, "out_of_range"},
		{"amplify_type 负数", gmMailGrantOptions{AmplifyType: -1}, "out_of_range"},
		{"amplify_type 超上界", gmMailGrantOptions{AmplifyType: gmMailAmplifyTypeMax + 1}, "out_of_range"},
		{"品级不在词表", gmMailGrantOptions{EquipmentQuality: "legend"}, "invalid_quality"},
		{"品级是数字", gmMailGrantOptions{EquipmentQuality: "1"}, "invalid_quality"},
		// 锻造：上限来自 PVF（测试档 7），不是真档的 8、更不是历史常量 8。
		{"锻造超 PVF 上界", gmMailGrantOptions{ForgingLevel: 8}, "forging_level_out_of_range"},
		{"锻造负数", gmMailGrantOptions{ForgingLevel: -1}, "forging_level_out_of_range"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			rejection := server.validateGMMailGrantOptions(sword, 1, testCase.options)
			if testCase.wantReason == "" {
				if rejection != nil {
					t.Fatalf("应当放行，实际被拒：%s（%s）", rejection.reason, rejection.message)
				}
				return
			}
			if rejection == nil {
				t.Fatalf("应当被拒（%s），实际放行", testCase.wantReason)
			}
			if rejection.reason != testCase.wantReason {
				t.Fatalf("reason = %s, want %s（%s）", rejection.reason, testCase.wantReason, rejection.message)
			}
			if strings.TrimSpace(rejection.message) == "" {
				t.Fatal("拒绝必须带可读原因")
			}
		})
	}

	// HTTP 面：越界必须明确失败 + 零写入（不是 200 里塞个 success:true）。
	t.Setenv(gmMailOptionsEnv, "on")
	harness := p7Harness(t)
	before := harness.mailbox(t)
	status, response := harness.p7Grant(t, p7GrantBody(p7SwordID, 1, "p7-on-oob-0001", map[string]any{
		"expire_days": 3651,
	}))
	message := requireFailure(t, status, response)
	if !strings.Contains(message, "expire_days") || !strings.Contains(message, "未做任何写入") {
		t.Fatalf("拒绝文案没有点名边界或零写入：%q", message)
	}
	harness.requireUnchanged(t, before)
	if fields := harness.eventFields(t, "gm-item-grant-options-rejected"); fields["reason"] != "out_of_range" {
		t.Fatalf("结构化 reason = %v, want out_of_range", fields["reason"])
	}
}

// 未知字段（改动前的 give.js 就在发 qualityMode/upgradeLevel/...）必须明确失败，
// 不能被静默丢掉 —— 那等于"operator 选的品级被吃了，却回一句发放成功"。
func TestP7MailOptionsRejectUnknownFields(t *testing.T) {
	t.Setenv(gmMailOptionsEnv, "on")
	harness := p7Harness(t)
	before := harness.mailbox(t)

	status, response := harness.p7Grant(t, p7GrantBody(p7SwordID, 1, "p7-on-unknown-0001", map[string]any{
		"qualityMode": 1,
	}))
	message := requireFailure(t, status, response)
	if !strings.Contains(message, "qualityMode") {
		t.Fatalf("拒绝文案没有点名未知字段：%q", message)
	}
	if !strings.Contains(message, "expire_days") {
		t.Fatalf("拒绝文案没有给出正确字段名：%q", message)
	}
	harness.requireUnchanged(t, before)
	if fields := harness.eventFields(t, "gm-item-grant-options-rejected"); fields["reason"] != "options_unparsable" {
		t.Fatalf("结构化 reason = %v, want options_unparsable", fields["reason"])
	}
	// 空对象 / null 都是合法的"没有 option"。
	for index, options := range []any{map[string]any{}, nil} {
		fresh := p7Harness(t)
		status, response := fresh.p7Grant(t, p7GrantBody(
			p7SwordID, 1, "p7-on-empty-000"+strconv.Itoa(index), options))
		requireOK(t, status, response)
	}
}

// ---------------------------------------------------------------------------
// #7 装备专属 option 只对"支持类型的装备"生效（UP gm.go:1815-1821）
// ---------------------------------------------------------------------------

func TestP7MailOptionsEquipmentOnlyAttributes(t *testing.T) {
	stackable := p7Entry(t, p7PotionID)
	title := p7Entry(t, p7TitleID)
	sword := p7Entry(t, p7SwordID)
	coat := p7Entry(t, p7CoatID)
	server := p7Harness(t).server

	cases := []struct {
		name       string
		entry      itemEntry
		options    gmMailGrantOptions
		wantReason string
	}{
		{"堆叠物 + 强化", stackable, gmMailGrantOptions{Reinforce: 1}, "attributes_require_equipment"},
		{"堆叠物 + 增幅", stackable, gmMailGrantOptions{AmplifyType: 1}, "attributes_require_equipment"},
		{"堆叠物 + 品级 top", stackable, gmMailGrantOptions{EquipmentQuality: "top"}, "attributes_require_equipment"},
		{"称号 + 品级 top", title, gmMailGrantOptions{EquipmentQuality: "top"}, "attributes_unsupported_type"},
		{"称号 + 强化", title, gmMailGrantOptions{Reinforce: 5}, "attributes_unsupported_type"},
		{"称号 + 增幅", title, gmMailGrantOptions{AmplifyType: 2}, "attributes_unsupported_type"},
		// 上游只把 "非空且非 default 的品级"算作装备专属请求：default / 空串对任何道具都合法，
		// expire_days 对任何道具都合法（UP:1815 与 :1843）。
		{"堆叠物 + 品级 default", stackable, gmMailGrantOptions{EquipmentQuality: "default"}, ""},
		{"堆叠物 + 品级空串", stackable, gmMailGrantOptions{EquipmentQuality: " "}, ""},
		{"堆叠物 + 期限", stackable, gmMailGrantOptions{ExpireDays: 30}, ""},
		{"称号 + 品级 default", title, gmMailGrantOptions{EquipmentQuality: "default"}, ""},
		{"称号 + 期限", title, gmMailGrantOptions{ExpireDays: 7}, ""},
		{"武器 + 全套", sword, gmMailGrantOptions{
			ExpireDays: 30, Reinforce: 12, AmplifyType: 3, EquipmentQuality: "top", ForgingLevel: 7,
		}, ""},
		// 锻造只对武器开门（vendor InventorySeparateUpgradeService.cs:70-74）。上衣这类
		// **支持强化/增幅/品级**的装备也必须被具名拒绝，而且报的是锻造那条原因
		// （不是 attributes_unsupported_type —— 上衣支持那三样，只是不能锻造）。
		{"上衣 + 锻造", coat, gmMailGrantOptions{ForgingLevel: 3}, "forging_not_weapon"},
		{"称号 + 锻造", title, gmMailGrantOptions{ForgingLevel: 3}, "forging_not_weapon"},
		{"堆叠物 + 锻造", stackable, gmMailGrantOptions{ForgingLevel: 3}, "forging_not_weapon"},
		{"时装(avatar) + 锻造", itemEntry{ID: 9, Name: "测试时装", Kind: "equipment", Tag: "coat avatar"},
			gmMailGrantOptions{ForgingLevel: 1}, "forging_not_weapon"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			rejection := server.validateGMMailGrantOptions(testCase.entry, 1, testCase.options)
			if testCase.wantReason == "" {
				if rejection != nil {
					t.Fatalf("应当放行，实际被拒：%s（%s）", rejection.reason, rejection.message)
				}
				return
			}
			if rejection == nil {
				t.Fatalf("应当被拒（%s），实际放行", testCase.wantReason)
			}
			if rejection.reason != testCase.wantReason {
				t.Fatalf("reason = %s, want %s（%s）", rejection.reason, testCase.wantReason, rejection.message)
			}
		})
	}

	// HTTP 面：堆叠物带强化 -> 明确失败 + 零写入 + 结构化 reason。
	t.Setenv(gmMailOptionsEnv, "on")
	harness := p7Harness(t)
	before := harness.mailbox(t)
	status, response := harness.p7Grant(t, p7GrantBody(p7PotionID, 1, "p7-on-potion-reinforce-0001", map[string]any{
		"reinforce": 5,
	}))
	message := requireFailure(t, status, response)
	if !strings.Contains(message, "只能发给装备") {
		t.Fatalf("拒绝文案没有点名原因：%q", message)
	}
	harness.requireUnchanged(t, before)
	if fields := harness.eventFields(t, "gm-item-grant-options-rejected"); fields["reason"] != "attributes_require_equipment" {
		t.Fatalf("结构化 reason = %v, want attributes_require_equipment", fields["reason"])
	}

	// 堆叠物只带 expire_days：放行，且到期时间真的落进附件。
	fresh := p7Harness(t)
	status, response = fresh.p7Grant(t, p7GrantBody(p7PotionID, 1, "p7-on-potion-expire-0001", map[string]any{
		"expire_days": 30,
	}))
	requireOK(t, status, response)
	attachment := p7Attachment(t, p7SingleMail(t, fresh))
	if attachment.ExpireAt.IsZero() || attachment.Extra["expiration_source"] != gmExpirationSource {
		t.Fatalf("堆叠物 expire_days 没有生效：%+v expireAt=%s", attachment.Extra, attachment.ExpireAt)
	}
	if attachment.Extra["gm_expire_days"] != "30" {
		t.Fatalf("gm_expire_days = %q, want 30", attachment.Extra["gm_expire_days"])
	}
}

// ---------------------------------------------------------------------------
// on 档：四个 option 真正落地（UP gm.go:1843-1889）
// ---------------------------------------------------------------------------

func TestP7MailOptionsProjection(t *testing.T) {
	t.Setenv(gmMailOptionsEnv, "on")
	harness := p7Harness(t)
	before := time.Now().UTC()
	status, response := harness.p7Grant(t, p7GrantBody(p7SwordID, 1, "p7-on-full-0001", map[string]any{
		"expire_days":       30,
		"reinforce":         12,
		"amplify_type":      3,
		"equipment_quality": "top",
	}))
	after := time.Now().UTC()
	requireOK(t, status, response)

	attachment := p7Attachment(t, p7SingleMail(t, harness))
	earliest := before.Add(30 * 24 * time.Hour)
	latest := after.Add(30 * 24 * time.Hour)
	if attachment.ExpireAt.Before(earliest) || attachment.ExpireAt.After(latest) {
		t.Fatalf("到期时间 %s 不在 [%s, %s] 里（应当 = 发放时刻 + 30 天）",
			attachment.ExpireAt, earliest, latest)
	}
	expireUnix := strconv.FormatInt(attachment.ExpireAt.Unix(), 10)
	if attachment.Extra["expire_time"] != expireUnix || attachment.Extra["expire_unix"] != expireUnix {
		t.Fatalf("expire_time/expire_unix = %q/%q, want %q",
			attachment.Extra["expire_time"], attachment.Extra["expire_unix"], expireUnix)
	}
	if attachment.Extra["expiration_source"] != gmExpirationSource {
		t.Fatalf("expiration_source = %q, want %q", attachment.Extra["expiration_source"], gmExpirationSource)
	}
	if attachment.Extra["gm_expire_days"] != "30" {
		t.Fatalf("gm_expire_days = %q, want 30", attachment.Extra["gm_expire_days"])
	}
	if attachment.Extra["reinforce"] != "12" || attachment.Extra["ext_data0"] != "12" {
		t.Fatalf("reinforce/ext_data0 = %q/%q, want 12/12",
			attachment.Extra["reinforce"], attachment.Extra["ext_data0"])
	}
	if attachment.Extra["amplify_type"] != "3" || attachment.Extra["byte_13"] != "3" {
		t.Fatalf("amplify_type/byte_13 = %q/%q, want 3/3",
			attachment.Extra["amplify_type"], attachment.Extra["byte_13"])
	}
	if attachment.Extra["quality_seed"] != strconv.FormatUint(uint64(itemquality.TopSeed), 10) {
		t.Fatalf("quality_seed = %q, want top 哨兵 %d",
			attachment.Extra["quality_seed"], itemquality.TopSeed)
	}
	// 发放端既有证据不许被覆盖
	if attachment.Extra["durability"] != strconv.Itoa(p7SwordDur) ||
		attachment.Extra["max_durability"] != strconv.Itoa(p7SwordDur) {
		t.Fatalf("option 把耐久证据冲掉了：%+v", attachment.Extra)
	}
	// 审计事件如实回报生效的 option
	fields := harness.eventFields(t, "gm-item-grant-options")
	if fields["applied"] != "expire_days,reinforce,amplify_type,equipment_quality" {
		t.Fatalf("applied = %v（全部字段 %+v）", fields["applied"], fields)
	}
	if fields["quality_seed"] != itemquality.TopSeed {
		t.Fatalf("审计事件里的 quality_seed = %v, want %d", fields["quality_seed"], itemquality.TopSeed)
	}
}

// ---------------------------------------------------------------------------
// J13-F：锻造（forging_level）—— 修的就是"填了等于没填"那个 bug
// ---------------------------------------------------------------------------

// 现场 bug：面板渲染了「锻造」输入框、giveConfigMemory 也记住了值，但组装请求时
// 从来不发它（give.js 改动前 :623 的注释说"服务端不认识 forgingLevel"），于是
// 操作者填了锻造、按发放，收到的是一件锻造 0 的装备，且**没有任何提示**。
// 这条用例钉死：on 档 forging_level 真的落进附件 Extra 的 byte_57/value_57。
func TestP7ForgingOptionAppliesToWeapon(t *testing.T) {
	t.Setenv(gmMailOptionsEnv, "on")
	harness := p7Harness(t)
	status, response := harness.p7Grant(t, p7GrantBody(p7SwordID, 1, "p7-forging-sword-0001", map[string]any{
		"forging_level": 7,
	}))
	requireOK(t, status, response)

	attachment := p7Attachment(t, p7SingleMail(t, harness))
	// 锻造落点唯一：Extra 的 byte_57 / value_57（0x77 行的 0x57 由这两个键生成，
	// 见 inventory/body.go:461；发放出去的附件本来就没有 0x77 行）。
	if attachment.Extra["byte_57"] != "7" || attachment.Extra["value_57"] != "7" {
		t.Fatalf("锻造没有落进附件 Extra：byte_57=%q value_57=%q（全部 %+v）",
			attachment.Extra["byte_57"], attachment.Extra["value_57"], attachment.Extra)
	}
	if attachment.RawEntry != nil {
		t.Fatalf("发放路径不该凭空造 0x77 行：%d 字节", len(attachment.RawEntry))
	}
	// 发放端既有证据不许被锻造冲掉。
	if attachment.Extra["durability"] != strconv.Itoa(p7SwordDur) {
		t.Fatalf("耐久证据丢了：%+v", attachment.Extra)
	}
	fields := harness.eventFields(t, "gm-item-grant-options")
	if fields["applied"] != "forging_level" {
		t.Fatalf("applied = %v, want forging_level（全部字段 %+v）", fields["applied"], fields)
	}
	if fields["forging_level"] != 7 || fields["forging_row_offset"] != itemcfgForgingRowOffset {
		t.Fatalf("审计事件没有如实回报锻造：%+v", fields)
	}

	// 0 = 不介入（与 reinforce/amplify_type 同口径）：一个锻造键都不写。
	fresh := p7Harness(t)
	status, response = fresh.p7Grant(t, p7GrantBody(p7SwordID, 1, "p7-forging-zero-0001", map[string]any{
		"forging_level": 0,
	}))
	requireOK(t, status, response)
	zero := p7Attachment(t, p7SingleMail(t, fresh))
	for _, key := range itemcfgForgingExtraKeys {
		if _, has := zero.Extra[key]; has {
			t.Fatalf("forging_level=0 不该写 %q：%+v", key, zero.Extra)
		}
	}
	if fresh.hasEvent("gm-item-grant-options") {
		t.Fatalf("forging_level=0 不该记成一次改动：%v", fresh.eventKinds())
	}
}

// 锻造失败必须**明确失败 + 点名原因 + 零写入**（三条具名原因各一例）。
func TestP7ForgingOptionRejectionsAreNamedAndWriteNothing(t *testing.T) {
	t.Setenv(gmMailOptionsEnv, "on")

	t.Run("非武器", func(t *testing.T) {
		harness := p7Harness(t)
		before := harness.mailbox(t)
		status, response := harness.p7Grant(t, p7GrantBody(p7CoatID, 1, "p7-forging-coat-0001", map[string]any{
			"forging_level": 3,
		}))
		message := requireFailure(t, status, response)
		if !strings.Contains(message, itemcfgErrForgingNotWeapon.Error()) {
			t.Fatalf("拒绝文案没有点名原因（%q）：%q", itemcfgErrForgingNotWeapon.Error(), message)
		}
		if !strings.Contains(message, "未做任何写入") {
			t.Fatalf("拒绝文案没有说明零写入：%q", message)
		}
		harness.requireUnchanged(t, before)
		fields := harness.eventFields(t, "gm-item-grant-options-rejected")
		if fields["reason"] != "forging_not_weapon" {
			t.Fatalf("结构化 reason = %v, want forging_not_weapon（全部 %+v）", fields["reason"], fields)
		}
	})

	t.Run("超出PVF上限", func(t *testing.T) {
		harness := p7Harness(t)
		before := harness.mailbox(t)
		// 测试档的 [separate upgrade max] 是 7（真档 8）：8 必须被拒。
		status, response := harness.p7Grant(t, p7GrantBody(p7SwordID, 1, "p7-forging-oob-0001", map[string]any{
			"forging_level": 8,
		}))
		message := requireFailure(t, status, response)
		if !strings.Contains(message, itemcfgErrForgingLevelOutOfRange.Error()) ||
			!strings.Contains(message, "0..7") {
			t.Fatalf("拒绝文案没有点名上限（必须来自 PVF 的 0..7）：%q", message)
		}
		harness.requireUnchanged(t, before)
		if fields := harness.eventFields(t, "gm-item-grant-options-rejected"); fields["reason"] != "forging_level_out_of_range" {
			t.Fatalf("结构化 reason = %v, want forging_level_out_of_range", fields["reason"])
		}
	})

	t.Run("读不到锻造表", func(t *testing.T) {
		source := p7Source()
		delete(source, itemcfgSeparateUpgradePVFPath) // 没有 etc/upgrade_separate.etc
		harness := p7HarnessWithSource(t, source)
		before := harness.mailbox(t)
		status, response := harness.p7Grant(t, p7GrantBody(p7SwordID, 1, "p7-forging-notable-0001", map[string]any{
			"forging_level": 1,
		}))
		message := requireFailure(t, status, response)
		if !strings.Contains(message, itemcfgErrForgingTableUnavailable.Error()) {
			t.Fatalf("拒绝文案没有点名锻造表：%q", message)
		}
		harness.requireUnchanged(t, before)
		if fields := harness.eventFields(t, "gm-item-grant-options-rejected"); fields["reason"] != "forging_table_unavailable" {
			t.Fatalf("结构化 reason = %v, want forging_table_unavailable", fields["reason"])
		}
	})
}

// 能力块必须如实报"能不能锻造"：前端 give.js:545 只在 canForge 为真时才渲染那个
// 输入框、上限直接读 maxForgingLevel。报错了就是"框都不出来"（今天的 canForge 恒
// false）或者"框出来了但一填就被服务端拒"。
func TestP7GrantOptionsForgingCapability(t *testing.T) {
	t.Setenv(gmMailOptionsEnv, "on")
	harness := p7Harness(t)

	status, response := harness.p7GrantOptions(t, strconv.Itoa(p7SwordID))
	requireOK(t, status, response)
	equipment := responseMap(t, response, "equipment")
	if equipment["canForge"] != true {
		t.Fatalf("武器的 canForge = %v, want true（否则前端根本不渲染锻造输入框）", equipment["canForge"])
	}
	// 上限必须来自 PVF（测试档 7，真档 8）—— api_items.go off 档那个历史常量 8 不许泄进 on 档。
	if equipment["maxForgingLevel"] != float64(7) ||
		equipment["forgingPVFPath"] != itemcfgSeparateUpgradePVFPath {
		t.Fatalf("maxForgingLevel/forgingPVFPath = %v/%v, want 7/%s",
			equipment["maxForgingLevel"], equipment["forgingPVFPath"], itemcfgSeparateUpgradePVFPath)
	}
	if equipment["currentForgingLevel"] != float64(0) {
		t.Fatalf("新发放实例的 currentForgingLevel = %v, want 0", equipment["currentForgingLevel"])
	}

	// 上衣（支持强化/增幅/品级，但不能锻造）：canForge=false + 上限 0，前端于是不渲染那个框。
	status, response = harness.p7GrantOptions(t, strconv.Itoa(p7CoatID))
	requireOK(t, status, response)
	coat := responseMap(t, response, "equipment")
	if coat["canForge"] != false || coat["maxForgingLevel"] != float64(0) {
		t.Fatalf("上衣的锻造能力 = canForge:%v maxForgingLevel:%v, want false/0",
			coat["canForge"], coat["maxForgingLevel"])
	}
	if coat["canUpgrade"] != true {
		t.Fatalf("上衣的 canUpgrade = %v, want true（锻造不可用不代表强化不可用）", coat["canUpgrade"])
	}

	// 读不到锻造表：如实报 false/0，并留一条具名降级日志（绝不假装上限是 8）。
	source := p7Source()
	delete(source, itemcfgSeparateUpgradePVFPath)
	degraded := p7HarnessWithSource(t, source)
	status, response = degraded.p7GrantOptions(t, strconv.Itoa(p7SwordID))
	requireOK(t, status, response)
	blind := responseMap(t, response, "equipment")
	if blind["canForge"] != false || blind["maxForgingLevel"] != float64(0) {
		t.Fatalf("读不到锻造表时的能力 = canForge:%v maxForgingLevel:%v, want false/0",
			blind["canForge"], blind["maxForgingLevel"])
	}
	if fields := degraded.eventFields(t, "gm-item-grant-capability-degraded"); fields["reason"] != "forging_table_unavailable" {
		t.Fatalf("降级日志 reason = %v, want forging_table_unavailable", fields["reason"])
	}
}

// 锻造值必须能穿过**邮件投递**那条路：发放写的是附件 Extra，领取端把它原样带进
// 玩家背包行，客户端再从 Extra 生成 0x77 行的 0x57。
//
// 领取发生在 dnfbridge/mail（gmtool 包里没有那条路径可跑），所以这里按本仓既有的
// "源码级交叉断言"手法（同 TestP7QualitySeedProjectionMatchesFork、P4b 的前端静态检查）
// 钉住这条链的三个环节：
//
//  1. 发放侧写的就是 Extra 的 byte_57/value_57   -> itemcfgSetForgingLevel
//  2. 领取侧 mail/owner.go mailboxAttachmentStack 只删三个 mailbox_* 信封键，
//     其余 Extra **逐字保留**（不是白名单重建）
//  3. 装备在领取时**不可能被并进已有的一格**（PVF 侧装备上限恒为 1，dnfbridge 强制），
//     所以锻造值不会被"并到另一件白板武器上"这件事吃掉
//  4. 客户端行 inventory/body.go writeCommonItemListEntry 从 Extra 生成 0x57
func TestP7ForgingOptionSurvivesMailClaimProjection(t *testing.T) {
	readSource := func(relative string) string {
		t.Helper()
		path := filepath.Join(p7GmtoolDir(t), "..", "..", relative)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读 %s: %v", relative, err)
		}
		return string(raw)
	}

	// 1. 发放侧的唯一写点（同一函数被在线配置接口复用）。
	writer := readSource(filepath.Join("services", "gmtool", "api_items_configure.go"))
	if !strings.Contains(writer, `stack.Extra[key] = text`) ||
		!strings.Contains(writer, `itemcfgForgingExtraKeys = []string{"byte_57", "value_57"}`) {
		t.Fatal("itemcfgSetForgingLevel 不再写 Extra 的 byte_57/value_57：邮件投递链的第一环断了")
	}

	// 2. 领取侧：附件 Extra -> 背包行 Extra（只删信封键）。
	claim := readSource(filepath.Join("modules", "dnf", "mail", "owner.go"))
	for _, marker := range []string{
		"func mailboxAttachmentStack(",
		"extra := cloneStringMap(attachment.Extra)",
		`delete(extra, "mailbox_pvf_kind")`,
		`delete(extra, "mailbox_equipment_type")`,
		`delete(extra, "mailbox_pvf_path")`,
	} {
		if !strings.Contains(claim, marker) {
			t.Fatalf("mailboxAttachmentStack 不再逐字保留附件 Extra（缺 %q）：锻造会在领取时被丢掉", marker)
		}
	}

	// 3. 装备不会被并栈：PVF 侧装备上限恒为 1（dnfbridge/mailbox_pvf.go），
	//    领取时"这一格已满就不并"（mail/owner.go）。两段一起保证附件上的锻造值
	//    不会被并进另一件同款装备的那一格。
	equipmentLimit := readSource(filepath.Join("services", "dnfbridge", "mailbox_pvf.go"))
	if !strings.Contains(equipmentLimit, "if definition.Kind == dungeonDropItemEquipment {\n\t\tstackLimit = 1\n\t}") {
		t.Fatal("dnfbridge 不再强制装备 stackLimit=1：锻造过的装备可能被并进白板那一格，锻造值就丢了")
	}
	if !strings.Contains(claim, "if existing.Count >= resolution.StackLimit {\n\t\t\t\tcontinue\n\t\t\t}") {
		t.Fatal("领取时不再按 StackLimit 拒绝并栈：锻造过的装备可能被并进已满的那一格")
	}

	// 4. 客户端行：Extra 的 byte_57/value_57 -> 0x77 行的 0x57。
	row := readSource(filepath.Join("modules", "dnf", "inventory", "body.go"))
	if !strings.Contains(row, `entry[0x57] = clampByte(firstExtraInt(extra, 0, "byte_57", "value_57"))`) {
		t.Fatal("inventory/body.go 不再从 Extra 的 byte_57/value_57 生成行 0x57：客户端读不到锻造")
	}

	// 端到端对照：发放侧写出来的键 == 客户端读的键 == itemcfg 读的键。
	entry := p7Entry(t, p7SwordID)
	projection, err := p7Harness(t).server.applyGMMailGrantOptions(entry,
		map[string]string{grantExtraKeyPVFKind: "equipment"}, gmMailGrantOptions{ForgingLevel: 5}, time.Now().UTC())
	if err != nil {
		t.Fatalf("applyGMMailGrantOptions: %v", err)
	}
	for _, key := range itemcfgForgingExtraKeys {
		if projection.extra[key] != "5" {
			t.Fatalf("发放投影没有写 %q=%q：%+v", key, projection.extra[key], projection.extra)
		}
	}
	claimed := dnfrepo.ItemStack{ItemID: int64(entry.ID), Count: 1, Extra: cloneGrantExtra(projection.extra)}
	if got := itemcfgForgingLevelOf(claimed); got != 5 {
		t.Fatalf("领取后按游戏自己的读器读锻造 = %d, want 5", got)
	}
}

// 随机品级：种子必须落在 itemquality 的合法区间，且不是 top 哨兵。
func TestP7MailOptionsRandomQualitySeed(t *testing.T) {
	t.Setenv(gmMailOptionsEnv, "on")
	harness := p7Harness(t)
	status, response := harness.p7Grant(t, p7GrantBody(p7SwordID, 1, "p7-on-random-0001", map[string]any{
		"equipment_quality": "random",
	}))
	requireOK(t, status, response)
	raw := p7Attachment(t, p7SingleMail(t, harness)).Extra["quality_seed"]
	seed, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 32)
	if err != nil {
		t.Fatalf("quality_seed = %q 不是整数：%v", raw, err)
	}
	if !itemquality.Valid(uint32(seed)) {
		t.Fatalf("quality_seed = %d 不在 itemquality.Valid 的区间里", seed)
	}
	if uint32(seed) >= itemquality.TopSeed {
		t.Fatalf("随机品级不该拿到 top 哨兵：%d", seed)
	}
}

// ---------------------------------------------------------------------------
// #9 到期覆盖是纯日期计算（计划书 §4.7 第 9 条）
// ---------------------------------------------------------------------------

func TestP7GMExpirationOverrideIsPureDate(t *testing.T) {
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	expiration := base.Add(30 * 24 * time.Hour)

	t.Run("零值到期时间原样返回", func(t *testing.T) {
		stack := dnfrepo.ItemStack{ItemID: 1, Extra: map[string]string{"durability": "10"}}
		got, changed := applyCurrentGMExpirationOverrideAt(stack, time.Time{}, 30, base)
		if changed {
			t.Fatal("零值到期时间不该产生变更")
		}
		if !reflect.DeepEqual(got.Extra, stack.Extra) || !got.ExpireAt.IsZero() {
			t.Fatalf("零值到期时间被改动了：%+v", got)
		}
		// Unix <= 0 同样原样返回（UP:274-276）。
		negative := time.Unix(-1, 0).UTC()
		if _, changed := applyCurrentGMExpirationOverrideAt(stack, negative, 30, base); changed {
			t.Fatal("Unix<=0 的到期时间不该产生变更")
		}
	})

	t.Run("写入的就是入参时间", func(t *testing.T) {
		got, changed := applyCurrentGMExpirationOverrideAt(dnfrepo.ItemStack{ItemID: 1}, expiration, 30, base)
		if !changed {
			t.Fatal("应当报告变更")
		}
		if !got.ExpireAt.Equal(expiration) {
			t.Fatalf("ExpireAt = %s, want %s", got.ExpireAt, expiration)
		}
		if got.Extra["expire_time"] != strconv.FormatInt(expiration.Unix(), 10) ||
			got.Extra["expire_unix"] != strconv.FormatInt(expiration.Unix(), 10) {
			t.Fatalf("记账键不对：%+v", got.Extra)
		}
		if got.Extra["expiration_source"] != gmExpirationSource || got.Extra["gm_expire_days"] != "30" {
			t.Fatalf("来源/天数记错了：%+v", got.Extra)
		}
		// 调用方的 map 不许被就地改写（UP:281-285 同样先 clone）。
		original := map[string]string{"durability": "10"}
		input := dnfrepo.ItemStack{ItemID: 1, Extra: original}
		_, _ = applyCurrentGMExpirationOverrideAt(input, expiration, 30, base)
		if len(original) != 1 {
			t.Fatalf("调用方的 Extra 被就地改写了：%+v", original)
		}
	})

	t.Run("输出只依赖入参（不读时钟 / 不读 PVF / 幂等）", func(t *testing.T) {
		input := dnfrepo.ItemStack{ItemID: 7, Extra: map[string]string{"item_kind": "equipment"}}
		first, _ := applyCurrentGMExpirationOverrideAt(input, expiration, 30, base)
		// now 换成完全不同的值（以及零值）：结果必须逐字段相同 —— 这就是"纯函数"的意思。
		second, _ := applyCurrentGMExpirationOverrideAt(input, expiration, 30, base.Add(5000*time.Hour))
		third, _ := applyCurrentGMExpirationOverrideAt(input, expiration, 30, time.Time{})
		if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(first, third) {
			t.Fatalf("结果依赖了 now：\nfirst=%+v\nsecond=%+v\nthird=%+v", first, second, third)
		}
		// 幂等：再跑一次不产生新的键。
		again, changed := applyCurrentGMExpirationOverrideAt(first, expiration, 30, base)
		if changed {
			t.Fatalf("同输入重跑应当无变更：%+v", again.Extra)
		}
		if !reflect.DeepEqual(first.Extra, again.Extra) {
			t.Fatalf("同输入重跑结果不稳定：%+v vs %+v", first.Extra, again.Extra)
		}
	})

	t.Run("expire_days=0 完全不介入", func(t *testing.T) {
		// 调用点只在 ExpireDays > 0 时才调用覆盖函数（与 UP:1843 一致），所以这里断言的是
		// 发放路径的行为：on 档但不选期限 -> 一个 option 键都不写、ExpireAt 为零。
		t.Setenv(gmMailOptionsEnv, "on")
		harness := p7Harness(t)
		status, response := harness.p7Grant(t, p7GrantBody(p7SwordID, 1, "p7-on-zero-expire-0001", map[string]any{
			"expire_days":       0,
			"reinforce":         0,
			"amplify_type":      0,
			"equipment_quality": "default",
		}))
		requireOK(t, status, response)
		attachment := p7Attachment(t, p7SingleMail(t, harness))
		for _, key := range []string{"expire_time", "expire_unix", "expiration_source", "gm_expire_days"} {
			if _, has := attachment.Extra[key]; has {
				t.Fatalf("expire_days=0 不该写 %q：%+v", key, attachment.Extra)
			}
		}
		if !attachment.ExpireAt.IsZero() {
			t.Fatalf("expire_days=0 必须保持 PVF 默认过期（ExpireAt 零值），实际 %s", attachment.ExpireAt)
		}
		if attachment.RawEntry != nil {
			t.Fatalf("expire_days=0 不该造 0x77 行：%d 字节", len(attachment.RawEntry))
		}
	})

	t.Run("函数体不读时钟、不碰服务器与 PVF", func(t *testing.T) {
		function := p7ParseFunction(t, "api_items_grant_options.go", "applyCurrentGMExpirationOverrideAt")
		if function.Recv != nil {
			t.Fatal("必须是自由函数（纯计算），不能挂在 *Server 上")
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.Ident:
				if typed.Name == "s" {
					t.Error("函数体引用了服务器变量 s")
				}
			case *ast.CallExpr:
				selector, ok := typed.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				owner, ok := selector.X.(*ast.Ident)
				if !ok {
					return true
				}
				if owner.Name == "time" && selector.Sel.Name == "Now" {
					t.Error("函数体读了系统时钟（time.Now）")
				}
				if owner.Name == "os" {
					t.Error("函数体碰了环境变量")
				}
				switch selector.Sel.Name {
				case "ReadText", "Parse", "source", "grantPVFItemDocument":
					t.Errorf("函数体碰了 PVF：%s", selector.Sel.Name)
				}
			}
			return true
		})
	})
}

// ---------------------------------------------------------------------------
// #8 品级种子投影与 dnfbridge 用的是同一份实现（计划书 §4.7 第 8 条）
// ---------------------------------------------------------------------------

func TestP7QualitySeedProjectionMatchesFork(t *testing.T) {
	// (a) top 哨兵：dnfbridge/equipment_quality.go:12 的 currentEquipmentTopQualitySeed
	//     就是 itemquality.TopSeed（跨包不能引用未导出符号，所以从源码侧断言"同一份实现"）。
	raw, err := os.ReadFile(filepath.Join(p7GmtoolDir(t), "..", "dnfbridge", "equipment_quality.go"))
	if err != nil {
		t.Fatalf("读 dnfbridge/equipment_quality.go: %v", err)
	}
	fork := string(raw)
	for _, marker := range []string{
		"currentEquipmentTopQualitySeed         uint32 = itemquality.TopSeed",
		"return itemquality.NewRandomSeed()",
		"return itemquality.Apply(stack, seed)",
	} {
		if !strings.Contains(fork, marker) {
			t.Fatalf("dnfbridge 的品级投影不再是 itemquality 的一行委托，缺少：%s", marker)
		}
	}
	if itemquality.TopSeed != 999999998 {
		t.Fatalf("itemquality.TopSeed = %d, want 999999998（与 dnfbridge 同一常量）", itemquality.TopSeed)
	}

	// (b) 本包的 top 投影与直接调用 itemquality.Apply（= applyCurrentEquipmentQualitySeed 的实现体）
	//     逐键相同。
	entry := p7Entry(t, p7SwordID)
	base := map[string]string{
		grantExtraKeyPVFKind:       "equipment",
		grantExtraKeyEquipmentType: "weapon",
		grantExtraKeyDurability:    strconv.Itoa(p7SwordDur),
		grantExtraKeyMaxDurability: strconv.Itoa(p7SwordDur),
		grantExtraKeyPVFPath:       "weapon/sword.equ",
	}
	projection, err := p7Harness(t).server.applyGMMailGrantOptions(entry, base, gmMailGrantOptions{EquipmentQuality: "top"}, time.Now().UTC())
	if err != nil {
		t.Fatalf("applyGMMailGrantOptions: %v", err)
	}
	reference := dnfrepo.ItemStack{ItemID: int64(entry.ID), Count: 1, Extra: cloneGrantExtra(base)}
	if err := itemquality.Apply(&reference, itemquality.TopSeed); err != nil {
		t.Fatalf("itemquality.Apply: %v", err)
	}
	if !reflect.DeepEqual(projection.extra, reference.Extra) {
		t.Fatalf("品级投影与 itemquality.Apply 不等价：\nprojection=%+v\nitemquality=%+v",
			projection.extra, reference.Extra)
	}
	if projection.qualitySeed != itemquality.TopSeed {
		t.Fatalf("projection.qualitySeed = %d, want %d", projection.qualitySeed, itemquality.TopSeed)
	}

	// (c) 本包不得自己发明种子常量/算法。
	own, err := os.ReadFile(filepath.Join(p7GmtoolDir(t), "api_items_grant_options.go"))
	if err != nil {
		t.Fatalf("读 api_items_grant_options.go: %v", err)
	}
	source := string(own)
	if strings.Contains(source, "999999998") {
		t.Fatal("api_items_grant_options.go 里出现了硬编码的 top 哨兵——必须走 itemquality.TopSeed")
	}
	if strings.Contains(source, "func newCurrentEquipmentQualitySeed") ||
		strings.Contains(source, "func applyCurrentEquipmentQualitySeed") {
		t.Fatal("gmtool 里另写了一套品级种子投影——必须复用 itemquality")
	}
	for _, marker := range []string{"itemquality.TopSeed", "itemquality.NewRandomSeed()", "itemquality.Apply("} {
		if !strings.Contains(source, marker) {
			t.Fatalf("api_items_grant_options.go 没有复用 %s", marker)
		}
	}
}

// ---------------------------------------------------------------------------
// 能力块（路由 33）：off 档逐字节不变，on 档如实报能力
// ---------------------------------------------------------------------------

// p7LegacyGrantOptionsPayload 是 api_items.go 改动前 handleGrantOptions 的响应原文
// （逐字复制，只是把 map 字面量搬到测试里当参照实现）。
func p7LegacyGrantOptionsPayload(panelID int, entry itemEntry) map[string]any {
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
	payload["success"] = true
	return payload
}

func (h *gmtoolWriteHarness) p7GrantOptions(t *testing.T, templateID string) (int, map[string]any) {
	t.Helper()
	return h.get(t, "/api/characters/1/items/"+templateID+"/grant-options",
		map[string]string{"id": "1", "templateId": templateID}, h.server.handleGrantOptions)
}

func TestP7GrantOptionsCapabilityByteIdenticalWhenOff(t *testing.T) {
	t.Setenv(gmMailOptionsEnv, "")
	harness := p7Harness(t)
	status, response := harness.p7GrantOptions(t, strconv.Itoa(p7SwordID))
	requireOK(t, status, response)

	got := p7Marshal(t, response)
	want := p7Marshal(t, p7LegacyGrantOptionsPayload(1, p7Entry(t, p7SwordID)))
	if got != want {
		t.Fatalf("默认档能力块变了：\ngot =%s\nwant=%s", got, want)
	}
}

func TestP7GrantOptionsCapabilityReportsSupportWhenOn(t *testing.T) {
	t.Setenv(gmMailOptionsEnv, "on")
	harness := p7Harness(t)

	status, response := harness.p7GrantOptions(t, strconv.Itoa(p7SwordID))
	requireOK(t, status, response)
	equipment := responseMap(t, response, "equipment")
	for _, key := range []string{"supportsQuality", "canUpgrade", "canAmplify"} {
		if equipment[key] != true {
			t.Fatalf("武器（[weapon]）的 %s = %v, want true", key, equipment[key])
		}
	}
	if equipment["maxUpgradeLevel"] != float64(gmMailReinforceMax) {
		t.Fatalf("maxUpgradeLevel = %v, want %d（= 服务端接受的 reinforce 上界）",
			equipment["maxUpgradeLevel"], gmMailReinforceMax)
	}
	quality := responseList(t, equipment, "qualityOptions")
	if len(quality) != 3 {
		t.Fatalf("qualityOptions = %+v, want default/random/top 三项", quality)
	}
	wantQuality := []string{gmQualityDefault, gmQualityRandom, gmQualityTop}
	for index, value := range wantQuality {
		entry := quality[index].(map[string]any)
		if entry["value"] != value {
			t.Fatalf("qualityOptions[%d].value = %v, want %s（服务端只认这三个 token）",
				index, entry["value"], value)
		}
	}
	// 锻造：武器 + PVF 可读 ⇒ canForge=true，上限取 [separate upgrade max]（测试档 7）。
	// 前端 give.js:545 靠这一个布尔位决定渲不渲染那个输入框。
	if equipment["canForge"] != true || equipment["maxForgingLevel"] != float64(7) {
		t.Fatalf("武器的锻造能力 = canForge:%v maxForgingLevel:%v, want true/7",
			equipment["canForge"], equipment["maxForgingLevel"])
	}
	expiration := responseMap(t, response, "expiration")
	if expiration["canOverride"] != true {
		t.Fatalf("expiration.canOverride = %v, want true（on 档 expire_days 可用）", expiration["canOverride"])
	}

	// 称号不在 UP:1948-1955 的支持类型表里：能力块必须如实说"不支持"，
	// 否则前端会渲染出注定被服务端拒绝的控件。
	status, response = harness.p7GrantOptions(t, strconv.Itoa(p7TitleID))
	requireOK(t, status, response)
	titleEquipment := responseMap(t, response, "equipment")
	for _, key := range []string{"supportsQuality", "canUpgrade", "canAmplify"} {
		if titleEquipment[key] != false {
			t.Fatalf("称号（[title name]）的 %s = %v, want false", key, titleEquipment[key])
		}
	}
	// 非武器（含称号/上衣）一律不可锻：能力块报 false/0，前端于是不渲染那个框。
	if titleEquipment["canForge"] != false || titleEquipment["maxForgingLevel"] != float64(0) {
		t.Fatalf("称号的锻造能力 = canForge:%v maxForgingLevel:%v, want false/0",
			titleEquipment["canForge"], titleEquipment["maxForgingLevel"])
	}
}

// ---------------------------------------------------------------------------
// 前端：只发服务端认识的字段 + 失败即服务端原文
// ---------------------------------------------------------------------------

func p7GiveScript(t *testing.T) string {
	t.Helper()
	raw, err := embeddedWWW.ReadFile("wwwroot/js/give.js")
	if err != nil {
		t.Fatalf("内嵌前端资产 wwwroot/js/give.js: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("wwwroot/js/give.js 是空的")
	}
	return string(raw)
}

// p7StripJSComments 去掉整行 `//` 注释与 `/* */` 块注释。
//
// 下面两条"前端到底发了什么"的断言必须看**代码**：直接对原文做 Contains 的话，
// 把 `options.forging_level = forging;` 那一行注释掉（或塞进注释里）依旧能骗过它们
// —— 本批的反假绿正是用这一步抓到的（本测试的第一版实现就漏了这一点）。
func p7StripJSComments(source string) string {
	var kept strings.Builder
	for _, line := range strings.Split(source, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		kept.WriteString(line)
		kept.WriteString("\n")
	}
	stripped := kept.String()
	for {
		start := strings.Index(stripped, "/*")
		if start < 0 {
			break
		}
		end := strings.Index(stripped[start:], "*/")
		if end < 0 {
			break
		}
		stripped = stripped[:start] + stripped[start+end+2:]
	}
	return stripped
}

func TestP7GiveScriptSendsOnlyUpstreamOptionFields(t *testing.T) {
	script := p7GiveScript(t)
	code := p7StripJSComments(script)
	for _, want := range []string{
		"options.equipment_quality =",
		"options.reinforce =",
		"options.amplify_type =",
		"options.expire_days =",
		// J13-F：锻造那个输入框的值必须真的发出去（bug 修复前它被静默丢掉）。
		"options.forging_level =",
		// 控件由服务端能力块驱动（off 档这几个布尔位是 false，渲染结果与改动前一致）
		"equipment.supportsQuality",
		"equipment.canUpgrade",
		"equipment.canAmplify",
		"equipment.canForge",
		"capability.expiration.canOverride",
	} {
		if !strings.Contains(code, want) {
			t.Fatalf("give.js 的**代码**里缺少 %s（注释里的不算）", want)
		}
	}
	// 服务端 on 档对未知字段是明确拒绝 —— 前端绝不能再发旧的驼峰字段名。
	for _, banned := range []string{
		"options.qualityMode", "options.upgradeLevel", "options.amplifyType",
		"options.forgingLevel", "options.expirationDays",
	} {
		if strings.Contains(code, banned) {
			t.Fatalf("give.js 仍在发送服务端不认识的字段：%s", banned)
		}
	}
	// 一个 option 都没选时不带 options（请求体与改动前逐字节相同）。
	if !strings.Contains(code, "if (options && Object.keys(options).length > 0) body.options = options;") {
		t.Fatal("give.js 必须在 options 为空时不带 options 字段")
	}
	// 期限只在"自定义天数"时才发。
	if !strings.Contains(code, "expirationMode.value === 'custom'") {
		t.Fatal("give.js 必须在期限方式为 custom 时才发 expire_days")
	}
}

// 前端发的 option 键与服务端词表（gmMailGrantOptionKeys）必须**双向**一致。
//
// 这就是本批这个 bug 的一般形式：give.js 曾经发 qualityMode/upgradeLevel/forgingLevel
// 这套驼峰名，而服务端只认四个 snake_case 键（多一个就整单拒绝）；修好之后又反过来
// 漏了一个 —— 面板渲染了「锻造」输入框，组装请求时却不带它，操作者填了等于没填。
// 两侧任何一边再加/改名，这条用例都会红。
func TestP7GiveScriptOptionKeysMatchServerVocabulary(t *testing.T) {
	script := p7GiveScript(t)
	body := p7StripJSComments(p7FunctionBody(t, script, "async function submitConfiguredGrant"))
	sent := map[string]bool{}
	for _, match := range regexp.MustCompile(`options\.([A-Za-z_][A-Za-z0-9_]*)\s*=`).FindAllStringSubmatch(body, -1) {
		sent[match[1]] = true
	}
	if len(sent) == 0 {
		t.Fatal("在 submitConfiguredGrant 的**代码**里一个 options.<key> = 都没抽到（提取正则与前端写法失配）")
	}
	for key := range sent {
		if !gmGrantOptionKeyKnown(key) {
			t.Fatalf("give.js 发送了服务端词表里没有的 option %q（on 档会被整单拒绝）：服务端只认 %v",
				key, gmMailGrantOptionKeys)
		}
	}
	for _, key := range gmMailGrantOptionKeys {
		if !sent[key] {
			t.Fatalf("服务端接受 option %q，但 give.js 从来不发送它 —— 面板上的控件会变成摆设（本次 bug 的形状）", key)
		}
	}
	// 锻造那个键尤其要点名：它正是被丢掉的那一个。
	if !sent["forging_level"] {
		t.Fatal("give.js 没有发送 forging_level（锻造输入框的值仍会被静默丢掉）")
	}
}

func gmGrantOptionKeyKnown(key string) bool {
	for _, known := range gmMailGrantOptionKeys {
		if known == key {
			return true
		}
	}
	return false
}

func TestP7GiveScriptDoesNotFakeSuccess(t *testing.T) {
	script := p7GiveScript(t)
	body := p7FunctionBody(t, script, "async function giveItem")
	postIndex := strings.Index(body, "await post(")
	if postIndex < 0 {
		t.Fatal("giveItem 里没有 await post(...)")
	}
	// 成功提示只能出现在请求返回之后。
	if loadedIndex := strings.Index(body, "toast(`${itemLabel}"); loadedIndex < 0 || loadedIndex < postIndex {
		t.Fatalf("成功提示出现在请求之前：post=%d toast=%d", postIndex, loadedIndex)
	}
	catchIndex := strings.Index(body, "} catch (e) {")
	if catchIndex < 0 {
		t.Fatal("giveItem 没有 catch 分支")
	}
	tail := body[catchIndex:]
	if !strings.Contains(tail, "toast(e.message, true)") {
		t.Fatalf("失败分支没有显示服务端原文：%s", tail)
	}
	if strings.Contains(tail, "成功") {
		t.Fatalf("失败分支里出现了「成功」字样（伪造成功）：%s", tail)
	}
}

// p7FunctionBody 抽出 `async function X` 的函数体（到下一个顶层 `}` 为止）。
func p7FunctionBody(t *testing.T, script string, marker string) string {
	t.Helper()
	start := strings.Index(script, marker)
	if start < 0 {
		t.Fatalf("脚本里没有 %q", marker)
	}
	rest := script[start:]
	end := strings.Index(rest, "\n}\n")
	if end < 0 {
		t.Fatalf("%q 没有找到结尾", marker)
	}
	return rest[:end]
}

// p7GmtoolDir 定位本包源码目录（用于源码级断言）。
func p7GmtoolDir(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(currentFile)
}

// p7ParseFunction 解析本包某个源文件里的一个函数声明。
func p7ParseFunction(t *testing.T, fileName string, name string) *ast.FuncDecl {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(p7GmtoolDir(t), fileName), nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", fileName, err)
	}
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != name {
			continue
		}
		return function
	}
	t.Fatalf("%s 里没有函数 %s", fileName, name)
	return nil
}

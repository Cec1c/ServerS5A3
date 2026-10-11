package quest

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 90CN 真档 Script.pvf（sha256 cc517a6e…aacee1）的 n_quest/quest.lst 里，
// [name] / [depend message] / [condition message] / [solve message] 四段的实测
// 存在率（用 cmd/scratch/pvfsections 遍历 2996 条得出）：
//
//	[name]              present=2741 nonEmpty=2733
//	[depend message]    present=2589 nonEmpty=2567
//	[condition message] present=2993 nonEmpty=2974
//	[solve message]     present=2636 nonEmpty=2614
//
// 也就是说这四段**确实存在**，但**不是每条都有**。缺段必须留空，绝不能合成文案。

func presentationQuest(name, depend, condition, solve string) string {
	var builder strings.Builder
	builder.WriteString("[grade]\n`[epic]`\n[type]\n`[quest clear]`\n[level]\n88 99\n")
	if name != "" {
		builder.WriteString("[name]\n`" + name + "`\n")
	}
	if depend != "" {
		builder.WriteString("[depend message]\n`" + depend + "`\n")
	}
	if condition != "" {
		builder.WriteString("[condition message]\n`" + condition + "`\n")
	}
	if solve != "" {
		builder.WriteString("[solve message]\n`" + solve + "`\n")
	}
	return builder.String()
}

func loadPresentationCatalog(t *testing.T, source catalogTestSource) *Catalog {
	t.Helper()
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatalf("build test PVF: %v", err)
	}
	catalog, err := Load(context.Background(), index)
	if err != nil {
		t.Fatalf("load test catalog: %v", err)
	}
	return catalog
}

func TestDefinitionPresentationFieldsReadFromPVFSections(t *testing.T) {
	catalog := loadPresentationCatalog(t, catalogTestSource{
		DefaultList: "3971 `presentation.qst`\n",
		"n_quest/presentation.qst": presentationQuest(
			"离别时刻",
			"第一段\n第二段",
			"    完成以下的全部任务。",
			"伤害 +20%% 与 100%% 命中",
		),
	})
	definition, found := catalog.Find(3971)
	if !found {
		t.Fatal("quest 3971 is missing from the test catalog")
	}
	if definition.Name != "离别时刻" {
		t.Fatalf("[name] = %q, want 离别时刻", definition.Name)
	}
	if definition.DependMessage != "第一段\n第二段" {
		t.Fatalf("[depend message] = %q, want 两段以换行连接", definition.DependMessage)
	}
	// 展示文本会裁剪首尾空白 —— 真档里 [condition message] 就带前导空格。
	if definition.ConditionMessage != "完成以下的全部任务。" {
		t.Fatalf("[condition message] = %q, want 已裁剪的 完成以下的全部任务。", definition.ConditionMessage)
	}
	// `%%` 是 PVF 的转义写法，展示时必须还原成单个 %。
	if definition.SolveMessage != "伤害 +20% 与 100% 命中" {
		t.Fatalf("[solve message] = %q, want %% 已还原为 %%", definition.SolveMessage)
	}
}

func TestDefinitionPresentationFieldsEmptyWhenSectionAbsent(t *testing.T) {
	catalog := loadPresentationCatalog(t, catalogTestSource{
		DefaultList:               "1 `bare.qst`\n",
		"n_quest/bare.qst":        presentationQuest("", "", "", ""),
		"n_quest/blank_only.qst":  "[name]\n`   `\n[depend message]\n`  `\n",
		"n_quest/unrelated.qst":   "[grade]\n`[normal]`\n",
		"n_quest/not_in_list.qst": "[name]\n`不应出现`\n",
	})
	definition, found := catalog.Find(1)
	if !found {
		t.Fatal("quest 1 is missing")
	}
	if definition.Name != "" || definition.DependMessage != "" ||
		definition.ConditionMessage != "" || definition.SolveMessage != "" {
		t.Fatalf("quest without presentation sections must stay empty, got name=%q depend=%q condition=%q solve=%q",
			definition.Name, definition.DependMessage, definition.ConditionMessage, definition.SolveMessage)
	}
}

func TestQuestPresentationTextIsWhitespaceOnlySafe(t *testing.T) {
	document, err := dnfpvf.Parse("x.qst", "[depend message]\n`   `\n[solve message]\n`a%%b`\n`  `\n`c`\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := questPresentationText(document, "depend message"); got != "" {
		t.Fatalf("whitespace-only section must collapse to empty, got %q", got)
	}
	if got := questPresentationText(document, "solve message"); got != "a%b\nc" {
		t.Fatalf("multi-entry section = %q, want a%%b\\nc", got)
	}
	if got := questPresentationText(document, "missing section"); got != "" {
		t.Fatalf("absent section must be empty, got %q", got)
	}
	if got := questPresentationText(nil, "name"); got != "" {
		t.Fatalf("nil document must be empty, got %q", got)
	}
}

func TestCatalogSearchMatchesNamePathAndQuestID(t *testing.T) {
	catalog := loadPresentationCatalog(t, catalogTestSource{
		DefaultList:         "10 `alpha.qst`\n20 `beta.qst`\n30 `gamma.qst`\n",
		"n_quest/alpha.qst": presentationQuest("离别时刻", "", "", ""),
		"n_quest/beta.qst":  presentationQuest("", "", "", ""),
		"n_quest/gamma.qst": presentationQuest("另一个任务", "", "", ""),
	})

	if ids := searchIDs(catalog.Search("离别", 10)); !reflect.DeepEqual(ids, []int64{10}) {
		t.Fatalf("search by [name] = %v, want [10]", ids)
	}
	// [name] 缺失的任务只能靠路径命中——这是降级，不是造名字。
	if ids := searchIDs(catalog.Search("beta", 10)); !reflect.DeepEqual(ids, []int64{20}) {
		t.Fatalf("search by path = %v, want [20]", ids)
	}
	if ids := searchIDs(catalog.Search("30", 10)); !reflect.DeepEqual(ids, []int64{30}) {
		t.Fatalf("search by numeric id = %v, want [30]", ids)
	}
	// 空 query 返回第一页（面板浏览用），保持目录顺序。
	if ids := searchIDs(catalog.Search("", 2)); !reflect.DeepEqual(ids, []int64{10, 20}) {
		t.Fatalf("empty query first page = %v, want [10 20]", ids)
	}
	// limit 钳制：<=0 -> 30，>200 -> 200（与上游 catalog.Search 同一处口径）。
	if got := len(catalog.Search("", 0)); got != 3 {
		t.Fatalf("default limit returned %d rows, want all 3", got)
	}
	if ids := searchIDs(catalog.Search("不存在的任务名", 10)); len(ids) != 0 {
		t.Fatalf("no-match query = %v, want empty", ids)
	}
	var nilCatalog *Catalog
	if got := nilCatalog.Search("x", 10); got != nil {
		t.Fatalf("nil catalog search must return nil, got %v", got)
	}
}

func searchIDs(definitions []Definition) []int64 {
	out := make([]int64, 0, len(definitions))
	for _, definition := range definitions {
		out = append(out, definition.ID)
	}
	return out
}

// TestRealScriptPVFQuest3971PresentationFields 用真档 PVF 证明补读生效，而不是
// 硬编码：3971 的 [name] == 离别时刻（既有用例已断言），三段展示文本逐段断言其
// 真实开头。需要 DNFBRIDGE_REAL_PVF_SMOKE 指向 Script.pvf 才会跑。
func TestRealScriptPVFQuest3971PresentationFields(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the quest presentation sections against the runtime PVF")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	index, err := dnfpvf.Build(context.Background(), archive, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	definition, found := catalog.Find(3971)
	if !found {
		t.Fatal("real PVF quest 3971 is missing")
	}
	if strings.TrimSpace(definition.Name) != "离别时刻" {
		t.Fatalf("quest 3971 Definition.Name=%q, want 离别时刻", definition.Name)
	}
	if !strings.HasPrefix(definition.DependMessage, "卢克大人拯救了异面族") {
		t.Fatalf("quest 3971 [depend message] prefix=%q", firstRunes(definition.DependMessage, 20))
	}
	if definition.ConditionMessage != "完成以下的全部任务。" {
		t.Fatalf("quest 3971 [condition message]=%q, want 完成以下的全部任务。", definition.ConditionMessage)
	}
	if !strings.HasPrefix(definition.SolveMessage, "给我安静！") {
		t.Fatalf("quest 3971 [solve message] prefix=%q", firstRunes(definition.SolveMessage, 20))
	}
	// 反向证据：这些字段不可能来自任何硬编码表 —— 同一份目录里必须有任务缺 [name]。
	missing := 0
	for _, candidate := range catalog.ordered {
		if strings.TrimSpace(candidate.Name) == "" {
			missing++
		}
	}
	if missing == 0 {
		t.Fatal("real catalog has no quest without [name]: the empty-name path is untested")
	}
	t.Logf("real catalog: %d/%d quests have no [name] (empty-name path is live)",
		missing, len(catalog.ordered))
}

func firstRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

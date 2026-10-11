package main

import (
	"bytes"
	"crypto/sha256"
	"flag"
	"fmt"
	"longheng.io/server/internal/modules/dnf/premium"
	doc "longheng.io/server/internal/modules/dnf/pvf"
	archive "longheng.io/server/internal/platform/pvf"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const newPath = "stackable/custom/black_diamond_30day.stk"

func key(text string) archive.ScriptToken { return archive.ScriptToken{Type: 3, Text: text} }
func str(text string) archive.ScriptToken { return archive.ScriptToken{Type: 6, Text: text} }
func num(value int64) archive.ScriptToken { return archive.ScriptToken{Type: 0, Value: int32(value)} }
func main() {
	input := flag.String("input", "", "source PVF")
	output := flag.String("output", "", "new PVF (must differ)")
	flag.Parse()
	if err := run(*input, *output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(input, output string) error {
	in, e := filepath.Abs(input)
	if e != nil {
		return e
	}
	out, e := filepath.Abs(output)
	if e != nil {
		return e
	}
	if input == "" || output == "" || strings.EqualFold(in, out) {
		return fmt.Errorf("separate input and output required")
	}
	a, e := archive.Open(in)
	if e != nil {
		return e
	}
	list, e := a.ReadText("stackable/stackable.lst")
	if e != nil {
		return e
	}
	if regexp.MustCompile(fmt.Sprintf(`\b%d\b`, premium.BlackDiamondThirtyDayItemID)).MatchString(list) {
		return fmt.Errorf("item id already exists")
	}
	premiumText, e := a.ReadText("etc/premiumlist_new.etc")
	if e != nil {
		return e
	}
	if regexp.MustCompile(`\[type\]\s*17\b`).MatchString(premiumText) {
		return fmt.Errorf("premium type 17 already exists")
	}
	template := "stackable/event/chn_event_20140819_spend_time/chn_mat_blackdiamond_ticket.stk"
	text, e := a.ReadText(template)
	if e != nil {
		return e
	}
	d, e := doc.Parse(template, text)
	if e != nil {
		return e
	}
	name, _ := d.Text("name")
	explain, _ := d.Text("explain")
	flavor, _ := d.Text("flavor text")
	name = strings.Trim(name, "` ")
	explain = strings.Trim(explain, "` ")
	flavor = strings.Trim(flavor, "` ")
	additions := []archive.ScriptAddition{
		{Path: newPath, TemplatePath: template, Strings: []archive.ScriptStringChange{
			{Old: name, New: "黑钻贵族契约 (30天)"},
			{Old: explain, New: "使用后开通30天黑钻贵族，重复使用延长有效期。疲劳上限188点，通关经验增加10%，可使用普通及高级黑钻售货机，并获得黑钻额外翻牌。每日06:00刷新疲劳及疲劳药水使用次数。"},
			{Old: flavor, New: "黑钻贵族特权"}, {Old: "[material]", New: "[waste]"}}, RemoveSections: []string{"[expiration date]"}},
		{Path: "stackable/stackable.lst", AppendToExisting: true, AppendTokens: []archive.ScriptToken{num(premium.BlackDiamondThirtyDayItemID), str("custom/black_diamond_30day.stk")}},
		{Path: "etc/premiumlist_new.etc", AppendToExisting: true, AppendTokens: []archive.ScriptToken{
			key("[type]"), num(premium.TypeBlackDiamond), key("[target item]"), num(1),
			key("[item]"), num(premium.BlackDiamondThirtyDayItemID), key("[term]"), num(30), key("[/term]"), key("[/item]"),
			key("[/type]")}},
	}
	patched, e := a.RepackScriptAdditions(additions)
	if e != nil {
		return e
	}
	b, e := archive.OpenBytes(patched)
	if e != nil {
		return e
	}
	if len(b.Files()) != len(a.Files())+1 {
		return fmt.Errorf("file count mismatch")
	}
	result, e := b.ReadText(newPath)
	if e != nil {
		return e
	}
	if !strings.Contains(result, "黑钻贵族契约 (30天)") || !strings.Contains(result, "[waste]") || strings.Contains(result, "[expiration date]") {
		return fmt.Errorf("item verification failed")
	}
	for _, path := range []string{"stackable/stackable.lst", "etc/premiumlist_new.etc"} {
		old, _ := a.ReadRaw(path)
		current, _ := b.ReadRaw(path)
		if !bytes.HasPrefix(current, old) {
			return fmt.Errorf("existing catalog bytes changed: %s", path)
		}
	}
	if e = os.WriteFile(out, patched, 0644); e != nil {
		return e
	}
	fmt.Printf("item_id=%d type=%d duration_days=30 files=%d sha256=%x output=%s\n", premium.BlackDiamondThirtyDayItemID, premium.TypeBlackDiamond, len(b.Files()), sha256.Sum256(patched), out)
	return nil
}

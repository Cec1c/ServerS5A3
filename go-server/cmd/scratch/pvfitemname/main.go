// Command pvfitemname 把 Script.pvf 里的"桩名字"道具补成正式名称/说明。
//
// 走 platformpvf 的 PatchScriptString（按文档作用域重建字符串分块），
// 不使用有损的 PvfRawDump/PvfPatch/PvfPackDir 文本往返；默认不覆盖输入文件，
// 只产出候选 PVF，由调用方核对哈希后再决定是否部署。
//
// 用法：
//
//	go run ./cmd/scratch/pvfitemname -pvf /opt/dnf90/data/dnf/Script.pvf -out /root/out.pvf
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

type edit struct {
	path    string
	old     string
	replace string
}

// 当前批次：爆竹产出的礼盒（运行时 PVF 里 name/explain 都是占位桩，
// 客户端因此显示 name_490701733 / explain_490701733）。
var scriptEdits = []edit{
	{
		path:    "stackable/cash/chn_490701733.stk",
		old:     "name_490701733",
		replace: "春节欢庆爆竹礼盒",
	},
	{
		path:    "stackable/cash/chn_490701733.stk",
		old:     "explain_490701733",
		replace: "春节欢庆爆竹开出的礼盒，打开后可以获得奖励。",
	},
}

func main() {
	pvfPath := flag.String("pvf", "", "source Script.pvf")
	outPath := flag.String("out", "", "candidate output path")
	flag.Parse()
	if *pvfPath == "" || *outPath == "" {
		panic("both -pvf and -out are required")
	}

	current, err := os.ReadFile(*pvfPath)
	must(err)
	inputSize := len(current)
	inputSum := sha256.Sum256(current)

	for _, e := range scriptEdits {
		archive, err := platformpvf.OpenBytes(current)
		must(err)
		if _, err := archive.ReadText(e.path); err != nil {
			panic(fmt.Errorf("source PVF has no %s: %w", e.path, err))
		}
		patched, report, err := archive.PatchScriptString([]string{e.path}, e.old, e.replace)
		must(err)
		fmt.Printf("patch %s: %q -> %q report=%+v\n", e.path, e.old, e.replace, report)
		current = patched
	}

	verified, err := platformpvf.OpenBytes(current)
	must(err)
	text, err := verified.ReadText(scriptEdits[0].path)
	must(err)
	for _, e := range scriptEdits {
		if !strings.Contains(text, e.replace) {
			panic(fmt.Errorf("verified %s is missing %q", e.path, e.replace))
		}
		if strings.Contains(text, e.old) {
			panic(fmt.Errorf("verified %s still contains %q", e.path, e.old))
		}
	}
	// 只允许目标文档变化：其它文档必须逐字节一致。
	for _, probe := range []string{
		"stackable/cash/chn_490701730.stk",
		"stackable/cash/creature/rename_card.stk",
		"itemshop/itemshop.lst",
	} {
		before, errBefore := platformpvf.OpenBytes(mustRead(*pvfPath))
		must(errBefore)
		after, errAfter := platformpvf.OpenBytes(current)
		must(errAfter)
		beforeText, err := before.ReadText(probe)
		must(err)
		afterText, err := after.ReadText(probe)
		must(err)
		if beforeText != afterText {
			panic(fmt.Errorf("unrelated document %s changed", probe))
		}
	}

	must(os.WriteFile(*outPath, current, 0o644))
	outSum := sha256.Sum256(current)
	fmt.Printf("input  %s %d bytes sha256=%s\n", *pvfPath, inputSize, strings.ToUpper(hex.EncodeToString(inputSum[:])))
	fmt.Printf("output %s %d bytes sha256=%s\n", *outPath, len(current), strings.ToUpper(hex.EncodeToString(outSum[:])))
	fmt.Printf("name/explain now: %s\n", firstLines(text, "name_", "explain_"))
}

func mustRead(path string) []byte {
	data, err := os.ReadFile(path)
	must(err)
	return data
}

// firstLines 打印改名后的 name/explain 两行，方便直接核对。
func firstLines(text string, keys ...string) string {
	out := make([]string, 0, len(keys))
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		for _, key := range keys {
			if strings.HasPrefix(trimmed, key) {
				out = append(out, trimmed)
			}
		}
	}
	return strings.Join(out, " | ")
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

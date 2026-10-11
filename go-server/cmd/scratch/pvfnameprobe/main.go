// Command pvfnameprobe 探测 Script.pvf 字符串池里"已经存在"的候选文案，并为桩名字道具
// 挑一个可用的正式名（PatchScriptString 只能把文档指向池子里已有的字符串，
// 不能凭空新增——这是线上实测出来的硬约束）。
//
//	go run ./cmd/scratch/pvfnameprobe -pvf /opt/dnf90/data/dnf/Script.pvf -out /root/out.pvf
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

const itemDoc = "stackable/cash/chn_490701733.stk"

// 按偏好排序：越靠前越接近"春节欢庆爆竹礼盒"。
var nameCandidates = []string{
	"春节欢庆爆竹礼盒",
	"春节欢庆礼盒",
	"春节礼盒",
	"爆竹礼盒",
	"春节欢庆爆竹",
	"爆竹",
	"礼盒",
}

var explainCandidates = []string{
	"春节欢庆爆竹开出的礼盒，打开后可以获得奖励。",
	"春节欢庆爆竹礼盒",
	"打开后可以获得奖励。",
	"春节欢庆爆竹",
	"礼盒",
}

func main() {
	pvfPath := flag.String("pvf", "", "source Script.pvf")
	outPath := flag.String("out", "", "candidate output path")
	stubName := flag.String("stub-name", "name_490701733", "placeholder name string in the PVF")
	stubExplain := flag.String("stub-explain", "explain_490701733", "placeholder explain string in the PVF")
	flag.Parse()
	if *pvfPath == "" || *outPath == "" {
		panic("both -pvf and -out are required")
	}

	current, err := os.ReadFile(*pvfPath)
	must(err)
	inputSum := sha256.Sum256(current)
	fmt.Printf("input %s %d bytes sha256=%s\n", *pvfPath, len(current), strings.ToUpper(hex.EncodeToString(inputSum[:])))

	nameBytes, name, log := pick(current, itemDoc, *stubName, nameCandidates)
	fmt.Print(log)
	if name == "" {
		panic("no existing pool string can serve as the item name")
	}
	current = nameBytes
	explain := ""
	if explainBytes, chosenExplain, log2 := pick(current, itemDoc, *stubExplain, explainCandidates); chosenExplain != "" {
		fmt.Print(log2)
		explain = chosenExplain
		current = explainBytes
	} else {
		fmt.Print(log2)
	}

	verified, err := platformpvf.OpenBytes(current)
	must(err)
	text, err := verified.ReadText(itemDoc)
	must(err)
	if !strings.Contains(text, name) {
		panic(fmt.Errorf("verified %s missing selected name %q", itemDoc, name))
	}
	for _, stub := range []string{*stubName, *stubExplain} {
		if strings.Contains(text, stub) {
			panic(fmt.Errorf("verified %s still contains %q", itemDoc, stub))
		}
	}
	must(os.WriteFile(*outPath, current, 0o644))
	outSum := sha256.Sum256(current)
	fmt.Printf("selected name=%q explain=%q\n", name, explain)
	fmt.Printf("output %s %d bytes sha256=%s\n", *outPath, len(current), strings.ToUpper(hex.EncodeToString(outSum[:])))
	fmt.Printf("name/explain lines: %s\n", pickLines(text))
}

// pick 依次尝试候选文案，返回第一个被字符串池接受的（同时回显每次尝试的结果）。
func pick(data []byte, doc, stub string, candidates []string) ([]byte, string, string) {
	var out strings.Builder
	for _, candidate := range candidates {
		archive, err := platformpvf.OpenBytes(data)
		must(err)
		patched, report, err := archive.PatchScriptString([]string{doc}, stub, candidate)
		if err != nil {
			fmt.Fprintf(&out, "  [no] %-24q %v\n", candidate, err)
			continue
		}
		fmt.Fprintf(&out, "  [OK] %-24q report=%+v\n", candidate, report)
		return patched, candidate, out.String()
	}
	return nil, "", out.String()
}

func pickLines(text string) string {
	kept := make([]string, 0, 4)
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[name]") || strings.HasPrefix(trimmed, "[explain]") {
			kept = append(kept, trimmed)
		}
	}
	return strings.Join(kept, " | ")
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

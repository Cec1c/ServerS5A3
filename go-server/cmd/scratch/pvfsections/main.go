// pvfsections 是一次性 PVF 取证程序（不进入生产路径）：
// 遍历某个 .lst 里的全部条目，统计指定 [section] 在真实 Script.pvf 里的存在率，
// 用于回答「这个段到底有没有」——而不是靠猜测段名。
//
// 用法：
//
//	go run ./cmd/scratch/pvfsections -pvf <Script.pvf> -list n_quest/quest.lst \
//	    -section name -section "depend message" -section "condition message"
package main

import (
	"context"
	"flag"
	"fmt"
	"sort"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type repeatable []string

func (r *repeatable) String() string { return strings.Join(*r, ",") }

func (r *repeatable) Set(value string) error {
	*r = append(*r, value)
	return nil
}

func main() {
	var lists, sections repeatable
	pvfPath := flag.String("pvf", `/opt/dnf90/data/dnf/Script.pvf`, "pvf path")
	flag.Var(&lists, "list", "PVF list file to walk (repeatable)")
	flag.Var(&sections, "section", "section name to probe (repeatable)")
	sample := flag.Int("sample", 5, "print up to N example entries per section")
	flag.Parse()

	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: *pvfPath})
	if err != nil {
		panic(err)
	}
	fmt.Printf("pvf=%s files=%d format=%s\n", *pvfPath, archive.FileCount(), archive.Format())

	index, err := dnfpvf.Build(context.Background(), archive, dnfpvf.BuildOptions{Lists: []string(lists)})
	if err != nil {
		panic(err)
	}

	for _, listPath := range lists {
		entries := index.List(listPath)
		withValue := make(map[string]int, len(sections))
		withText := make(map[string]int, len(sections))
		examples := make(map[string][]string, len(sections))
		readable := 0
		for _, entry := range entries {
			if entry.ID <= 0 {
				continue
			}
			doc, ok := index.Document(entry.Path)
			if !ok {
				continue
			}
			readable++
			for _, section := range sections {
				// 存在性看「段标记本身在不在」——不能只看 Texts()，否则
				// [weight] 1 / [part set index] 3 这类纯数字段会被漏判成不存在。
				tokens, ok := doc.Section(section)
				if !ok {
					continue
				}
				withValue[section]++
				for _, token := range tokens {
					if strings.TrimSpace(token.Value) != "" {
						withText[section]++
						if len(examples[section]) < *sample {
							examples[section] = append(examples[section], fmt.Sprintf("%d@%s", entry.ID, entry.Path))
						}
						break
					}
				}
			}
		}
		fmt.Printf("LIST %s entries=%d readable=%d\n", listPath, len(entries), readable)
		sorted := append([]string(nil), sections...)
		sort.Strings(sorted)
		for _, section := range sorted {
			fmt.Printf("  SECTION [%s] present=%d nonEmpty=%d examples=%v\n",
				section, withValue[section], withText[section], examples[section])
		}
	}
}

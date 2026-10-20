// slotsurvey 是一次性取证程序（不进入生产路径）：
// 按真实运行时 Script.pvf 全量列出 `[reward type] `[slot expansion]“ 的任务，
// 打印任务 ID、路径、`[reward int data]` 索引，并在归档里统计
// `[support]`/`[magic stone]`/`[earring]`/`[support weapon]` 的 .equ 文档条数与首条样本，
// 用于核实"开左槽/开右槽/开耳环槽"在本服务端到底改哪一位。
//
// 用法：
//
//	go run ./cmd/scratch/slotsurvey -pvf <Script.pvf>
package main

import (
	"context"
	"flag"
	"fmt"
	"sort"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/quest"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func normalize(value string) string {
	return strings.Trim(strings.ToLower(strings.TrimSpace(value)), "`[] ")
}

func main() {
	pvfPath := flag.String("pvf", `D:/DNF/runtime/data/dnf/Script.pvf`, "Script.pvf path")
	flag.Parse()

	archive, err := platformpvf.Open(*pvfPath)
	if err != nil {
		panic(err)
	}
	index, err := dnfpvf.Build(context.Background(), archive, dnfpvf.BuildOptions{
		Lists: []string{quest.DefaultList},
	})
	if err != nil {
		panic(err)
	}

	type hit struct {
		id    int64
		path  string
		grade string
		typ   string
		ints  []int64
	}
	var hits []hit
	for _, entry := range index.List(quest.DefaultList) {
		doc, ok := index.Document(entry.Path)
		if !ok {
			continue
		}
		rewardType, _ := doc.Text("reward type")
		if normalize(rewardType) != "slot expansion" {
			continue
		}
		grade, _ := doc.Text("grade")
		typ, _ := doc.Text("type")
		hits = append(hits, hit{id: entry.ID, path: entry.Path, grade: grade, typ: typ, ints: doc.Ints("reward int data")})
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].id < hits[j].id })
	fmt.Printf("slot_expansion_quests=%d\n", len(hits))
	for _, item := range hits {
		fmt.Printf("  id=%d path=%s grade=%s type=%s reward_int_data=%v\n",
			item.id, item.path, item.grade, item.typ, item.ints)
	}

	sort.Strings([]string{})
	wanted := []string{"[support]", "[magic stone]", "[earring]", "[support weapon]"}
	counts := map[string]int{}
	first := map[string]string{}
	for _, file := range archive.Files() {
		lowerPath := strings.ToLower(file.ArchivePath)
		if !strings.HasSuffix(lowerPath, ".equ") {
			continue
		}
		text, readErr := archive.ReadText(file.ArchivePath)
		if readErr != nil {
			continue
		}
		if !strings.Contains(text, "[equipment type]") {
			continue
		}
		for _, token := range wanted {
			if strings.Contains(text, "`"+token+"`") {
				counts[token]++
				if _, exists := first[token]; !exists {
					first[token] = file.ArchivePath
				}
			}
		}
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	fmt.Printf("equ_documents_with_equipment_type=%d\n", len(counts))
	for _, key := range keys {
		fmt.Printf("  %s count=%d first=%s\n", key, counts[key], first[key])
	}
}

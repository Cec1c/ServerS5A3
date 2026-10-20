// itemuseaudit 枚举 PVF stackable.lst 的每一条，导出其 [name]/[stackable type]
// 与文档里出现过的全部 [section] 名，供"哪些消耗品有服务端处理器"的审计使用。
//
// 只读：不写 PVF，不写数据库。
//
//	go run ./cmd/scratch/itemuseaudit -pvf <Script.pvf> -out <jsonl 路径>
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"sync"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type entry struct {
	ItemID    int64    `json:"item_id"`
	Name      string   `json:"name"`
	Path      string   `json:"path"`
	StackType string   `json:"stack_type"`
	GroupName string   `json:"group_name,omitempty"`
	Explain   string   `json:"explain,omitempty"`
	Grade     int64    `json:"grade,omitempty"`
	Rarity    int64    `json:"rarity,omitempty"`
	Sections  []string `json:"sections"`
}

type summary struct {
	StackType string  `json:"stack_type"`
	Count     int     `json:"count"`
	SampleIDs []int64 `json:"sample_ids"`
	Sample    string  `json:"sample_name,omitempty"`
}

type report struct {
	Total    int       `json:"total"`
	Parsed   int       `json:"parsed"`
	Types    []summary `json:"types"`
	EntriesN int       `json:"entries"`
}

func main() {
	pvfPath := flag.String("pvf", `D:/DNF/runtime/data/dnf/Script.pvf`, "Script.pvf path")
	outPath := flag.String("out", "itemuseaudit.jsonl", "JSONL output path")
	sumPath := flag.String("summary", "", "optional summary JSON path")
	flag.Parse()

	archive, err := platformpvf.Open(*pvfPath)
	if err != nil {
		panic(err)
	}
	listText, err := archive.ReadText("stackable/stackable.lst")
	if err != nil {
		panic(err)
	}
	listDoc, err := dnfpvf.Parse("stackable/stackable.lst", listText)
	if err != nil {
		panic(err)
	}
	refs := dnfpvf.ParseList(listDoc)
	fmt.Fprintf(os.Stderr, "stackable.lst entries=%d\n", len(refs))

	type job struct {
		id   int64
		path string
	}
	jobs := make([]job, 0, len(refs))
	for _, ref := range refs {
		candidate := strings.Trim(strings.TrimSpace(ref.Path), "`\"'")
		resolved := ""
		for _, probe := range []string{candidate, path.Join("stackable", candidate)} {
			if file, ok := archive.FindFile(probe); ok {
				resolved = file.ArchivePath
				break
			}
		}
		if resolved == "" {
			continue
		}
		jobs = append(jobs, job{id: ref.ID, path: resolved})
	}

	out, err := os.Create(*outPath)
	if err != nil {
		panic(err)
	}
	writer := bufio.NewWriterSize(out, 1<<20)

	var mu sync.Mutex
	results := make([]entry, 0, len(jobs))
	var wg sync.WaitGroup
	work := make(chan job, 256)
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := make([]entry, 0, 1024)
			for item := range work {
				text, readErr := archive.ReadText(item.path)
				if readErr != nil {
					continue
				}
				doc, parseErr := dnfpvf.Parse(item.path, text)
				if parseErr != nil {
					continue
				}
				name, _ := doc.Text("name")
				stackType, _ := doc.Text("stackable type")
				groupName, _ := doc.Text("item group name")
				explain, _ := doc.Text("explain")
				grade, _ := doc.Int("grade")
				rarity, _ := doc.Int("rarity")
				seen := map[string]bool{}
				sections := make([]string, 0, 48)
				for _, section := range doc.Sections {
					key := strings.ToLower(strings.TrimSpace(section.Name))
					if key == "" || seen[key] {
						continue
					}
					seen[key] = true
					sections = append(sections, key)
				}
				sort.Strings(sections)
				local = append(local, entry{
					ItemID:    item.id,
					Name:      name,
					Path:      item.path,
					StackType: strings.TrimSpace(stackType),
					GroupName: strings.TrimSpace(groupName),
					Explain:   strings.TrimSpace(strings.ReplaceAll(explain, "\n", " ")),
					Grade:     grade,
					Rarity:    rarity,
					Sections:  sections,
				})
			}
			mu.Lock()
			results = append(results, local...)
			mu.Unlock()
		}()
	}
	for _, item := range jobs {
		work <- item
	}
	close(work)
	wg.Wait()

	sort.Slice(results, func(i, j int) bool { return results[i].ItemID < results[j].ItemID })
	encoder := json.NewEncoder(writer)
	for _, row := range results {
		if err := encoder.Encode(row); err != nil {
			panic(err)
		}
	}
	if err := writer.Flush(); err != nil {
		panic(err)
	}
	if err := out.Close(); err != nil {
		panic(err)
	}

	type agg struct {
		count  int
		ids    []int64
		sample string
	}
	byType := map[string]*agg{}
	for _, row := range results {
		key := row.StackType
		if key == "" {
			key = "(none)"
		}
		bucket, ok := byType[key]
		if !ok {
			bucket = &agg{sample: row.Name}
			byType[key] = bucket
		}
		bucket.count++
		if len(bucket.ids) < 20 {
			bucket.ids = append(bucket.ids, row.ItemID)
		}
	}
	summaryRows := make([]summary, 0, len(byType))
	for key, bucket := range byType {
		summaryRows = append(summaryRows, summary{
			StackType: key,
			Count:     bucket.count,
			SampleIDs: bucket.ids,
			Sample:    bucket.sample,
		})
	}
	sort.Slice(summaryRows, func(i, j int) bool {
		if summaryRows[i].Count != summaryRows[j].Count {
			return summaryRows[i].Count > summaryRows[j].Count
		}
		return summaryRows[i].StackType < summaryRows[j].StackType
	})
	rep := report{Total: len(jobs), Parsed: len(results), Types: summaryRows, EntriesN: len(results)}
	fmt.Fprintf(os.Stderr, "parsed=%d types=%d\n", len(results), len(summaryRows))
	if *sumPath != "" {
		data, marshalErr := json.MarshalIndent(rep, "", "  ")
		if marshalErr != nil {
			panic(marshalErr)
		}
		if writeErr := os.WriteFile(*sumPath, data, 0o644); writeErr != nil {
			panic(writeErr)
		}
	}
}

// pvfkeys 枚举某个归档路径前缀下所有文档出现过的 [key] 记号及出现次数。
//
// 用途：在不确定 PVF 是否支持某个字段（例如"技能攻击力""百分比防御"）时，
// 用真实档案里的字段全集来回答，而不是靠猜或靠外部资料。
//
//	go run ./cmd/scratch/pvfkeys -pvf <Script.pvf> -prefix equipment/character/common/title/ -top 120
package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

var keyPattern = regexp.MustCompile(`\[([^\[\]\r\n]{1,64})\]`)

func main() {
	pvfPath := flag.String("pvf", `D:/DNF/runtime/data/dnf/Script.pvf`, "Script.pvf path")
	prefix := flag.String("prefix", "equipment/character/common/title/", "archive path prefix to scan")
	top := flag.Int("top", 120, "print the N most frequent keys")
	filter := flag.String("filter", "", "only print keys containing this substring (case-insensitive)")
	flag.Parse()

	archive, err := platformpvf.Open(*pvfPath)
	if err != nil {
		panic(err)
	}
	lowerPrefix := strings.ToLower(strings.TrimSpace(*prefix))

	type fileRef struct{ path string }
	files := make([]fileRef, 0, 4096)
	for _, f := range archive.Files() {
		if strings.HasPrefix(strings.ToLower(f.ArchivePath), lowerPrefix) {
			files = append(files, fileRef{path: f.ArchivePath})
		}
	}
	fmt.Fprintf(os.Stderr, "scanning %d documents under %q\n", len(files), lowerPrefix)

	var mu sync.Mutex
	counts := map[string]int{}
	jobs := make(chan fileRef)
	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := map[string]int{}
			for f := range jobs {
				text, readErr := archive.ReadText(f.path)
				if readErr != nil {
					continue
				}
				for _, m := range keyPattern.FindAllStringSubmatch(text, -1) {
					local[strings.ToLower(strings.TrimSpace(m[1]))]++
				}
			}
			mu.Lock()
			for k, v := range local {
				counts[k] += v
			}
			mu.Unlock()
		}()
	}
	for _, f := range files {
		jobs <- f
	}
	close(jobs)
	wg.Wait()

	type kv struct {
		k string
		n int
	}
	rows := make([]kv, 0, len(counts))
	for k, n := range counts {
		if *filter != "" && !strings.Contains(k, strings.ToLower(*filter)) {
			continue
		}
		rows = append(rows, kv{k, n})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].n != rows[j].n {
			return rows[i].n > rows[j].n
		}
		return rows[i].k < rows[j].k
	})
	if len(rows) > *top {
		rows = rows[:*top]
	}
	for _, r := range rows {
		fmt.Printf("%7d  [%s]\n", r.n, r.k)
	}
}

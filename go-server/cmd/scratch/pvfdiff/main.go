// Command pvfdiff structurally compares two Script.pvf archives and reports
// added, removed, and content-changed entries. It exists as a forensic scratch
// tool for deciding whether a deployed client PVF differs from a vendor
// baseline, and for dumping the exact text of any entry that changed.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

type change struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	SizeA     int    `json:"size_a"`
	SizeB     int    `json:"size_b"`
	SHA256A   string `json:"sha256_a"`
	SHA256B   string `json:"sha256_b"`
	TextA     string `json:"text_a,omitempty"`
	TextB     string `json:"text_b,omitempty"`
	TextError string `json:"text_error,omitempty"`
}

type report struct {
	PathA       string   `json:"path_a"`
	SizeA       int64    `json:"size_a"`
	SHA256A     string   `json:"sha256_a"`
	FilesA      int      `json:"files_a"`
	PathB       string   `json:"path_b"`
	SizeB       int64    `json:"size_b"`
	SHA256B     string   `json:"sha256_b"`
	FilesB      int      `json:"files_b"`
	Added       []string `json:"added"`
	Removed     []string `json:"removed"`
	Changed     []change `json:"changed"`
	ChangedList []string `json:"changed_list"`
}

func main() {
	pathA := flag.String("a", "", "PVF A (deployed/candidate)")
	pathB := flag.String("b", "", "PVF B (baseline/reference)")
	jsonOut := flag.String("json", "", "optional JSON report output path")
	maxText := flag.Int("max-text", 4000, "max characters of text captured per changed entry")
	listNeedle := flag.String("list", "", "list archive paths containing this lowercase substring and exit")
	dumpPath := flag.String("dump", "", "print both texts of this exact archive path and exit")
	extractPath := flag.String("extract", "", "write the raw bytes of this archive path (from -a) to -out and exit")
	outPath := flag.String("out", "", "output path for -extract")
	fastMode := flag.Bool("fast", false, "compare only path sets and indexed sizes (no content hashing)")
	flag.Parse()

	if *pathA == "" || *pathB == "" {
		fmt.Fprintln(os.Stderr, "need -a and -b")
		os.Exit(2)
	}
	archiveA := open(*pathA)
	archiveB := open(*pathB)

	if *extractPath != "" {
		raw, err := archiveA.ReadRaw(*extractPath)
		if err != nil {
			panic(err)
		}
		if *outPath == "" {
			panic("need -out with -extract")
		}
		if err := os.WriteFile(*outPath, raw, 0o644); err != nil {
			panic(err)
		}
		sum := sha256.Sum256(raw)
		fmt.Printf("EXTRACT path=%s bytes=%d sha=%s out=%s\n", *extractPath, len(raw), strings.ToUpper(hex.EncodeToString(sum[:])), *outPath)
		return
	}

	if *listNeedle != "" {
		needle := strings.ToLower(*listNeedle)
		for _, file := range archiveA.Files() {
			if strings.Contains(strings.ToLower(file.ArchivePath), needle) {
				fmt.Printf("A %s size=%d type=%d\n", file.ArchivePath, file.Size, file.DataType)
			}
		}
		for _, file := range archiveB.Files() {
			if strings.Contains(strings.ToLower(file.ArchivePath), needle) {
				fmt.Printf("B %s size=%d type=%d\n", file.ArchivePath, file.Size, file.DataType)
			}
		}
		return
	}
	if *dumpPath != "" {
		dumpOne(archiveA, archiveB, *dumpPath, *maxText)
		return
	}

	filesA := archiveA.Files()
	filesB := archiveB.Files()
	indexA := map[string]int{}
	for idx, file := range filesA {
		indexA[strings.ToLower(file.ArchivePath)] = idx
	}
	indexB := map[string]int{}
	for idx, file := range filesB {
		indexB[strings.ToLower(file.ArchivePath)] = idx
	}

	result := report{
		PathA:   *pathA,
		PathB:   *pathB,
		FilesA:  len(filesA),
		FilesB:  len(filesB),
		Changed: []change{},
	}
	result.SizeA, result.SHA256A = fileDigest(*pathA)
	result.SizeB, result.SHA256B = fileDigest(*pathB)

	keys := make([]string, 0, len(indexA))
	for key := range indexA {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	// added = in A not in B; removed = in B not in A
	seen := map[string]bool{}
	for _, key := range keys {
		seen[key] = true
		idxA := indexA[key]
		idxB, ok := indexB[key]
		fileA := filesA[idxA]
		if !ok {
			result.Added = append(result.Added, fileA.ArchivePath)
			continue
		}
		fileB := filesB[idxB]
		if *fastMode {
			if fileA.Size != fileB.Size || fileA.DataType != fileB.DataType {
				result.Changed = append(result.Changed, change{
					Path: fileA.ArchivePath, Kind: "size-only",
					SizeA: fileA.Size, SizeB: fileB.Size,
				})
				result.ChangedList = append(result.ChangedList, fileA.ArchivePath)
			}
			continue
		}
		rawA, errA := archiveA.FileRaw(idxA)
		rawB, errB := archiveB.FileRaw(idxB)
		if errA != nil || errB != nil {
			if fileA.Size != fileB.Size {
				result.Changed = append(result.Changed, change{
					Path: fileA.ArchivePath, Kind: "size-only",
					SizeA: fileA.Size, SizeB: fileB.Size,
					TextError: fmt.Sprintf("rawread a=%v b=%v", errA, errB),
				})
				result.ChangedList = append(result.ChangedList, fileA.ArchivePath)
			}
			continue
		}
		sumA := sha256.Sum256(rawA)
		sumB := sha256.Sum256(rawB)
		if sumA == sumB {
			continue
		}
		item := change{
			Path:    fileA.ArchivePath,
			Kind:    "content",
			SizeA:   len(rawA),
			SizeB:   len(rawB),
			SHA256A: strings.ToUpper(hex.EncodeToString(sumA[:])),
			SHA256B: strings.ToUpper(hex.EncodeToString(sumB[:])),
		}
		textA, textErrA := archiveA.ReadText(fileA.ArchivePath)
		textB, textErrB := archiveB.ReadText(fileB.ArchivePath)
		if textErrA == nil && textErrB == nil {
			item.TextA = truncate(textA, *maxText)
			item.TextB = truncate(textB, *maxText)
		} else {
			item.TextError = fmt.Sprintf("text a=%v b=%v", textErrA, textErrB)
		}
		result.Changed = append(result.Changed, item)
		result.ChangedList = append(result.ChangedList, fileA.ArchivePath)
	}
	keysB := make([]string, 0, len(indexB))
	for key := range indexB {
		keysB = append(keysB, key)
	}
	sort.Strings(keysB)
	for _, key := range keysB {
		if seen[key] {
			continue
		}
		result.Removed = append(result.Removed, filesB[indexB[key]].ArchivePath)
	}

	fmt.Fprintf(os.Stderr, "A files=%d size=%d sha=%s\n", result.FilesA, result.SizeA, result.SHA256A)
	fmt.Fprintf(os.Stderr, "B files=%d size=%d sha=%s\n", result.FilesB, result.SizeB, result.SHA256B)
	fmt.Fprintf(os.Stderr, "added=%d removed=%d changed=%d\n", len(result.Added), len(result.Removed), len(result.Changed))
	for _, path := range result.Added {
		fmt.Printf("ADDED %s\n", path)
	}
	for _, path := range result.Removed {
		fmt.Printf("REMOVED %s\n", path)
	}
	for _, item := range result.Changed {
		fmt.Printf("CHANGED %s kind=%s sizeA=%d sizeB=%d\n", item.Path, item.Kind, item.SizeA, item.SizeB)
	}
	if *jsonOut != "" {
		encoded, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			panic(err)
		}
		if err := os.WriteFile(*jsonOut, encoded, 0o644); err != nil {
			panic(err)
		}
		fmt.Fprintf(os.Stderr, "json=%s\n", *jsonOut)
	}
}

func dumpOne(archiveA, archiveB *platformpvf.Archive, path string, maxText int) {
	for name, archive := range map[string]*platformpvf.Archive{"A": archiveA, "B": archiveB} {
		raw, err := archive.ReadRaw(path)
		if err != nil {
			fmt.Printf("%s RAW ERROR %v\n", name, err)
			continue
		}
		sum := sha256.Sum256(raw)
		fmt.Printf("%s raw=%d sha=%s\n", name, len(raw), strings.ToUpper(hex.EncodeToString(sum[:])))
		text, textErr := archive.ReadText(path)
		if textErr != nil {
			fmt.Printf("%s TEXT ERROR %v\n", name, textErr)
			fmt.Printf("%s RAWHEX %s\n", name, hex.EncodeToString(raw[:min(len(raw), 512)]))
			continue
		}
		fmt.Printf("%s TEXT >>>\n%s\n<<<\n", name, truncate(text, maxText))
	}
}

func open(path string) *platformpvf.Archive {
	archive, err := platformpvf.Open(path)
	if err != nil {
		panic(fmt.Errorf("open %s: %w", path, err))
	}
	return archive
}

func fileDigest(path string) (int64, string) {
	info, err := os.Stat(path)
	if err != nil {
		panic(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(data)
	return info.Size(), strings.ToUpper(hex.EncodeToString(sum[:]))
}

func truncate(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + fmt.Sprintf("\n...[truncated %d chars]", len(value)-limit)
}

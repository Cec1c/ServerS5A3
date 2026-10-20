// Command pvfuioff disables a whole client UI family by replacing every
// matching archive entry with an empty script, then repacking a candidate PVF.
//
// It never edits the active PVF in place and it never resizes or reorders the
// shared string pools: each touched entry keeps its identity, only its content
// becomes empty.
//
//	go run ./cmd/scratch/pvfuioff -pvf Script.pvf -prefix ui/pvp02 -prefix ui/tournamentsystem -out Script-no-pvp02.pvf
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

type prefixFlag []string

func (p *prefixFlag) String() string { return strings.Join(*p, ",") }

func (p *prefixFlag) Set(value string) error {
	*p = append(*p, value)
	return nil
}

type report struct {
	Mode         string   `json:"mode"`
	InputPath    string   `json:"input_path"`
	OutputPath   string   `json:"output_path"`
	Prefixes     []string `json:"prefixes"`
	Matched      int      `json:"matched_entries"`
	Replaced     int      `json:"replaced_entries"`
	NonEmpty     int      `json:"non_empty_before"`
	AlreadyEmpty int      `json:"already_empty_before"`
	BytesBefore  int      `json:"bytes_before"`
	InputSize    int      `json:"input_size"`
	OutputSize   int      `json:"output_size"`
	OutputSHA256 string   `json:"output_sha256"`
	Sample       []string `json:"sample_paths"`
}

func main() {
	pvfPath := flag.String("pvf", "", "Script.pvf to base the candidate on (read only)")
	outPath := flag.String("out", "", "candidate output path (required)")
	reportPath := flag.String("report", "", "optional JSON report path")
	var prefixes prefixFlag
	flag.Var(&prefixes, "prefix", "archive path prefix to disable (repeatable)")
	flag.Parse()

	if *pvfPath == "" || *outPath == "" || len(prefixes) == 0 {
		fmt.Fprintln(os.Stderr, "usage: pvfuioff -pvf <Script.pvf> -prefix <archive/path> [-prefix ...] -out <candidate.pvf>")
		os.Exit(2)
	}

	archive, err := platformpvf.Open(*pvfPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open %s: %v\n", *pvfPath, err)
		os.Exit(1)
	}
	files := archive.Files()
	result := report{
		Mode:       "ui-family-off",
		InputPath:  *pvfPath,
		OutputPath: *outPath,
		Prefixes:   prefixes,
	}
	lowered := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		lowered = append(lowered, strings.ToLower(p))
	}

	replacements := make([]platformpvf.RawReplacement, 0, 1024)
	for idx := range files {
		// Path 是条目所在目录，ArchivePath 才是目录+文件名的完整键；
		// FindFile/ReadRaw/RepackRaw 都按完整键（pathIdx）解析。
		path := files[idx].ArchivePath
		lower := strings.ToLower(path)
		match := false
		for _, p := range lowered {
			if strings.HasPrefix(lower, p) {
				match = true
				break
			}
		}
		if !match {
			continue
		}
		result.Matched++
		raw, err := archive.ReadRaw(path)
		if err != nil {
			// Entries whose chunk cannot be read are skipped, not guessed.
			fmt.Fprintf(os.Stderr, "[skip] %s: %v\n", path, err)
			continue
		}
		result.BytesBefore += len(raw)
		if len(raw) == 0 {
			result.AlreadyEmpty++
		} else {
			result.NonEmpty++
		}
		if len(result.Sample) < 20 {
			result.Sample = append(result.Sample, fmt.Sprintf("%s (%d bytes)", path, len(raw)))
		}
		replacements = append(replacements, platformpvf.RawReplacement{Path: path, Data: []byte{}})
	}

	if len(replacements) == 0 {
		fmt.Fprintln(os.Stderr, "[错误] 没有任何条目匹配给定前缀")
		os.Exit(1)
	}

	patched, err := archive.RepackRaw(replacements)
	if err != nil {
		fmt.Fprintf(os.Stderr, "repack: %v\n", err)
		os.Exit(1)
	}
	verified, err := platformpvf.OpenBytes(patched)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify candidate: %v\n", err)
		os.Exit(1)
	}
	// 复核：被清空的条目必须确实为空，未匹配的条目必须仍可读。
	for _, r := range replacements {
		raw, err := verified.ReadRaw(r.Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[错误] 复核读取失败 %s: %v\n", r.Path, err)
			os.Exit(1)
		}
		if len(raw) != 0 {
			fmt.Fprintf(os.Stderr, "[错误] 复核发现 %s 仍非空（%d 字节）\n", r.Path, len(raw))
			os.Exit(1)
		}
		result.Replaced++
	}
	if verified.FileCount() != archive.FileCount() {
		fmt.Fprintf(os.Stderr, "[错误] 条目数变化 %d -> %d\n", archive.FileCount(), verified.FileCount())
		os.Exit(1)
	}

	sum := sha256.Sum256(patched)
	result.OutputSHA256 = hex.EncodeToString(sum[:])
	result.InputSize = len(archive.Bytes())
	result.OutputSize = len(patched)
	if err := os.WriteFile(*outPath, patched, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", *outPath, err)
		os.Exit(1)
	}
	if *reportPath != "" {
		encoded, _ := json.MarshalIndent(result, "", "  ")
		if err := os.WriteFile(*reportPath, encoded, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write report %s: %v\n", *reportPath, err)
			os.Exit(1)
		}
	}
	encoded, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(encoded))
}

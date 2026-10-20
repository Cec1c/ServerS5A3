// pvfsetfile 是一次性 PVF 整条目内容替换工具（scratch 区，不进入生产路径）。
//
// 与 pvfpatch 的差别：pvfpatch 只能把 type-1 脚本里对共享字符串池"已存在值"的
// 引用换成池里另一个已存在的值（等宽、不能新增段落）；本工具把某个归档条目的
// 内容整体替换成本地文件提供的文本/字节，长度可变，用于修改 .etc/.lst 这类
// 独立文档。实现直接复用 internal/platform/pvf 的 RepackRaw：只重打包受影响的
// body chunk，不改字符串池、不改 HASH 段、不新增或删除条目。
//
// 安全约束：
//   - 默认绝不原地覆盖输入文件：必须显式给出与 -pvf 不同的 -out 输出路径；
//   - 写入后立即回读新档做校验：条目总数、全部条目的路径/类型/大小、目标条目
//     的原始字节与解码文本，并对目标邻近条目 + 抽样条目（-verify-full 时全部
//     条目）重算 sha256 与输入档逐条比对。
//
// 用法：
//
//	# 1) 抽取原文（用于备份/回滚）
//	go run ./cmd/scratch/pvfsetfile -pvf Script.pvf \
//	  -dump etc/channeluiinfo_chn.etc -dump-out ./orig
//
//	# 2) 整文件替换
//	go run ./cmd/scratch/pvfsetfile -pvf Script.pvf \
//	  -file etc/channeluiinfo_chn.etc -in ./new.etc -encoding auto \
//	  -out ./Script.patched.pvf -verify-full -json
package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf16"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }
func (l *stringList) Set(value string) error {
	*l = append(*l, value)
	return nil
}

type replaceRequest struct {
	archivePath string
	localPath   string
	text        string
	expected    string
	hasExpected bool
	raw         []byte
	encoding    string
	bom         bool
}

type replacementReport struct {
	ArchivePath     string `json:"archive_path"`
	FileIndex       int    `json:"file_index"`
	DataType        int    `json:"data_type"`
	LocalPath       string `json:"local_path"`
	Encoding        string `json:"encoding"`
	BOM             bool   `json:"bom"`
	OldSize         int    `json:"old_size"`
	OldSHA256       string `json:"old_sha256"`
	NewSize         int    `json:"new_size"`
	NewSHA256       string `json:"new_sha256"`
	TextRuneCount   int    `json:"text_rune_count"`
	ReadbackOK      bool   `json:"readback_ok"`
	ReadbackSize    int    `json:"readback_size"`
	ReadbackSHA256  string `json:"readback_sha256"`
	ReadbackTextOK  bool   `json:"readback_text_ok"`
	TextChecked     bool   `json:"text_checked"`
	ReadbackSnippet string `json:"readback_snippet"`
}

type dumpReport struct {
	ArchivePath string `json:"archive_path"`
	FileIndex   int    `json:"file_index"`
	DataType    int    `json:"data_type"`
	Size        int    `json:"size"`
	SHA256      string `json:"sha256"`
	LocalPath   string `json:"local_path,omitempty"`
	TextPath    string `json:"text_path,omitempty"`
	TextRunes   int    `json:"text_rune_count"`
	TextSHA256  string `json:"text_sha256"`
	HasBOM      bool   `json:"has_bom"`
}

type verifyReport struct {
	FileCountEqual bool     `json:"file_count_equal"`
	InputCount     int      `json:"input_file_count"`
	OutputCount    int      `json:"output_file_count"`
	MetadataEqual  bool     `json:"metadata_equal"`
	MetadataDiffs  []string `json:"metadata_diffs,omitempty"`
	HashedFiles    int      `json:"hashed_files"`
	ComparedFiles  int      `json:"compared_files"`
	HashMismatches []string `json:"hash_mismatches,omitempty"`
	FullScan       bool     `json:"full_scan"`
	NeighborWindow int      `json:"neighbor_window"`
	SampleTarget   int      `json:"sample_target"`
}

type report struct {
	Input              string              `json:"input"`
	InputSize          int64               `json:"input_size"`
	InputSHA256        string              `json:"input_sha256"`
	InputFileCount     int                 `json:"input_file_count"`
	InputTotalDataSize int64               `json:"input_total_data_size"`
	Format             string              `json:"format"`
	Output             string              `json:"output"`
	OutputSize         int                 `json:"output_size"`
	OutputSHA256       string              `json:"output_sha256"`
	SizeDelta          int                 `json:"size_delta"`
	Dumps              []dumpReport        `json:"dumps,omitempty"`
	Replacements       []replacementReport `json:"replacements,omitempty"`
	Verify             *verifyReport       `json:"verify,omitempty"`
	InfoOnly           bool                `json:"info_only,omitempty"`
	OK                 bool                `json:"ok"`
	Failures           []string            `json:"failures,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "pvfsetfile:", err)
		os.Exit(1)
	}
}

func run() error {
	var files, inputs, dumps, expectTexts stringList
	input := flag.String("pvf", "", "输入 PVF 路径（永不修改）")
	output := flag.String("out", "", "输出 PVF 路径（替换模式必填，且必须不同于 -pvf）")
	encoding := flag.String("encoding", "auto", "auto|utf16le-bom|utf16le-nobom|utf8|raw")
	eol := flag.String("eol", "keep", "文本换行处理：keep|crlf|lf")
	dumpOut := flag.String("dump-out", "", "抽取模式输出目录（或单文件路径）")
	dumpTextOut := flag.String("dump-text-out", "", "抽取模式同时把解码文本写成 UTF-8 到该目录")
	info := flag.Bool("info", false, "只打印归档概要，不做任何写入")
	asJSON := flag.Bool("json", false, "输出 JSON 报告")
	full := flag.Bool("verify-full", false, "回读校验时对全部条目重算 sha256")
	sample := flag.Int("sample", 64, "回读校验额外抽样的条目数（均匀分布）")
	neighbors := flag.Int("neighbors", 64, "目标条目索引前后各多少条强制参与 sha256 比对")
	flag.Var(&files, "file", "要替换的归档条目路径（可重复）")
	flag.Var(&inputs, "in", "替换内容来源的本地文件（可重复，与 -file 顺序一一对应）")
	flag.Var(&expectTexts, "expect-text", "期望的回读文本（可重复；raw 模式下用它断言解码结果）")
	flag.Var(&dumps, "dump", "要抽取的归档条目路径（可重复）")
	flag.Parse()

	if *input == "" {
		return fmt.Errorf("-pvf is required")
	}
	inputAbs, err := filepath.Abs(*input)
	if err != nil {
		return err
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: inputAbs})
	if err != nil {
		return err
	}
	inputInfo, err := os.Stat(inputAbs)
	if err != nil {
		return err
	}

	result := report{
		Input:              inputAbs,
		InputSize:          inputInfo.Size(),
		Format:             string(archive.Format()),
		InfoOnly:           *info && len(files) == 0 && len(dumps) == 0,
		InputSHA256:        fileSHA256(inputAbs),
		InputFileCount:     archive.FileCount(),
		InputTotalDataSize: totalDataSize(archive),
	}
	if result.InfoOnly {
		result.OK = true
		fmt.Printf("pvf=%s format=%s size=%d sha256=%s files=%d total_data_size=%d\n",
			inputAbs, result.Format, result.InputSize, result.InputSHA256,
			result.InputFileCount, result.InputTotalDataSize)
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(result)
		}
		return nil
	}

	if len(dumps) > 0 {
		records, err := dumpEntries(archive, dumps, *dumpOut, *dumpTextOut)
		if err != nil {
			return err
		}
		result.Dumps = records
		for _, record := range records {
			fmt.Printf("dump path=%s index=%d type=%d size=%d sha256=%s bom=%v runes=%d text_sha256=%s -> %s\n",
				record.ArchivePath, record.FileIndex, record.DataType, record.Size,
				record.SHA256, record.HasBOM, record.TextRunes, record.TextSHA256, record.LocalPath)
			if record.TextPath != "" {
				fmt.Printf("     text -> %s\n", record.TextPath)
			}
		}
	}

	if len(files) == 0 {
		// 只做抽取/查看，没有写入，也就没有可失败的校验项。
		result.OK = true
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(result)
		}
		return nil
	}
	if *output == "" {
		return fmt.Errorf("-out is required when -file is used")
	}
	if len(inputs) != len(files) {
		return fmt.Errorf("-file count %d does not match -in count %d", len(files), len(inputs))
	}
	if len(expectTexts) != 0 && len(expectTexts) != len(files) {
		return fmt.Errorf("-expect-text count %d must be 0 or match -file count %d", len(expectTexts), len(files))
	}
	outputAbs, err := filepath.Abs(*output)
	if err != nil {
		return err
	}
	if strings.EqualFold(inputAbs, outputAbs) {
		return fmt.Errorf("输出必须不同于输入；不支持原地覆盖：%s", outputAbs)
	}
	if err := os.MkdirAll(filepath.Dir(outputAbs), 0o755); err != nil {
		return err
	}

	requests, err := buildRequests(archive, files, inputs, *encoding, *eol)
	if err != nil {
		return err
	}
	if len(expectTexts) != 0 {
		for index, path := range expectTexts {
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read expect-text %s: %w", path, err)
			}
			requests[index].expected = string(data)
			requests[index].hasExpected = true
		}
	}
	replacements := make([]platformpvf.RawReplacement, 0, len(requests))
	for _, request := range requests {
		replacements = append(replacements, platformpvf.RawReplacement{
			Path: request.archivePath,
			Data: request.raw,
		})
	}
	patched, err := archive.RepackRaw(replacements)
	if err != nil {
		return fmt.Errorf("repack: %w", err)
	}
	if err := os.WriteFile(outputAbs, patched, 0o644); err != nil {
		return err
	}
	result.Output = outputAbs
	result.OutputSize = len(patched)
	result.OutputSHA256 = sha256Hex(patched)
	result.SizeDelta = len(patched) - int(inputInfo.Size())

	readback, err := platformpvf.LoadArchive(platformpvf.Options{Path: outputAbs})
	if err != nil {
		return fmt.Errorf("readback open %s: %w", outputAbs, err)
	}
	verify, records, err := verifyOutput(archive, readback, requests, *full, *sample, *neighbors)
	if err != nil {
		return err
	}
	result.Verify = verify
	result.Replacements = records

	failures := make([]string, 0)
	if !verify.FileCountEqual {
		failures = append(failures, fmt.Sprintf("条目总数从 %d 变为 %d", verify.InputCount, verify.OutputCount))
	}
	if !verify.MetadataEqual {
		failures = append(failures, fmt.Sprintf("条目元信息出现 %d 处差异", len(verify.MetadataDiffs)))
	}
	if len(verify.HashMismatches) > 0 {
		failures = append(failures, fmt.Sprintf("%d 个条目的内容哈希发生变化", len(verify.HashMismatches)))
	}
	for _, record := range records {
		if !record.ReadbackOK {
			failures = append(failures, fmt.Sprintf("%s 回读字节与预期不一致", record.ArchivePath))
		}
		if record.TextChecked && !record.ReadbackTextOK {
			failures = append(failures, fmt.Sprintf("%s 回读文本与预期不一致", record.ArchivePath))
		}
	}
	result.Failures = failures
	result.OK = len(failures) == 0

	if *asJSON {
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			return err
		}
	} else {
		fmt.Printf("input  %s size=%d files=%d\n", result.Input, result.InputSize, verify.InputCount)
		fmt.Printf("output %s size=%d (%+d) sha256=%s files=%d\n",
			result.Output, result.OutputSize, result.SizeDelta, result.OutputSHA256, verify.OutputCount)
		for _, record := range records {
			fmt.Printf("replace %s index=%d type=%d enc=%s bom=%v %d->%d bytes readback_ok=%v text_ok=%v\n",
				record.ArchivePath, record.FileIndex, record.DataType, record.Encoding, record.BOM,
				record.OldSize, record.NewSize, record.ReadbackOK, record.ReadbackTextOK)
		}
		fmt.Printf("verify file_count_equal=%v metadata_equal=%v compared=%d hashed=%d mismatches=%d full_scan=%v\n",
			verify.FileCountEqual, verify.MetadataEqual, verify.ComparedFiles, verify.HashedFiles,
			len(verify.HashMismatches), verify.FullScan)
		for _, diff := range verify.MetadataDiffs {
			fmt.Printf("  METADATA-DIFF %s\n", diff)
		}
		for _, mismatch := range verify.HashMismatches {
			fmt.Printf("  HASH-DIFF %s\n", mismatch)
		}
		for _, failure := range failures {
			fmt.Printf("  FAIL %s\n", failure)
		}
		fmt.Printf("ok=%v\n", result.OK)
	}
	if !result.OK {
		return fmt.Errorf("回读校验失败（%d 项）：见上方 FAIL 行", len(failures))
	}
	return nil
}

func buildRequests(archive *platformpvf.Archive, files, inputs []string, encoding, eol string) ([]*replaceRequest, error) {
	requests := make([]*replaceRequest, 0, len(files))
	for index, archivePath := range files {
		idx := archive.FindFileIndex(archivePath)
		if idx < 0 {
			return nil, fmt.Errorf("%w: %s", platformpvf.ErrFileNotFound, archivePath)
		}
		raw, err := os.ReadFile(inputs[index])
		if err != nil {
			return nil, fmt.Errorf("read replacement %s: %w", inputs[index], err)
		}
		original, err := archive.FileRaw(idx)
		if err != nil {
			return nil, fmt.Errorf("read original %s: %w", archivePath, err)
		}
		request := &replaceRequest{archivePath: archivePath, localPath: inputs[index]}
		if strings.EqualFold(encoding, "raw") {
			request.encoding = "raw"
			request.raw = raw
			requests = append(requests, request)
			continue
		}
		if encoding != "auto" && !isKnownEncoding(encoding) {
			return nil, fmt.Errorf("unknown -encoding %q", encoding)
		}
		text := string(raw)
		switch strings.ToLower(strings.TrimSpace(eol)) {
		case "keep", "":
		case "crlf":
			text = toCRLF(text)
		case "lf":
			text = strings.ReplaceAll(toCRLF(text), "\r\n", "\n")
		default:
			return nil, fmt.Errorf("unknown -eol %q", eol)
		}
		resolved, withBOM := resolveEncoding(encoding, original)
		request.encoding = resolved
		request.bom = withBOM
		request.text = text
		request.raw = encodeText(text, resolved, withBOM)
		requests = append(requests, request)
	}
	return requests, nil
}

func isKnownEncoding(value string) bool {
	switch value {
	case "utf16le-bom", "utf16le-nobom", "utf8":
		return true
	}
	return false
}

// resolveEncoding 决定写出时的编码：auto 时跟随原文（有 BOM 就带 BOM，
// 原文看起来是 UTF-16LE 就写 UTF-16LE，否则按 UTF-8/原字节处理）。
func resolveEncoding(requested string, original []byte) (string, bool) {
	switch requested {
	case "utf16le-bom":
		return "utf16le-bom", true
	case "utf16le-nobom":
		return "utf16le-nobom", false
	case "utf8":
		return "utf8", false
	case "auto":
		if hasUTF16BOM(original) {
			return "utf16le-bom", true
		}
		if looksUTF16LE(original) {
			return "utf16le-nobom", false
		}
		return "utf8", false
	}
	return requested, false
}

func hasUTF16BOM(raw []byte) bool {
	return len(raw) >= 2 && raw[0] == 0xFF && raw[1] == 0xFE
}

// looksUTF16LE 用"奇数位大量为 0"这一 ASCII 文本的典型特征判断 UTF-16LE。
func looksUTF16LE(raw []byte) bool {
	if len(raw) < 8 {
		return false
	}
	pairs, zeros := 0, 0
	for index := 1; index < len(raw); index += 2 {
		pairs++
		if raw[index] == 0 {
			zeros++
		}
	}
	return pairs > 0 && zeros*100/pairs >= 40
}

func encodeText(text, encoding string, withBOM bool) []byte {
	switch encoding {
	case "utf8":
		return []byte(text)
	default:
		out := utf16LEBytes(text)
		if withBOM {
			return append([]byte{0xFF, 0xFE}, out...)
		}
		return out
	}
}

func utf16LEBytes(value string) []byte {
	units := utf16.Encode([]rune(value))
	out := make([]byte, len(units)*2)
	for index, unit := range units {
		binary.LittleEndian.PutUint16(out[index*2:index*2+2], unit)
	}
	return out
}

func toCRLF(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\n", "\r\n")
}

func dumpEntries(archive *platformpvf.Archive, dumps []string, dumpOut, dumpTextOut string) ([]dumpReport, error) {
	records := make([]dumpReport, 0, len(dumps))
	multi := len(dumps) > 1
	for _, archivePath := range dumps {
		idx := archive.FindFileIndex(archivePath)
		if idx < 0 {
			return nil, fmt.Errorf("%w: %s", platformpvf.ErrFileNotFound, archivePath)
		}
		raw, err := archive.FileRaw(idx)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", archivePath, err)
		}
		file := archive.Files()[idx]
		text, _ := archive.ReadText(archivePath)
		record := dumpReport{
			ArchivePath: file.ArchivePath,
			FileIndex:   idx,
			DataType:    file.DataType,
			Size:        len(raw),
			SHA256:      sha256Hex(raw),
			TextRunes:   len([]rune(text)),
			TextSHA256:  sha256Hex([]byte(text)),
			HasBOM:      hasUTF16BOM(raw),
		}
		if dumpOut != "" {
			target, err := resolveDumpTarget(dumpOut, file.ArchivePath, multi)
			if err != nil {
				return nil, err
			}
			if err := os.WriteFile(target, raw, 0o644); err != nil {
				return nil, err
			}
			record.LocalPath = target
		}
		if dumpTextOut != "" {
			if err := os.MkdirAll(dumpTextOut, 0o755); err != nil {
				return nil, err
			}
			target := filepath.Join(dumpTextOut, sanitizeName(file.ArchivePath)+".txt")
			if err := os.WriteFile(target, []byte(text), 0o644); err != nil {
				return nil, err
			}
			record.TextPath = target
		}
		records = append(records, record)
	}
	return records, nil
}

// resolveDumpTarget 在 -dump-out 是目录（已存在目录、或一次抽多条）时把条目
// 路径摊平成文件名，否则把 -dump-out 当成单文件路径。
func resolveDumpTarget(dumpOut, archivePath string, multi bool) (string, error) {
	info, err := os.Stat(dumpOut)
	if (err == nil && info.IsDir()) || multi || strings.HasSuffix(dumpOut, string(os.PathSeparator)) {
		if err := os.MkdirAll(dumpOut, 0o755); err != nil {
			return "", err
		}
		return filepath.Join(dumpOut, sanitizeName(archivePath)), nil
	}
	if err := os.MkdirAll(filepath.Dir(dumpOut), 0o755); err != nil {
		return "", err
	}
	return dumpOut, nil
}

func totalDataSize(archive *platformpvf.Archive) int64 {
	var total int64
	for _, file := range archive.Files() {
		total += int64(file.Size)
	}
	return total
}

func sanitizeName(archivePath string) string {
	replaced := strings.NewReplacer("/", "_", "\\", "_", ":", "_")
	return replaced.Replace(archivePath)
}

func verifyOutput(input, output *platformpvf.Archive, requests []*replaceRequest, full bool, sample, neighbors int) (*verifyReport, []replacementReport, error) {
	inFiles := input.Files()
	outFiles := output.Files()
	report := &verifyReport{
		InputCount:     len(inFiles),
		OutputCount:    len(outFiles),
		FileCountEqual: len(inFiles) == len(outFiles),
		MetadataEqual:  true,
		FullScan:       full,
		NeighborWindow: neighbors,
		SampleTarget:   sample,
	}

	replacedByIndex := make(map[int]*replaceRequest, len(requests))
	for _, request := range requests {
		idx := input.FindFileIndex(request.archivePath)
		if idx < 0 {
			return nil, nil, fmt.Errorf("%w: %s", platformpvf.ErrFileNotFound, request.archivePath)
		}
		replacedByIndex[idx] = request
	}

	if len(inFiles) != len(outFiles) {
		report.MetadataEqual = false
		report.MetadataDiffs = append(report.MetadataDiffs,
			fmt.Sprintf("file count %d -> %d", len(inFiles), len(outFiles)))
	}
	limit := len(inFiles)
	if len(outFiles) < limit {
		limit = len(outFiles)
	}
	for idx := 0; idx < limit; idx++ {
		left, right := inFiles[idx], outFiles[idx]
		if !strings.EqualFold(left.ArchivePath, right.ArchivePath) {
			report.MetadataEqual = false
			report.MetadataDiffs = append(report.MetadataDiffs,
				fmt.Sprintf("index %d path %q -> %q", idx, left.ArchivePath, right.ArchivePath))
			continue
		}
		if left.DataType != right.DataType {
			report.MetadataEqual = false
			report.MetadataDiffs = append(report.MetadataDiffs,
				fmt.Sprintf("index %d %s data_type %d -> %d", idx, left.ArchivePath, left.DataType, right.DataType))
		}
		if _, replaced := replacedByIndex[idx]; replaced {
			continue
		}
		if left.Size != right.Size {
			report.MetadataEqual = false
			report.MetadataDiffs = append(report.MetadataDiffs,
				fmt.Sprintf("index %d %s size %d -> %d", idx, left.ArchivePath, left.Size, right.Size))
		}
	}

	wanted := make(map[int]bool, len(replacedByIndex))
	for idx := range replacedByIndex {
		for offset := -neighbors; offset <= neighbors; offset++ {
			if idx+offset >= 0 && idx+offset < limit {
				wanted[idx+offset] = true
			}
		}
	}
	if sample > 0 && limit > 0 {
		step := limit / sample
		if step < 1 {
			step = 1
		}
		for idx := 0; idx < limit; idx += step {
			wanted[idx] = true
		}
	}
	if full {
		wanted = make(map[int]bool, limit)
		for idx := 0; idx < limit; idx++ {
			wanted[idx] = true
		}
	}

	indexes := make([]int, 0, len(wanted))
	for idx := range wanted {
		indexes = append(indexes, idx)
	}
	sort.Ints(indexes)

	records := make([]replacementReport, 0, len(requests))
	for _, request := range requests {
		idx := input.FindFileIndex(request.archivePath)
		oldRaw, err := input.FileRaw(idx)
		if err != nil {
			return nil, nil, err
		}
		file := inFiles[idx]
		record := replacementReport{
			ArchivePath:   file.ArchivePath,
			FileIndex:     idx,
			DataType:      file.DataType,
			LocalPath:     request.localPath,
			Encoding:      request.encoding,
			BOM:           request.bom,
			OldSize:       len(oldRaw),
			OldSHA256:     sha256Hex(oldRaw),
			NewSize:       len(request.raw),
			NewSHA256:     sha256Hex(request.raw),
			TextRuneCount: len([]rune(request.text)),
		}
		readbackRaw, err := output.FileRaw(idx)
		if err != nil {
			return nil, nil, fmt.Errorf("readback %s: %w", request.archivePath, err)
		}
		record.ReadbackSize = len(readbackRaw)
		record.ReadbackSHA256 = sha256Hex(readbackRaw)
		record.ReadbackOK = record.ReadbackSHA256 == record.NewSHA256
		expected := request.text
		record.TextChecked = !strings.EqualFold(request.encoding, "raw") || request.hasExpected
		if request.hasExpected {
			expected = request.expected
		}
		readbackText, err := output.ReadText(request.archivePath)
		if err == nil {
			record.ReadbackTextOK = readbackText == expected
			record.ReadbackSnippet = snippet(readbackText, 160)
		} else {
			record.ReadbackSnippet = "READ-ERROR: " + err.Error()
		}
		records = append(records, record)
	}

	for _, idx := range indexes {
		if idx >= len(inFiles) || idx >= len(outFiles) {
			continue
		}
		leftRaw, err := input.FileRaw(idx)
		if err != nil {
			return nil, nil, fmt.Errorf("hash input index %d: %w", idx, err)
		}
		rightRaw, err := output.FileRaw(idx)
		if err != nil {
			return nil, nil, fmt.Errorf("hash output index %d: %w", idx, err)
		}
		report.ComparedFiles++
		leftSum, rightSum := sha256Hex(leftRaw), sha256Hex(rightRaw)
		if _, replaced := replacedByIndex[idx]; replaced {
			report.HashedFiles++
			continue
		}
		if leftSum != rightSum {
			report.HashMismatches = append(report.HashMismatches,
				fmt.Sprintf("index %d %s %s -> %s", idx, inFiles[idx].ArchivePath, leftSum, rightSum))
			continue
		}
		report.HashedFiles++
	}
	return report, records, nil
}

func snippet(text string, limit int) string {
	replaced := strings.NewReplacer("\r\n", "\\n", "\r", "\\n", "\n", "\\n")
	flat := replaced.Replace(text)
	if len(flat) > limit {
		return flat[:limit] + "…"
	}
	return flat
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func fileSHA256(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return sha256Hex(data)
}

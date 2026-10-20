// pvfscriptedit 按显式 spec 重建 type-1 PVF 脚本文档的 token 流，并把结果写回
// 一个新的 PVF 副本（scratch 区，不进入生产路径）。
//
// 与 pvfsetfile 的区别：pvfsetfile 把条目内容当作"字符集编码的纯文本"，而
// type-1 脚本条目是 5 字节 token 流（[type:1][value:int32 LE]），因此用
// pvfsetfile 写脚本会得到 text_rune_count=0 且回读文本与预期不符。本工具直接
// 重建 token 流，不经过任何文本编码层。
//
// 设计约束（刻意保守，不猜）：
//   - 每个 token 都由 spec 显式给出：类型 0/2/3/5/6/7 用语义 kind，其余一律走
//     raw（调用方自己给 int32 负载）；
//   - kind=key/str 的文本必须已存在于归档 ANSI 字符串池，否则整体中止并列出
//     缺失项（fail closed），绝不写"猜"出来的 magic；
//   - spec 可带第 4 列 expected_magic，解析结果与该值不符时中止（防止池内重复
//     字符串解析到另一个偏移，从而静默改变原 token）；
//   - 默认绝不原地覆盖：-out 必须与 -pvf 不同；
//   - 写入后立即重新 Open 输出档，断言条目总数不变、目标条目回读文本与
//     -expect-text 给出的黄金文本逐字符相等、且原文本是新文本的精确前缀。
//
// spec 文件格式（UTF-8，制表符分隔，空行/# 与 // 注释行忽略）：
//
//	<type>\t<int|float|key|str|marker5|marker7|raw>\t<value>[\t<expected_magic>][\t# 注释]
//
// 用法：
//
//	# 1) 导出某脚本条目的 token 流为 spec（可直接当 -spec 用，字节级还原）
//	go run ./cmd/scratch/pvfscriptedit -pvf Script.pvf \
//	  -file equipment/character/common/title/dnf_administrator.equ \
//	  -dump-spec ./dnf_administrator.spec -dump-text ./dnf_administrator.txt
//
//	# 2) 查池（缺失即非零退出）
//	go run ./cmd/scratch/pvfscriptedit -pvf Script.pvf \
//	  -lookup "[physical attack]" -lookup "[HP MAX]"
//
//	# 3) 写补丁
//	go run ./cmd/scratch/pvfscriptedit -pvf Script.pvf \
//	  -file equipment/character/common/title/dnf_administrator.equ \
//	  -spec ./dnf_administrator.patched.spec -expect-text ./golden.txt \
//	  -preserve-prefix-bytes -stat-probe -out ./Script.patched.pvf -json
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const (
	tokenInt     byte = 0
	tokenFloat   byte = 2
	tokenKey     byte = 3
	tokenMarker5 byte = 5
	tokenString  byte = 6
	tokenMarker7 byte = 7
	tokenSize         = 5
)

type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }
func (l *stringList) Set(value string) error {
	*l = append(*l, value)
	return nil
}

// tokenSpec 是 spec 文件里的一行：一个待写入的 token。
type tokenSpec struct {
	Line             int    `json:"line"`
	Type             byte   `json:"type"`
	Kind             string `json:"kind"`
	Value            string `json:"value"`
	ExpectedMagic    int    `json:"expected_magic,omitempty"`
	HasExpectedMagic bool   `json:"-"`
}

// builtToken 是解析后的 token（含池 magic 与解码文本），用于报告与自检。
type builtToken struct {
	Index        int    `json:"index"`
	Line         int    `json:"line"`
	Type         byte   `json:"type"`
	Kind         string `json:"kind"`
	Value        string `json:"value,omitempty"`
	Payload      int32  `json:"payload"`
	Magic        *int   `json:"magic,omitempty"`
	DecodedText  string `json:"decoded_text"`
	OriginalByte bool   `json:"original_token"`
}

type statProbeEntry struct {
	Keys   []string `json:"keys"`
	Used   string   `json:"used_key,omitempty"`
	Number *float64 `json:"number,omitempty"`
}

type report struct {
	Mode               string           `json:"mode"`
	Input              string           `json:"input"`
	InputSize          int64            `json:"input_size"`
	InputSHA256        string           `json:"input_sha256"`
	InputFileCount     int              `json:"input_file_count"`
	ArchivePath        string           `json:"archive_path"`
	FileIndex          int              `json:"file_index"`
	DataType           int              `json:"data_type"`
	OriginalRawSize    int              `json:"original_raw_size"`
	OriginalTokenCount int              `json:"original_token_count"`
	OriginalSHA256     string           `json:"original_sha256"`
	OriginalTextRunes  int              `json:"original_text_rune_count"`
	NewRawSize         int              `json:"new_raw_size"`
	NewTokenCount      int              `json:"new_token_count"`
	NewSHA256          string           `json:"new_sha256"`
	NewTextRunes       int              `json:"new_text_rune_count"`
	SpecPath           string           `json:"spec_path,omitempty"`
	Output             string           `json:"output,omitempty"`
	OutputSize         int64            `json:"output_size,omitempty"`
	OutputSHA256       string           `json:"output_sha256,omitempty"`
	OutputFileCount    int              `json:"output_file_count,omitempty"`
	Lookups            map[string]int   `json:"lookups,omitempty"`
	MissingLookups     []string         `json:"missing_lookups,omitempty"`
	Tokens             []builtToken     `json:"tokens,omitempty"`
	StatProbe          []statProbeEntry `json:"stat_probe,omitempty"`
	PreservePrefix     bool             `json:"preserve_prefix_bytes"`
	PrefixBytesEqual   bool             `json:"prefix_bytes_equal,omitempty"`
	FileCountEqual     bool             `json:"file_count_equal,omitempty"`
	ReadbackTextEqual  bool             `json:"readback_text_equal,omitempty"`
	OriginalTextPrefix bool             `json:"original_text_is_prefix,omitempty"`
	ReadbackSnippet    string           `json:"readback_snippet,omitempty"`
	OK                 bool             `json:"ok"`
	Failures           []string         `json:"failures,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "pvfscriptedit:", err)
		os.Exit(1)
	}
}

func run() error {
	var lookups, optionalLookups stringList
	input := flag.String("pvf", "", "输入 PVF 路径（永不修改）")
	archivePath := flag.String("file", "", "归档内条目路径（必须是 type-1 脚本）")
	specPath := flag.String("spec", "", "spec 文件路径（写模式必填）")
	output := flag.String("out", "", "输出 PVF 路径（写模式必填，且必须不同于 -pvf）")
	expectText := flag.String("expect-text", "", "黄金文本文件（UTF-8，写模式必填）：回读文本必须与之逐字符相等")
	dumpSpec := flag.String("dump-spec", "", "把条目的 token 流导出为该路径的 spec 文件")
	dumpText := flag.String("dump-text", "", "把条目的解码文本导出到该路径（UTF-8，无 BOM）")
	overwrite := flag.Bool("overwrite", false, "允许覆盖已存在的 -out 文件")
	asJSON := flag.Bool("json", false, "输出 JSON 报告")
	printTokens := flag.Bool("print-tokens", true, "在 stdout 打印 token 表")
	preservePrefix := flag.Bool("preserve-prefix-bytes", false, "断言新 token 流的前 N 字节与原条目原始字节完全一致")
	// 默认强制"原文必须是回读文本的精确前缀"，即本工具只能**追加**字段。
	// 这在"要改已有字段的值 / 要删字段"时是做不到的（改一个数字就会破坏前缀），
	// 所以给出这个显式开关。切到它意味着放弃那条追加式守卫，必须靠
	// -expect-text 黄金文本 + 回读比对 + -stat-probe 三重独立校验兜底。
	allowTextRewrite := flag.Bool("allow-text-rewrite", false,
		"允许非追加式改写（改已有字段值/删字段）；默认关闭，开启后不再要求原文是回读文本的前缀")
	statProbe := flag.Bool("stat-probe", false, "写完后用 internal/modules/dnf/pvf 解析回读文本并按 equipmentStatVector 的键取值")
	flag.Var(&lookups, "lookup", "查询 ANSI 字符串池中的完整值（可重复；缺失则整体失败）")
	flag.Var(&optionalLookups, "lookup-optional", "同上，但缺失只记录不失败（用于变体排查）")
	flag.Parse()

	if *input == "" {
		return fmt.Errorf("-pvf is required")
	}
	if *archivePath == "" {
		return fmt.Errorf("-file is required")
	}
	inputAbs, err := filepath.Abs(*input)
	if err != nil {
		return err
	}
	archive, err := platformpvf.Open(inputAbs)
	if err != nil {
		return fmt.Errorf("open %s: %w", inputAbs, err)
	}
	idx := archive.FindFileIndex(*archivePath)
	if idx < 0 {
		return fmt.Errorf("%w: %s", platformpvf.ErrFileNotFound, *archivePath)
	}
	raw, err := archive.FileRaw(idx)
	if err != nil {
		return fmt.Errorf("read raw %s: %w", *archivePath, err)
	}
	if file := archive.Files()[idx]; file.DataType != 1 {
		return fmt.Errorf("%s has data type %d, want type-1 script", *archivePath, file.DataType)
	}
	originalText, err := archive.ReadText(*archivePath)
	if err != nil {
		return fmt.Errorf("read text %s: %w", *archivePath, err)
	}
	inputInfo, err := os.Stat(inputAbs)
	if err != nil {
		return err
	}
	result := report{
		Input:              inputAbs,
		InputSize:          inputInfo.Size(),
		InputSHA256:        fileSHA256(inputAbs),
		InputFileCount:     archive.FileCount(),
		ArchivePath:        *archivePath,
		FileIndex:          idx,
		DataType:           1,
		OriginalRawSize:    len(raw),
		OriginalTokenCount: len(raw) / tokenSize,
		OriginalSHA256:     sha256Hex(raw),
		OriginalTextRunes:  len([]rune(originalText)),
		SpecPath:           *specPath,
		PreservePrefix:     *preservePrefix,
		Lookups:            map[string]int{},
	}

	writeMode := *specPath != "" || *output != ""
	if !writeMode {
		result.Mode = "read-only"
		result.OK = true
	}

	// 池查询先做记录，但导出动作照常执行；缺值只在写路径上 fail closed。
	lookupErr := collectLookups(archive, lookups, optionalLookups, &result)

	if *dumpSpec != "" {
		spec, err := dumpSpecFile(archive, raw, *dumpSpec)
		if err != nil {
			return err
		}
		result.Mode = "dump"
		fmt.Printf("dump-spec %s tokens=%d bytes=%d\n", *dumpSpec, len(raw)/tokenSize, len(spec))
	}
	if *dumpText != "" {
		if err := writeFileExact(*dumpText, []byte(originalText)); err != nil {
			return err
		}
		fmt.Printf("dump-text %s runes=%d bytes=%d\n", *dumpText, len([]rune(originalText)), len(originalText))
	}

	if lookupErr != nil {
		result.Failures = append(result.Failures, lookupErr.Error())
		result.OK = false
		fmt.Printf("  FAIL %s\n", lookupErr.Error())
		if *asJSON {
			if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
				return err
			}
		}
		return lookupErr
	}

	if !writeMode {
		result.OK = true
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(result)
		}
		return nil
	}

	if *output == "" {
		return fmt.Errorf("-out is required in write mode")
	}
	if *specPath == "" {
		return fmt.Errorf("-spec is required in write mode")
	}
	if *expectText == "" {
		return fmt.Errorf("-expect-text is required in write mode")
	}
	outputAbs, err := filepath.Abs(*output)
	if err != nil {
		return err
	}
	if strings.EqualFold(inputAbs, outputAbs) {
		return fmt.Errorf("输出必须不同于输入；不支持原地覆盖：%s", outputAbs)
	}
	if _, err := os.Stat(outputAbs); err == nil && !*overwrite {
		return fmt.Errorf("输出文件已存在（要覆盖请加 -overwrite）：%s", outputAbs)
	}
	result.Mode = "write"
	result.Output = outputAbs

	specs, err := parseSpecFile(*specPath)
	if err != nil {
		return err
	}
	stream, tokens, err := buildStream(archive, specs, len(raw))
	if err != nil {
		result.Failures = append(result.Failures, err.Error())
		if *asJSON {
			_ = json.NewEncoder(os.Stdout).Encode(result)
		}
		return err
	}
	result.Tokens = tokens
	result.NewRawSize = len(stream)
	result.NewTokenCount = len(stream) / tokenSize
	result.NewSHA256 = sha256Hex(stream)

	goldenBytes, err := os.ReadFile(*expectText)
	if err != nil {
		return fmt.Errorf("read -expect-text %s: %w", *expectText, err)
	}
	golden := string(goldenBytes)

	if *preservePrefix {
		if len(stream) < len(raw) || !bytes.Equal(stream[:len(raw)], raw) {
			result.Failures = append(result.Failures, "新 token 流的前缀字节与原条目不一致")
			if *asJSON {
				_ = json.NewEncoder(os.Stdout).Encode(result)
			}
			return fmt.Errorf("前缀字节断言失败：%s 的前 %d 字节未被逐字节复现", *archivePath, len(raw))
		}
		result.PrefixBytesEqual = true
	}

	patched, err := archive.RepackRaw([]platformpvf.RawReplacement{{Path: *archivePath, Data: stream}})
	if err != nil {
		return fmt.Errorf("repack: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(outputAbs), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(outputAbs, patched, 0o644); err != nil {
		return err
	}
	result.OutputSize = int64(len(patched))
	result.OutputSHA256 = sha256Hex(patched)

	// 回读校验只用读取路径（Open + ReadText），与写路径相互独立。
	readback, err := platformpvf.Open(outputAbs)
	if err != nil {
		return fmt.Errorf("readback open %s: %w", outputAbs, err)
	}
	result.OutputFileCount = readback.FileCount()
	result.FileCountEqual = readback.FileCount() == archive.FileCount()
	text, err := readback.ReadText(*archivePath)
	if err != nil {
		return fmt.Errorf("readback text %s: %w", *archivePath, err)
	}
	result.NewTextRunes = len([]rune(text))
	result.ReadbackSnippet = snippet(text, 200)
	result.ReadbackTextEqual = text == golden
	result.OriginalTextPrefix = strings.HasPrefix(text, originalText)

	if !result.FileCountEqual {
		result.Failures = append(result.Failures,
			fmt.Sprintf("条目总数从 %d 变为 %d", archive.FileCount(), readback.FileCount()))
	}
	if !result.ReadbackTextEqual {
		result.Failures = append(result.Failures,
			fmt.Sprintf("回读文本与黄金文本不一致（rune 数 %d vs 期望 %d）", len([]rune(text)), len([]rune(golden))))
	}
	if !result.OriginalTextPrefix && !*allowTextRewrite {
		result.Failures = append(result.Failures, "原文不是回读文本的精确前缀（原 token 未被逐字符复现）；"+
			"确认这是有意的改写而非误伤时，加 -allow-text-rewrite 重跑")
	}
	if result.NewTextRunes == 0 {
		result.Failures = append(result.Failures, "text_rune_count 为 0")
	}
	backRaw, err := readback.FileRaw(idx)
	if err != nil {
		return fmt.Errorf("readback raw %s: %w", *archivePath, err)
	}
	if sha256Hex(backRaw) != result.NewSHA256 {
		result.Failures = append(result.Failures, "回读原始字节与写入的 token 流 sha256 不一致")
	}

	if *statProbe {
		probe, err := runStatProbe(text)
		if err != nil {
			result.Failures = append(result.Failures, "stat probe: "+err.Error())
		}
		result.StatProbe = probe
	}
	result.OK = len(result.Failures) == 0

	if *printTokens {
		printTokenTable(result.Tokens)
	}
	fmt.Printf("input  %s size=%d files=%d\n", result.Input, result.InputSize, result.InputFileCount)
	fmt.Printf("target %s index=%d old_tokens=%d old_bytes=%d old_runes=%d sha256=%s\n",
		result.ArchivePath, result.FileIndex, result.OriginalTokenCount, result.OriginalRawSize,
		result.OriginalTextRunes, result.OriginalSHA256)
	fmt.Printf("new    tokens=%d bytes=%d sha256=%s\n", result.NewTokenCount, result.NewRawSize, result.NewSHA256)
	fmt.Printf("output %s size=%d sha256=%s files=%d\n",
		result.Output, result.OutputSize, result.OutputSHA256, result.OutputFileCount)
	fmt.Printf("lookups=%v missing=%v\n", result.Lookups, result.MissingLookups)
	fmt.Printf("verify file_count_equal=%v prefix_bytes_equal=%v text_rune_count=%d readback_text_equal=%v original_text_is_prefix=%v\n",
		result.FileCountEqual, result.PrefixBytesEqual, result.NewTextRunes, result.ReadbackTextEqual, result.OriginalTextPrefix)
	for _, entry := range result.StatProbe {
		value := "MISSING"
		if entry.Number != nil {
			value = strconv.FormatFloat(*entry.Number, 'f', -1, 64)
		}
		fmt.Printf("  stat %-18s used=%-28s value=%s\n", strings.Join(entry.Keys, "|"), entry.Used, value)
	}
	fmt.Printf("readback=%q\n", result.ReadbackSnippet)
	for _, failure := range result.Failures {
		fmt.Printf("  FAIL %s\n", failure)
	}
	fmt.Printf("ok=%v\n", result.OK)
	if *asJSON {
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			return err
		}
	}
	if !result.OK {
		return fmt.Errorf("回读校验失败（%d 项）：见上方 FAIL 行", len(result.Failures))
	}
	return nil
}

func collectLookups(archive *platformpvf.Archive, required, optional stringList, result *report) error {
	for _, value := range required {
		magic, ok := archive.LookupStringMagic(value)
		if !ok {
			result.MissingLookups = append(result.MissingLookups, value)
			continue
		}
		result.Lookups[value] = magic
	}
	for _, value := range optional {
		if magic, ok := archive.LookupStringMagic(value); ok {
			result.Lookups[value] = magic
		} else if _, seen := result.Lookups[value]; !seen {
			result.Lookups[value] = -1
		}
	}
	if len(result.MissingLookups) > 0 {
		return fmt.Errorf("ANSI 字符串池缺少 %d 个必需值：%s", len(result.MissingLookups),
			strings.Join(result.MissingLookups, ", "))
	}
	return nil
}

// dumpSpecFile 把条目的 token 流写成可直接复用的 spec：字符串 token 优先写成
// 可读文本 + expected_magic（只有解析结果与原 magic 一致时才这么做），否则退回
// raw + 字面 magic，并在注释里给出解码文本，便于人工评审。
func dumpSpecFile(archive *platformpvf.Archive, raw []byte, target string) (string, error) {
	var b strings.Builder
	b.WriteString("# pvfscriptedit spec：由现有 type-1 脚本文档导出\n")
	b.WriteString("# 列：type\\tkind\\tvalue[\\texpected_magic][\\t# 注释]\n")
	b.WriteString("# kind=key(type3)/str(type6) 的文本必须存在于 ANSI 字符串池，否则写入时报错\n")
	for offset := 0; offset+tokenSize <= len(raw); offset += tokenSize {
		tokenType := raw[offset]
		value := int32(binary.LittleEndian.Uint32(raw[offset+1 : offset+tokenSize]))
		text := archive.ResolveString(int(value))
		switch tokenType {
		case tokenInt:
			fmt.Fprintf(&b, "%d\tint\t%d\n", tokenType, value)
		case tokenFloat:
			fmt.Fprintf(&b, "%d\traw\t%d\t# float32 %s\n", tokenType, value,
				strconv.FormatFloat(float64(math.Float32frombits(uint32(value))), 'f', -1, 32))
		case tokenKey, tokenString:
			kind := "key"
			if tokenType == tokenString {
				kind = "str"
			}
			if magic, ok := archive.LookupStringMagic(text); ok && magic == int(value) {
				fmt.Fprintf(&b, "%d\t%s\t%s\t%d\n", tokenType, kind, text, value)
				continue
			}
			fmt.Fprintf(&b, "%d\traw\t%d\t# text=%q\n", tokenType, value, text)
		case tokenMarker5:
			fmt.Fprintf(&b, "%d\tmarker5\t{}\n", tokenType)
		case tokenMarker7:
			fmt.Fprintf(&b, "%d\tmarker7\t{}\n", tokenType)
		default:
			fmt.Fprintf(&b, "%d\traw\t%d\t# undocumented token type, decoded text=%q\n",
				tokenType, value, text)
		}
	}
	if err := writeFileExact(target, []byte(b.String())); err != nil {
		return "", err
	}
	return b.String(), nil
}

func parseSpecFile(path string) ([]tokenSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read -spec %s: %w", path, err)
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	specs := make([]tokenSpec, 0, len(lines))
	for index, line := range lines {
		lineNo := index + 1
		if cut := strings.Index(line, "\t#"); cut >= 0 {
			line = line[:cut]
		} else if strings.HasPrefix(strings.TrimSpace(line), "#") || strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			return nil, fmt.Errorf("spec 第 %d 行字段不足（需要 type\\tkind\\tvalue）：%q", lineNo, line)
		}
		typeValue, err := strconv.ParseUint(strings.TrimSpace(fields[0]), 10, 8)
		if err != nil {
			return nil, fmt.Errorf("spec 第 %d 行 type 非法：%q", lineNo, fields[0])
		}
		spec := tokenSpec{Line: lineNo, Type: byte(typeValue), Kind: strings.TrimSpace(fields[1]), Value: fields[2]}
		if len(fields) >= 4 && strings.TrimSpace(fields[3]) != "" {
			magic, err := strconv.Atoi(strings.TrimSpace(fields[3]))
			if err != nil {
				return nil, fmt.Errorf("spec 第 %d 行 expected_magic 非法：%q", lineNo, fields[3])
			}
			spec.ExpectedMagic = magic
			spec.HasExpectedMagic = true
		}
		specs = append(specs, spec)
	}
	if len(specs) == 0 {
		return nil, fmt.Errorf("spec %s 没有任何 token", path)
	}
	return specs, nil
}

// buildStream 把 spec 解析成 5 字节 token 流。任何无法确定的取值都必须报错，
// 不做默认值猜测。
func buildStream(archive *platformpvf.Archive, specs []tokenSpec, originalSize int) ([]byte, []builtToken, error) {
	stream := make([]byte, 0, len(specs)*tokenSize)
	tokens := make([]builtToken, 0, len(specs))
	missing := make([]string, 0)
	for _, spec := range specs {
		token := builtToken{
			Index: len(tokens),
			Line:  spec.Line,
			Type:  spec.Type,
			Kind:  spec.Kind,
			Value: spec.Value,
		}
		var payload int32
		switch spec.Kind {
		case "int":
			if spec.Type != tokenInt {
				return nil, tokens, fmt.Errorf("spec 第 %d 行：kind=int 必须配 type=0，得到 type=%d", spec.Line, spec.Type)
			}
			value, err := strconv.ParseInt(strings.TrimSpace(spec.Value), 10, 32)
			if err != nil {
				return nil, tokens, fmt.Errorf("spec 第 %d 行 int 非法：%q", spec.Line, spec.Value)
			}
			payload = int32(value)
		case "float":
			if spec.Type != tokenFloat {
				return nil, tokens, fmt.Errorf("spec 第 %d 行：kind=float 必须配 type=2，得到 type=%d", spec.Line, spec.Type)
			}
			value, err := strconv.ParseFloat(strings.TrimSpace(spec.Value), 32)
			if err != nil {
				return nil, tokens, fmt.Errorf("spec 第 %d 行 float 非法：%q", spec.Line, spec.Value)
			}
			payload = int32(math.Float32bits(float32(value)))
		case "key", "str":
			want := tokenKey
			if spec.Kind == "str" {
				want = tokenString
			}
			if spec.Type != want {
				return nil, tokens, fmt.Errorf("spec 第 %d 行：kind=%s 必须配 type=%d，得到 type=%d",
					spec.Line, spec.Kind, want, spec.Type)
			}
			if spec.Value == "" {
				if !spec.HasExpectedMagic {
					return nil, tokens, fmt.Errorf("spec 第 %d 行：空字符串必须显式给出 expected_magic（用 kind=raw）", spec.Line)
				}
				payload = int32(spec.ExpectedMagic)
				break
			}
			magic, ok := archive.LookupStringMagic(spec.Value)
			if !ok {
				missing = append(missing, spec.Value)
				continue
			}
			if spec.HasExpectedMagic && magic != spec.ExpectedMagic {
				return nil, tokens, fmt.Errorf("spec 第 %d 行：%q 解析到 magic %d，与 expected_magic %d 不符",
					spec.Line, spec.Value, magic, spec.ExpectedMagic)
			}
			payload = int32(magic)
			resolved := magic
			token.Magic = &resolved
		case "marker5", "marker7":
			want := tokenMarker5
			if spec.Kind == "marker7" {
				want = tokenMarker7
			}
			if spec.Type != want {
				return nil, tokens, fmt.Errorf("spec 第 %d 行：kind=%s 必须配 type=%d，得到 type=%d",
					spec.Line, spec.Kind, want, spec.Type)
			}
			trimmed := strings.TrimSpace(spec.Value)
			if trimmed != "{}" && trimmed != "-" {
				return nil, tokens, fmt.Errorf("spec 第 %d 行：%s 的 value 只能是 {} 或 -，得到 %q",
					spec.Line, spec.Kind, spec.Value)
			}
		case "raw":
			value, err := strconv.ParseInt(strings.TrimSpace(spec.Value), 10, 32)
			if err != nil {
				return nil, tokens, fmt.Errorf("spec 第 %d 行 raw 负载非法：%q", spec.Line, spec.Value)
			}
			if spec.HasExpectedMagic && int(value) != spec.ExpectedMagic {
				return nil, tokens, fmt.Errorf("spec 第 %d 行：raw 负载 %d 与 expected_magic %d 不符",
					spec.Line, value, spec.ExpectedMagic)
			}
			payload = int32(value)
		default:
			return nil, tokens, fmt.Errorf("spec 第 %d 行 kind 未知：%q", spec.Line, spec.Kind)
		}
		stream = append(stream, spec.Type, 0, 0, 0, 0)
		binary.LittleEndian.PutUint32(stream[len(stream)-4:], uint32(payload))
		token.Payload = payload
		token.DecodedText = decodeTokenText(archive, spec.Type, payload)
		token.OriginalByte = token.Index*tokenSize+tokenSize <= originalSize
		tokens = append(tokens, token)
	}
	if len(missing) > 0 {
		return nil, tokens, fmt.Errorf("ANSI 字符串池缺少 %d 个必需值（fail closed）：%s",
			len(missing), strings.Join(missing, ", "))
	}
	return stream, tokens, nil
}

// decodeTokenText 复刻 internal/platform/pvf 的 token 文本语义，仅用于报告。
func decodeTokenText(archive *platformpvf.Archive, tokenType byte, payload int32) string {
	switch tokenType {
	case tokenInt:
		return strconv.FormatInt(int64(payload), 10) + " "
	case tokenFloat:
		return strconv.FormatFloat(float64(math.Float32frombits(uint32(payload))), 'f', 2, 32) + " "
	case tokenKey:
		return "\\n" + archive.ResolveString(int(payload)) + "\\n"
	case tokenMarker5:
		return "\\n{5=``}"
	case tokenString:
		return "`" + archive.ResolveString(int(payload)) + "` "
	case tokenMarker7:
		return "\\n{7=``}"
	}
	return ""
}

func printTokenTable(tokens []builtToken) {
	fmt.Printf("%-5s %-5s %-8s %-14s %-12s %s\n", "idx", "type", "kind", "payload", "magic", "decoded")
	for _, token := range tokens {
		magic := "-"
		if token.Magic != nil {
			magic = strconv.Itoa(*token.Magic)
		}
		mark := " "
		if token.OriginalByte {
			mark = "*"
		}
		fmt.Printf("%s%-4d %-5d %-8s %-14d %-12s %q\n",
			mark, token.Index, token.Type, token.Kind, token.Payload, magic, token.DecodedText)
	}
}

// runStatProbe 用服务端真正使用的解析器核对六个战斗属性段落。
func runStatProbe(text string) ([]statProbeEntry, error) {
	doc, err := dnfpvf.Parse("equipment/character/common/title/dnf_administrator.equ", text)
	if err != nil {
		return nil, fmt.Errorf("dnf pvf parse: %w", err)
	}
	groups := [][]string{
		{"HP MAX", "hp max"},
		{"physical attack", "physical weapon attack", "equipment physical attack"},
		{"physical defense", "equipment physical defense"},
		{"magical attack", "magical weapon attack", "equipment magical attack"},
		{"magical defense", "equipment magical defense"},
		{"independent attack", "separate attack"},
	}
	out := make([]statProbeEntry, 0, len(groups))
	for _, keys := range groups {
		entry := statProbeEntry{Keys: keys}
		for _, key := range keys {
			if values := doc.Numbers(key); len(values) > 0 {
				value := values[0]
				entry.Used = key
				entry.Number = &value
				break
			}
			if texts := doc.Texts(key); len(texts) > 0 {
				value, err := strconv.ParseFloat(strings.TrimSpace(texts[0]), 64)
				if err == nil {
					entry.Used = key
					entry.Number = &value
					break
				}
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

func writeFileExact(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
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

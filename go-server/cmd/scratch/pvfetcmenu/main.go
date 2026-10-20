// pvfetcmenu 是一次性 PVF 频道 UI 文档（type-1 token 脚本）结构改写工具
// （scratch 区，不进入生产路径）。
//
// 背景：etc/channeluiinfo_chn.etc（与 etc/channeluiinfo.etc 内容完全相同）不是纯
// 文本，而是 PVF 的 5 字节 token 流：每 5 字节 = 1 字节 token 类型 + 4 字节小端
// 值。字符串不是内联的，而是对全档共享字符串池的"编码引用"（magic offset）；
// 因此 pvfpatch（只能替换池里已存在值的等宽引用）无法新增段落，而自由拼文本也
// 不可行——必须按 token 结构改写。
//
// 本工具只做"字节级"改写，产出新的原始 token 字节（-out-raw）与该字节流的
// 渲染文本（-out-text，用于人读与和回读结果对拍）；写回 PVF 由 pvfsetfile
// -encoding raw 完成，保持单一写入路径。
//
// 用法示例：
//
//	# 列出所有 [channel] 家族块
//	go run ./cmd/scratch/pvfetcmenu -pvf Script.pvf -list
//	# 打印 token 明细
//	go run ./cmd/scratch/pvfetcmenu -pvf Script.pvf -dump-tokens
//	# 把 CHANNEL_ZOMBIE 的 [main menu] 段复制给 PVP 家族，并把四项改成 1 1
//	go run ./cmd/scratch/pvfetcmenu -pvf Script.pvf \
//	  -variant copy-main-menu -from-family CHANNEL_ZOMBIE -to-family CHANNEL_TOURNAMENT_PVP \
//	  -set SELECT_CHARACTER_WINDOW=1,1 -set CHANNEL_SELECT_WINDOW=1,1 \
//	  -set GO_TO_TOWN=1,1 -set EXIT_GAME=1,1 \
//	  -out-raw new.etc -out-text new.etc.txt
package main

import (
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

	platformpvf "longheng.io/server/internal/platform/pvf"
)

const (
	tokenSize   = 5
	sectionOpen = "[channel]"
	sectionMenu = "[main menu]"
	sectionTown = "[town index]"
)

type token struct {
	typ   byte
	value int32
}

func (t token) isStringLine(value string, resolve func(int32) string) bool {
	return t.typ == 3 && resolve(t.value) == value
}

type span struct{ start, end int } // 闭区间 token 下标

type block struct {
	channel span
	names   []int // [channel] 列表里 type-6 名字 token 的下标
	menu    *span
	town    *span
	all     span
}

type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }
func (l *stringList) Set(value string) error {
	*l = append(*l, value)
	return nil
}

type setSpec struct {
	item string
	v1   int32
	v2   int32
}

type setList []setSpec

func (s *setList) String() string {
	parts := make([]string, 0, len(*s))
	for _, spec := range *s {
		parts = append(parts, fmt.Sprintf("%s=%d,%d", spec.item, spec.v1, spec.v2))
	}
	return strings.Join(parts, ";")
}

func (s *setList) Set(value string) error {
	parts := strings.SplitN(value, "=", 2)
	if len(parts) != 2 {
		return fmt.Errorf("want ITEM=v1,v2, got %q", value)
	}
	values := strings.SplitN(parts[1], ",", 2)
	if len(values) != 2 {
		return fmt.Errorf("want ITEM=v1,v2, got %q", value)
	}
	v1, err := strconv.ParseInt(strings.TrimSpace(values[0]), 10, 32)
	if err != nil {
		return err
	}
	v2, err := strconv.ParseInt(strings.TrimSpace(values[1]), 10, 32)
	if err != nil {
		return err
	}
	*s = append(*s, setSpec{item: strings.TrimSpace(parts[0]), v1: int32(v1), v2: int32(v2)})
	return nil
}

type blockReport struct {
	Index       int      `json:"index"`
	Names       []string `json:"names"`
	ChannelSpan [2]int   `json:"channel_span"`
	HasMenu     bool     `json:"has_main_menu"`
	MenuSpan    *[2]int  `json:"menu_span,omitempty"`
	HasTown     bool     `json:"has_town_index"`
	TownSpan    *[2]int  `json:"town_span,omitempty"`
	BlockSpan   [2]int   `json:"block_span"`
	TokenCount  int      `json:"token_count"`
}

type report struct {
	Input       string        `json:"input"`
	ArchivePath string        `json:"archive_path"`
	FileIndex   int           `json:"file_index"`
	DataType    int           `json:"data_type"`
	OldSize     int           `json:"old_size"`
	OldSHA256   string        `json:"old_sha256"`
	NewSize     int           `json:"new_size"`
	NewSHA256   string        `json:"new_sha256"`
	Variant     string        `json:"variant,omitempty"`
	Blocks      []blockReport `json:"blocks"`
	Applied     []string      `json:"applied,omitempty"`
	OutRaw      string        `json:"out_raw,omitempty"`
	OutText     string        `json:"out_text,omitempty"`
	OldTextLen  int           `json:"old_text_runes"`
	NewTextLen  int           `json:"new_text_runes"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "pvfetcmenu:", err)
		os.Exit(1)
	}
}

func run() error {
	var sets setList
	var names stringList
	input := flag.String("pvf", "", "输入 PVF 路径（永不修改）")
	archivePath := flag.String("file", "etc/channeluiinfo_chn.etc", "目标条目路径")
	list := flag.Bool("list", false, "列出所有 [channel] 家族块")
	dumpTokens := flag.Bool("dump-tokens", false, "打印 token 明细")
	variant := flag.String("variant", "", "copy-main-menu|drop-names|drop-block")
	fromFamily := flag.String("from-family", "", "复制 [main menu] 段的源家族（按 [channel] 列表里的名字）")
	toFamily := flag.String("to-family", "", "被改写的目标家族")
	allNames := flag.Bool("all-names", false, "drop-names 时删除目标家族 [channel] 列表里的全部名字")
	outRaw := flag.String("out-raw", "", "写出改写后的原始 token 字节")
	outText := flag.String("out-text", "", "写出改写后 token 流的渲染文本")
	asJSON := flag.Bool("json", false, "输出 JSON 报告")
	flag.Var(&names, "name", "drop-names 时要删除的名字（可重复）")
	flag.Var(&sets, "set", "copy-main-menu 时覆盖条目值 ITEM=v1,v2（可重复）")
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
	resolve := func(magicOffset int32) string { return archive.ResolveString(int(magicOffset)) }
	idx := archive.FindFileIndex(*archivePath)
	if idx < 0 {
		return fmt.Errorf("%w: %s", platformpvf.ErrFileNotFound, *archivePath)
	}
	raw, err := archive.FileRaw(idx)
	if err != nil {
		return err
	}
	file := archive.Files()[idx]
	tokens, err := parseTokens(raw)
	if err != nil {
		return fmt.Errorf("parse %s: %w", *archivePath, err)
	}
	blocks, namesByBlock, err := parseBlocks(tokens, resolve)
	if err != nil {
		return fmt.Errorf("parse blocks %s: %w", *archivePath, err)
	}

	result := report{
		Input:       inputAbs,
		ArchivePath: file.ArchivePath,
		FileIndex:   idx,
		DataType:    file.DataType,
		OldSize:     len(raw),
		OldSHA256:   sha256Hex(raw),
		Variant:     *variant,
		Blocks:      describeBlocks(blocks, namesByBlock),
		OldTextLen:  len([]rune(render(tokens, resolve))),
	}
	if *list || *dumpTokens {
		for _, record := range result.Blocks {
			fmt.Printf("BLOCK #%d span=[%d,%d] tokens=%d names=%v menu=%v town=%v\n",
				record.Index, record.BlockSpan[0], record.BlockSpan[1], record.TokenCount,
				record.Names, record.HasMenu, record.HasTown)
		}
	}
	if *dumpTokens {
		for tokenIndex, item := range tokens {
			detail := ""
			if item.typ == 3 || item.typ == 6 {
				detail = " " + strconv.Quote(resolve(item.value))
			}
			fmt.Printf("TOKEN %4d type=%d value=%d%s\n", tokenIndex, item.typ, item.value, detail)
		}
	}
	if *variant == "" {
		if *outRaw == "" && *outText == "" {
			if *asJSON {
				return json.NewEncoder(os.Stdout).Encode(result)
			}
			return nil
		}
		fmt.Println("no -variant given: writing the unchanged token stream (identity mode)")
	}

	patched := append([]token(nil), tokens...)
	var applied []string
	if *variant != "" {
		patched, applied, err = apply(tokens, blocks, namesByBlock, resolve, *variant, *fromFamily, *toFamily, names, *allNames, sets)
		if err != nil {
			return err
		}
	} else {
		applied = append(applied, "identity: token stream unchanged")
	}
	result.Applied = applied
	newRaw := encodeTokens(patched)
	newText := render(patched, resolve)
	result.NewSize = len(newRaw)
	result.NewSHA256 = sha256Hex(newRaw)
	result.NewTextLen = len([]rune(newText))

	// 自检：新字节流必须能被重新解析，并且解析出的块结构要合理。
	if _, err := parseTokens(newRaw); err != nil {
		return fmt.Errorf("self-check parse: %w", err)
	}
	newBlocks, newNames, err := parseBlocks(patched, resolve)
	if err != nil {
		return fmt.Errorf("self-check blocks: %w", err)
	}
	result.Blocks = describeBlocks(newBlocks, newNames)

	if *outRaw != "" {
		if err := os.MkdirAll(filepath.Dir(*outRaw), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(*outRaw, newRaw, 0o644); err != nil {
			return err
		}
		target, err := filepath.Abs(*outRaw)
		if err != nil {
			return err
		}
		result.OutRaw = target
	}
	if *outText != "" {
		if err := os.MkdirAll(filepath.Dir(*outText), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(*outText, []byte(newText), 0o644); err != nil {
			return err
		}
		target, err := filepath.Abs(*outText)
		if err != nil {
			return err
		}
		result.OutText = target
	}

	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	fmt.Printf("variant=%s %s %d -> %d bytes (sha256 %s -> %s) tokens=%d -> %d\n",
		*variant, *archivePath, result.OldSize, result.NewSize,
		result.OldSHA256[:12], result.NewSHA256[:12], len(tokens), len(patched))
	for _, line := range applied {
		fmt.Printf("  APPLIED %s\n", line)
	}
	for _, record := range result.Blocks {
		fmt.Printf("  BLOCK #%d span=[%d,%d] names=%v menu=%v town=%v\n",
			record.Index, record.BlockSpan[0], record.BlockSpan[1], record.Names, record.HasMenu, record.HasTown)
	}
	if result.OutRaw != "" {
		fmt.Printf("  out-raw  %s\n", result.OutRaw)
	}
	if result.OutText != "" {
		fmt.Printf("  out-text %s\n", result.OutText)
	}
	return nil
}

func parseTokens(raw []byte) ([]token, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty script")
	}
	if len(raw)%tokenSize != 0 {
		return nil, fmt.Errorf("size %d is not a multiple of %d", len(raw), tokenSize)
	}
	out := make([]token, 0, len(raw)/tokenSize)
	for offset := 0; offset < len(raw); offset += tokenSize {
		out = append(out, token{
			typ:   raw[offset],
			value: int32(binary.LittleEndian.Uint32(raw[offset+1 : offset+tokenSize])),
		})
	}
	return out, nil
}

func encodeTokens(tokens []token) []byte {
	out := make([]byte, len(tokens)*tokenSize)
	for index, item := range tokens {
		offset := index * tokenSize
		out[offset] = item.typ
		binary.LittleEndian.PutUint32(out[offset+1:offset+tokenSize], uint32(item.value))
	}
	return out
}

// render 复刻客户端脚本渲染：type-0 整数、type-2 浮点、type-3 整行字符串、
// type-6 反引号字符串；其余类型不产生可见文本。
func render(tokens []token, resolve func(int32) string) string {
	var b strings.Builder
	for _, item := range tokens {
		switch item.typ {
		case 0:
			fmt.Fprintf(&b, "%d ", item.value)
		case 2:
			fmt.Fprintf(&b, "%.2f ", float32frombits(uint32(item.value)))
		case 3:
			b.WriteByte('\n')
			b.WriteString(resolve(item.value))
			b.WriteByte('\n')
		case 5:
			b.WriteString("\n{5=``}")
		case 6:
			b.WriteByte('`')
			b.WriteString(resolve(item.value))
			b.WriteString("` ")
		case 7:
			b.WriteString("\n{7=``}")
		}
	}
	return b.String()
}

func float32frombits(bits uint32) float32 {
	return math.Float32frombits(bits)
}

func parseBlocks(tokens []token, resolve func(int32) string) ([]block, map[int][]string, error) {
	var blocks []block
	namesByBlock := make(map[int][]string)
	index := 0
	for index < len(tokens) {
		if !tokens[index].isStringLine(sectionOpen, resolve) {
			index++
			continue
		}
		current := block{channel: span{start: index}}
		index++
		for index < len(tokens) {
			item := tokens[index]
			if item.typ == 6 {
				current.names = append(current.names, index)
				namesByBlock[len(blocks)] = append(namesByBlock[len(blocks)], resolve(item.value))
				index++
				continue
			}
			break
		}
		if index >= len(tokens) || !tokens[index].isStringLine("[/channel]", resolve) {
			return nil, nil, fmt.Errorf("[channel] at token %d is not closed by [/channel]", current.channel.start)
		}
		current.channel.end = index
		index++
		// 收集紧跟其后的可选段，直到下一个 [channel] 或文件结束。
		for index < len(tokens) {
			switch {
			case tokens[index].isStringLine(sectionOpen, resolve):
				goto close
			case tokens[index].isStringLine(sectionMenu, resolve):
				end, next, err := sectionEnd(tokens, resolve, index, sectionMenu, "[/mainmenu]")
				if err != nil {
					return nil, nil, err
				}
				section := span{start: index, end: end}
				current.menu = &section
				index = next
			case tokens[index].isStringLine(sectionTown, resolve):
				end, next, err := sectionEnd(tokens, resolve, index, sectionTown, "[/town index]")
				if err != nil {
					return nil, nil, err
				}
				section := span{start: index, end: end}
				current.town = &section
				index = next
			default:
				index++
			}
		}
	close:
		current.all = span{start: current.channel.start, end: index - 1}
		blocks = append(blocks, current)
	}
	return blocks, namesByBlock, nil
}

func sectionEnd(tokens []token, resolve func(int32) string, start int, open, close string) (int, int, error) {
	for index := start + 1; index < len(tokens); index++ {
		if tokens[index].isStringLine(close, resolve) {
			return index, index + 1, nil
		}
	}
	return 0, 0, fmt.Errorf("%s at token %d is not closed by %s", open, start, close)
}

func describeBlocks(blocks []block, names map[int][]string) []blockReport {
	out := make([]blockReport, 0, len(blocks))
	for index, item := range blocks {
		record := blockReport{
			Index:       index,
			Names:       names[index],
			ChannelSpan: [2]int{item.channel.start, item.channel.end},
			HasMenu:     item.menu != nil,
			HasTown:     item.town != nil,
			BlockSpan:   [2]int{item.all.start, item.all.end},
			TokenCount:  item.all.end - item.all.start + 1,
		}
		if item.menu != nil {
			record.MenuSpan = &[2]int{item.menu.start, item.menu.end}
		}
		if item.town != nil {
			record.TownSpan = &[2]int{item.town.start, item.town.end}
		}
		out = append(out, record)
	}
	return out
}

func findBlock(blocks []block, names map[int][]string, family string) (int, error) {
	for index, list := range names {
		for _, name := range list {
			if name == family {
				return index, nil
			}
		}
	}
	return -1, fmt.Errorf("family %q not found in any [channel] block", family)
}

func apply(tokens []token, blocks []block, names map[int][]string, resolve func(int32) string,
	variant, fromFamily, toFamily string, dropNames []string, allNames bool, sets setList) ([]token, []string, error) {
	var applied []string
	switch variant {
	case "copy-main-menu":
		sourceIndex, err := findBlock(blocks, names, fromFamily)
		if err != nil {
			return nil, nil, err
		}
		targetIndex, err := findBlock(blocks, names, toFamily)
		if err != nil {
			return nil, nil, err
		}
		source := blocks[sourceIndex]
		if source.menu == nil {
			return nil, nil, fmt.Errorf("family %q has no [main menu] section to copy", fromFamily)
		}
		if blocks[targetIndex].menu != nil {
			return nil, nil, fmt.Errorf("family %q already has a [main menu] section", toFamily)
		}
		copied := append([]token(nil), tokens[source.menu.start:source.menu.end+1]...)
		for _, spec := range sets {
			if err := setItem(copied, resolve, spec); err != nil {
				return nil, nil, err
			}
			applied = append(applied, fmt.Sprintf("set %s = %d %d", spec.item, spec.v1, spec.v2))
		}
		applied = append(applied,
			fmt.Sprintf("copied [main menu] tokens [%d,%d] (%d tokens) from %s to %s",
				source.menu.start, source.menu.end, len(copied), fromFamily, toFamily))
		target := blocks[targetIndex]
		insertAt := target.all.end + 1
		if target.town != nil {
			insertAt = target.town.start
		}
		out := make([]token, 0, len(tokens)+len(copied))
		out = append(out, tokens[:insertAt]...)
		out = append(out, copied...)
		out = append(out, tokens[insertAt:]...)
		return out, applied, nil
	case "drop-names":
		targetIndex, err := findBlock(blocks, names, toFamily)
		if err != nil {
			return nil, nil, err
		}
		target := blocks[targetIndex]
		drop := make(map[int]bool)
		if allNames {
			for _, tokenIndex := range target.names {
				drop[tokenIndex] = true
			}
			applied = append(applied, fmt.Sprintf("dropped all %d channel names from %s", len(target.names), toFamily))
		} else {
			wanted := make(map[string]bool, len(dropNames))
			for _, name := range dropNames {
				wanted[name] = true
			}
			for _, tokenIndex := range target.names {
				if wanted[resolve(tokens[tokenIndex].value)] {
					drop[tokenIndex] = true
					applied = append(applied, fmt.Sprintf("dropped channel name %s", resolve(tokens[tokenIndex].value)))
				}
			}
			if len(drop) != len(wanted) {
				return nil, nil, fmt.Errorf("wanted to drop %d names but matched %d", len(wanted), len(drop))
			}
		}
		out := make([]token, 0, len(tokens)-len(drop))
		for index, item := range tokens {
			if drop[index] {
				continue
			}
			out = append(out, item)
		}
		return out, applied, nil
	case "drop-block":
		targetIndex, err := findBlock(blocks, names, toFamily)
		if err != nil {
			return nil, nil, err
		}
		target := blocks[targetIndex]
		applied = append(applied, fmt.Sprintf("dropped block #%d %s tokens [%d,%d]",
			targetIndex, toFamily, target.all.start, target.all.end))
		out := make([]token, 0, len(tokens))
		out = append(out, tokens[:target.all.start]...)
		out = append(out, tokens[target.all.end+1:]...)
		return out, applied, nil
	}
	return nil, nil, fmt.Errorf("unknown -variant %q", variant)
}

// setItem 在段内定位 `ITEM v1 v2` 三元组并覆盖两个整数。
func setItem(section []token, resolve func(int32) string, spec setSpec) error {
	for index := 0; index < len(section); index++ {
		if section[index].typ != 6 || resolve(section[index].value) != spec.item {
			continue
		}
		if index+2 >= len(section) || section[index+1].typ != 0 || section[index+2].typ != 0 {
			return fmt.Errorf("item %s is not followed by two integers", spec.item)
		}
		section[index+1].value = spec.v1
		section[index+2].value = spec.v2
		return nil
	}
	return fmt.Errorf("item %s not found in the copied section", spec.item)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

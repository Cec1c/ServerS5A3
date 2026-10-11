// Command pvfnameraw 把桩名字道具的 [name]/[explain] 用"同宽就地改写"补成正式文案。
//
// 线上实测（2026-09-24，Script.pvf EC7050524D…）：
//   - [name]/[explain] 是字符串池引用（token type 6 + ANSI 池 offset）；
//   - PatchScriptString 走不通（目标文案不在池里）；
//   - RepackStrings 可用，但**逐字节**写池、宽度按 Go 字符串字节数比较
//     （name 槽位 14 字节、explain 槽位 17 字节，报错形如
//     "replacement width changed from 14 to 24"）。
//
// 关键教训：池子是 ANSI(CP936/GBK)，写进去的必须是 **GBK 字节**。
// 上一版直接把 Go 的 UTF-8 文案写进池，宽度恰好相等所以通过了校验，
// 客户端按 GBK 解码就是乱码（截图 `纈垮 绌肩湧`）。
// 所以本工具：
//
//   - 支持 `-name-hex` / `-explain-hex` 直接指定**写入池的原始字节**（UTF-8 十六进制串）；
//
//   - 写入前按 GBK 解码回读校验（必须等于目标中文），并按字节宽度对齐槽位（补 0x20 空格）；
//
//   - 写入后再做三重字节级验证：产物字节里必须含目标 GBK 串、必须不含该文本的 UTF-8 串、
//     重新打开 PVF 按 GBK 回读该 offset 必须等于目标中文。
//
//     go run ./cmd/scratch/pvfnameraw -pvf pristine.pvf -out named.pvf \
//     -name-hex b4babddabbb6c7ecc0f1bad02020 -explain-hex c0f1bad0c4dabdb1c0f8202020
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"

	"golang.org/x/text/encoding/simplifiedchinese"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

const doc = "stackable/cash/chn_490701733.stk"

var unrelatedProbes = []string{
	"stackable/cash/chn_490701730.stk",
	"stackable/cash/creature/rename_card.stk",
	"itemshop/itemshop.lst",
}

type slotRef struct {
	magic int
	value string
}

func main() {
	pvfPath := flag.String("pvf", "", "source Script.pvf")
	outPath := flag.String("out", "", "candidate output path")
	nameHex := flag.String("name-hex", "", "bytes written into the ANSI pool for [name] (hex of GBK bytes)")
	explainHex := flag.String("explain-hex", "", "bytes written into the ANSI pool for [explain] (hex of GBK bytes)")
	flag.Parse()
	if *pvfPath == "" || *outPath == "" || *nameHex == "" || *explainHex == "" {
		panic("-pvf -out -name-hex -explain-hex are all required")
	}

	targets := map[string][]byte{}
	for label, raw := range map[string]string{"name": *nameHex, "explain": *explainHex} {
		decoded, err := hex.DecodeString(strings.ReplaceAll(strings.TrimSpace(raw), " ", ""))
		must(err)
		// 写入前就把"字节 → 中文"钉死：池子里必须是 GBK 编码的这两句。
		human, err := gbkDecode(decoded)
		must(err)
		targets[label] = decoded
		fmt.Printf("target %s: % x (%d bytes) -> gbk decode %q\n", label, decoded, len(decoded), human)
	}

	data, err := os.ReadFile(*pvfPath)
	must(err)
	inputSum := sha256.Sum256(data)
	archive, err := platformpvf.OpenBytes(data)
	must(err)
	raw, err := archive.ReadRaw(doc)
	must(err)
	slots := locateSlots(archive, raw)
	if len(slots) != 2 {
		panic(fmt.Errorf("expected [name] and [explain] in %s, found %d", doc, len(slots)))
	}

	human := map[string]string{}
	humanBytes := map[string][]byte{}
	for _, label := range []string{"name", "explain"} {
		slot := slots[label]
		fitted, decoded, err := fitToPool(archive, slot.magic, slot.value, targets[label])
		if err != nil {
			panic(fmt.Errorf("%s: %w", label, err))
		}
		patched, err := archive.RepackStrings([]platformpvf.StringReplacement{{
			MagicOffset: slot.magic, Old: slot.value, New: string(fitted),
		}})
		must(err)
		data = patched
		archive, err = platformpvf.OpenBytes(data)
		must(err)
		human[label] = decoded
		humanBytes[label] = append([]byte(nil), fitted...)
		fmt.Printf("[ok] %s %q -> %q (pool bytes % x, %d bytes)\n", label, slot.value, decoded, fitted, len(fitted))
	}

	verified, err := platformpvf.OpenBytes(data)
	must(err)
	text, err := verified.ReadText(doc)
	must(err)
	rawAfter, err := verified.ReadRaw(doc)
	must(err)
	slotsAfter := locateSlots(verified, rawAfter)
	for _, label := range []string{"name", "explain"} {
		want := targets[label]
		wantText := human[label]
		// 1) GBK 回读：直接按池里该 offset 的现值重新解码，必须等于目标中文
		gotText := strings.TrimSpace(verified.ResolveString(slotsAfter[label].magic))
		if gotText != strings.TrimSpace(wantText) {
			panic(fmt.Errorf("%s gbk read-back mismatch: %q != %q", label, gotText, wantText))
		}
		// 2) 写进去的字节必须是 GBK（不是 UTF-8）—— 这正是上一版翻车的地方：
		//    容器里的字符串池是分块加密的，所以不能靠"文件里搜字节"验证；这里改成
		//    把写入池的原始字节按 GBK 解一次，并用"不等于该文本的 UTF-8 编码"兜底。
		decodedWant, err := gbkDecode(want)
		must(err)
		if strings.TrimSpace(decodedWant) != strings.TrimSpace(wantText) {
			panic(fmt.Errorf("%s bytes are not GBK for %q (decoded %q)", label, wantText, decodedWant))
		}
		if bytes.Equal(want, []byte(wantText)) {
			panic(fmt.Errorf("%s wrote the UTF-8 form of %q", label, wantText))
		}
		if len(want) != len(humanBytes[label]) {
			panic(fmt.Errorf("%s pool width mismatch: wrote %d want %d", label, len(want), len(humanBytes[label])))
		}
		// 3) 文档读回 + 池 offset 未移动
		if !strings.Contains(text, wantText) {
			panic(fmt.Errorf("document %s does not read back %q", doc, wantText))
		}
		if slotsAfter[label].magic != slots[label].magic {
			panic(fmt.Errorf("%s pool offset moved: %d -> %d", label, slots[label].magic, slotsAfter[label].magic))
		}
		fmt.Printf("[verify] %s offset=%d gbk=% x decode=%q utf8-form-absent=true\n",
			label, slots[label].magic, want, wantText)
	}
	for _, stub := range []string{"name_490701733", "explain_490701733"} {
		if strings.Contains(text, stub) {
			panic(fmt.Errorf("document %s still contains placeholder %q", doc, stub))
		}
	}
	for _, probe := range unrelatedProbes {
		before, err := archive.ReadText(probe)
		must(err)
		after, err := verified.ReadText(probe)
		must(err)
		if before != after {
			panic(fmt.Errorf("unrelated document %s changed", probe))
		}
	}
	must(os.WriteFile(*outPath, data, 0o644))
	outSum := sha256.Sum256(data)
	fmt.Printf("input  %s %d bytes sha256=%s\n", *pvfPath, len(data), strings.ToUpper(hex.EncodeToString(inputSum[:])))
	fmt.Printf("output %s %d bytes sha256=%s\n", *outPath, len(data), strings.ToUpper(hex.EncodeToString(outSum[:])))
}

// locateSlots 找出文档里 [name]/[explain] 两个 section 的池 offset 与现值。
func locateSlots(archive *platformpvf.Archive, raw []byte) map[string]slotRef {
	slots := map[string]slotRef{}
	for off := 0; off+10 <= len(raw); off += 5 {
		tok := raw[off : off+5]
		if tok[0] != 3 && tok[0] != 6 {
			continue
		}
		label := normalize(archive.ResolveString(tokenInt(tok)))
		if label != "name" && label != "explain" {
			continue
		}
		valueToken := raw[off+5 : off+10]
		slots[label] = slotRef{magic: tokenInt(valueToken), value: archive.ResolveString(tokenInt(valueToken))}
	}
	return slots
}

// fitToPool 把目标 GBK 字节对齐到池槽位宽度（不足补 0x20 空格，超宽直接报错）。
// 槽位宽度从 RepackStrings 的报错里读（"changed from 14 to N"）。
func fitToPool(archive *platformpvf.Archive, magic int, oldValue string, want []byte) ([]byte, string, error) {
	attempt := func(candidate []byte) error {
		_, err := archive.RepackStrings([]platformpvf.StringReplacement{{
			MagicOffset: magic, Old: oldValue, New: string(candidate),
		}})
		return err
	}
	if err := attempt(want); err == nil {
		decoded, err := gbkDecode(want)
		return want, decoded, err
	} else if width := parseWidthFromError(err.Error()); width > 0 {
		if len(want) > width {
			return nil, "", fmt.Errorf("needs %d bytes but the pool slot holds %d", len(want), width)
		}
		padded := append(append([]byte(nil), want...), bytes.Repeat([]byte{' '}, width-len(want))...)
		if err := attempt(padded); err != nil {
			return nil, "", err
		}
		decoded, err := gbkDecode(padded)
		return padded, decoded, err
	} else {
		return nil, "", err
	}
}

func gbkDecode(raw []byte) (string, error) {
	decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(raw)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

// parseWidthFromError 从 "replacement width changed from 14 to 24" 里取出槽位宽度。
func parseWidthFromError(message string) int {
	const marker = "changed from "
	index := strings.Index(message, marker)
	if index < 0 {
		return 0
	}
	rest := message[index+len(marker):]
	end := strings.IndexAny(rest, " to")
	if end < 0 {
		return 0
	}
	width := 0
	for _, r := range rest[:end] {
		if r < '0' || r > '9' {
			return 0
		}
		width = width*10 + int(r-'0')
	}
	return width
}

func normalize(value string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(value), "[]"))
}

func tokenInt(token []byte) int {
	return int(int32(binary.LittleEndian.Uint32(token[1:5])))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

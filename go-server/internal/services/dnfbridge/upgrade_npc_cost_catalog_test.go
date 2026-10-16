package dnfbridge

import (
	"errors"
	"os"
	"strings"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// PVF 的字符串定界符是反引号；夹具用 \x60 写出它，避免源码里出现裸引号。
const npcFixtureTick = "\x60"

func mustParseNPCFixture(t *testing.T, path, body string) *dnfpvf.Document {
	t.Helper()
	document, err := dnfpvf.Parse(path, body)
	if err != nil {
		t.Fatalf("parse fixture %s: %v", path, err)
	}
	return document
}

// 与真档同形：一行 token 流「id + 反引号包裹的 .npc 文件名」，中间夹一个超出 uint16 的
// 9 位 id（真档里有 400000012 这类）。
func TestParseCurrentUpgradeNPCListReadsLstShape(t *testing.T) {
	raw := "1 " + npcFixtureTick + "Linus.npc" + npcFixtureTick +
		" 476 " + npcFixtureTick + "476_Jonathan.npc" + npcFixtureTick +
		" 20275 " + npcFixtureTick + "chn_DayDungeon_SeriaRoom_01.npc" + npcFixtureTick +
		" 400000012 " + npcFixtureTick + "chn_event_10th_expect_market.npc" + npcFixtureTick
	refs, err := parseCurrentUpgradeNPCList(currentUpgradeNPCListPath, raw)
	if err != nil {
		t.Fatalf("parse npc list: %v", err)
	}
	if len(refs) != 3 {
		t.Fatalf("entries = %d, want 3 (a 9-digit id cannot be addressed by a uint16 query)", len(refs))
	}
	if refs[476] != "476_Jonathan.npc" {
		t.Fatalf("refs[476] = %q, want the real lst reference 476_Jonathan.npc", refs[476])
	}
	if refs[1] != "Linus.npc" {
		t.Fatalf("refs[1] = %q", refs[1])
	}

	if _, err := parseCurrentUpgradeNPCList(currentUpgradeNPCListPath, "1 "+npcFixtureTick+"a.npc"+npcFixtureTick+" 1 "+npcFixtureTick+"b.npc"+npcFixtureTick); err == nil {
		t.Fatal("duplicate npc id must be rejected")
	}
	if _, err := parseCurrentUpgradeNPCList(currentUpgradeNPCListPath, "\n\n"); err == nil {
		t.Fatal("an npc list without entries must be rejected")
	}
}

// lst 引用必须留在 list 目录以内：绝对路径、含 .. 段、或拼出来越出 npc/ 的都必须失败关闭。
func TestResolveCurrentUpgradeNPCReferenceStaysInsideTheListDirectory(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		listPath  string
		reference string
		want      string
		wantErr   bool
	}{
		{name: "bare name uses the list directory", listPath: "npc/npc.lst", reference: "476_Jonathan.npc", want: "npc/476_Jonathan.npc"},
		{name: "already prefixed stays", listPath: "npc/npc.lst", reference: "npc/already.npc", want: "npc/already.npc"},
		{name: "backslashes normalized", listPath: "npc/npc.lst", reference: "npc\x5cwin.npc", want: "npc/win.npc"},
		{name: "dot slash prefix dropped", listPath: "npc/npc.lst", reference: "./rel.npc", want: "npc/rel.npc"},
		{name: "flat list keeps the bare name", listPath: "npc.lst", reference: "flat.npc", want: "flat.npc"},
		{name: "absolute path rejected", listPath: "npc/npc.lst", reference: "/etc/upgrade.etc", wantErr: true},
		{name: "upwards traversal rejected", listPath: "npc/npc.lst", reference: "../etc/upgrade.etc", wantErr: true},
		{name: "inner traversal rejected", listPath: "npc/npc.lst", reference: "sub/../../etc/upgrade.etc", wantErr: true},
		{name: "empty reference rejected", listPath: "npc/npc.lst", reference: "   ", wantErr: true},
		{name: "flat list rejects subdirectories", listPath: "npc.lst", reference: "sub/x.npc", wantErr: true},
	} {
		got, err := resolveCurrentUpgradeNPCReference(testCase.listPath, testCase.reference)
		if testCase.wantErr {
			if err == nil {
				t.Errorf("%s: got %q, want an out-of-scope rejection", testCase.name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error %v", testCase.name, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: got %q want %q", testCase.name, got, testCase.want)
		}
	}
}

// blocks 就是该段的段体，0 个 = 文档里没有这一段（普通 NPC），2 个 = 重复段。
func npcConversionDocument(t *testing.T, blocks ...string) *dnfpvf.Document {
	t.Helper()
	raw := "[role]\n" + npcFixtureTick + "[trading system]" + npcFixtureTick + " 0 " +
		npcFixtureTick + "[upgrade item]" + npcFixtureTick + " 0 \n[/role]\n"
	for _, block := range blocks {
		raw += "\n[upgrade gold convert to material]\n" + block + " \n[/upgrade gold convert to material]\n"
	}
	raw += "\n[name]\n" + npcFixtureTick + "fixture" + npcFixtureTick + "\n"
	return mustParseNPCFixture(t, "npc/fixture.npc", raw)
}

// 本用例钉：无段 ⇒ converts=false；恰两个正 int32 ⇒ 规则；其余（含重复段）⇒ 报错。
func TestParseCurrentUpgradeNPCConversionRuleDistinguishesAbsentFromMalformed(t *testing.T) {
	one := "10158964"
	two := "10158964 100000"
	three := "10158964 100000 7"
	zeroMaterial := "0 100000"
	zeroGold := "10158964 0"
	negative := "-1 100000"
	floating := "10158964.5 100000"
	quoted := npcFixtureTick + "oops" + npcFixtureTick + " 100000"
	empty := ""
	materialAboveInt32 := "2147483648 100000"
	goldAboveInt32 := "10158964 2147483648"
	atInt32Ceiling := "2147483647 2147483647"
	otherGold := "10158964 200000"

	for _, testCase := range []struct {
		name        string
		blocks      []string
		wantID      int64
		wantGold    int64
		wantConvert bool
		wantErr     bool
	}{
		{name: "ordinary npc without the section", wantConvert: false},
		{name: "real shape 10158964 100000", blocks: []string{two}, wantID: 10158964, wantGold: 100000, wantConvert: true},
		{name: "int32 ceiling accepted", blocks: []string{atInt32Ceiling}, wantID: 2147483647, wantGold: 2147483647, wantConvert: true},
		{name: "one value", blocks: []string{one}, wantErr: true},
		{name: "three values", blocks: []string{three}, wantErr: true},
		{name: "empty section", blocks: []string{empty}, wantErr: true},
		{name: "zero material", blocks: []string{zeroMaterial}, wantErr: true},
		{name: "zero unit gold", blocks: []string{zeroGold}, wantErr: true},
		{name: "negative material", blocks: []string{negative}, wantErr: true},
		{name: "float value", blocks: []string{floating}, wantErr: true},
		{name: "quoted value", blocks: []string{quoted}, wantErr: true},
		{name: "material above int32", blocks: []string{materialAboveInt32}, wantErr: true},
		{name: "unit gold above int32", blocks: []string{goldAboveInt32}, wantErr: true},
		{name: "duplicate identical section", blocks: []string{two, two}, wantErr: true},
		{name: "duplicate conflicting section", blocks: []string{two, otherGold}, wantErr: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			document := npcConversionDocument(t, testCase.blocks...)
			materialID, unitGold, converts, err := parseCurrentUpgradeNPCConversionRule(document)
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("must be rejected, got (%d, %d, %t)", materialID, unitGold, converts)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if materialID != testCase.wantID || unitGold != testCase.wantGold || converts != testCase.wantConvert {
				t.Fatalf("got (%d, %d, %t), want (%d, %d, %t)", materialID, unitGold, converts, testCase.wantID, testCase.wantGold, testCase.wantConvert)
			}
		})
	}
}

func TestParseCurrentUpgradeNPCConversionRuleRejectsNilDocument(t *testing.T) {
	if _, _, _, err := parseCurrentUpgradeNPCConversionRule(nil); err == nil {
		t.Fatal("nil document must be rejected")
	}
}

// 真档验收：476（红尾乔纳森）声明 10158964/100000；普通 NPC（1 Linus.npc、2 Seria.npc）
// 都没有这一段；0xFFFF 这种不在 npc.lst 里的 id 必须是 errCurrentUpgradeNPCUnknown。
func TestRealPVFUpgradeNPCConversionOnlyJonathanDeclaresTheRule(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to run the real Script.pvf NPC conversion check")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatalf("open real Script.pvf: %v", err)
	}
	service := &Service{initialEquipmentArchive: archive}

	// npc.lst 的 476 引用：真档是大写 J，归档路径是小写，读侧走 pathKey 小写查找。
	index, err := service.currentUpgradeNPCIndex(archive)
	if err != nil {
		t.Fatalf("npc index: %v", err)
	}
	reference, listed := index[476]
	if !listed {
		t.Fatalf("npc 476 is not listed in %s", currentUpgradeNPCListPath)
	}
	if strings.ToLower(strings.TrimSpace(reference)) != "476_jonathan.npc" {
		t.Fatalf("npc 476 reference = %q, want 476_Jonathan.npc (case-insensitive)", reference)
	}
	resolved, err := resolveCurrentUpgradeNPCReference(currentUpgradeNPCListPath, reference)
	if err != nil {
		t.Fatalf("real npc 476 reference must resolve inside npc/: %v", err)
	}
	if strings.ToLower(resolved) != "npc/476_jonathan.npc" {
		t.Fatalf("npc 476 resolved path = %q", resolved)
	}

	materialID, unitGold, converts, err := service.currentUpgradeNPCConversion(476)
	if err != nil || !converts {
		t.Fatalf("npc 476: material=%d unitGold=%d converts=%t err=%v", materialID, unitGold, converts, err)
	}
	if materialID != 10158964 || unitGold != 100000 {
		t.Fatalf("npc 476 rule = %d/%d, want 10158964/100000", materialID, unitGold)
	}
	// 第二次调用走缓存，必须给同一答案。
	if againID, againGold, againConverts, err := service.currentUpgradeNPCConversion(476); err != nil || !againConverts || againID != materialID || againGold != unitGold {
		t.Fatalf("cached npc 476 = %d/%d/%t err=%v", againID, againGold, againConverts, err)
	}

	for _, ordinary := range []uint16{1, 2} {
		material, gold, converts, err := service.currentUpgradeNPCConversion(ordinary)
		if err != nil {
			t.Fatalf("ordinary npc %d: %v", ordinary, err)
		}
		if converts || material != 0 || gold != 0 {
			t.Fatalf("ordinary npc %d must report converts=false, got %d/%d/%t", ordinary, material, gold, converts)
		}
	}

	if _, _, _, err := service.currentUpgradeNPCConversion(0xFFFF); !errors.Is(err, errCurrentUpgradeNPCUnknown) {
		t.Fatalf("0xFFFF err = %v, want errCurrentUpgradeNPCUnknown", err)
	}
}

func TestCurrentUpgradeNPCConversionWithoutArchiveFailsClosed(t *testing.T) {
	service := &Service{}
	service.initialEquipmentLoadErr = errors.New("fixture: no runtime PVF")
	if _, _, _, err := service.currentUpgradeNPCConversion(476); err == nil {
		t.Fatal("a Service without a runtime PVF must fail closed")
	}
	var nilService *Service
	if _, _, _, err := nilService.currentUpgradeNPCConversion(476); !errors.Is(err, errCurrentUpgradeNPCConversionUnavailable) {
		t.Fatalf("nil service err = %v, want errCurrentUpgradeNPCConversionUnavailable", err)
	}
}

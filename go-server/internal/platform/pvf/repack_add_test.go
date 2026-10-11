package pvf

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"
)

func TestRealPVFAddBlackDiamondScriptPreservesExistingFiles(t *testing.T) {
	source := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if source == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := Open(source)
	if err != nil {
		t.Fatal(err)
	}
	template := "stackable/event/chn_event_20140819_spend_time/chn_mat_blackdiamond_ticket.stk"
	output, err := archive.RepackScriptAdditions([]ScriptAddition{{Path: "stackable/custom/black_diamond_30day.stk", TemplatePath: template, Strings: []ScriptStringChange{{Old: "黑钻7天体验券", New: "黑钻贵族契约 (30天)"}, {Old: "[material]", New: "[waste]"}}}})
	if err != nil {
		t.Fatal(err)
	}
	patched, err := OpenBytes(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(patched.Files()) != len(archive.Files())+1 {
		t.Fatal("new file count mismatch")
	}
	added := patched.Files()[len(archive.Files())]
	if added.Path != "stackable/custom" || added.Name != "black_diamond_30day.stk" {
		t.Fatalf("native directory/basename fields: %+v", added)
	}
	text, err := patched.ReadText("stackable/custom/black_diamond_30day.stk")
	if err != nil || !bytes.Contains([]byte(text), []byte("黑钻贵族契约 (30天)")) || !bytes.Contains([]byte(text), []byte("[waste]")) {
		t.Fatalf("added script readback: %s %v", text, err)
	}
	for i, item := range archive.items {
		if item != patched.items[i] {
			t.Fatalf("unrelated file metadata changed: %d", i)
		}
	}
	oldLayout, _ := archive.layout()
	newLayout, _ := patched.layout()
	if !bytes.Equal(archive.data[oldLayout.bodyOffset:oldLayout.bodyOffset+oldLayout.bodySize], patched.data[newLayout.bodyOffset:newLayout.bodyOffset+oldLayout.bodySize]) {
		t.Fatal("original encrypted body changed")
	}
	t.Logf("new script added; all %d original files unchanged", len(archive.Files()))
}

func TestRealPVFImportTokenStreamPreservesKindsAndFloatBits(t *testing.T) {
	source := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if source == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	a, err := Open(source)
	if err != nil {
		t.Fatal(err)
	}
	template := "stackable/cash/chn_20130702_summer_fatezero/chn_2013_Summer_Fate_package_sm1.stk"
	tokens := []ScriptToken{{Type: 3, Text: "[name]"}, {Type: 6, Text: "导入类型验证"}, {Type: 5, Text: "typed-five"}, {Type: 7, Text: "typed-seven"}, {Type: 2, Value: 0x3f99999a}, {Type: 0, Value: -1}, {Type: 6, Text: "typed-seven"}, {Type: 6, Text: "导入类型验证"}}
	out, err := a.RepackScriptAdditions([]ScriptAddition{{Path: "stackable/custom/import_token_test.stk", TemplatePath: template, ReplaceTokens: true, AppendTokens: tokens}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenBytes(out)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := b.ReadRaw("stackable/custom/import_token_test.stk")
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 5*len(tokens) {
		t.Fatal("template sections leaked into full import")
	}
	for i, token := range tokens {
		pos := i * 5
		value := int32(binary.LittleEndian.Uint32(raw[pos+1:]))
		if raw[pos] != token.Type {
			t.Fatalf("token kind changed at %d", i)
		}
		if token.Type == 0 || token.Type == 2 {
			if value != token.Value {
				t.Fatalf("numeric/float bits changed at %d", i)
			}
		} else if b.resolveString(int(value)) != token.Text {
			t.Fatalf("string import mismatch at %d", i)
		}
	}
	if !bytes.Equal(raw[3*5+1:3*5+5], raw[6*5+1:6*5+5]) || !bytes.Equal(raw[1*5+1:1*5+5], raw[7*5+1:7*5+5]) {
		t.Fatal("repeated imported strings received duplicate pool offsets")
	}
	for i, item := range a.items {
		if item != b.items[i] {
			t.Fatalf("original file metadata changed at %d", i)
		}
	}
	if !bytes.HasPrefix(b.strA, a.strA) || !bytes.HasPrefix(b.strW, a.strW) {
		t.Fatal("original string offsets changed")
	}
	oldLayout, _ := a.layout()
	newLayout, _ := b.layout()
	if !bytes.Equal(a.data[oldLayout.bodyOffset:oldLayout.bodyOffset+oldLayout.bodySize], b.data[newLayout.bodyOffset:newLayout.bodyOffset+oldLayout.bodySize]) {
		t.Fatal("original encrypted body changed")
	}
	for _, invalid := range []ScriptAddition{
		{Path: template, AppendToExisting: true, ReplaceTokens: true, AppendTokens: tokens},
		{Path: "stackable/custom/empty_import.stk", TemplatePath: template, ReplaceTokens: true},
		{Path: "stackable/custom/bad_import.stk", TemplatePath: template, ReplaceTokens: true, AppendTokens: []ScriptToken{{Type: 9}}},
	} {
		if _, err := a.RepackScriptAdditions([]ScriptAddition{invalid}); err == nil {
			t.Fatal("invalid full import was accepted")
		}
	}
}

func TestRealPVFRewriteExistingScriptPreservesOtherFiles(t *testing.T) {
	source := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if source == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	a, err := Open(source)
	if err != nil {
		t.Fatal(err)
	}
	path := "stackable/cash/chn_20130702_summer_fatezero/chn_2013_Summer_Fate_package_sm1.stk"
	original, err := a.ReadRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	tokens := []ScriptToken{}
	changed := 0
	for pos := 0; pos < len(original); pos += 5 {
		kind := original[pos]
		value := int32(binary.LittleEndian.Uint32(original[pos+1:]))
		token := ScriptToken{Type: kind, Value: value}
		if kind == 3 || kind == 5 || kind == 6 || kind == 7 {
			token.Text = a.resolveString(int(value))
			token.Value = 0
			if token.Text == "[cera package]" {
				token.Text = "[usable cera package]"
				changed++
			}
		}
		tokens = append(tokens, token)
	}
	if changed != 1 {
		t.Fatalf("expected one actual legacy package type, found %d", changed)
	}
	out, err := a.RepackScriptAdditions([]ScriptAddition{{Path: path, AppendToExisting: true, ReplaceExistingTokens: true, AppendTokens: tokens}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenBytes(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.items) != len(b.items) {
		t.Fatal("rewriting added or removed file definitions")
	}
	target := a.FindFileIndex(path)
	for i, item := range a.items {
		if i != target && item != b.items[i] {
			t.Fatalf("unrelated metadata changed: %d", i)
		}
	}
	raw, err := b.ReadRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != len(original) {
		t.Fatal("rewritten stream changed token count")
	}
	for i, token := range tokens {
		pos := i * 5
		value := int32(binary.LittleEndian.Uint32(raw[pos+1:]))
		if raw[pos] != token.Type {
			t.Fatalf("token kind changed at %d", i)
		}
		if token.Type == 0 || token.Type == 2 {
			if value != token.Value {
				t.Fatalf("numeric bit pattern changed at %d", i)
			}
		} else if b.resolveString(int(value)) != token.Text {
			t.Fatalf("string changed unexpectedly at %d", i)
		}
	}
	unchanged, err := a.ReadRaw(path)
	if err != nil || !bytes.Equal(original, unchanged) {
		t.Fatal("source archive was mutated")
	}
	if !bytes.HasPrefix(b.strA, a.strA) || !bytes.HasPrefix(b.strW, a.strW) {
		t.Fatal("old string offsets changed")
	}
	oldLayout, _ := a.layout()
	newLayout, _ := b.layout()
	if !bytes.Equal(a.data[oldLayout.bodyOffset:oldLayout.bodyOffset+oldLayout.bodySize], b.data[newLayout.bodyOffset:newLayout.bodyOffset+oldLayout.bodySize]) {
		t.Fatal("original encrypted body changed")
	}
	for _, invalid := range []ScriptAddition{{Path: path, AppendToExisting: true, ReplaceExistingTokens: true}, {Path: path, AppendToExisting: true, ReplaceTokens: true, ReplaceExistingTokens: true, AppendTokens: tokens}, {Path: "stackable/custom/invalid_rewrite.stk", TemplatePath: path, ReplaceExistingTokens: true, AppendTokens: tokens}} {
		if _, err := a.RepackScriptAdditions([]ScriptAddition{invalid}); err == nil {
			t.Fatal("invalid rewrite ownership or empty stream accepted")
		}
	}
}

package main

import (
	"bytes"
	"fmt"
	script "longheng.io/server/internal/modules/dnf/pvf"
	archive "longheng.io/server/internal/platform/pvf"
	"strings"
)

func repairIrisShop(a *archive.Archive) ([]byte, int, error) {
	old, err := a.ReadRaw(listPath)
	if err != nil {
		return nil, 0, err
	}
	if digest(old) != listSHA {
		return nil, 0, fmt.Errorf("unreviewed shop list")
	}
	text, err := a.ReadText(listPath)
	if err != nil {
		return nil, 0, err
	}
	doc, err := script.Parse(listPath, text)
	if err != nil {
		return nil, 0, err
	}
	for _, entry := range script.ParseList(doc) {
		if entry.ID == 63 || strings.EqualFold(entry.Path, "iris2.shp") {
			return nil, 0, fmt.Errorf("shop registration already exists or conflicts")
		}
	}
	raw, err := a.ReadRaw(shopPath)
	if err != nil {
		return nil, 0, err
	}
	if digest(raw) != shopSHA {
		return nil, 0, fmt.Errorf("Iris shop body changed")
	}
	shopText, err := a.ReadText(shopPath)
	if err != nil {
		return nil, 0, err
	}
	shop, err := script.Parse(shopPath, shopText)
	if err != nil {
		return nil, 0, err
	}
	npc, ok := shop.Int("NPC")
	if !ok || npc != 132 {
		return nil, 0, fmt.Errorf("Iris NPC identity mismatch")
	}
	npcText, err := a.ReadText("npc/iris2.npc")
	if err != nil {
		return nil, 0, err
	}
	nd, err := script.Parse("npc/iris2.npc", npcText)
	if err != nil {
		return nil, 0, err
	}
	roles, ok := nd.Section("role")
	found := false
	for i, t := range roles {
		if t.Value == "[item shop]" && i+1 < len(roles) && roles[i+1].Kind == script.TokenInt && roles[i+1].Int == 63 {
			found = true
		}
	}
	if !ok || !found {
		return nil, 0, fmt.Errorf("Iris role no longer references shop 63")
	}
	output, err := a.RepackScriptAdditions([]archive.ScriptAddition{{Path: listPath, AppendToExisting: true, AppendTokens: []archive.ScriptToken{{Type: 0, Value: 63}, {Type: 6, Text: "Iris2.shp"}}}})
	if err != nil {
		return nil, 0, err
	}
	next, err := archive.OpenBytes(output)
	if err != nil {
		return nil, 0, err
	}
	count, err := a.VerifyScriptReplacementPreservation(next, []string{listPath})
	if err != nil {
		return nil, 0, err
	}
	now, err := next.ReadRaw(listPath)
	if err != nil {
		return nil, 0, err
	}
	if !bytes.HasPrefix(now, old) || len(now) != len(old)+10 {
		return nil, 0, fmt.Errorf("original list modified")
	}
	nextText, err := next.ReadText(listPath)
	if err != nil {
		return nil, 0, err
	}
	nextDoc, err := script.Parse(listPath, nextText)
	if err != nil {
		return nil, 0, err
	}
	entries := script.ParseList(nextDoc)
	if len(entries) != len(script.ParseList(doc))+1 {
		return nil, 0, fmt.Errorf("list count mismatch")
	}
	last := entries[len(entries)-1]
	if last.ID != 63 || !strings.EqualFold(last.Path, "Iris2.shp") {
		return nil, 0, fmt.Errorf("restored reference cannot be parsed")
	}
	return output, count, nil
}

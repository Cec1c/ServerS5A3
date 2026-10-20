// Command pvfjoustevict produces client Script.pvf candidates used to isolate
// and remove the horse-joust (masang) town-NPC behaviour introduced by the
// earlier pvfjoustentry work. It never edits the active PVF in place.
//
// Modes:
//
//	restore            - restore npc/chn_event_lions10th_2.npc to the retired
//	                     original bytes and move town NPC 20449 back to its
//	                     original (1478, 354) placement.
//	minimal            - drop the `2365` all-day gates only and restore the
//	                     original town placement; keep the role.
//	no-visible-event   - drop the whole `[open visible event] <id>` declaration
//	                     (and any `[event open]` section) from the NPC. No other
//	                     file is touched, so a full diff against the baseline
//	                     must report exactly one changed file.
//	no-popupmenu       - drop the whole `[npc popupmenu step]...[/npc popupmenu
//	                     step]` block, which is what names the popup window the
//	                     NPC opens.
//	no-role-no-event   - all three blocks above removed, so the NPC keeps only
//	                     its display fields.
//
// Every edit either replaces a whole raw file or rewrites 5-byte script tokens
// (delete tokens / rewrite int32 payloads). The shared ANSI/UTF-16 string pools
// are never resized or reordered by this tool.
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
	"strings"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

const (
	npcPath = "npc/chn_event_lions10th_2.npc"
	mapPath = "map/cataclysm/town/hendonmyre/new_hendon_main.map"

	npcID      = 20449
	joustEvent = 2365

	oldX, oldY = 2183, 156
	newX, newY = 1478, 354
)

type poolCheck struct {
	TokensCompared int      `json:"tokens_compared"`
	Mismatches     []string `json:"mismatches"`
	RawCopySafe    bool     `json:"raw_copy_safe"`
}

type report struct {
	Mode              string    `json:"mode"`
	InputPath         string    `json:"input_path"`
	OriginalPath      string    `json:"original_path,omitempty"`
	OutputPath        string    `json:"output_path"`
	NPCPath           string    `json:"npc_path"`
	TownMap           string    `json:"town_map"`
	MapTouched        bool      `json:"map_touched"`
	Pool              poolCheck `json:"string_pool_check"`
	OriginalNPCBytes  int       `json:"original_npc_bytes,omitempty"`
	TargetNPCBytesIn  int       `json:"target_npc_bytes_before"`
	TargetNPCBytesOut int       `json:"target_npc_bytes_after"`
	NPCEdit           string    `json:"npc_edit"`
	RoleRestored      bool      `json:"role_label_restored,omitempty"`
	TokensDropped     int       `json:"tokens_dropped"`
	MapNpcMoved       bool      `json:"map_npc_moved,omitempty"`
	MapTupleBefore    string    `json:"map_tuple_before,omitempty"`
	MapTupleAfter     string    `json:"map_tuple_after,omitempty"`
	InputSize         int64     `json:"input_size"`
	OutputSize        int       `json:"output_size"`
	OutputSHA256      string    `json:"output_sha256"`
}

var validModes = map[string]bool{
	"restore":          true,
	"minimal":          true,
	"no-visible-event": true,
	"no-popupmenu":     true,
	"no-role-no-event": true,
}

// blockStrippers are applied in order by -mode no-role-no-event.
var blockStrippers = []func(*platformpvf.Archive, []byte) ([]byte, int, error){
	stripRoleSection,
	stripVisibleEvent,
	stripPopupMenu,
}

func main() {
	pvfPath := flag.String("pvf", "", "target Script.pvf to base the candidate on (read only)")
	origPath := flag.String("orig", "", "retired-original Script.pvf used by -mode restore (read only)")
	outPath := flag.String("out", "", "candidate output path (required)")
	reportPath := flag.String("report", "", "optional JSON report output path")
	mode := flag.String("mode", "no-visible-event", "restore | minimal | no-visible-event | no-role-no-event")
	flag.Parse()
	if *pvfPath == "" || *outPath == "" {
		fmt.Fprintln(os.Stderr, "need -pvf and -out")
		os.Exit(2)
	}
	if !validModes[*mode] {
		fmt.Fprintf(os.Stderr, "unknown -mode %q\n", *mode)
		os.Exit(2)
	}
	// The event-declaration modes must not touch any file other than the NPC.
	skipMap := *mode == "no-visible-event" || *mode == "no-popupmenu" || *mode == "no-role-no-event"

	archive, err := platformpvf.Open(*pvfPath)
	must(err)
	targetNPC, err := archive.ReadRaw(npcPath)
	must(err)

	result := report{
		Mode:             *mode,
		InputPath:        *pvfPath,
		OriginalPath:     *origPath,
		OutputPath:       *outPath,
		NPCPath:          npcPath,
		TownMap:          mapPath,
		TargetNPCBytesIn: len(targetNPC),
		Pool:             poolCheck{Mismatches: []string{}},
	}

	var outNPC []byte
	switch *mode {
	case "restore":
		var reference []byte
		if *origPath != "" {
			origArchive, openErr := platformpvf.Open(*origPath)
			must(openErr)
			reference, err = origArchive.ReadRaw(npcPath)
			must(err)
			result.OriginalNPCBytes = len(reference)
			result.Pool = comparePools(origArchive, archive, reference)
		}
		outNPC, result.NPCEdit, result.RoleRestored, result.TokensDropped, err =
			restoreNPC(archive, targetNPC, reference, result.Pool.RawCopySafe)
		must(err)
	case "minimal":
		outNPC, result.TokensDropped, err = stripEventGates(archive, targetNPC)
		must(err)
		result.NPCEdit = "gates-only (role retained)"
	case "no-visible-event":
		outNPC, result.TokensDropped, err = stripVisibleEvent(archive, targetNPC)
		must(err)
		result.NPCEdit = "whole [open visible event] declaration removed (role and popup menu retained)"
	case "no-popupmenu":
		outNPC, result.TokensDropped, err = stripPopupMenu(archive, targetNPC)
		must(err)
		result.NPCEdit = "whole [npc popupmenu step] block removed (role and visible event retained)"
	case "no-role-no-event":
		stripped := targetNPC
		totalDropped := 0
		for _, strip := range blockStrippers {
			next, drops, stepErr := strip(archive, stripped)
			must(stepErr)
			stripped = next
			totalDropped += drops
		}
		outNPC = stripped
		result.TokensDropped = totalDropped
		result.NPCEdit = "[role] + [open visible event] + [npc popupmenu step] all removed"
	}
	result.TargetNPCBytesOut = len(outNPC)

	replacements := []platformpvf.RawReplacement{{Path: npcPath, Data: outNPC}}
	if !skipMap {
		mapRaw, readErr := archive.ReadRaw(mapPath)
		must(readErr)
		placedMap, tupleBefore, tupleAfter, moved, placeErr := restoreTownPlacement(archive, mapRaw)
		must(placeErr)
		result.MapTouched = true
		result.MapTupleBefore = tupleBefore
		result.MapTupleAfter = tupleAfter
		result.MapNpcMoved = moved
		replacements = append(replacements, platformpvf.RawReplacement{Path: mapPath, Data: placedMap})
	}

	patched, err := archive.RepackRaw(replacements)
	must(err)

	verified, err := platformpvf.OpenBytes(patched)
	must(err)
	verifiedNPC, err := verified.ReadText(npcPath)
	must(err)
	verifyNPC(*mode, verifiedNPC)

	must(os.MkdirAll(filepath.Dir(*outPath), 0o755))
	must(os.WriteFile(*outPath, patched, 0o644))
	info, err := os.Stat(*pvfPath)
	must(err)
	sum := sha256.Sum256(patched)
	result.InputSize = info.Size()
	result.OutputSize = len(patched)
	result.OutputSHA256 = strings.ToUpper(hex.EncodeToString(sum[:]))
	encoded, err := json.MarshalIndent(result, "", "  ")
	must(err)
	fmt.Println(string(encoded))
	if *reportPath != "" {
		must(os.WriteFile(*reportPath, encoded, 0o644))
	}
}

// verifyNPC re-reads the repacked archive and panics unless the candidate shows
// exactly the intended shape. A candidate is never written when this fails.
func verifyNPC(mode, text string) {
	// Only the modes that remove the declaration may require its absence.
	stripsEvent := mode == "no-visible-event" || mode == "no-role-no-event"
	if stripsEvent && strings.Contains(text, "[open visible event]") {
		panic(fmt.Errorf("verified %s still declares [open visible event]", npcPath))
	}
	if mode == "no-popupmenu" && !strings.Contains(text, "[open visible event]") {
		panic(fmt.Errorf("verified %s lost [open visible event]", npcPath))
	}
	if strings.Contains(text, "[event open]") {
		panic(fmt.Errorf("verified %s still declares [event open]", npcPath))
	}
	if !strings.Contains(text, "[name]") && !strings.Contains(text, "[field name]") {
		panic(fmt.Errorf("verified %s lost the NPC display fields", npcPath))
	}
	hasRole := strings.Contains(text, "[role]")
	hasPopup := strings.Contains(text, "[npc popupmenu step]")
	hasPopupWindow := strings.Contains(text, "POPUP_WINDOW_TYPE_LIONS")
	switch mode {
	case "no-role-no-event":
		if hasRole || hasPopup || hasPopupWindow {
			panic(fmt.Errorf("verified %s still carries role=%v popupmenu=%v popupwindow=%v", npcPath, hasRole, hasPopup, hasPopupWindow))
		}
	case "no-popupmenu":
		if hasPopup || hasPopupWindow {
			panic(fmt.Errorf("verified %s still carries the popup menu block", npcPath))
		}
		if !hasRole {
			panic(fmt.Errorf("verified %s lost the [role] section", npcPath))
		}
	default:
		if !hasRole {
			panic(fmt.Errorf("verified %s lost the [role] section", npcPath))
		}
		if !hasPopup {
			panic(fmt.Errorf("verified %s lost the popup menu block", npcPath))
		}
	}
}

// comparePools resolves every string token of the reference file in both
// archives. Identical resolutions mean the reference bytes can be copied
// verbatim, because the tokens' pool offsets mean the same thing on both sides.
func comparePools(reference *platformpvf.Archive, target *platformpvf.Archive, raw []byte) poolCheck {
	check := poolCheck{Mismatches: []string{}}
	for offset := 0; offset+5 <= len(raw); offset += 5 {
		token := raw[offset : offset+5]
		if token[0] != 3 && token[0] != 6 {
			continue
		}
		magic := scriptTokenInt(token)
		want := reference.ResolveString(magic)
		got := target.ResolveString(magic)
		check.TokensCompared++
		if want != got {
			check.Mismatches = append(check.Mismatches,
				fmt.Sprintf("off=%d magic=%d reference=%q target=%q", offset, magic, want, got))
		}
	}
	check.RawCopySafe = len(check.Mismatches) == 0 && check.TokensCompared > 0
	return check
}

func restoreNPC(archive *platformpvf.Archive, target, reference []byte, rawCopySafe bool) ([]byte, string, bool, int, error) {
	if rawCopySafe && len(reference) > 0 {
		return append([]byte(nil), reference...), "verbatim copy of the reference file bytes", true, 0, nil
	}
	trimmed, dropped, err := stripJoustRole(archive, target)
	if err != nil {
		return nil, "", false, 0, err
	}
	gates, gateDrops, err := stripEventGates(archive, trimmed)
	if err != nil {
		return nil, "", false, 0, err
	}
	return gates, "token-translated restore (joust role dropped)", false, dropped + gateDrops, nil
}

// stripVisibleEvent drops the `[open visible event]` label token together with
// the event-id value token that follows it, plus every `[event open]` section.
// It requires at least one declaration to be present.
func stripVisibleEvent(archive *platformpvf.Archive, raw []byte) ([]byte, int, error) {
	if len(raw) == 0 || len(raw)%5 != 0 {
		return nil, 0, fmt.Errorf("%s token stream is invalid: bytes=%d", npcPath, len(raw))
	}
	out := make([]byte, 0, len(raw))
	dropped := 0
	removed := 0
	for offset := 0; offset < len(raw); offset += 5 {
		token := raw[offset : offset+5]
		label := normalizedLabel(archive.ResolveString(scriptTokenInt(token)))
		if token[0] == 3 && label == "open visible event" {
			removed++
			dropped++
			if offset+10 <= len(raw) {
				next := raw[offset+5 : offset+10]
				if next[0] == 0 {
					dropped++
					offset += 5
				}
			}
			continue
		}
		if token[0] == 3 && label == "event open" {
			// Drop the label plus up to three integer operands when present.
			dropped++
			for skip := 0; skip < 3; skip++ {
				if offset+10+skip*5 > len(raw) {
					break
				}
				next := raw[offset+5+skip*5 : offset+10+skip*5]
				if next[0] != 0 {
					break
				}
				dropped++
				offset += 5
			}
			continue
		}
		out = append(out, token...)
	}
	if removed == 0 {
		return nil, 0, fmt.Errorf("%s declares no [open visible event] section", npcPath)
	}
	return out, dropped, nil
}

// stripRoleSection drops everything from the `[role]` label through the
// matching `[/role]` label, inclusive.
func stripRoleSection(archive *platformpvf.Archive, raw []byte) ([]byte, int, error) {
	if len(raw) == 0 || len(raw)%5 != 0 {
		return nil, 0, fmt.Errorf("%s token stream is invalid: bytes=%d", npcPath, len(raw))
	}
	out := make([]byte, 0, len(raw))
	dropped := 0
	inRole := false
	opens := 0
	for offset := 0; offset < len(raw); offset += 5 {
		token := raw[offset : offset+5]
		label := normalizedLabel(archive.ResolveString(scriptTokenInt(token)))
		if token[0] == 3 && label == "role" && !inRole {
			inRole = true
			opens++
			dropped++
			continue
		}
		if inRole {
			dropped++
			if token[0] == 3 && label == "/role" {
				inRole = false
			}
			continue
		}
		out = append(out, token...)
	}
	if opens != 1 || inRole {
		return nil, 0, fmt.Errorf("%s [role] sections=%d unterminated=%v, want 1 closed", npcPath, opens, inRole)
	}
	return out, dropped, nil
}

// stripPopupMenu drops everything from the `[npc popupmenu step]` label through
// the matching `[/npc popupmenu step]` label, inclusive. That block is what
// names the popup window the client opens for the NPC.
func stripPopupMenu(archive *platformpvf.Archive, raw []byte) ([]byte, int, error) {
	if len(raw) == 0 || len(raw)%5 != 0 {
		return nil, 0, fmt.Errorf("%s token stream is invalid: bytes=%d", npcPath, len(raw))
	}
	out := make([]byte, 0, len(raw))
	dropped := 0
	inSection := false
	opens := 0
	for offset := 0; offset < len(raw); offset += 5 {
		token := raw[offset : offset+5]
		label := normalizedLabel(archive.ResolveString(scriptTokenInt(token)))
		if token[0] == 3 && label == "npc popupmenu step" && !inSection {
			inSection = true
			opens++
			dropped++
			continue
		}
		if inSection {
			dropped++
			if token[0] == 3 && label == "/npc popupmenu step" {
				inSection = false
			}
			continue
		}
		out = append(out, token...)
	}
	if opens != 1 || inSection {
		return nil, 0, fmt.Errorf("%s [npc popupmenu step] blocks=%d unterminated=%v, want 1 closed", npcPath, opens, inSection)
	}
	return out, dropped, nil
}

// stripJoustRole drops the `[joust event]` label token and its trailing value
// token inside the [role] section. The item-shop binding is preserved.
func stripJoustRole(archive *platformpvf.Archive, raw []byte) ([]byte, int, error) {
	if len(raw) == 0 || len(raw)%5 != 0 {
		return nil, 0, fmt.Errorf("%s token stream is invalid: bytes=%d", npcPath, len(raw))
	}
	out := make([]byte, 0, len(raw))
	dropped := 0
	removed := 0
	for offset := 0; offset < len(raw); offset += 5 {
		token := raw[offset : offset+5]
		if token[0] == 6 && archive.ResolveString(scriptTokenInt(token)) == "[joust event]" {
			removed++
			dropped++
			if offset+10 > len(raw) {
				return nil, 0, fmt.Errorf("%s truncated [joust event] role", npcPath)
			}
			offset += 5
			dropped++
			continue
		}
		out = append(out, token...)
	}
	if removed != 1 {
		return nil, 0, fmt.Errorf("%s [joust event] role matches=%d, want=1", npcPath, removed)
	}
	return out, dropped, nil
}

// stripEventGates removes the event-id value token that follows
// `[open visible event]` and every token of the `[event open] 2365 -1 -1`
// section. It requires both sections to be present.
func stripEventGates(archive *platformpvf.Archive, raw []byte) ([]byte, int, error) {
	if len(raw) == 0 || len(raw)%5 != 0 {
		return nil, 0, fmt.Errorf("%s token stream is invalid: bytes=%d", npcPath, len(raw))
	}
	out := make([]byte, 0, len(raw))
	dropped := 0
	sawVisible := 0
	sawEventOpen := 0
	for offset := 0; offset < len(raw); offset += 5 {
		token := raw[offset : offset+5]
		label := normalizedLabel(archive.ResolveString(scriptTokenInt(token)))
		switch {
		case token[0] == 3 && label == "open visible event":
			sawVisible++
			out = append(out, token...)
			if offset+10 <= len(raw) {
				next := raw[offset+5 : offset+10]
				if next[0] == 0 && scriptTokenInt(next) == joustEvent {
					dropped++
					offset += 5
				}
			}
		case token[0] == 3 && label == "event open":
			sawEventOpen++
			if offset+20 > len(raw) {
				return nil, 0, fmt.Errorf("%s truncated [event open] section", npcPath)
			}
			v := raw[offset+5 : offset+20]
			if !(v[0] == 0 && scriptTokenInt(v[0:5]) == joustEvent &&
				v[5] == 0 && scriptTokenInt(v[5:10]) == -1 &&
				v[10] == 0 && scriptTokenInt(v[10:15]) == -1) {
				return nil, 0, fmt.Errorf("%s [event open] shape is not `%d -1 -1`", npcPath, joustEvent)
			}
			dropped += 4
			offset += 15
		default:
			out = append(out, token...)
		}
	}
	if sawVisible != 1 || sawEventOpen != 1 {
		return nil, 0, fmt.Errorf("%s gate sections: open visible event=%d event open=%d, want 1 each", npcPath, sawVisible, sawEventOpen)
	}
	return out, dropped, nil
}

// restoreTownPlacement moves the town-map NPC tuple back to its original
// coordinates. The original tuple is accepted as an idempotent no-op.
func restoreTownPlacement(archive *platformpvf.Archive, raw []byte) ([]byte, string, string, bool, error) {
	if len(raw) == 0 || len(raw)%5 != 0 {
		return nil, "", "", false, fmt.Errorf("%s token stream is invalid: bytes=%d", mapPath, len(raw))
	}
	out := append([]byte(nil), raw...)
	inNPCSection := false
	matches := 0
	before := ""
	after := ""
	moved := false
	for offset := 0; offset < len(out); offset += 5 {
		token := out[offset : offset+5]
		if token[0] == 3 {
			inNPCSection = normalizedLabel(archive.ResolveString(scriptTokenInt(token))) == "npc"
			continue
		}
		if !inNPCSection || token[0] != 0 || scriptTokenInt(token) != npcID || offset+25 > len(out) {
			continue
		}
		direction := out[offset+5 : offset+10]
		xToken := out[offset+10 : offset+15]
		yToken := out[offset+15 : offset+20]
		zToken := out[offset+20 : offset+25]
		if direction[0] != 6 || normalizedLabel(archive.ResolveString(scriptTokenInt(direction))) != "left" ||
			xToken[0] != 0 || yToken[0] != 0 || zToken[0] != 0 || scriptTokenInt(zToken) != 0 {
			return nil, "", "", false, fmt.Errorf("town NPC %d placement does not match the proved tuple", npcID)
		}
		x, y := scriptTokenInt(xToken), scriptTokenInt(yToken)
		before = fmt.Sprintf("%d %d", x, y)
		switch {
		case x == oldX && y == oldY:
			binary.LittleEndian.PutUint32(xToken[1:5], uint32(int32(newX)))
			binary.LittleEndian.PutUint32(yToken[1:5], uint32(int32(newY)))
			moved = true
		case x == newX && y == newY:
		default:
			return nil, "", "", false, fmt.Errorf("town NPC %d placement=(%d,%d), want midpoint=(%d,%d) or original=(%d,%d)", npcID, x, y, oldX, oldY, newX, newY)
		}
		after = fmt.Sprintf("%d %d", scriptTokenInt(xToken), scriptTokenInt(yToken))
		matches++
	}
	if matches != 1 {
		return nil, "", "", false, fmt.Errorf("town NPC %d placement matches=%d, want=1", npcID, matches)
	}
	return out, before, after, moved, nil
}

func normalizedLabel(value string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(value), "[]"))
}

func scriptTokenInt(token []byte) int {
	return int(int32(binary.LittleEndian.Uint32(token[1:5])))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

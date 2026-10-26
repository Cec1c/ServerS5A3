package dnfbridge

import (
	"bytes"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

const (
	round118SpendTimeMenuTitle       = "累计在线奖励"
	round118SpendTimeMenuDescription = "累计在线时间达到对应阶段要求后，可领取该阶段奖励。"
)

type round118SpendTimeMenuMetadata struct {
	genericTitle       string
	genericDescription string
	category           byte
	presentation       byte
	title              string
	subtitle           string
	description        string
	start              uint32
	end                uint32
	route              string
	status             string
	tail               byte
}

func round118ReadClientText(t *testing.T, reader *currentSpendTimeCatalogReader) string {
	t.Helper()
	raw := reader.dstr(t)
	decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(raw)
	if err != nil {
		t.Fatalf("decode GB18030 op108 text: %v", err)
	}
	return string(decoded)
}

func round118ParseSpendTimeMenuMetadata(t *testing.T, row currentSpendTimeCatalogBaseRow) round118SpendTimeMenuMetadata {
	t.Helper()
	if !row.hasExtended {
		t.Fatalf("event %d has no extended menu descriptor", row.eventID)
	}
	reader := &currentSpendTimeCatalogReader{body: row.raw}
	if got := reader.uint16Value(t); got != uint16(currentSpendTimeEventID) {
		t.Fatalf("menu row event=%d", got)
	}
	if got := reader.uint32Value(t); got != currentSpendTimeBaseCatalogValue {
		t.Fatalf("menu row value=%d", got)
	}
	metadata := round118SpendTimeMenuMetadata{
		genericTitle:       round118ReadClientText(t, reader),
		genericDescription: round118ReadClientText(t, reader),
	}
	if got := reader.byteValue(t); got != 1 {
		t.Fatalf("menu row has_extended=%d", got)
	}
	metadata.category = reader.byteValue(t)
	metadata.presentation = reader.byteValue(t)
	metadata.title = round118ReadClientText(t, reader)
	metadata.subtitle = round118ReadClientText(t, reader)
	metadata.description = round118ReadClientText(t, reader)
	metadata.start = reader.uint32Value(t)
	metadata.end = reader.uint32Value(t)
	metadata.route = round118ReadClientText(t, reader)
	metadata.status = round118ReadClientText(t, reader)
	metadata.tail = reader.byteValue(t)
	if reader.remaining() != 0 {
		t.Fatalf("event %d menu row has %d trailing bytes", row.eventID, reader.remaining())
	}
	return metadata
}

func round118BaseRowsByEvent(t *testing.T, shape currentSpendTimeCatalogShape, skip uint16) map[uint16][]byte {
	t.Helper()
	rows := make(map[uint16][]byte, len(shape.baseRows))
	for _, row := range shape.baseRows {
		if row.eventID == skip {
			continue
		}
		if _, exists := rows[row.eventID]; exists {
			t.Fatalf("duplicate base event %d", row.eventID)
		}
		rows[row.eventID] = row.raw
	}
	return rows
}

func round118ActiveRowsByEvent(t *testing.T, shape currentSpendTimeCatalogShape, skip uint16) map[uint16][]byte {
	t.Helper()
	rows := make(map[uint16][]byte, len(shape.activeRows))
	for _, row := range shape.activeRows {
		if row.eventID == skip {
			continue
		}
		if _, exists := rows[row.eventID]; exists {
			t.Fatalf("duplicate active event %d", row.eventID)
		}
		rows[row.eventID] = row.raw
	}
	return rows
}

func round118AssertSameRows(t *testing.T, got, want map[uint16][]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("catalog row ownership changed: got=%d want=%d", len(got), len(want))
	}
	for eventID, gotRaw := range got {
		wantRaw, ok := want[eventID]
		if !ok {
			t.Fatalf("event %d appeared in enabled catalog", eventID)
		}
		if !bytes.Equal(gotRaw, wantRaw) {
			t.Fatalf("event %d layout changed: got=%x want=%x", eventID, gotRaw, wantRaw)
		}
	}
}

func TestCurrentSpendTime2347PublishesVisibleGeneralMenuEntry(t *testing.T) {
	ids := []uint32{490006574, 490006575, 490006576, 490006577}
	build := func(includeSpendTime bool) []byte {
		t.Helper()
		body, err := buildCurrentActivityEventInfoBodyWithEpicRoad(
			ids, 10800,
			false, false, true, true, includeSpendTime,
			0, false, false, false, true, false,
		)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}

	enabled := parseCurrentSpendTimeEventInfoBody(t, build(true))
	disabled := parseCurrentSpendTimeEventInfoBody(t, build(false))
	if disabled.hasEvent(uint16(currentSpendTimeEventID)) {
		t.Fatal("disabled 2347 catalog still contains the event")
	}
	if enabled.baseCount != disabled.baseCount+1 || enabled.activeCount != disabled.activeCount+1 {
		t.Fatalf("2347 counts: enabled=%d/%d disabled=%d/%d",
			enabled.baseCount, enabled.activeCount, disabled.baseCount, disabled.activeCount)
	}

	round118AssertSameRows(t,
		round118BaseRowsByEvent(t, enabled, uint16(currentSpendTimeEventID)),
		round118BaseRowsByEvent(t, disabled, uint16(currentSpendTimeEventID)))
	round118AssertSameRows(t,
		round118ActiveRowsByEvent(t, enabled, uint16(currentSpendTimeEventID)),
		round118ActiveRowsByEvent(t, disabled, uint16(currentSpendTimeEventID)))

	var baseRow *currentSpendTimeCatalogBaseRow
	for index := range enabled.baseRows {
		if enabled.baseRows[index].eventID == uint16(currentSpendTimeEventID) {
			baseRow = &enabled.baseRows[index]
			break
		}
	}
	if baseRow == nil {
		t.Fatal("enabled catalog omits the 2347 base row")
	}
	metadata := round118ParseSpendTimeMenuMetadata(t, *baseRow)
	if metadata.genericTitle != "" || metadata.genericDescription != "" {
		t.Fatalf("2347 generic fields changed: title=%q description=%q",
			metadata.genericTitle, metadata.genericDescription)
	}
	if metadata.category != 0 || metadata.presentation != 4 {
		t.Fatalf("2347 menu kind: category=%d presentation=%d", metadata.category, metadata.presentation)
	}
	if metadata.title != round118SpendTimeMenuTitle {
		t.Fatalf("2347 menu title=%q want=%q", metadata.title, round118SpendTimeMenuTitle)
	}
	if metadata.subtitle != "" {
		t.Fatalf("2347 menu subtitle=%q", metadata.subtitle)
	}
	if metadata.description != round118SpendTimeMenuDescription {
		t.Fatalf("2347 menu description=%q want=%q", metadata.description, round118SpendTimeMenuDescription)
	}
	if metadata.start != 0 || metadata.end != 0 {
		t.Fatalf("2347 schedule changed: start=%d end=%d", metadata.start, metadata.end)
	}
	if metadata.route != currentSpendTimeEventRoute {
		t.Fatalf("2347 route=%q want=%q", metadata.route, currentSpendTimeEventRoute)
	}
	if metadata.status != "" || metadata.tail != 1 {
		t.Fatalf("2347 menu finishing fields: status=%q tail=%d", metadata.status, metadata.tail)
	}

	var activeRow *currentSpendTimeCatalogActiveRow
	for index := range enabled.activeRows {
		if enabled.activeRows[index].eventID == uint16(currentSpendTimeEventID) {
			activeRow = &enabled.activeRows[index]
			break
		}
	}
	if activeRow == nil {
		t.Fatal("enabled catalog omits the 2347 active row")
	}
	for index, want := range ids {
		if got := activeRow.params[index]; got != want {
			t.Fatalf("2347 reward[%d]=%d want=%d", index, got, want)
		}
	}
	for index := len(ids); index < 10; index++ {
		if got := activeRow.params[index]; got != currentSpendTimeUnusedParam {
			t.Fatalf("2347 unused reward[%d]=%#x", index, got)
		}
	}
	if activeRow.params[10] != uint32(len(ids)) || activeRow.params[11] != 10800 {
		t.Fatalf("2347 progress descriptor: count=%d duration=%d", activeRow.params[10], activeRow.params[11])
	}
}

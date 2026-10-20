package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"golang.org/x/text/encoding/simplifiedchinese"
	onlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	"os"
	"testing"
)

func TestOrdinaryMenuFallbackOwnedEntryAndLegacyBytes(t *testing.T) {
	ids := currentSpendTimeJoustCatalogGoldenRewards
	disabled, err := buildCurrentActivityEventInfoBodyWithOrdinary(ids, 10800, true, true, true, true, true, 2423, true, true, true, false)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := buildCurrentActivityEventInfoBodyWithWeekly(ids, 10800, true, true, true, true, true, 2423, true, true, true)
	if err != nil || !bytes.Equal(disabled, legacy) {
		t.Fatal("ordinary disabled changed legacy catalog", err)
	}
	enabled, err := buildCurrentActivityEventInfoBodyWithOrdinary(ids, 10800, true, true, true, true, true, 2423, true, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	a, b := parseCurrentSpendTimeEventInfoBody(t, disabled), parseCurrentSpendTimeEventInfoBody(t, enabled)
	for _, row := range a.baseRows {
		for _, next := range b.baseRows {
			if row.eventID == next.eventID && !bytes.Equal(row.raw, next.raw) {
				t.Fatalf("foreign event %d changed", row.eventID)
			}
		}
	}
	count := 0
	for _, row := range b.baseRows {
		if row.eventID == 331 {
			count++
			if !row.hasExtended || row.route != "attendance daily event/0/0" || row.start != 0 || row.end != 0 || row.raw[len(row.raw)-1] != 1 {
				t.Fatalf("ordinary menu contract: %+v tail%d", row, row.raw[len(row.raw)-1])
			}
		}
	}
	if count != 1 || !b.hasEvent(331) {
		t.Fatal("ordinary owner duplicated or not active")
	}
	if p := os.Getenv("ORDINARY_MENU_NATIVE_BODY"); p != "" {
		if e := os.WriteFile(p, enabled, 0644); e != nil {
			t.Fatal(e)
		}
	}
}

func TestOrdinaryMenuFallbackUnavailableCatalogCannotEnable(t *testing.T) {
	s, _, _, _ := prepareMonthlyBridge(t)
	// Empty PVF-backed catalog is unavailable; no server row may be published.
	s.initialEquipmentMu.Lock()
	s.initialEquipmentArchive = nil
	s.initialEquipmentMu.Unlock()

	if s.currentOrdinaryAttendanceEnabled() {
		t.Fatal("unavailable catalog allowed menu fallback")
	}
}

func TestOrdinaryMenuFallbackClosedAndUnsupportedOwnerCannotEnable(t *testing.T) {
	s, _, _, _ := prepareMonthlyBridge(t)
	if !s.currentOrdinaryAttendanceEnabled() {
		t.Fatal("actual resource-backed ordinary owner unavailable")
	}
	if _, err := s.activityControls.Save(context.Background(), s.activityControls.Snapshot().Revision, []onlineevent.ControlChange{{ID: 331, Enabled: false}}); err != nil {
		t.Fatal(err)
	}
	if err := s.initializeActivityControls(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.currentOrdinaryAttendanceEnabled() {
		t.Fatal("closed owner enabled fallback")
	}
	s.options.currentClientVersion = "unknown"
	if s.currentOrdinaryAttendanceEnabled() {
		t.Fatal("unsupported client enabled fallback")
	}
}

func TestOrdinaryMetadataRegularKindAndOwnedTitle(t *testing.T) {
	body, err := buildCurrentActivityEventInfoBodyWithOrdinary(currentSpendTimeJoustCatalogGoldenRewards, 10800, true, true, true, true, true, 2423, true, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range parseCurrentSpendTimeEventInfoBody(t, body).baseRows {
		if row.eventID != 331 {
			continue
		}
		p := 6
		read := func() string {
			if p+4 > len(row.raw) {
				t.Fatal("truncated dstr")
			}
			n := int(binary.LittleEndian.Uint32(row.raw[p : p+4]))
			p += 4
			if n < 0 || p+n > len(row.raw) {
				t.Fatal("invalid dstr")
			}
			b := row.raw[p : p+n]
			p += n
			decoded, e := simplifiedchinese.GB18030.NewDecoder().Bytes(b)
			if e != nil {
				t.Fatal(e)
			}
			return string(decoded)
		}
		if read() != "" || read() != "每天登录选择角色后登记签到，领取对应日期奖励。" {
			t.Fatal("generic metadata")
		}
		if !bytes.Equal(row.raw[p:p+3], []byte{1, 0, 4}) {
			t.Fatal("unverified presentation kind")
		}
		p += 3
		if read() != "每日签到得好礼" {
			t.Fatal("empty title")
		}
		if read() != "" || read() != "" {
			t.Fatal("optional hyperlink metadata must stay empty")
		}
		return
	}
	t.Fatal("ordinary331 missing")
}

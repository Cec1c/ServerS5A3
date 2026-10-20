package dnfbridge

import (
	"fmt"
	"strings"
	"testing"
)

func ordinaryAttendanceUIFixture() (string, string) {
	var main strings.Builder
	main.WriteString(`<ROOT><CONTROL><CNUIControlText ID="STR:cur"/><CNButton ID="STR:recv"/><CNUIControlAnimationForRDAR ID="STR:today"/><CNUIControlImage ID="STR:block"/>`)
	for day := 0; day < 28; day++ {
		fmt.Fprintf(&main, `<UIContainer ID="STR:%d" UIPath="STR:UI/Event/AttendanceDailyEvent/slot.xui"/>`, day)
	}
	main.WriteString(`</CONTROL></ROOT>`)
	return main.String(), `<ROOT><CONTROL><CNUIControlImage ID="STR:board"/><CNUIControlImage ID="STR:board1"/><ItemControl ID="STR:reward"/><CNUIControlText ID="STR:title"/><CNUIControlAnimationForRDAR ID="STR:checkani"/><CNUIControlImage ID="STR:checkimg"/></CONTROL></ROOT>`
}

func TestOrdinaryAttendanceUIRejectsIncompleteNativeBinding(t *testing.T) {
	main, slot := ordinaryAttendanceUIFixture()
	if err := validateOrdinaryAttendanceUI(main+"\x00", slot+"\x00"); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(string, string) (string, string){
		"original eight slots": func(m, s string) (string, string) {
			start := strings.Index(m, `<UIContainer ID="STR:8"`)
			return m[:start] + `</CONTROL></ROOT>`, s
		},
		"gap":       func(m, s string) (string, string) { return strings.Replace(m, `ID="STR:11"`, `ID="STR:99"`, 1), s },
		"duplicate": func(m, s string) (string, string) { return strings.Replace(m, `ID="STR:11"`, `ID="STR:10"`, 1), s },
		"wrong slot template": func(m, s string) (string, string) {
			return strings.Replace(m, `UI/Event/AttendanceDailyEvent/slot.xui`, `UI/Event/Other/slot.xui`, 1), s
		},
		"wrong reward type":  func(m, s string) (string, string) { return m, strings.Replace(s, `ItemControl`, `CNButton`, 1) },
		"missing top button": func(m, s string) (string, string) { return strings.Replace(m, `ID="STR:recv"`, `ID="STR:other"`, 1), s },
		"missing check image": func(m, s string) (string, string) {
			return m, strings.Replace(s, `ID="STR:checkimg"`, `ID="STR:other"`, 1)
		},
		"invalid XML": func(m, s string) (string, string) { return m + `<`, s },
	} {
		t.Run(name, func(t *testing.T) {
			m, s := mutate(main, slot)
			if err := validateOrdinaryAttendanceUI(m, s); err == nil {
				t.Fatal("incompatible native window was accepted")
			}
		})
	}
}

func TestRealPVFOrdinaryAttendanceUIContract(t *testing.T) {
	s, _, _, _ := prepareMonthlyBridge(t)
	if _, err := s.currentOrdinaryCatalog(nil); err != nil {
		t.Fatal(err)
	}
}

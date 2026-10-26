package dnfbridge

import (
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const ordinaryAttendanceSlotPath = "ui/event/attendancedailyevent/slot.xui"

// BF4F90 binds consecutive numeric containers, while BF3E60 indexes them using
// the 28 reward days. A shorter layout reaches the native vector bounds abort.
func validateOrdinaryAttendanceUI(mainText, slotText string) error {
	main, err := ordinaryAttendanceUIElements(mainText)
	if err != nil {
		return err
	}
	slot, err := ordinaryAttendanceUIElements(slotText)
	if err != nil {
		return err
	}
	for id, kind := range map[string]string{"cur": "CNUIControlText", "recv": "CNButton", "today": "CNUIControlAnimationForRDAR", "block": "CNUIControlImage"} {
		if err := requireOrdinaryAttendanceUIElement(main, id, kind); err != nil {
			return err
		}
	}
	for day := 0; day < 28; day++ {
		id := strconv.Itoa(day)
		if err := requireOrdinaryAttendanceUIElement(main, id, "UIContainer"); err != nil {
			return err
		}
		if !strings.EqualFold(strings.TrimPrefix(main[id][0].attrs["UIPath"], "STR:"), ordinaryAttendanceSlotPath) {
			return fmt.Errorf("ordinary attendance slot %s has an incompatible template", id)
		}
	}
	for id, elements := range main {
		if _, err := strconv.Atoi(id); err == nil && len(elements) > 0 && elements[0].kind == "UIContainer" {
			day, _ := strconv.Atoi(id)
			if day < 0 || day >= 28 {
				return fmt.Errorf("ordinary attendance has an extra numeric slot %s", id)
			}
		}
	}
	for id, kind := range map[string]string{"board": "CNUIControlImage", "board1": "CNUIControlImage", "reward": "ItemControl", "title": "CNUIControlText", "checkani": "CNUIControlAnimationForRDAR", "checkimg": "CNUIControlImage"} {
		if err := requireOrdinaryAttendanceUIElement(slot, id, kind); err != nil {
			return err
		}
	}
	return nil
}

type ordinaryAttendanceUIElement struct {
	kind  string
	attrs map[string]string
}

func ordinaryAttendanceUIElements(text string) (map[string][]ordinaryAttendanceUIElement, error) {
	decoder := xml.NewDecoder(strings.NewReader(strings.TrimRight(text, "\x00")))
	out := make(map[string][]ordinaryAttendanceUIElement)
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("ordinary attendance XUI: %w", err)
		}
		element, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		attrs := make(map[string]string, len(element.Attr))
		for _, attr := range element.Attr {
			attrs[attr.Name.Local] = attr.Value
		}
		if id := strings.TrimPrefix(attrs["ID"], "STR:"); id != "" {
			out[id] = append(out[id], ordinaryAttendanceUIElement{kind: element.Name.Local, attrs: attrs})
		}
	}
}

func requireOrdinaryAttendanceUIElement(elements map[string][]ordinaryAttendanceUIElement, id, kind string) error {
	if matches := elements[id]; len(matches) != 1 || matches[0].kind != kind {
		return fmt.Errorf("ordinary attendance requires one %s control %s", kind, id)
	}
	return nil
}

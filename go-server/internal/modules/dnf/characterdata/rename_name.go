package characterdata

import (
	"fmt"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// Current native DSTR storage accepts at most 30 encoded bytes. Reject names
// that cannot survive a GB18030 round trip or contain invisible separators.
func ValidateRenameName(name string) error {
	if !utf8.ValidString(name) {
		return fmt.Errorf("%w: invalid UTF-8", ErrRenameUnavailable)
	}
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(name))
	if err != nil || len(encoded) < 2 || len(encoded) > 30 {
		return fmt.Errorf("%w: name length", ErrRenameUnavailable)
	}
	for _, value := range name {
		if unicode.IsSpace(value) || unicode.IsControl(value) || unicode.Is(unicode.Cf, value) || !unicode.IsGraphic(value) {
			return ErrRenameUnavailable
		}
		switch value {
		case '\'', '\\', '%', '<', '>', '"', '|':
			return ErrRenameUnavailable
		}
	}
	return nil
}

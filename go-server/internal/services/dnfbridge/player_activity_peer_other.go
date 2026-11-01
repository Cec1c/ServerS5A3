//go:build !windows

package dnfbridge

import (
	"net/http"
	"time"
)

func localPlayerHTTPProcess(*http.Request) (uint32, time.Time, bool) {
	return 0, time.Time{}, false
}

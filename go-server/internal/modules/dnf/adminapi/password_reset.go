package adminapi

import (
	"errors"
	"regexp"
)

// PasswordReset is transient credential material. Never log or persist this
// request. The game stores only its fingerprint and a non-secret receipt.
type PasswordReset struct {
	RequestID string `json:"requestId"`
	Account   string `json:"account"`
	Password  string `json:"password"`
}

type PasswordResetResult struct {
	RequestID string `json:"requestId"`
	AccountID string `json:"accountId"`
	Account   string `json:"account"`
	ResetAt   int64  `json:"resetAt"`
}

var resetRequestPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,96}$`)
var resetAccountPattern = regexp.MustCompile(`^[A-Za-z0-9_]{3,20}$`)
var resetPasswordPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{20,64}$`)

func (r PasswordReset) Validate() error {
	if !resetRequestPattern.MatchString(r.RequestID) || !resetAccountPattern.MatchString(r.Account) || !resetPasswordPattern.MatchString(r.Password) {
		return errors.New("invalid password reset request")
	}
	return nil
}

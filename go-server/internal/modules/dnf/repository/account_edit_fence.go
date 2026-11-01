package repository

import "context"

// AccountEditFence joins the same account-row lock used by administration
// transactions. A transport must announce admission before waiting here and
// retain that announcement until all session callbacks have stopped.
type AccountEditFence interface {
	WaitForAccountEdits(context.Context, string) error
}

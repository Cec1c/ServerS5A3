package dnfbridge

import (
	"errors"
)

type pvpWriteFailureConn struct {
	bufferConn
	remainingFailures int
	writeErr          error
}

func (c *pvpWriteFailureConn) Write(p []byte) (int, error) {
	if c.remainingFailures > 0 {
		c.remainingFailures--
		if c.writeErr != nil {
			return 0, c.writeErr
		}
		return 0, errors.New("模拟写包失败")
	}
	return c.bufferConn.Write(p)
}

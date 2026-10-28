package dnfbridge

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

// 只提前读取字节，不在读取协程中拆包、访问角色或执行资产事务。
// 有界队列让原生一次循环发出的后续请求在前一件落库时继续进入服务器。
type nativeResultReadChunk struct {
	data               []byte
	err                error
	resume             chan struct{}
	deadlineGeneration uint64
}

type nativeResultReadAhead struct {
	conn               net.Conn
	ready              chan nativeResultReadChunk
	stopping           chan struct{}
	done               chan struct{}
	once               sync.Once
	resumeError        chan struct{}
	deadlineMu         sync.Mutex
	deadlineGeneration uint64
}

func newNativeResultReadAhead(conn net.Conn) *nativeResultReadAhead {
	r := &nativeResultReadAhead{conn: conn, ready: make(chan nativeResultReadChunk, 16), stopping: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(r.done)
		buffer := make([]byte, 4096)
		for {
			r.deadlineMu.Lock()
			generation := r.deadlineGeneration
			r.deadlineMu.Unlock()
			n, err := conn.Read(buffer)
			chunk := nativeResultReadChunk{data: append([]byte(nil), buffer[:n]...), err: err, deadlineGeneration: generation}
			if err != nil {
				chunk.resume = make(chan struct{})
			}
			select {
			case r.ready <- chunk:
			case <-r.stopping:
				return
			}
			if chunk.resume != nil {
				// 超时由原读包循环判定、清除截止后再恢复，避免把同一次
				// op19静默截止重复排队，误判成网络断线。
				select {
				case <-chunk.resume:
				case <-r.stopping:
					return
				}
			}
		}
	}()
	return r
}

func (r *nativeResultReadAhead) changeDeadline(set func()) {
	r.deadlineMu.Lock()
	defer r.deadlineMu.Unlock()
	set()
	r.deadlineGeneration++
}

// 读取期间静默截止可能已经被后续包清除或重新设置。旧超时不能关闭会话，
// 也不能结算新一轮挂起状态；恢复读取后仍会按最新截止正常返回超时。
func (r *nativeResultReadAhead) obsoleteTimeout(chunk nativeResultReadChunk) bool {
	var nerr net.Error
	if !errors.As(chunk.err, &nerr) || !nerr.Timeout() {
		return false
	}
	r.deadlineMu.Lock()
	defer r.deadlineMu.Unlock()
	return chunk.deadlineGeneration != r.deadlineGeneration
}

func (r *nativeResultReadAhead) releaseError() {
	if r.resumeError != nil {
		close(r.resumeError)
		r.resumeError = nil
	}
}

func (r *nativeResultReadAhead) tryNext() (nativeResultReadChunk, bool) {
	r.releaseError()
	select {
	case chunk := <-r.ready:
		r.resumeError = chunk.resume
		return chunk, true
	default:
		return nativeResultReadChunk{}, false
	}
}

func (r *nativeResultReadAhead) next(ctx context.Context) nativeResultReadChunk {
	r.releaseError()
	select {
	case chunk := <-r.ready:
		r.resumeError = chunk.resume
		return chunk
	case <-ctx.Done():
		return nativeResultReadChunk{err: ctx.Err()}
	}
}

func (r *nativeResultReadAhead) stop() {
	r.once.Do(func() {
		close(r.stopping)
		if err := r.conn.SetReadDeadline(time.Now()); err != nil {
			_ = r.conn.Close()
		}
		<-r.done
	})
}

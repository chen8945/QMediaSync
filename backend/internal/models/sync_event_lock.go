package models

import (
	"context"
	"sync"
)

// syncEventLocks 串行化同一历史记录的数据库变更及事件发布。
// 只保存正在使用的 ID，释放后移除；等待不持有数据库事务。
type syncEventLocks struct {
	mu     sync.Mutex
	active map[uint]chan struct{}
}

var syncRecordEvents syncEventLocks

func (l *syncEventLocks) acquire(ctx context.Context, id uint) (func(), error) {
	for {
		l.mu.Lock()
		if err := ctx.Err(); err != nil {
			l.mu.Unlock()
			return nil, err
		}
		done, busy := l.active[id]
		if !busy {
			done = make(chan struct{})
			if l.active == nil {
				l.active = make(map[uint]chan struct{})
			}
			l.active[id] = done
			l.mu.Unlock()
			return sync.OnceFunc(func() {
				l.mu.Lock()
				delete(l.active, id)
				close(done)
				l.mu.Unlock()
			}), nil
		}
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-done:
		}
	}
}

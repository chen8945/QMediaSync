package proxyhttp

import (
	"net"
	"sync"
)

// Connections 登记包括 HTTP 升级后的连接；关闭操作在锁外执行。
type Connections struct {
	mu     sync.Mutex
	closed bool
	conns  map[*connection]struct{}
}

func (c *Connections) Wrap(listener net.Listener) net.Listener {
	return &trackedListener{Listener: listener, owner: c}
}

func (c *Connections) Close() {
	c.mu.Lock()
	c.closed = true
	conns := make([]*connection, 0, len(c.conns))
	for conn := range c.conns {
		conns = append(conns, conn)
	}
	c.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

type trackedListener struct {
	net.Listener
	owner *Connections
}

func (l *trackedListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tracked := &connection{Conn: conn, owner: l.owner}
	l.owner.mu.Lock()
	if l.owner.closed {
		l.owner.mu.Unlock()
		_ = conn.Close()
		return nil, net.ErrClosed
	}
	if l.owner.conns == nil {
		l.owner.conns = make(map[*connection]struct{})
	}
	l.owner.conns[tracked] = struct{}{}
	l.owner.mu.Unlock()
	return tracked, nil
}

type connection struct {
	net.Conn
	owner *Connections
}

func (c *connection) Close() error {
	err := c.Conn.Close()
	c.owner.mu.Lock()
	delete(c.owner.conns, c)
	c.owner.mu.Unlock()
	return err
}

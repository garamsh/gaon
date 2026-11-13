package proxy

import (
	"net"
	"time"
)

// idleConn closes its connection when no byte has moved in either direction
// for the idle time. A hijacked or wrapped connection keeps the timer, so a
// streaming response, an upgraded connection and a passthrough connection all
// end the same way.
type idleConn struct {
	net.Conn
	idle  time.Duration
	timer *time.Timer
}

func newIdleConn(c net.Conn, idle time.Duration) *idleConn {
	return &idleConn{Conn: c, idle: idle, timer: time.AfterFunc(idle, func() { _ = c.Close() })}
}

func (c *idleConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.timer.Reset(c.idle)
	}
	return n, err
}

func (c *idleConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		c.timer.Reset(c.idle)
	}
	return n, err
}

func (c *idleConn) Close() error {
	c.timer.Stop()
	return c.Conn.Close()
}

// idleListener accepts connections that close after idle without traffic.
type idleListener struct {
	net.Listener
	idle time.Duration
}

func (l idleListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return newIdleConn(c, l.idle), nil
}

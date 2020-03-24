package irc

import (
	"context"
	"fmt"
	"time"

	"github.com/golang/glog"
)

var (
	errBanned = fmt.Errorf("user is shitlisted")
)

// getconn either gets a connection by username, or creates a new one (after
// evicting the least recently used connection).
func (m *Manager) getconn(ctx context.Context, user string) (*ircconn, error) {
	// Is the user shitlisted?
	if t, ok := m.shitlist[user]; ok && time.Now().Before(t) {
		return nil, errBanned
	}
	// Do we already have a connection?
	c, ok := m.conns[user]
	if ok {
		// Bump and return.
		c.last = time.Now()
		return c, nil
	}

	// Are we at the limit of allowed connections?
	if len(m.conns) >= m.max {
		// Evict least recently used
		evict := ""
		lru := time.Now()
		for _, c := range m.conns {
			if c.last.Before(lru) {
				evict = c.user
				lru = c.last
			}
		}
		if evict == "" {
			glog.Exitf("could not find connection to evict, %v", m.conns)
		}
		m.conns[evict].Evict()
		delete(m.conns, evict)
	}

	// Allocate new connection
	return m.newconn(ctx, user, false)
}

// newconn creates a new IRC connection as a given user, and saves it to the
// conns map.
func (m *Manager) newconn(ctx context.Context, user string, backup bool) (*ircconn, error) {
	c, err := NewConn(m.server, m.channel, user, backup, m.Event)
	if err != nil {
		return nil, err
	}
	m.conns[user] = c

	go c.Run(m.runctx)

	return c, nil
}

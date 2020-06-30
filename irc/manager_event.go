package irc

import (
	"context"
	"time"

	"github.com/golang/glog"
)

func (m *Manager) Event(e *event) {
	m.event <- e
}

// Event: a connection has a new nick.
func (m *Manager) UpdateNickmap(conn *ircconn, nick string) {
	m.event <- &event{
		nick: &eventNick{
			conn: conn,
			nick: nick,
		},
	}
}

// Event: mark a given connection as dead.
func (m *Manager) MarkDead(i *ircconn) {
	m.event <- &event{
		dead: &eventDead{
			conn: i,
		},
	}
}

// event message from IRC connections. Only one member can be set.
type event struct {
	// a connection has gotten a (new) nick
	nick *eventNick
	// a connection received a new PRIVMSG
	message *eventMessage
	// a connection is banned
	banned *eventBanned
	// a connection died
	dead *eventDead
}

// eventNick is emitted when a connection has received a new nickname from IRC
type eventNick struct {
	conn *ircconn
	nick string
}

// eventMessage is emitted when there is a PRIVMSG to the IRC channel. This
// does not contain messages sent by ourselves, and messages are deduplicated
// from multiple active IRC connections.
type eventMessage struct {
	conn    *ircconn
	nick    string
	message string
}

// eventBanned is amitted when a connection is banned from a channel.
type eventBanned struct {
	conn *ircconn
}

// eventDead is emitted when a connection has died and needs to be disposed of
type eventDead struct {
	conn *ircconn
}

func (m *Manager) notifyAll(n *Notification) {
	for s, _ := range m.subscribers {
		go func(c chan *Notification, n *Notification) {
			c <- n
		}(s, n)
	}
}

// doevent handles incoming events.
func (m *Manager) doevent(ctx context.Context, e *event) {
	glog.V(16).Infof("event/debug16: New event handling")
	switch {
	case e.nick != nil:
		glog.V(8).Infof("event/debug8: nick (%s)", e.nick.nick)
		// Nick update from connection

		// Ensure this connection is still used.
		if m.conns[e.nick.conn.user] != e.nick.conn {
			return
		}

		// Edge-detect changes.
		changed := false
		if m.nickmap[e.nick.conn.user] != e.nick.nick {
			glog.Infof("Event: Nick change for %s: %q -> %q", e.nick.conn.user, m.nickmap[e.nick.conn.user], e.nick.nick)
			m.nickmap[e.nick.conn.user] = e.nick.nick
			changed = true
		}

		if !changed {
			return
		}

		// Notify subscribers about a new nickmap.
		nm := make(map[string]string)
		for k, v := range m.nickmap {
			nm[k] = v
		}
		m.notifyAll(&Notification{
			Nickmap: &nm,
		})

	case e.banned != nil:
		// A connection is banned. Shitlist the given user to not retry again.
		user := e.banned.conn.user
		glog.Infof("Event: %s is banned!", user)
		m.shitlist[user] = time.Now().Add(time.Hour)

	case e.dead != nil:
		// Dead update from connection.

		// Ensure this connection is still used.
		if m.conns[e.dead.conn.user] != e.dead.conn {
			return
		}

		// Delete connection.
		glog.Infof("Event: Connection for %s died", e.dead.conn.user)
		delete(m.conns, e.dead.conn.user)

	case e.message != nil:
		// Route messages from receivers.

		// Drop non-receiver events.
		if !e.message.conn.receiver {
			return
		}

		// Ensure this is not from us.
		for _, i := range m.nickmap {
			if e.message.nick == i {
				return
			}
		}

		m.notifyAll(&Notification{
			Message: &NotificationMessage{
				Nick:    e.message.nick,
				Message: e.message.message,
			},
		})

	default:
		glog.Errorf("Event: Unhandled event %+v", e)
	}
}

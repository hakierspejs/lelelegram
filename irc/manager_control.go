package irc

import (
	"context"
	"fmt"

	"github.com/golang/glog"
)

// Control: send a message to IRC.
func (m *Manager) SendMessage(ctx context.Context, user, text string) error {
	done := make(chan error)

	msg := &control{
		message: &controlMessage{
			from:    user,
			message: text,
			done:    done,
		},
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case m.ctrl <- msg:
		return <-done
	}
}

// Control: subscribe to notifiactions.
func (m *Manager) Subscribe(c chan *Notification) {
	m.ctrl <- &control{
		subscribe: &controlSubscribe{
			c: c,
		},
	}
}

// control message from owner. Only one member can be set.
type control struct {
	// message needs to be send to IRC
	message *controlMessage
	// a new subscription channel for notifications is presented
	subscribe *controlSubscribe
}

// controlMessage is a request to send a message to IRC as a given user
type controlMessage struct {
	// user name (native to application)
	from string
	// plaintext message
	message string
	// channel that will be sent nil or an error when the message has been
	// succesfully sent or an error occured
	done chan error
}

// controlSubscribe is a request to send notifications to a given channel
type controlSubscribe struct {
	c chan *Notification
}

// doctrl processes a given control message.
func (m *Manager) doctrl(ctx context.Context, c *control) {
	switch {

	case c.message != nil:
		// Send a message to IRC.

		// Find a relevant connection, or make one.
		conn, err := m.getconn(ctx, c.message.from)
		if err != nil {
			// Do not attempt to redeliver bans.
			if err == errBanned {
				c.message.done <- nil
			} else {
				c.message.done <- fmt.Errorf("getting connection: %v", err)
			}
			return
		}

		// Route message to connection.
		conn.Say(c.message)

	case c.subscribe != nil:
		// Subscribe to notifications.
		m.subscribers[c.subscribe.c] = true

	default:
		glog.Errorf("unhandled control %+v", c)
	}
}

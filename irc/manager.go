package irc

import (
	"context"
	"time"

	"github.com/golang/glog"
)

// Manager maintains a set of IRC connections to a server and channel. Its has
// three interfaces to the outside world:
//  - control, from the owner of Manager (eg. a bridge to another protocol)
//    that allows sending messages as a given user and to subscribe to
//    notifications
//  - events, from IRC connections, to update the manager about a connection
//    state (lifecycle or nick change)
//  - subscriptions, that pass received messages from IRC to a channel requested
//    by control.
//
// The Manager will maintain exactly one 'receiver', which is an IRC connection
// that is used as a source of truth for messages on an IRC channel. This will
// either be an existing connection for a user, or a 'backup' connection that
// will close as soon as a real/named connection exists and is fully connected.
type Manager struct {
	// maximum IRC sessions to maintain
	max int
	// IRC bot's username
	login string
	// IRC server address
	server string
	// IRC channel name
	channel string
	// control channel (from owner)
	ctrl chan *control
	// event channel (from connections)
	event chan *event

	// map from user name to IRC connection
	conns map[string]*ircconn
	// map from user name to IRC nick
	nickmap map[string]string
	// set of users that we shouldn't attempt to bridge, and their expiry times
	shitlist map[string]time.Time
	// set of subscribing channels for notifications
	subscribers map[chan *Notification]bool
	// context representing the Manager lifecycle
	runctx context.Context
}

func NewManager(max int, server, channel string, login string) *Manager {
	return &Manager{
		max:     max,
		login:	 login,
		server:  server,
		channel: channel,
		ctrl:    make(chan *control),
		event:   make(chan *event),
	}
}

// Notifications are sent to subscribers when things happen on IRC
type Notification struct {
	// A new message appeared on the channel
	Message *NotificationMessage
	// Nicks of our connections have changed
	Nickmap *map[string]string
}

// NotificationMessage is a message that happened in the connected IRC channel
type NotificationMessage struct {
	// Nick is the IRC nickname of the sender
	Nick string
	// Message is the plaintext message from IRC
	Message string
}

// Run maintains the main logic of the Manager - servicing control and event
// messages, and ensuring there is a receiver on the given channel.
func (m *Manager) Run(ctx context.Context) {
	m.conns = make(map[string]*ircconn)
	m.nickmap = make(map[string]string)
	m.shitlist = make(map[string]time.Time)
	m.subscribers = make(map[chan *Notification]bool)
	m.runctx = context.Background()

	glog.Infof("IRC Manager %s/%s running...", m.server, m.channel)

	t := time.NewTicker(1 * time.Second)

	for {
		select {
		case <-ctx.Done():
			return
		case c := <-m.ctrl:
			m.doctrl(ctx, c)
		case e := <-m.event:
			m.doevent(ctx, e)
		case <-t.C:
		}

		m.ensureReceiver(ctx)
	}
}

// ensureReceiver ensures that there is exactly one 'receiver' IRC connection,
// possibly creating a backup receiver if needed.
func (m *Manager) ensureReceiver(ctx context.Context) {
	// Ensure backup listener does not exist if there is a named connection
	active := 0
	for _, c := range m.conns {
		if !c.IsConnected() {
			continue
		}
		active += 1
	}
	if active > 1 {
		var backup *ircconn
		for _, c := range m.conns {
			if c.backup {
				backup = c
			}
		}
		if backup != nil {
			glog.Infof("Evicting backup listener")
			backup.Evict()
			delete(m.conns, backup.user)
		}
	}
	// Ensure there exists exactly one reciever
	count := 0
	for _, c := range m.conns {
		if !c.IsConnected() && !c.backup {
			c.receiver = false
			continue
		}
		if c.receiver {
			count += 1
		}
		if count >= 2 {
			c.receiver = false
		}
	}
	// No receivers? make one.
	if count == 0 {
		if len(m.conns) == 0 {
			// Noone said anything on telegram, make backup
			glog.Infof("No receiver found, making backup")
			name := m.login
			c, err := m.newconn(ctx, name, true)
			if err != nil {
				glog.Errorf("Could not make backup receiver: %v", err)
			} else {
				m.conns[name] = c
			}
		} else {
			// Make first conn a receiver
			glog.Infof("No receiver found, using conn")
			for _, v := range m.conns {
				glog.Infof("Elected %s for receiver", v.user)
				v.receiver = true
			}
		}
	}
}

package irc

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang/glog"
	irc "gopkg.in/irc.v3"
)

// ircconn is a connection to IRC as a given user.
type ircconn struct {
	// server to connect to
	server string
	// channel to join
	channel string
	// 'native' name of this connection.
	user string

	// Event Handler, usually a Manager
	eventHandler func(e *event)

	// TCP connection to IRC
	conn net.Conn
	// IRC client
	irc *irc.Client

	/// Fields used by the manager - do not access from ircconn.
	// last time this connection was used
	last time.Time
	// is primary source of IRC data
	receiver bool
	// only exists to be a receiver
	backup bool
	// iq is the IRC Queue of IRC messages, populated by the IRC client and
	// read by the connection.
	iq chan *irc.Message
	// sq is the Say Queue of controlMessages, populated by the Manager and
	// read by the connection (and passed onto IRC)
	sq chan *controlMessage
	// eq is the Evict Queue, used by the manager to signal that a connection
	// should die.
	eq chan struct{}

	// connected is a flag (via sync/atomic) that is used to signal to the
	// manager that this connection is up and healthy.
	connected int64
}

var reIRCNick = regexp.MustCompile(`[^A-Za-z0-9]`)

// Say is called by the Manager when a message should be sent out by the
// connection.
func (i *ircconn) Say(msg *controlMessage) {
	i.sq <- msg
}

// Evict is called by the Manager when a connection should die.
func (i *ircconn) Evict() {
	close(i.eq)
}

// ircMessage is a message received on IRC by a connection, sent over to the
// Manager.
type IRCMessage struct {
	conn *ircconn
	nick string
	text string
}

func NewConn(server, channel, userTelegram string, backup bool, nickPrefix string, nickSuffix string,
				h func(e *event)) (*ircconn, error) {
	// Generate IRC nick from username.
	// RFC standard - 9. Freenode allows 16 chars for nickname
	const maxIRCNick = 16
    nick := reIRCNick.ReplaceAllString(userTelegram, "")
	username := nick
    var nickLen = maxIRCNick - len(nickPrefix) - len(nickSuffix)
	if len(username) > 9 {
		username = username[:9]
	}
	nick = strings.ToLower(nick)
	if len(nick) > nickLen {
		nick = nick[:nickLen]
	}
	if len(nick) == 0 {
		glog.Errorf("Could not create IRC nick for %q", userTelegram)
		nick = "wtf"
	}
	// Add prefix and suffix at the end
	nick = nickPrefix + nick + nickSuffix

	glog.Infof("Connecting to IRC/%s/%s/%s as %s from %s...", server, channel, userTelegram, nick, username)
	conn, err := net.Dial("tcp", server)
	if err != nil {
		return nil, fmt.Errorf("Dial(_, %q): %v", server, err)
	}

	i := &ircconn{
		server:  server,
		channel: channel,
		user:    userTelegram,

		eventHandler: h,

		conn: conn,
		irc:  nil,

		last:     time.Now(),
		backup:   backup,
		receiver: backup,

		iq: make(chan *irc.Message),
		sq: make(chan *controlMessage),
		eq: make(chan struct{}),

		connected: int64(0),
	}




	// Configure IRC client to populate the IRC Queue.
	config := irc.ClientConfig{
		Nick: nick,
		User: username,
		Name: userTelegram,
		Handler: irc.HandlerFunc(func(c *irc.Client, m *irc.Message) {
			i.iq <- m
		}),
	}

	i.irc = irc.NewClient(conn, config)
	return i, nil
}

func (i *ircconn) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		i.loop(ctx)
		wg.Done()
	}()

	go func() {
		err := i.irc.RunContext(ctx)
		if err != ctx.Err() {
			glog.Errorf("IRC/%s/%s/%s exited: %v", i.server, i.channel, i.user, err)
			i.conn.Close()
			i.eventHandler(&event{
				dead: &eventDead{i},
			})
		}
		wg.Wait()
	}()

	wg.Wait()
}

// IsConnected returns whether a connection is fully alive and able to receive
// messages.
func (i *ircconn) IsConnected() bool {
	return atomic.LoadInt64(&i.connected) > 0
}

// loop is the main loop of an IRC connection.
// It synchronizes the Handler Queue, Say Queue and Evict Queue, parses
func (i *ircconn) loop(ctx context.Context) {
	sayqueue := []*controlMessage{}
	connected := false
	dead := false

	die := func(err error) {
		// drain queue of say messages...
		for _, s := range sayqueue {
			glog.Infof("IRC/%s/say: [drop] %q", i.user, s.message)
			s.done <- err
		}
		sayqueue = []*controlMessage{}
		dead = true
		i.conn.Close()
		go i.eventHandler(&event{
			dead: &eventDead{i},
		})
	}
	msg := func(s *controlMessage) {
		lines := strings.Split(s.message, "\n")
		for _, l := range lines {
			l = strings.TrimSpace(l)
			if l == "" {
				continue
			}
			err := i.irc.WriteMessage(&irc.Message{
				Command: "PRIVMSG",
				Params: []string{
					i.channel,
					l,
				},
			})
			if err != nil {
				glog.Errorf("IRC/%s: WriteMessage: %v", i.user, err)
				die(err)
				s.done <- err
				return
			}
		}
		s.done <- nil
	}

	// Timeout ticker - give up connecting to IRC after 15 seconds.
	t := time.NewTicker(time.Second * 15)

	previousNick := ""

	for {
		select {
		case <-ctx.Done():
			return

		case <-i.eq:
			glog.Infof("IRC/%s/info: got evicted", i.user)
			die(fmt.Errorf("evicted"))
			return

		case m := <-i.iq:
			if m.Command != "372" {
				glog.V(1).Infof("IRC/%s/debug: %+v", i.user, m)
			}

			glog.V(16).Infof("irc/debug16: Message: cmd(%s), channel(%s)", m.Command, m.Params[0])
			glog.V(16).Infof("irc/debug16: Current: channel(%s), command(%s)", i.channel, "PRIVMSG")
			glog.V(16).Infof("irc/debug16: Current: channel-eq(%t), command-eq(%t)", i.channel == m.Params[0], "PRIVMSG" == m.Command)
			switch {
			case m.Command == "001":
				glog.Infof("IRC/%s/info: joining %s...", i.user, i.channel)
				i.irc.Write("JOIN " + i.channel)

			case m.Command == "353":
				glog.Infof("IRC/%s/info: joined and ready", i.user)
				connected = true
				atomic.StoreInt64(&i.connected, 1)
				// drain queue of say messages...
				for _, s := range sayqueue {
					glog.Infof("IRC/%s/say: [backlog] %q", i.user, s.message)
					msg(s)
				}
				sayqueue = []*controlMessage{}

			case m.Command == "474":
				// We are banned! :(
				glog.Infof("IRC/%s/info: banned!", i.user)
				go i.eventHandler(&event{
					banned: &eventBanned{i},
				})
				die(nil)
				return

			case m.Command == "KICK" && m.Params[1] == i.irc.CurrentNick():
				glog.Infof("IRC/%s/info: got kicked", i.user)
				die(nil)
				return
			case m.Command == "PRIVMSG" && strings.ToLower(m.Params[0]) == i.channel:
				glog.V(8).Infof("IRC/%s/debug8: received message on %s", i.user, i.channel)
				go i.eventHandler(&event{
					message: &eventMessage{i, m.Prefix.Name, m.Params[1]},
				})
			}

			// update nickmap if needed
			nick := i.irc.CurrentNick()
			if previousNick != nick {
				i.eventHandler(&event{
					nick: &eventNick{i, nick},
				})
				previousNick = nick
			}

		case s := <-i.sq:
			if dead {
				glog.Infof("IRC/%s/say: [DEAD] %q", i.user, s.message)
				s.done <- fmt.Errorf("connection is dead")
			} else if connected {
				glog.Infof("IRC/%s/say: %s", i.user, s.message)
				msg(s)
			} else {
				glog.Infof("IRC/%s/say: [writeback] %q", i.user, s.message)
				sayqueue = append(sayqueue, s)
			}

		case <-t.C:
			if !connected {
				glog.Errorf("IRC/%s/info: connection timed out, dying", i.user)
				die(fmt.Errorf("connection timeout"))
				return
			}
		}
	}
}

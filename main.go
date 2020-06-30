package main

import (
	"context"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"github.com/golang/glog"

	"github.com/hakierspejs/lelelegram/irc"
)

func init() {
	flag.Set("logtostderr", "true")
}

var (
	flagTelegramToken     string
	flagTelegramChat      string
	flagTeleimgRoot       string
	flagIRCMaxConnections int
	flagIRCServer         string
	flagIRCChannel        string
	flagIRCLogin          string
    flagNickPrefix        string
    flagNickSuffix         string
)

// server is responsible for briding IRC and Telegram.
type server struct {
	// groupId is the Telegram Group ID to bridge.
	groupId int64
	tel     *tgbotapi.BotAPI
	mgr     *irc.Manager

	// backlog from telegram
	telLog chan *telegramPlain
	// backlog from IRC
	ircLog chan *irc.Notification
}

type ircFlags struct {
	prefix 		string
	suffix 		string
	defaultNick string
}

// telegramPlain is a plaintext telegram message - ie. one that's ready to send
// to IRC, possibly in mutliple lines.
type telegramPlain struct {
	// Telegram name that sent message - without '@'.
	user string
	// Plain text of message, possibly multiline.
	text string
}

func newServer(groupId int64, mgr *irc.Manager) (*server, error) {
	tel, err := tgbotapi.NewBotAPI(flagTelegramToken)
	if err != nil {
		return nil, fmt.Errorf("when creating telegram bot: %v", err)
	}

	glog.Infof("Authorized with Telegram as %q", tel.Self.UserName)

	return &server{
		groupId: groupId,
		tel:     tel,
		mgr:     mgr,

		telLog: make(chan *telegramPlain),
		ircLog: make(chan *irc.Notification),
	}, nil
}

func main() {
	flag.StringVar(&flagTelegramToken, "telegram_token", "", "Telegram Bot API Token")
	flag.StringVar(&flagTelegramChat, "telegram_chat", "", "Telegram chat/group ID to bridge. If not given, bridge will start in lame mode and allow you to find out IDs of groups which the bridge bot is part of")
	flag.StringVar(&flagTeleimgRoot, "teleimg_root", "https://teleimg.hswaw.net/fileid/", "Root URL of teleimg file serving URL")
	flag.IntVar(&flagIRCMaxConnections, "irc_max_connections", 10, "How many simulataneous connections can there be to IRC before they get recycled")
	flag.StringVar(&flagIRCServer, "irc_server", "chat.freenode.net:6667", "The address (with port) of the IRC server to connect to")
	flag.StringVar(&flagIRCChannel, "irc_channel", "", "The channel name (including hash(es)) to bridge")
	flag.StringVar(&flagIRCLogin, "irc_login", "lelegram[t]", "The login of irc user used by bot")
    flag.StringVar(&flagNickPrefix, "nick_prefix", "", "Prefix for nicks used on irc channel")
    flag.StringVar(&flagNickSuffix, "nick_suffix", "[t]", "Sufix for nicks used on irc channel")
	flag.Parse()

	if flagTelegramToken == "" {
		glog.Exitf("telegram_token must be set")
	}

	if flagIRCChannel == "" {
		glog.Exitf("irc_channel must be set")
	}
	if flagIRCLogin == "" {
		flagIRCLogin = "lelegram"
	}
	glog.Infof("dabug: Backup login in IRC: %s", flagIRCLogin)
    if (flagNickSuffix == "" && flagNickPrefix == "") {
        glog.Warning("No prefix nor suffix for nicks has been choosen. Default nick will have [t] suffix")
        flagNickSuffix = "[t]"
    }

	// Parse given group ID.
	// If not set, start server in 'lame' mode, ie. one that will not actually
	// perform any bridging, but will let you figure out the IDs of groups that
	// this bot is part of.
	var groupId int64
	if flagTelegramChat == "" {
		glog.Warningf("telegram_chat NOT GIVEN, STARTING IN LAME MODE")
		glog.Warningf("Watch for logs to find out the ID of groups which this bot is part of. Then, restart the bot with telegram_chat set.")
	} else {
		g, err := strconv.ParseInt(flagTelegramChat, 10, 64)
		if err != nil {
			glog.Exitf("telegram_chat must be a number")
		}
		groupId = g
	}


	// https://tools.ietf.org/html/rfc2812#section-1.3 "Channel names are case insensitive"
	mgr := irc.NewManager(flagIRCMaxConnections, flagIRCServer, strings.ToLower(flagIRCChannel), flagIRCLogin, flagNickPrefix, flagNickSuffix)
	glog.V(4).Infof("telegram/debug4: Linking to group: %d", groupId)
	s, err := newServer(groupId, mgr)
	if err != nil {
		glog.Exitf("newServer(): %v", err)
	}

	ctx := context.Background()

	// Start IRC manager
	go mgr.Run(ctx)

	// Start piping Telegram messages into telLog
	go s.telegramLoop(ctx)

	// Start piping IRC messages into ircLog
	mgr.Subscribe(s.ircLog)

	// Start message processing bridge (connecting telLog and ircLog)
	s.bridge(ctx)
}

// bridge connects telLog with ircLog, exchanging messages both ways and
// performing nick translation given an up-to-date nickmap.
func (s *server) bridge(ctx context.Context) {
	nickmap := make(map[string]string)
	for {
		glog.V(32).Info("bridge/debug32: New element in queue")
		select {
		case <-ctx.Done():
			return
		case m := <-s.telLog:
			// Event from Telegram (message). Translate Telegram names into IRC names.
			text := m.text
			for t, i := range nickmap {
				text = strings.ReplaceAll(text, "@"+t, i)
			}
			glog.Infof("telegram/info/%s: %v", m.user, text)

			// Attempt to route message to IRC twice.
			// This blocks until success or failure, making sure the log stays
			// totally ordered in the face of some of our IRC connections being
			// dead/slow.
			ctxT, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := s.mgr.SendMessage(ctxT, m.user, text)
			if err != nil {
				glog.Warningf("Attempting redelivery of %v after error: %v...", m, err)
				err = s.mgr.SendMessage(ctx, m.user, text)
				glog.Errorf("Redelivery of %v failed: %v...", m, err)
			}
			cancel()

		case n := <-s.ircLog:
			glog.V(4).Infof("bridge/irc/debug4: Get message from irc: %s", n.Message)
			// Notification from IRC (message or new nickmap)
			switch {
			case n.Nickmap != nil:
				// Nicks on IRC changed.
				for k, v := range *n.Nickmap {
					nickmap[k] = v
				}
				glog.Infof("New nickmap: %v", nickmap)

			case n.Message != nil:
				// New IRC message. Translate IRC names into Telegram names.
				text := n.Message.Message
				for t, i := range nickmap {
					text = strings.ReplaceAll(text, i, "@"+t)
				}
				// And send message to Telegram.
				msg := tgbotapi.NewMessage(s.groupId, fmt.Sprintf("<%s> %s", n.Message.Nick, text))
				s.tel.Send(msg)
			}
		}
	}
}

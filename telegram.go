package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"github.com/golang/glog"
)

// telegramConnection runs a long-lived connection to the Telegram API to receive
// updates and pipe resulting messages into telLog.
func (s *server) telegramConnection(ctx context.Context) error {
	u := tgbotapi.NewUpdate(0)
	// TODO(q3k): figure out what the _fuck_ does this even mean
	u.Timeout = 60

	updates, err := s.tel.GetUpdatesChan(u)
	if err != nil {
		return fmt.Errorf("GetUpdatesChan(%+v): %v", u, err)
	}
	glog.V(8).Infof("telegram/debug8: New update")

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case update, ok := <-updates:
			if !ok {
				return fmt.Errorf("Updates channel closed")
			}

			// Dispatch update.
			switch {
			case update.Message != nil:
				glog.V(4).Infof("telegram/debug4: New message: %d", update.Message.Chat.ID)
				if update.Message.Chat.ID != s.groupId {
					glog.Infof("[ignored group %d] <%s> %v", update.Message.Chat.ID, update.Message.From, update.Message.Text)
					continue
				}
				date := time.Unix(int64(update.Message.Date), 0)
				if time.Since(date) > 2*time.Minute {
					glog.Infof("[old message] <%s> %v", update.Message.From, update.Message.Text)
					continue
				}
				if msg := plainFromTelegram(s.tel.Self.ID, &update); msg != nil {
					s.telLog <- msg
				}
			}
		}
	}
}

// telegramLoop maintains a telegramConnection.
func (s *server) telegramLoop(ctx context.Context) {
	for {
		glog.V(4).Info("telegram/debug4: Starting telegram connection loop")
		err := s.telegramConnection(ctx)
		if err == ctx.Err() {
			glog.Infof("Telegram connection closing: %v", err)
			return
		}

		glog.Errorf("Telegram connection error: %v", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(1 * time.Second):
			continue
		}
	}
}

// plainFromTelegram turns a Telegram message into a plain text message.
func plainFromTelegram(selfID int, u *tgbotapi.Update) *telegramPlain {
	parts := []string{}

	from := u.Message.From
	replyto := u.Message.ReplyToMessage
	text := u.Message.Text

	// This message is in reply to someone.
	if replyto != nil && text != "" && replyto.From != nil {
		// The rendered name of the author of the quote.
		ruid := "@" + replyto.From.String()

		// First line of the quoted text.
		quotedLine := ""

		// Check if the quoted message is from our bridge.
		if replyto.From.ID == selfID {
			// Someone replied to an IRC bridge message, extract nick and line from there
			// eg: "<q3k> foo bar baz" -> ruid = q3k; quotedLine = foo bar baz
			t := replyto.Text
			if strings.HasPrefix(t, "<") {
				p := strings.SplitN(t[1:], ">", 2)
				nick := p[0]
				quoted := strings.TrimSpace(p[1])

				// ensure nick looks sane
				if len(nick) < 16 && len(strings.Fields(nick)) == 1 {
					quotedLine = strings.TrimSpace(strings.Split(quoted, "\n")[0])
					ruid = nick
				}
			}
		} else {
			// Someone replied to a native telegram message.
			quoted := strings.TrimSpace(replyto.Text)
			quotedLine = strings.TrimSpace(strings.Split(quoted, "\n")[0])
		}

		// If we have a line, quote it. Otherwise just refer to the nick without a quote.
		if quotedLine != "" {
			parts = append(parts, fmt.Sprintf("%s: >%s\n", ruid, quotedLine))
		} else {
			parts = append(parts, fmt.Sprintf("%s: ", ruid))
		}
	}

	// This message contains a sticker.
	if u.Message.Sticker != nil {
		emoji := ""
		if u.Message.Sticker.SetName != "" {
			emoji += "/" + u.Message.Sticker.SetName
		}
		if u.Message.Sticker.Emoji != "" {
			emoji += "/" + u.Message.Sticker.Emoji
		}
		parts = append(parts, fmt.Sprintf("<sticker%s>", emoji))
	}

	// Mutually exclusive stuff.
	switch {
	case u.Message.Animation != nil:
		// This message contains an animation.
		a := u.Message.Animation
		parts = append(parts, fmt.Sprintf("<uploaded animation: %s >\n", fileURL(a.FileID, "mp4")))

	case u.Message.Document != nil:
		// This message contains a document.
		d := u.Message.Document
		fnp := strings.Split(d.FileName, ".")
		ext := "bin"
		if len(fnp) > 1 {
			ext = fnp[len(fnp)-1]
		}
		parts = append(parts, fmt.Sprintf("<uploaded file: %s >\n", fileURL(d.FileID, ext)))

	case u.Message.Photo != nil:
		// This message contains a photo.
		// Multiple entries are for different file sizes, choose the highest quality one.
		hq := (*u.Message.Photo)[0]
		for _, p := range *u.Message.Photo {
			if p.FileSize > hq.FileSize {
				hq = p
			}
		}
		parts = append(parts, fmt.Sprintf("<uploaded photo: %s >\n", fileURL(hq.FileID, "jpg")))
	}

	// This message has some plain text.
	if text != "" {
		parts = append(parts, text)
	}

	// Was there anything that we extracted?
	if len(parts) > 0 {
		return &telegramPlain{from.String(), strings.Join(parts, " ")}
	}
	return nil
}

func fileURL(fid, ext string) string {
	return flagTeleimgRoot + fid + "." + ext
}

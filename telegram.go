package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"github.com/golang/glog"
)

func mergeStringSplices(stringSplice1 []string, stringSplice2 []string) []string {
	resultSplice := make([]string, len(stringSplice1)+len(stringSplice2))
	copy(resultSplice, stringSplice1)
	copy(resultSplice[len(stringSplice1):], stringSplice2)
	return resultSplice
}

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

func extractStickerToIRCText(m *tgbotapi.Message, parts []string) []string {
	// This message contains a sticker.
	if m.Sticker != nil {
		emoji := ""
		if m.Sticker.SetName != "" {
			emoji += "/" + m.Sticker.SetName
		}
		if m.Sticker.Emoji != "" {
			emoji += "/" + m.Sticker.Emoji
		}
		parts = append(parts, fmt.Sprintf("<sticker%s>", emoji))
	}
	return parts
}

func extractMediaFromMessage(m *tgbotapi.Message) []string {
	parts := []string{}
	switch {
	case m.Animation != nil:
		// This message contains an animation.
		a := m.Animation
		parts = append(parts, fmt.Sprintf("<uploaded animation: %s >\n", fileURL(a.FileID, "mp4")))

	case m.Document != nil:
		// This message contains a document.
		d := m.Document
		fnp := strings.Split(d.FileName, ".")
		ext := "bin"
		if len(fnp) > 1 {
			ext = fnp[len(fnp)-1]
		}
		parts = append(parts, fmt.Sprintf("<uploaded file: %s >\n", fileURL(d.FileID, ext)))

	case m.Photo != nil:
		// This message contains a photo.
		// Multiple entries are for different file sizes, choose the highest quality one.
		hq := (*m.Photo)[0]
		for _, p := range *m.Photo {
			if p.FileSize > hq.FileSize {
				hq = p
			}
		}
		parts = append(parts, fmt.Sprintf("<uploaded photo: %s >\n", fileURL(hq.FileID, "jpg")))
	}
	if len(m.Caption) > 0 {
		parts = append(parts, fmt.Sprintf("<caption: %s>\n", m.Caption))
	}
	return parts
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
			switch {
			case replyto.Text != "":
				quoted := strings.TrimSpace(replyto.Text)
				quotedLine = strings.TrimSpace(strings.Split(quoted, "\n")[0])
				if quotedLine != "" {
					quotedLine = quotedLine + "\n"
				}
			case replyto.ReplyToMessage != nil:
				nestedReply := replyto.ReplyToMessage
				quoted := strings.TrimSpace(nestedReply.Text)
				quotedLine = strings.TrimSpace(strings.Split(quoted, "\n")[0])
				if quotedLine != "" {
					quotedLine = quotedLine + "\n"
				}
			case replyto.Document != nil || replyto.Animation != nil || replyto.Photo != nil:
				quotedLine = extractMediaFromMessage(replyto)[0]
			case replyto.Sticker != nil:
				quotedLine = extractStickerToIRCText(replyto, []string{})[0]
			}
		}
		// If we have a line, quote it. Otherwise just refer to the nick without a quote.
		if quotedLine != "" {
			// Truncate quoted message
			if len(quotedLine) > 120 {
				quotedLine = quotedLine[:115] + "... "
			}
			parts = append(parts, fmt.Sprintf("%s: >%s", ruid, quotedLine))
		} else {
			parts = append(parts, fmt.Sprintf("%s: ", ruid))
		}
	}

	parts = extractStickerToIRCText(u.Message, parts)
	parts = mergeStringSplices(parts, extractMediaFromMessage(u.Message))
	// This message has some plain text.
	if text != "" {
		// Messages were truncated about length of 460. This length (412) is similar to what can be seen in irssi
		for len(text) > 412 {
			glog.V(16).Infof("telegram/debug16: Long message - %d", len(text))
			separatorIndex := strings.LastIndex(text[:412], " ")
			if separatorIndex == -1 {
				separatorIndex = 410
			}
			parts = append(parts, text[:separatorIndex]+"\n")
			text = text[separatorIndex+1:]
		}
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

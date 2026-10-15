// Package bot answers members in their own Telegram chat
// (docs/processes/platform/telegram-bot.md). It long-polls the bot of
// Settings → Integrations → Telegram, knows a member by the private chat
// they chose for their notifications, and runs the commands modules declare
// (module.BotCommandDeclarer) with that member's grant. A command is a
// short conversation: each reply may ask for the next message or offer
// buttons, and what comes back goes to the handler the reply named. The
// conversation lives in memory and ends after Expiry, on /cancel, with a
// new command, or on a restart.
package bot

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/auth"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/notify/telegram"
	"github.com/tikhonp/proxier/internal/platform/settings"
)

// Handler answers one step of a conversation.
type Handler func(ctx context.Context, r Request) (Reply, error)

// Command is what a member writes as /Name.
type Command struct {
	Name string // "link"
	// Area and Level are what the member's grant must reach.
	Area  access.Area
	Level access.Level
	Help  string // i18n key: one line for the command menu and /help
	// Run starts the conversation; r.Text is what follows the command.
	Run Handler
}

// Request is a member's message or tap, as the member it comes from.
type Request struct {
	Who   access.Who
	Actor string // member:<id>
	Loc   *i18n.Localizer
	Text  string // the message, trimmed; "" for a tap
}

// Reply is what the bot answers. Without Ask or Choices the conversation
// ends with it.
type Reply struct {
	Text string // plain; the bot escapes it
	// Code is shown monospaced under Text, where a tap copies it.
	Code string
	// Link is a button that opens a page.
	Link *Link
	// Choices are buttons, one per row; a tap runs its Then.
	Choices []Choice
	// Ask takes the member's next message.
	Ask Handler
}

type Link struct{ Text, URL string }

type Choice struct {
	Label string
	Then  Handler
}

// Expiry is how long a conversation waits for the member.
const Expiry = 15 * time.Minute

// Long-poll wait, and the pauses of Run.
const (
	pollWait  = 25 * time.Second
	idleWait  = 30 * time.Second
	errorWait = 10 * time.Second
)

// keepChats is how many chats Detect chat can offer.
const keepChats = 100

// Service is the bot.
type Service struct {
	Now  func() time.Time
	Log  *slog.Logger
	Wait time.Duration // the long poll's; tests set 0

	d   *db.DB
	st  *settings.Store
	a   *auth.Service
	cat *i18n.Catalog
	tg  *telegram.Channel

	commands []Command

	// mu is held for a whole poll: updates are handled one at a time, in
	// order, and the fields under it belong to the poll.
	mu     sync.Mutex
	token  string // the bot the offset and the menu are for
	offset int64
	menu   bool
	convs  map[int64]*conversation // by chat
	seq    int64
}

type conversation struct {
	id      int64
	ask     Handler
	choices []Handler
	expires time.Time
}

// New returns the bot; it calls Telegram through tg's address.
func New(d *db.DB, st *settings.Store, a *auth.Service, cat *i18n.Catalog, tg *telegram.Channel, log *slog.Logger) *Service {
	return &Service{Now: time.Now, Log: log, Wait: pollWait, d: d, st: st, a: a, cat: cat, tg: tg, convs: map[int64]*conversation{}}
}

// Add registers commands; call it before Run.
func (s *Service) Add(cmds ...Command) error {
	for _, c := range cmds {
		if c.Name == "" || c.Name == "start" || c.Name == "help" || c.Name == "cancel" || c.Run == nil || !c.Area.Valid() {
			return fmt.Errorf("bot: bad command %q", c.Name)
		}
		for _, o := range s.commands {
			if o.Name == c.Name {
				return fmt.Errorf("bot: command %q registered twice", c.Name)
			}
		}
		s.commands = append(s.commands, c)
	}
	return nil
}

var errNoToken = errors.New("bot: no token")

// Run polls until ctx ends. With no bot token it checks again now and then;
// Telegram failing is logged and tried again.
func (s *Service) Run(ctx context.Context) {
	for ctx.Err() == nil {
		wait := time.Duration(0)
		switch err := s.Poll(ctx); {
		case errors.Is(err, errNoToken):
			wait = idleWait
		case err != nil && ctx.Err() == nil:
			s.Log.Warn("bot: telegram", "error", err)
			wait = errorWait
		}
		if wait > 0 {
			t := time.NewTimer(wait)
			select {
			case <-ctx.Done():
			case <-t.C:
			}
			t.Stop()
		}
	}
}

// Poll reads the updates waiting (up to Wait for the first) and answers
// each, in order.
func (s *Service) Poll(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	token, err := s.st.Get(ctx, "telegram.bot_token")
	if err != nil {
		return err
	}
	if token == "" {
		return errNoToken
	}
	if token != s.token {
		// Update IDs and the menu are another bot's.
		s.token, s.offset, s.menu, s.convs = token, 0, false, map[int64]*conversation{}
	}
	c := s.tg.Client(token)
	if !s.menu {
		if err := s.setMenu(ctx, c); err != nil {
			s.Log.Warn("bot: setting the command menu", "error", err)
		} else {
			s.menu = true
		}
	}
	updates, err := c.Poll(ctx, s.offset, s.Wait)
	if err != nil {
		return err
	}
	for _, u := range updates {
		s.offset = u.ID + 1
		if u.Chat.ID == 0 {
			continue
		}
		if err := s.heard(ctx, u.Chat); err != nil {
			return err
		}
		if err := s.handle(ctx, c, u); err != nil {
			s.Log.Warn("bot: answering", "error", err)
		}
	}
	return nil
}

func (s *Service) setMenu(ctx context.Context, c *telegram.Client) error {
	for _, lang := range []i18n.Lang{i18n.EN, i18n.RU} {
		loc := s.cat.Localizer(lang, time.UTC)
		var cmds []telegram.BotCommand
		for _, cmd := range s.commands {
			cmds = append(cmds, telegram.BotCommand{Command: cmd.Name, Description: loc.T(cmd.Help)})
		}
		cmds = append(cmds, telegram.BotCommand{Command: "cancel", Description: loc.T("telegram.bot.cancel.help")})
		code := string(lang)
		if lang == i18n.EN {
			code = "" // every language but Russian
		}
		if err := c.SetCommands(ctx, code, cmds); err != nil {
			return err
		}
	}
	return nil
}

// Chat is a chat the bot heard from.
type Chat struct {
	ID          int64
	Type, Title string
	Username    string // a public chat's @name, without the @
	HeardAt     db.Time
}

// heard keeps the chat for Detect chat: bookkeeping, not an event.
func (s *Service) heard(ctx context.Context, c telegram.Chat) error {
	title := c.Title
	if r := []rune(title); len(r) > 256 {
		title = string(r[:256])
	}
	if len(c.Username) > 64 {
		c.Username = ""
	}
	return s.d.Write(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO telegram_chats (chat_id, type, title, username, heard_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (chat_id) DO UPDATE SET type = excluded.type, title = excluded.title, username = excluded.username, heard_at = excluded.heard_at`,
			c.ID, c.Type, title, c.Username, db.At(s.Now()))
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM telegram_chats WHERE chat_id NOT IN (SELECT chat_id FROM telegram_chats ORDER BY heard_at DESC, chat_id LIMIT ?)`, keepChats)
		return err
	})
}

// Chats lists the chats the bot heard from, newest first.
func (s *Service) Chats(ctx context.Context) ([]Chat, error) {
	var rows []struct {
		ID       int64   `db:"chat_id"`
		Type     string  `db:"type"`
		Title    string  `db:"title"`
		Username string  `db:"username"`
		HeardAt  db.Time `db:"heard_at"`
	}
	if err := s.d.R.SelectContext(ctx, &rows, `SELECT chat_id, type, title, username, heard_at FROM telegram_chats ORDER BY heard_at DESC, chat_id`); err != nil {
		return nil, fmt.Errorf("bot: chats: %w", err)
	}
	out := make([]Chat, len(rows))
	for i, r := range rows {
		out[i] = Chat{ID: r.ID, Type: r.Type, Title: r.Title, Username: r.Username, HeardAt: r.HeardAt}
	}
	return out, nil
}

// member is the member whose chat this is: in, set up, and with this
// private chat as their notification chat.
func (s *Service) member(ctx context.Context, chat int64) (auth.RosterEntry, bool, error) {
	roster, err := s.a.Roster(ctx)
	if err != nil {
		return auth.RosterEntry{}, false, err
	}
	id := strconv.FormatInt(chat, 10)
	for _, m := range roster {
		if m.TelegramChatID == id && !m.Removed() && m.SetUp() {
			return m, true, nil
		}
	}
	return auth.RosterEntry{}, false, nil
}

func (s *Service) zone(ctx context.Context) *time.Location {
	if name, err := s.st.Get(ctx, "general.time_zone"); err == nil {
		if tz, err := time.LoadLocation(name); err == nil {
			return tz
		}
	}
	return time.UTC
}

// handle answers one update. Only a private chat is answered: in a group,
// whoever writes isn't the member the chat belongs to.
func (s *Service) handle(ctx context.Context, c *telegram.Client, u telegram.Incoming) error {
	if u.Chat.Type != "private" || (u.Text == "" && u.Callback == "") {
		return nil
	}
	chat := strconv.FormatInt(u.Chat.ID, 10)
	m, ok, err := s.member(ctx, u.Chat.ID)
	if err != nil {
		return err
	}
	if !ok {
		delete(s.convs, u.Chat.ID)
		lang := i18n.EN
		if strings.HasPrefix(u.Lang, "ru") {
			lang = i18n.RU
		}
		loc := s.cat.Localizer(lang, time.UTC)
		if u.Callback != "" {
			return c.AnswerCallback(ctx, u.Callback, loc.T("telegram.bot.unknown_chat"))
		}
		return s.send(ctx, c, chat, 0, Reply{Text: loc.T("telegram.bot.unknown_chat")})
	}
	r := Request{Who: access.Who{Member: m.ID, Grant: m.Grant}, Actor: events.MemberActor(m.ID),
		Loc: s.cat.Localizer(m.Language, s.zone(ctx)), Text: strings.TrimSpace(u.Text)}

	if u.Callback != "" {
		next, ok := s.choice(u.Chat.ID, u.Data)
		if !ok {
			return c.AnswerCallback(ctx, u.Callback, r.Loc.T("telegram.bot.expired"))
		}
		if err := c.AnswerCallback(ctx, u.Callback, ""); err != nil {
			return err
		}
		return s.run(ctx, c, chat, u.MessageID, u.Chat.ID, r, next)
	}

	name, args, isCommand := command(r.Text)
	if !isCommand {
		conv := s.live(u.Chat.ID)
		if conv == nil || conv.ask == nil {
			return s.send(ctx, c, chat, 0, s.help(r))
		}
		return s.run(ctx, c, chat, 0, u.Chat.ID, r, conv.ask)
	}
	switch name {
	case "start", "help":
		delete(s.convs, u.Chat.ID)
		return s.send(ctx, c, chat, 0, s.help(r))
	case "cancel":
		delete(s.convs, u.Chat.ID)
		return s.send(ctx, c, chat, 0, Reply{Text: r.Loc.T("telegram.bot.cancelled")})
	}
	for _, cmd := range s.commands {
		if cmd.Name != name {
			continue
		}
		delete(s.convs, u.Chat.ID)
		if !m.Grant.Can(cmd.Area, cmd.Level) {
			return s.send(ctx, c, chat, 0, Reply{Text: r.Loc.T("telegram.bot.forbidden")})
		}
		r.Text = args
		return s.run(ctx, c, chat, 0, u.Chat.ID, r, cmd.Run)
	}
	delete(s.convs, u.Chat.ID)
	return s.send(ctx, c, chat, 0, s.help(r))
}

// command splits "/link@proxier_bot Mom" into "link" and "Mom".
func command(text string) (name, args string, ok bool) {
	if !strings.HasPrefix(text, "/") {
		return "", "", false
	}
	head, args, _ := strings.Cut(text[1:], " ")
	name, _, _ = strings.Cut(head, "@")
	return strings.ToLower(name), strings.TrimSpace(args), true
}

// live is the chat's conversation, unless it has expired.
func (s *Service) live(chat int64) *conversation {
	conv := s.convs[chat]
	if conv != nil && !s.Now().Before(conv.expires) {
		delete(s.convs, chat)
		return nil
	}
	return conv
}

// choice finds what a button's data ("c<conversation>.<choice>") runs. A
// button of an earlier or expired conversation runs nothing.
func (s *Service) choice(chat int64, data string) (Handler, bool) {
	convPart, choicePart, ok := strings.Cut(strings.TrimPrefix(data, "c"), ".")
	id, err1 := strconv.ParseInt(convPart, 10, 64)
	i, err2 := strconv.Atoi(choicePart)
	conv := s.live(chat)
	if !ok || err1 != nil || err2 != nil || conv == nil || conv.id != id || i < 0 || i >= len(conv.choices) {
		return nil, false
	}
	return conv.choices[i], true
}

// run runs a step and sends its reply: as an edit of the message whose
// button was tapped, or as a new message. A failing step ends the
// conversation with an apology; its error is logged.
func (s *Service) run(ctx context.Context, c *telegram.Client, chat string, edit, chatID int64, r Request, h Handler) error {
	reply, err := h(ctx, r)
	if err != nil {
		s.Log.Error("bot: command failed", "member", r.Who.Member, "error", err)
		reply = Reply{Text: r.Loc.T("telegram.bot.failed")}
	}
	delete(s.convs, chatID)
	if reply.Ask != nil || len(reply.Choices) > 0 {
		s.seq++
		conv := &conversation{id: s.seq, ask: reply.Ask, expires: s.Now().Add(Expiry)}
		for _, ch := range reply.Choices {
			conv.choices = append(conv.choices, ch.Then)
		}
		s.convs[chatID] = conv
	}
	return s.send(ctx, c, chat, edit, reply)
}

// send writes a reply as Telegram HTML. The text may hold a link's URL, so
// nothing here logs it.
func (s *Service) send(ctx context.Context, c *telegram.Client, chat string, edit int64, reply Reply) error {
	text := html.EscapeString(reply.Text)
	if reply.Code != "" {
		text += "\n\n<code>" + html.EscapeString(reply.Code) + "</code>"
	}
	var buttons [][]telegram.Button
	for i, ch := range reply.Choices {
		buttons = append(buttons, []telegram.Button{{Text: ch.Label, Data: fmt.Sprintf("c%d.%d", s.seq, i)}})
	}
	if reply.Link != nil {
		buttons = append(buttons, []telegram.Button{{Text: reply.Link.Text, URL: reply.Link.URL}})
	}
	if edit != 0 {
		return c.EditMessage(ctx, chat, edit, text, buttons)
	}
	return c.SendMessage(ctx, chat, text, buttons)
}

// help lists the commands the member's grant reaches.
func (s *Service) help(r Request) Reply {
	var lines []string
	for _, cmd := range s.commands {
		if r.Who.Grant.Can(cmd.Area, cmd.Level) {
			lines = append(lines, "/"+cmd.Name+" — "+r.Loc.T(cmd.Help))
		}
	}
	if len(lines) == 0 {
		return Reply{Text: r.Loc.T("telegram.bot.help.none")}
	}
	lines = append(lines, "/cancel — "+r.Loc.T("telegram.bot.cancel.help"))
	return Reply{Text: r.Loc.T("telegram.bot.help") + "\n\n" + strings.Join(lines, "\n")}
}

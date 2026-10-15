package subscriptions

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/links"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/store"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/subs"
	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/bot"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

// BotCommands: /link makes a link from the member's own Telegram chat
// (docs/processes/subscriptions/link-lifecycle.md#steps-from-telegram): a
// name, then a subscription, then the URL.
func (m *Module) BotCommands() []bot.Command {
	return []bot.Command{{Name: "link", Area: access.Subscriptions, Level: access.LevelOwn, Help: "links.bot.help", Run: m.botLink}}
}

func (m *Module) botLink(ctx context.Context, r bot.Request) (bot.Reply, error) {
	if r.Text != "" {
		return m.botPick(ctx, r)
	}
	rows, err := m.botSubscriptions(ctx, r.Who)
	if err != nil || len(rows) == 0 {
		return botNoSubscriptions(r.Loc), err
	}
	return bot.Reply{Text: r.Loc.T("links.bot.ask_name"), Ask: m.botPick}, nil
}

// botPick takes the name and offers the subscriptions the member may
// change. The name is checked when the link is made.
func (m *Module) botPick(ctx context.Context, r bot.Request) (bot.Reply, error) {
	name := r.Text
	rows, err := m.botSubscriptions(ctx, r.Who)
	if err != nil || len(rows) == 0 {
		return botNoSubscriptions(r.Loc), err
	}
	reply := bot.Reply{Text: r.Loc.T("links.bot.pick", i18n.Args{"name": name})}
	for _, s := range rows {
		reply.Choices = append(reply.Choices, bot.Choice{
			Label: s.Name + " · " + r.Loc.N("subs.n_servers", int64(len(s.Members))),
			Then:  m.botCreate(name, s.ID, s.Name),
		})
	}
	return reply, nil
}

func (m *Module) botSubscriptions(ctx context.Context, w access.Who) ([]subs.Row, error) {
	rows, err := m.Subs.List(ctx, w.Scope(access.Subscriptions).Writable())
	sort.SliceStable(rows, func(a, b int) bool { return strings.ToLower(rows[a].Name) < strings.ToLower(rows[b].Name) })
	return rows, err
}

func botNoSubscriptions(loc *i18n.Localizer) bot.Reply {
	return bot.Reply{Text: loc.T("links.bot.no_subs")}
}

// botCreate makes the link and answers with its URL. A name the link can't
// have asks for another, keeping the subscription; a subscription gone or
// no longer the member's to use offers the choice again.
func (m *Module) botCreate(name string, subID int64, subName string) bot.Handler {
	return func(ctx context.Context, r bot.Request) (bot.Reply, error) {
		var id int64
		var token string
		err := m.deps.DB.Write(ctx, func(tx *sqlx.Tx) error {
			var err error
			id, token, err = m.Links.CreateTx(ctx, tx, r.Who, links.New{Name: name, SubscriptionID: subID, Lang: m.Links.DefaultLang(ctx)}, r.Actor)
			return err
		})
		var fe store.FieldErrors
		if errors.As(err, &fe) {
			if key, ok := fe["name"]; ok {
				return bot.Reply{Text: r.Loc.T(key) + " " + r.Loc.T("links.bot.ask_again"), Ask: func(ctx context.Context, r bot.Request) (bot.Reply, error) {
					return m.botCreate(r.Text, subID, subName)(ctx, r)
				}}, nil
			}
			r.Text = name
			reply, err := m.botPick(ctx, r)
			reply.Text = r.Loc.T("links.bot.sub_gone") + " " + reply.Text
			return reply, err
		}
		if err != nil {
			return bot.Reply{}, err
		}
		page := strings.TrimSuffix(m.deps.Cfg.BaseURL.String(), "/") + "/links/" + strconv.FormatInt(id, 10)
		return bot.Reply{
			Text: r.Loc.T("links.bot.created", i18n.Args{"name": strings.TrimSpace(name), "subscription": subName}),
			Code: m.Links.URL(token),
			Link: &bot.Link{Text: r.Loc.T("links.bot.open"), URL: page},
		}, nil
	}
}

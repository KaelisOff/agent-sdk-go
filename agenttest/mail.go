package agenttest

import (
	"encoding/json"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KaelisOff/agent-sdk-go/agent"
	"github.com/KaelisOff/agent-sdk-go/manifest"
)

// Email limits of a box (its mail service).
const (
	MailWindowStart = 8  // hour, box time
	MailWindowEnd   = 19 // hour, excluded; and never on Sunday
	MaxMailSubject  = 150
	MaxMailBody     = 5000
	// DefaultMailLimit is a new mailbox's daily limit: the box starts at
	// 20 emails a day and doubles every week, up to the owner's setting.
	DefaultMailLimit = 20
)

// SentMail is an email the agent sent (the box adds its footer: not
// shown here).
type SentMail struct {
	agent.Mail
	ID     int64
	SentAt time.Time
}

type mailState struct {
	sent      []SentMail
	contacted map[string]bool
	optOuts   map[string]bool
	replies   []agent.Reply
}

func (b *Box) mail() *mailState {
	if b.mailState == nil {
		b.mailState = &mailState{contacted: map[string]bool{}, optOuts: map[string]bool{}}
	}
	return b.mailState
}

// SentMail returns the emails the agent sent, in order.
func (b *Box) SentMail() []SentMail { return append([]SentMail(nil), b.mail().sent...) }

// OptOut blocks an address, as if the person had asked the owner.
func (b *Box) OptOut(address string) { b.mail().optOuts[strings.ToLower(address)] = true }

// Reply simulates a reply to the email sent to address; the agent gets it
// from Replies.
func (b *Box) Reply(address, text string) { b.addReply(address, "reply", text) }

// AutoReply simulates an automatic reply (out of office…).
func (b *Box) AutoReply(address, text string) { b.addReply(address, "auto", text) }

// Unsubscribe simulates a "STOP": the address is blocked for good.
func (b *Box) Unsubscribe(address string) {
	b.addReply(address, "unsubscribe", "")
	b.OptOut(address)
}

// Bounce simulates a delivery failure: the address is blocked for good.
func (b *Box) Bounce(address string) {
	b.addReply(address, "bounce", "")
	b.OptOut(address)
}

func (b *Box) addReply(address, kind, text string) {
	m := b.mail()
	address = strings.ToLower(address)
	for _, s := range m.sent {
		if strings.EqualFold(s.To, address) {
			r := agent.Reply{
				ID: int64(len(m.replies) + 1), Kind: kind, To: address, Text: text,
				Campaign: s.Campaign, Variant: s.Variant, SentAt: s.SentAt, ReceivedAt: s.SentAt.Add(time.Hour),
			}
			if kind == "reply" || kind == "auto" || kind == "unsubscribe" {
				r.From, r.Subject = address, "Re: "+s.Subject
			}
			m.replies = append(m.replies, r)
			return
		}
	}
	panic("agenttest: no email was sent to " + address)
}

var (
	tagRe       = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)
	roleAddress = map[string]bool{"abuse": true, "postmaster": true, "mailer-daemon": true, "noreply": true, "no-reply": true,
		"donotreply": true, "do-not-reply": true, "bounce": true, "bounces": true, "hostmaster": true, "webmaster": true}
)

func (b *Box) mailSend(raw json.RawMessage) (any, *callError) {
	if err := b.need(manifest.CapMailSend); err != nil {
		return nil, err
	}
	if b.NoMailbox {
		return nil, fail(agent.CodeNotConfigured, "no mailbox is configured on this box")
	}
	var p agent.Mail
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	for _, tag := range []string{p.Campaign, p.Variant} {
		if tag != "" && !tagRe.MatchString(tag) {
			return nil, fail(agent.CodeInvalid, "campaign and variant: up to 40 characters among a-z, 0-9, - and _")
		}
	}
	a, err := mail.ParseAddress(strings.TrimSpace(p.To))
	if err != nil || a.Name != "" || a.Address != strings.TrimSpace(p.To) || strings.ContainsAny(p.To, "\r\n<>") {
		return nil, fail(agent.CodeInvalid, "to: not a plain email address")
	}
	to := strings.ToLower(a.Address)
	local, _, _ := strings.Cut(to, "@")
	local, _, _ = strings.Cut(local, "+")
	if roleAddress[local] {
		return nil, fail(agent.CodeInvalid, "to: this address must not receive prospecting")
	}
	subject := strings.TrimSpace(p.Subject)
	if n := utf8.RuneCountInString(subject); n == 0 || n > MaxMailSubject || strings.ContainsAny(subject, "\r\n\t") {
		return nil, fail(agent.CodeInvalid, "subject: 1 to %d characters on one line", MaxMailSubject)
	}
	body := strings.TrimSpace(strings.ReplaceAll(p.Body, "\r\n", "\n"))
	if n := utf8.RuneCountInString(body); n == 0 || n > MaxMailBody {
		return nil, fail(agent.CodeInvalid, "body: 1 to %d characters", MaxMailBody)
	}
	m := b.mail()
	now := b.runNow
	switch {
	case b.MailPaused:
		return nil, fail(agent.CodePaused, "sending is paused until the owner resumes it")
	case now.Weekday() == time.Sunday || now.Hour() < MailWindowStart || now.Hour() >= MailWindowEnd:
		return nil, fail(agent.CodeOutsideHours, "emails are sent Monday to Saturday, 8:00 to 19:00")
	case m.optOuts[to]:
		return nil, fail(agent.CodeOptedOut, "this address asked not to be contacted, or does not exist")
	case m.contacted[to]:
		return nil, fail(agent.CodeAlreadyContacted, "this address was already sent an email: one per address, ever")
	}
	today := b.sentOn(now)
	limit := b.MailLimit
	if limit == 0 {
		limit = DefaultMailLimit
	}
	if today >= limit || today >= b.Manifest.Permissions.MailSend {
		return nil, fail(agent.CodeQuota, "daily limit reached")
	}
	p.To, p.Subject, p.Body = to, subject, body
	m.contacted[to] = true
	m.sent = append(m.sent, SentMail{Mail: p, ID: int64(len(m.sent) + 1), SentAt: now})
	return agent.MailSent{ID: int64(len(m.sent)), Remaining: min(limit, b.Manifest.Permissions.MailSend) - today - 1}, nil
}

func (b *Box) sentOn(day time.Time) int {
	n := 0
	y, mo, d := day.Date()
	for _, s := range b.mail().sent {
		if sy, sm, sd := s.SentAt.Date(); sy == y && sm == mo && sd == d {
			n++
		}
	}
	return n
}

func (b *Box) mailReplies(raw json.RawMessage) (any, *callError) {
	if err := b.need(manifest.CapMailRead); err != nil {
		return nil, err
	}
	var p struct {
		After int64 `json:"after"`
		Limit int   `json:"limit"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	if p.Limit < 1 || p.Limit > 100 {
		p.Limit = 100
	}
	out := []agent.Reply{}
	next := p.After
	for _, r := range b.mail().replies {
		if r.ID > p.After && len(out) < p.Limit {
			out = append(out, r)
			next = r.ID
		}
	}
	return map[string]any{"replies": out, "next": next}, nil
}

func (b *Box) mailStatus() (any, *callError) {
	perms := b.Manifest.Permissions
	if !perms.Has(manifest.CapMailSend) && !perms.Has(manifest.CapMailRead) {
		return nil, fail(agent.CodePermission, "capability mail.send or mail.read was not granted to this agent")
	}
	limit := b.MailLimit
	if limit == 0 {
		limit = DefaultMailLimit
	}
	now := b.runNow
	open := now.Weekday() != time.Sunday && now.Hour() >= MailWindowStart && now.Hour() < MailWindowEnd
	st := agent.MailStatus{
		Configured: !b.NoMailbox, Paused: b.MailPaused, WindowOpen: open,
		SentToday: b.sentOn(now), LimitToday: limit, AgentSentToday: b.sentOn(now), AgentLimit: perms.MailSend,
		Stats: []agent.MailStat{},
	}
	// Each email counts once, under its most telling outcome.
	rank := map[string]int{"auto": 1, "reply": 2, "unsubscribe": 3, "bounce": 4}
	outcome := map[string]string{}
	for _, r := range b.mail().replies {
		if rank[r.Kind] > rank[outcome[r.To]] {
			outcome[r.To] = r.Kind
		}
	}
	index := map[[2]string]int{}
	for _, s := range b.mail().sent {
		k := [2]string{s.Campaign, s.Variant}
		i, ok := index[k]
		if !ok {
			i = len(st.Stats)
			index[k] = i
			st.Stats = append(st.Stats, agent.MailStat{Campaign: s.Campaign, Variant: s.Variant})
		}
		x := &st.Stats[i]
		x.Sent++
		switch outcome[s.To] {
		case "reply":
			x.Replied++
		case "auto":
			x.AutoReplies++
		case "unsubscribe":
			x.Unsubscribed++
		case "bounce":
			x.Bounced++
		}
	}
	return st, nil
}

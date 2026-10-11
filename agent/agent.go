// Package agent is the Go SDK for Kaelis agents.
//
// A Kaelis agent is a small program that runs on a Kaelis box, in a
// container without network access. It talks to the box only through its
// standard input and output, and acts only through the capabilities its
// manifest asks for and the box's owner accepted: the model, a private
// storage, notifications, requests for approval… The box enforces the
// guardrails; an agent cannot go around them.
//
// A minimal agent:
//
//	func main() {
//		agent.Main(func(box *agent.Box, run agent.Run) (string, error) {
//			text, err := box.Chat(agent.User("Donne-moi un conseil du jour."))
//			if err != nil {
//				return "", err
//			}
//			return "conseil envoyé", box.Notify("Conseil du jour", text)
//		})
//	}
//
// Protocol (version 1): JSON objects, one per line. The box writes
// {"type":"start","run":{…}} first; the agent sends
// {"type":"call","id":N,"method":…,"params":…} and reads
// {"type":"result","id":N,"result":…} or {"type":"error","id":N,"error":…};
// it may end with {"type":"done","summary":…}. Standard error is a free
// log the owner can read. The full description is in docs/protocole.md.
//
// To test an agent without a box, see package agenttest.
package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// Run tells the agent why and when it runs.
type Run struct {
	ID       string    `json:"id"`
	Trigger  Trigger   `json:"trigger"`
	AgentID  string    `json:"agent_id"`
	Version  string    `json:"version"`
	Now      time.Time `json:"now"`
	TimeZone string    `json:"time_zone"`
	Locale   string    `json:"locale"`
}

// Trigger kinds.
const (
	TriggerSchedule = "schedule" // one of the manifest's schedules
	TriggerManual   = "manual"   // started by the owner or an admin
	TriggerApproval = "approval" // a request for approval was answered (or expired)
)

// Trigger is the reason of a run.
type Trigger struct {
	Kind     string    `json:"kind"`
	Schedule string    `json:"schedule,omitempty"`
	Approval *Approval `json:"approval,omitempty"`
}

// Approval is the answer to a request for approval.
type Approval struct {
	ID       string          `json:"id"`
	Approved bool            `json:"approved"`
	Expired  bool            `json:"expired,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
}

// Error is an error returned by the box for a call.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Error codes.
const (
	CodePermission = "permission_denied"
	CodeInvalid    = "invalid_params"
	CodeQuota      = "quota_exceeded" // also: today's email limit is reached
	CodeUnknown    = "unknown_method"
	CodeTooMany    = "too_many_calls"
	CodeFailed     = "failed"

	// Refusals of the box's email guardrails (SendMail).
	CodeNotConfigured    = "not_configured"    // no mailbox on this box
	CodeAlreadyContacted = "already_contacted" // one email per address, ever
	CodeOptedOut         = "opted_out"         // the person said STOP, or the address bounced
	CodeOutsideHours     = "outside_hours"     // Monday to Saturday, 8:00 to 19:00 only
	CodePaused           = "paused"            // too many bounces or opt-outs: the owner must resume
	CodeBounced          = "bounced"           // the server refused this address
)

// IsCode reports whether err is a box error with this code.
func IsCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// Box is the agent's connection to the box.
type Box struct {
	in     *bufio.Reader
	out    io.Writer
	log    io.Writer
	nextID int64
}

// NewBox connects through the given streams (os.Stdin/os.Stdout in an
// agent; pipes in tests).
func NewBox(in io.Reader, out, log io.Writer) *Box {
	return &Box{in: bufio.NewReaderSize(in, 64<<10), out: out, log: log}
}

// Start reads the run description.
func (b *Box) Start() (Run, error) {
	line, err := b.in.ReadBytes('\n')
	if err != nil {
		return Run{}, fmt.Errorf("agent: no start message: %w", err)
	}
	var m struct {
		Type     string `json:"type"`
		Protocol int    `json:"protocol"`
		Run      Run    `json:"run"`
	}
	if err := json.Unmarshal(line, &m); err != nil || m.Type != "start" {
		return Run{}, errors.New("agent: bad start message")
	}
	if m.Protocol != 1 {
		return Run{}, fmt.Errorf("agent: unsupported protocol %d", m.Protocol)
	}
	return m.Run, nil
}

// Call invokes a capability and decodes its result into out (may be nil).
func (b *Box) Call(method string, params, out any) error {
	b.nextID++
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	msg, _ := json.Marshal(map[string]any{"type": "call", "id": b.nextID, "method": method, "params": json.RawMessage(raw)})
	if _, err := fmt.Fprintf(b.out, "%s\n", msg); err != nil {
		return err
	}
	line, err := b.in.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("agent: connection to the box lost: %w", err)
	}
	var resp struct {
		Type   string          `json:"type"`
		ID     int64           `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *Error          `json:"error"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		return fmt.Errorf("agent: bad answer: %w", err)
	}
	if resp.ID != b.nextID {
		return fmt.Errorf("agent: answer %d for call %d", resp.ID, b.nextID)
	}
	if resp.Error != nil {
		return resp.Error
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(resp.Result, out)
}

// Logf writes to the run's log (visible to the owner and admins).
func (b *Box) Logf(format string, args ...any) {
	fmt.Fprintf(b.log, format+"\n", args...)
}

// Done reports the run's summary.
func (b *Box) Done(summary string) error {
	msg, _ := json.Marshal(map[string]string{"type": "done", "summary": summary})
	_, err := fmt.Fprintf(b.out, "%s\n", msg)
	return err
}

// --- capabilities ---------------------------------------------------------------------

// Message is a chat message for the model.
type Message struct {
	Role    string `json:"role"` // system, user, assistant
	Content string `json:"content"`
}

// System, User and Assistant build messages.
func System(s string) Message    { return Message{"system", s} }
func User(s string) Message      { return Message{"user", s} }
func Assistant(s string) Message { return Message{"assistant", s} }

// Chat asks the box's model (capability "model") and returns its answer.
func (b *Box) Chat(messages ...Message) (string, error) {
	return b.ChatMax(512, messages...)
}

// ChatMax is Chat with a limit on the answer's length, in tokens.
func (b *Box) ChatMax(maxTokens int, messages ...Message) (string, error) {
	var r struct {
		Text string `json:"text"`
	}
	err := b.Call("model.chat", map[string]any{"messages": messages, "max_tokens": maxTokens}, &r)
	return r.Text, err
}

// Get reads a value of the agent's storage (capability "storage"); ok is
// false when the key is absent.
func (b *Box) Get(key string) (value string, ok bool, err error) {
	var r struct {
		Value *string `json:"value"`
	}
	if err := b.Call("storage.get", map[string]string{"key": key}, &r); err != nil {
		return "", false, err
	}
	if r.Value == nil {
		return "", false, nil
	}
	return *r.Value, true, nil
}

// Set writes a value (the storage size is limited by the manifest).
func (b *Box) Set(key, value string) error {
	return b.Call("storage.set", map[string]string{"key": key, "value": value}, nil)
}

// GetJSON and SetJSON store JSON values.
func (b *Box) GetJSON(key string, v any) (bool, error) {
	s, ok, err := b.Get(key)
	if err != nil || !ok {
		return ok, err
	}
	return true, json.Unmarshal([]byte(s), v)
}

// SetJSON stores v as JSON.
func (b *Box) SetJSON(key string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return b.Set(key, string(data))
}

// Delete removes a key.
func (b *Box) Delete(key string) error {
	return b.Call("storage.delete", map[string]string{"key": key}, nil)
}

// Keys lists the keys starting with prefix.
func (b *Box) Keys(prefix string) ([]string, error) {
	var r struct {
		Keys []string `json:"keys"`
	}
	err := b.Call("storage.list", map[string]string{"prefix": prefix}, &r)
	return r.Keys, err
}

// Notify sends a notification to the box's owner and admins (capability
// "notify").
func (b *Box) Notify(title, body string) error {
	return b.Call("notify", map[string]string{"title": title, "body": body}, nil)
}

// RequestApproval asks the owner or an admin (capability "approval").
// The answer comes in a later run, whose trigger is "approval" and carries
// data back. No answer before expiry (in hours, 72 by default) means no.
func (b *Box) RequestApproval(title, details string, data any, expiresHours int) (string, error) {
	params := map[string]any{"title": title, "details": details, "expires_hours": expiresHours}
	if data != nil {
		params["data"] = data
	}
	var r struct {
		ID string `json:"id"`
	}
	err := b.Call("approval.request", params, &r)
	return r.ID, err
}

// Mail is a prospecting email (capability mail.send). The box adds the
// sender's identity, the legal notice and how to opt out; it sends plain
// text only.
type Mail struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	// Campaign and Variant group emails in the statistics (MailStatus),
	// e.g. for A/B tests: up to 40 characters among a-z, 0-9, - and _.
	Campaign string `json:"campaign,omitempty"`
	Variant  string `json:"variant,omitempty"`
}

// MailSent describes a sent email.
type MailSent struct {
	ID int64 `json:"id"`
	// Remaining is how many more emails the agent may send today.
	Remaining int `json:"remaining"`
}

// SendMail sends a prospecting email. The box's guardrails apply whatever
// the agent does: one email per address, ever; never to someone who said
// STOP or whose address bounced; Monday to Saturday, 8:00 to 19:00; daily
// limits; automatic pause. A refusal is an *Error with one of the Code…
// constants (see IsCode).
func (b *Box) SendMail(m Mail) (MailSent, error) {
	var r MailSent
	err := b.Call("mail.send", m, &r)
	return r, err
}

// Reply is what came back for one of the agent's emails.
type Reply struct {
	ID int64 `json:"id"`
	// Kind is "reply", "auto" (automatic reply, e.g. out of office),
	// "unsubscribe" (the person opted out: the box already blocked the
	// address) or "bounce" (the address does not exist).
	Kind       string    `json:"kind"`
	To         string    `json:"to"` // the address the agent wrote to
	From       string    `json:"from,omitempty"`
	Subject    string    `json:"subject,omitempty"`
	Text       string    `json:"text,omitempty"` // what the person wrote, without the quoted email
	Campaign   string    `json:"campaign,omitempty"`
	Variant    string    `json:"variant,omitempty"`
	SentAt     time.Time `json:"sent_at"`
	ReceivedAt time.Time `json:"received_at"`
}

// Replies returns what came back for the agent's emails after the cursor
// after (0 the first time; keep next in storage), at most limit (100).
// Capability mail.read. The box reads the replies every few minutes.
func (b *Box) Replies(after int64, limit int) (replies []Reply, next int64, err error) {
	var r struct {
		Replies []Reply `json:"replies"`
		Next    int64   `json:"next"`
	}
	err = b.Call("mail.replies", map[string]any{"after": after, "limit": limit}, &r)
	return r.Replies, r.Next, err
}

// MailStat counts what came of the agent's emails, by campaign and
// variant (opens are not measured: no tracking).
type MailStat struct {
	Campaign     string `json:"campaign"`
	Variant      string `json:"variant"`
	Sent         int    `json:"sent"`
	Replied      int    `json:"replied"`
	AutoReplies  int    `json:"auto_replies"`
	Unsubscribed int    `json:"unsubscribed"`
	Bounced      int    `json:"bounced"`
}

// MailStatus is the state of the box's email, for the agent.
type MailStatus struct {
	Configured     bool       `json:"configured"`
	Paused         bool       `json:"paused"`
	WindowOpen     bool       `json:"window_open"`
	NextWindow     *time.Time `json:"next_window,omitempty"`
	SentToday      int        `json:"sent_today"`  // the box's, all agents
	LimitToday     int        `json:"limit_today"` // the box's
	AgentSentToday int        `json:"agent_sent_today"`
	AgentLimit     int        `json:"agent_limit"` // the manifest's mail_send
	Stats          []MailStat `json:"stats"`
}

// MailStatus returns the state of the box's email and the agent's
// figures (capability mail.send or mail.read).
func (b *Box) MailStatus() (MailStatus, error) {
	var r MailStatus
	err := b.Call("mail.status", map[string]any{}, &r)
	return r, err
}

// --- entry point ---------------------------------------------------------------------

// Handler does the work of a run and returns its summary.
type Handler func(box *Box, run Run) (summary string, err error)

// Main runs handler once over stdin/stdout and exits: 0 on success, 1 on
// error (the error goes to the log).
func Main(handler Handler) {
	box := NewBox(os.Stdin, os.Stdout, os.Stderr)
	os.Exit(Serve(box, handler))
}

// Serve is Main without exiting (tests).
func Serve(box *Box, handler Handler) int {
	run, err := box.Start()
	if err != nil {
		box.Logf("%v", err)
		return 2
	}
	summary, err := handler(box, run)
	if err != nil {
		box.Logf("error: %v", err)
		if summary != "" {
			_ = box.Done(summary)
		}
		return 1
	}
	_ = box.Done(summary)
	return 0
}

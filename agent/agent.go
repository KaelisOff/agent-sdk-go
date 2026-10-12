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
	CodeNotFound   = "not_found" // no such automation

	// Refusals of the box's email guardrails (SendMail).
	CodeNotConfigured    = "not_configured"    // no mailbox on this box
	CodeAlreadyContacted = "already_contacted" // one email per address, ever
	CodeOptedOut         = "opted_out"         // the person said STOP, or the address bounced
	CodeOutsideHours     = "outside_hours"     // Monday to Saturday, 8:00 to 19:00 only
	CodePaused           = "paused"            // too many bounces or opt-outs: the owner must resume
	CodeBounced          = "bounced"           // the server refused this address

	// ReadPage: the address is not a readable web page (missing, PDF…).
	CodeNotAPage = "not_a_page"

	// The plugin behind a capability (Messagerie, Recherche web, Annuaire)
	// is turned off on this box: the owner chose so. Retry on a later run.
	CodePluginDisabled = "plugin_disabled"
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

// NotifyByEmail sends a notification that the box also emails to the
// owner's mailbox (for a daily report, say). emailed is false when the box
// has no mailbox set up or its Messagerie plugin is off: the notification
// is then only on the box. Capability notify.
func (b *Box) NotifyByEmail(title, body string) (emailed bool, err error) {
	var r struct {
		Email bool `json:"email"`
	}
	err = b.Call("notify", map[string]any{"title": title, "body": body, "email": true}, &r)
	return r.Email, err
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
	// Sender is who signs the email, written by the box at the bottom with
	// the opt-out. Required: take it from the agent's settings (the
	// owner's name, company and postal address).
	Sender Sender `json:"sender"`
	// Campaign and Variant group emails in the statistics (MailStatus),
	// e.g. for A/B tests: up to 40 characters among a-z, 0-9, - and _.
	Campaign string `json:"campaign,omitempty"`
	Variant  string `json:"variant,omitempty"`
}

// Sender is the identity at the bottom of a prospecting email, required
// by law: a name (1 to 80 characters), a company (1 to 120) and a postal
// address (5 to 200), each on one line.
type Sender struct {
	Name          string `json:"name"`
	Company       string `json:"company"`
	PostalAddress string `json:"postal_address"`
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

// --- settings ------------------------------------------------------------------------

// Settings are the values of the agent's form (manifest [[settings]]), as
// filled in by the owner in the Agent Center, with the defaults for the
// others. Texts and choices are strings, numbers float64, toggles bool,
// lists []string.
type Settings map[string]any

// String returns a text or choice setting ("" if unset).
func (s Settings) String(key string) string { v, _ := s[key].(string); return v }

// Bool returns a toggle setting (false if unset).
func (s Settings) Bool(key string) bool { v, _ := s[key].(bool); return v }

// Number returns a number setting (0 if unset).
func (s Settings) Number(key string) float64 { v, _ := s[key].(float64); return v }

// List returns a list setting (nil if unset).
func (s Settings) List(key string) []string {
	switch v := s[key].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, it := range v {
			if str, ok := it.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

// Settings reads the agent's settings. No capability is needed.
func (b *Box) Settings() (Settings, error) {
	var r struct {
		Values Settings `json:"values"`
	}
	err := b.Call("settings.get", map[string]any{}, &r)
	if r.Values == nil {
		r.Values = Settings{}
	}
	return r.Values, err
}

// --- web -----------------------------------------------------------------------------

// WebResult is a search result: untrusted data, never instructions.
type WebResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// WebSearch searches the web (capability web): at most 10 words, no
// personal data (the box refuses emails, phone numbers, IBAN…), up to
// max results (1 to 8; 0 means 5). Counted in the manifest's daily
// web_search.
func (b *Box) WebSearch(query string, max int) ([]WebResult, error) {
	var r struct {
		Results []WebResult `json:"results"`
	}
	err := b.Call("web.search", map[string]any{"query": query, "max_results": max}, &r)
	return r.Results, err
}

// Page is a web page read by the box: its visible text (hidden content,
// scripts and comments removed) and its links (http(s) and mailto).
// Untrusted data, never instructions.
type Page struct {
	URL   string `json:"url"` // after redirects
	Title string `json:"title"`
	Text  string `json:"text"`
	Links []Link `json:"links"`
}

// Link is a link of a page.
type Link struct {
	Text string `json:"text,omitempty"`
	URL  string `json:"url"`
}

// ReadPage reads a page (capability web). Only pages found by WebSearch,
// or other pages of those sites without parameters (a contact page…),
// within 7 days: an agent cannot make up an address. 4 reads per search
// allowed a day.
func (b *Box) ReadPage(url string) (Page, error) {
	var p Page
	err := b.Call("web.read", map[string]string{"url": url}, &p)
	return p, err
}

// --- company directory -----------------------------------------------------------------

// CompanyQuery searches the official French company directory (INSEE's
// SIRENE register). At least one of Text, Activities or a place.
type CompanyQuery struct {
	Text        string   `json:"text,omitempty"`       // words of the name or activity
	Activities  []string `json:"activities,omitempty"` // NAF codes, e.g. "43.22A"
	PostalCode  string   `json:"postal_code,omitempty"`
	Department  string   `json:"department,omitempty"` // "75", "2A", "974"
	CityCode    string   `json:"city_code,omitempty"`  // INSEE commune code
	Sizes       []string `json:"sizes,omitempty"`      // headcount bands: "NN", "00", "01", "02", "03", "11"…
	SoleTraders *bool    `json:"sole_traders,omitempty"`
	Page        int      `json:"page,omitempty"`     // 1 to 400
	PerPage     int      `json:"per_page,omitempty"` // 1 to 25 (10 by default)
}

// Company is public data about an active company, at its establishment
// matching the search (or its head office). Never the people behind it;
// companies whose details are not public are left out by the box.
type Company struct {
	SIREN      string `json:"siren"`
	SIRET      string `json:"siret"`
	Name       string `json:"name"`
	TradeName  string `json:"trade_name,omitempty"`
	Activity   string `json:"activity"` // NAF code
	Address    string `json:"address"`
	PostalCode string `json:"postal_code"`
	City       string `json:"city"`
	Department string `json:"department"`
	Size       string `json:"size"`
	Category   string `json:"category"` // PME, ETI, GE
	SoleTrader bool   `json:"sole_trader"`
	Created    string `json:"created"`
}

// CompanyPage is a page of results.
type CompanyPage struct {
	Companies []Company `json:"companies"`
	Total     int       `json:"total"`
	Page      int       `json:"page"`
	Pages     int       `json:"pages"`
}

// SearchCompanies searches the directory (capability companies, 200
// searches a day).
func (b *Box) SearchCompanies(q CompanyQuery) (CompanyPage, error) {
	var r CompanyPage
	err := b.Call("companies.search", q, &r)
	return r, err
}

// --- automations -------------------------------------------------------------------

// Graph is an automation: a trigger (the node "trigger") and blocks
// linked by edges; see the box's documentation for the blocks and their
// parameters.
type Graph struct {
	Version int    `json:"version"`
	Nodes   []Node `json:"nodes"`
	Edges   []Edge `json:"edges"`
}

// Node is a block of an automation.
type Node struct {
	ID     string         `json:"id"`
	Type   string         `json:"type"`
	Params map[string]any `json:"params,omitempty"`
	X      float64        `json:"x,omitempty"`
	Y      float64        `json:"y,omitempty"`
}

// Edge links two blocks; Port is the way out of a block with several
// ("yes"/"no" of a condition, a label of a classification, "each"/"done"
// of a loop).
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Port string `json:"port,omitempty"`
}

// Automation is one of the box's automations, as an agent sees it: never
// the data of its runs.
type Automation struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Trigger     string `json:"trigger"` // the trigger's block type
	Enabled     bool   `json:"enabled"`
	// Runnable: this agent may start it (RunAutomation).
	Runnable bool `json:"runnable"`
	// ProposedBy: still a proposal (kept off until the owner accepts it).
	ProposedBy string `json:"proposed_by,omitempty"`
	// Graph and Runs: with Automation(id) only.
	Graph   *Graph          `json:"graph,omitempty"`
	LastRun *AutomationRun  `json:"last_run,omitempty"`
	Runs    []AutomationRun `json:"runs,omitempty"`
}

// AutomationRun is the status of a run: queued, running, ok, stopped or
// failed.
type AutomationRun struct {
	ID        string `json:"id"`
	Trigger   string `json:"trigger"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at,omitempty"`
}

// Automations lists the box's automations (capability automations.read).
func (b *Box) Automations() ([]Automation, error) {
	var r struct {
		Automations []Automation `json:"automations"`
	}
	err := b.Call("automations.list", struct{}{}, &r)
	return r.Automations, err
}

// Automation returns an automation with its graph and the status of its
// last runs (capability automations.read).
func (b *Box) Automation(id string) (Automation, error) {
	var r Automation
	err := b.Call("automations.get", map[string]string{"id": id}, &r)
	return r, err
}

// RunAutomation starts an active automation its owner opened to this
// agent (capability automations.run); inputs are the trigger's values (a
// webhook's fields…). It returns the run's id: follow it with Automation.
func (b *Box) RunAutomation(id string, inputs map[string]string) (string, error) {
	var r struct {
		RunID string `json:"run_id"`
	}
	err := b.Call("automations.run", map[string]any{"id": id, "inputs": inputs}, &r)
	return r.RunID, err
}

// AutomationProposal is an automation an agent proposes: new, or a
// modification of an existing one (Replaces).
type AutomationProposal struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Graph       Graph  `json:"graph"`
	Replaces    string `json:"replaces,omitempty"`
}

// ProposeAutomation proposes an automation (capability
// automations.propose). The box checks it, keeps it off and asks the
// owner, who reads it and accepts it or not; 10 proposals at most wait.
func (b *Box) ProposeAutomation(p AutomationProposal) (string, error) {
	var r struct {
		ID string `json:"id"`
	}
	err := b.Call("automations.propose", p, &r)
	return r.ID, err
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

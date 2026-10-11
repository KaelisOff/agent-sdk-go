// Package agenttest runs an agent against a simulated box, in tests.
//
// The simulated box answers like a real one: the agent gets exactly the
// capabilities its manifest asks for, with the box's checks, limits and
// error codes. What the agent did (notifications, requests for approval,
// storage, calls) is recorded for the test to check.
//
//	func TestTip(t *testing.T) {
//		box, err := agenttest.Load("manifest.toml")
//		if err != nil {
//			t.Fatal(err)
//		}
//		box.Model = func(msgs []agent.Message, maxTokens int) (string, error) {
//			return "Relancez vos devis du mois dernier.", nil
//		}
//		res := box.Run(run, agenttest.Manual())
//		if res.Status != "ok" || len(box.Notifications) != 1 {
//			t.Fatalf("%+v\n%s", res, res.Logs)
//		}
//	}
package agenttest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KaelisOff/agent-sdk-go/agent"
	"github.com/KaelisOff/agent-sdk-go/manifest"
)

// Limits of a run on a box (protocol version 1).
const (
	MaxCalls            = 200     // calls per run; more and the run is stopped
	MaxLine             = 1 << 20 // bytes per message
	MaxSummary          = 500     // characters kept from the summary
	MaxModelChars       = 24000   // characters in all the messages of a model.chat
	MaxModelMessages    = 50
	MaxModelTokens      = 2048
	MaxStorageKey       = 200 // bytes
	MaxStorageKeys      = 1000
	MaxTitle            = 160 // characters (notifications, approvals)
	MaxBody             = 4000
	MaxApprovalData     = 16 << 10
	MaxPendingApprovals = 10
)

// Box is a simulated box. Set Model (and, if useful, Now and Location)
// before Run; read the recorded fields afterwards. Storage, Notifications
// and Approvals persist across runs, like on a box.
type Box struct {
	Manifest manifest.Manifest

	// Model answers model.chat. Without it, model.chat fails as when the
	// box's model is unavailable.
	Model func(messages []agent.Message, maxTokens int) (string, error)
	// Now is the time given to the agent (time.Now() when zero), in
	// Location (time.Local when nil).
	Now      time.Time
	Location *time.Location

	Storage       map[string]string
	Notifications []Notification
	Approvals     []*Approval
	// Runs counts the runs so far.
	Runs int

	// Email (capabilities mail.send and mail.read): the box's daily limit
	// (DefaultMailLimit when 0), a box without a mailbox, a pause of the
	// brakes. See SentMail, Reply, Unsubscribe, Bounce.
	MailLimit  int
	NoMailbox  bool
	MailPaused bool

	mailState *mailState
	runNow    time.Time
}

// Notification is a notification sent by the agent.
type Notification struct {
	Title, Body string
}

// Approval is a request for approval sent by the agent.
type Approval struct {
	ID, Title, Details string
	Data               json.RawMessage
	ExpiresHours       int
	Status             string // pending, approved, rejected, expired
}

// Result describes a run.
type Result struct {
	// Status is "ok" (exit code 0), "failed" (other exit code) or "killed"
	// (the box stopped the agent: too many calls, a message that is not
	// JSON or too long).
	Status   string
	ExitCode int
	Summary  string
	// Logs is what the agent wrote to its log (standard error).
	Logs  string
	Calls []Call
}

// Call is a call made by the agent; Error is the error code it got ("" on
// success).
type Call struct {
	Method string
	Error  string
}

// New returns a simulated box for an agent, from its manifest.
func New(manifestTOML []byte) (*Box, error) {
	m, err := manifest.Parse(manifestTOML)
	if err != nil {
		return nil, err
	}
	return &Box{Manifest: m, Storage: map[string]string{}}, nil
}

// Load is New with the manifest read from a file.
func Load(path string) (*Box, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return New(data)
}

// Manual is the trigger of a run started by the owner or an admin.
func Manual() agent.Trigger { return agent.Trigger{Kind: agent.TriggerManual} }

// Scheduled is the trigger of a run started by one of the manifest's
// schedules.
func Scheduled(expr string) agent.Trigger {
	return agent.Trigger{Kind: agent.TriggerSchedule, Schedule: expr}
}

// Answer records the owner's answer to a pending request for approval and
// returns the trigger of the run that follows.
func (b *Box) Answer(id string, approved bool) agent.Trigger {
	a := b.pending(id)
	a.Status = "rejected"
	if approved {
		a.Status = "approved"
	}
	return agent.Trigger{Kind: agent.TriggerApproval, Approval: &agent.Approval{ID: a.ID, Approved: approved, Data: a.Data}}
}

// Expire lets a pending request for approval expire (no answer means no)
// and returns the trigger of the run that follows.
func (b *Box) Expire(id string) agent.Trigger {
	a := b.pending(id)
	a.Status = "expired"
	return agent.Trigger{Kind: agent.TriggerApproval, Approval: &agent.Approval{ID: a.ID, Expired: true, Data: a.Data}}
}

func (b *Box) pending(id string) *Approval {
	for _, a := range b.Approvals {
		if a.ID == id && a.Status == "pending" {
			return a
		}
	}
	panic("agenttest: no pending request for approval " + id)
}

// Run runs the agent's handler once, as the box would for trigger.
func (b *Box) Run(h agent.Handler, trig agent.Trigger) Result {
	if msg := b.checkTrigger(trig); msg != "" {
		return Result{Status: "failed", ExitCode: -1, Logs: "agenttest: " + msg + "\n"}
	}
	if b.Storage == nil {
		b.Storage = map[string]string{}
	}
	b.Runs++
	loc := b.Location
	if loc == nil {
		loc = time.Local
	}
	now := b.Now
	if now.IsZero() {
		now = time.Now()
	}
	b.runNow = now.In(loc)
	info := agent.Run{
		ID: fmt.Sprintf("run-%d", b.Runs), Trigger: trig, AgentID: b.Manifest.ID, Version: b.Manifest.Version,
		Now: now.In(loc), TimeZone: loc.String(), Locale: "fr-FR",
	}

	toAgentR, toAgentW := io.Pipe()
	fromAgentR, fromAgentW := io.Pipe()
	var logs bytes.Buffer
	exit := make(chan int, 1)
	go func() {
		code := 2
		defer func() {
			if p := recover(); p != nil {
				fmt.Fprintf(&logs, "panic: %v\n", p)
			}
			fromAgentW.Close()
			toAgentR.Close()
			exit <- code
		}()
		code = agent.Serve(agent.NewBox(toAgentR, fromAgentW, &logs), h)
	}()

	var res Result
	send := func(v any) {
		line, _ := json.Marshal(v)
		_, _ = toAgentW.Write(append(line, '\n'))
	}
	killed := ""
	kill := func(reason string) {
		if killed == "" {
			killed = reason
			toAgentW.CloseWithError(io.ErrClosedPipe)
		}
	}
	send(map[string]any{"type": "start", "protocol": 1, "run": info})

	rd := bufio.NewReader(fromAgentR)
	for {
		line, err := rd.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 && killed == "" {
			b.handle(line, &res, send, kill)
		}
		if err != nil {
			break
		}
	}
	res.ExitCode = <-exit
	toAgentW.Close()
	res.Logs = logs.String()
	switch {
	case killed != "":
		res.Status = "killed"
		res.Logs += "agenttest: stopped by the box: " + killed + "\n"
	case res.ExitCode == 0:
		res.Status = "ok"
	default:
		res.Status = "failed"
	}
	return res
}

func (b *Box) checkTrigger(trig agent.Trigger) string {
	p := b.Manifest.Permissions
	switch trig.Kind {
	case agent.TriggerManual:
	case agent.TriggerSchedule:
		if !slices.Contains(p.Schedule, trig.Schedule) {
			return fmt.Sprintf("schedule %q is not in the manifest", trig.Schedule)
		}
	case agent.TriggerApproval:
		if !p.Approval || trig.Approval == nil {
			return "approval trigger without the approval capability or an answer"
		}
	default:
		return fmt.Sprintf("unknown trigger %q", trig.Kind)
	}
	return ""
}

type callError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func fail(code, format string, args ...any) *callError {
	return &callError{Code: code, Message: fmt.Sprintf(format, args...)}
}

func (b *Box) handle(line []byte, res *Result, send func(any), kill func(string)) {
	if len(line) > MaxLine {
		kill("message longer than 1 MB")
		return
	}
	var msg struct {
		Type    string          `json:"type"`
		ID      int64           `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
		Summary string          `json:"summary"`
	}
	if err := json.Unmarshal(line, &msg); err != nil {
		kill("output that is not a JSON message (use Box.Logf for logs)")
		return
	}
	switch msg.Type {
	case "done":
		res.Summary = truncate(msg.Summary, MaxSummary)
	case "call":
		if len(res.Calls) >= MaxCalls {
			res.Calls = append(res.Calls, Call{Method: msg.Method, Error: agent.CodeTooMany})
			send(map[string]any{"type": "error", "id": msg.ID, "error": fail(agent.CodeTooMany, "too many calls in one run")})
			kill("too many calls")
			return
		}
		result, cerr := b.call(msg.Method, msg.Params)
		c := Call{Method: msg.Method}
		if cerr != nil {
			c.Error = cerr.Code
			send(map[string]any{"type": "error", "id": msg.ID, "error": cerr})
		} else {
			send(map[string]any{"type": "result", "id": msg.ID, "result": result})
		}
		res.Calls = append(res.Calls, c)
	}
}

func (b *Box) need(c string) *callError {
	if !b.Manifest.Permissions.Has(c) {
		return fail(agent.CodePermission, "capability %s was not granted to this agent", c)
	}
	return nil
}

func decode(raw json.RawMessage, v any) *callError {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fail(agent.CodeInvalid, "%v", err)
	}
	return nil
}

func (b *Box) call(method string, raw json.RawMessage) (any, *callError) {
	switch method {
	case "model.chat":
		return b.modelChat(raw)
	case "storage.get", "storage.set", "storage.delete", "storage.list":
		if err := b.need(manifest.CapStorage); err != nil {
			return nil, err
		}
		return b.storage(method, raw)
	case "notify":
		return b.notify(raw)
	case "approval.request":
		return b.approval(raw)
	case "mail.send":
		return b.mailSend(raw)
	case "mail.replies":
		return b.mailReplies(raw)
	case "mail.status":
		return b.mailStatus()
	default:
		return nil, fail(agent.CodeUnknown, "unknown method %s", method)
	}
}

func (b *Box) modelChat(raw json.RawMessage) (any, *callError) {
	if err := b.need(manifest.CapModel); err != nil {
		return nil, err
	}
	var p struct {
		Messages  []agent.Message `json:"messages"`
		MaxTokens int             `json:"max_tokens"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	if len(p.Messages) == 0 || len(p.Messages) > MaxModelMessages {
		return nil, fail(agent.CodeInvalid, "1 to %d messages", MaxModelMessages)
	}
	if p.MaxTokens == 0 {
		p.MaxTokens = 512
	}
	if p.MaxTokens < 1 || p.MaxTokens > MaxModelTokens {
		return nil, fail(agent.CodeInvalid, "max_tokens: 1 to %d", MaxModelTokens)
	}
	total := 0
	for _, m := range p.Messages {
		if m.Role != "system" && m.Role != "user" && m.Role != "assistant" {
			return nil, fail(agent.CodeInvalid, "role must be system, user or assistant")
		}
		total += utf8.RuneCountInString(m.Content)
	}
	if total > MaxModelChars {
		return nil, fail(agent.CodeInvalid, "at most %d characters in all", MaxModelChars)
	}
	if b.Model == nil {
		return nil, fail(agent.CodeFailed, "the model is not available (agenttest: set Box.Model)")
	}
	text, err := b.Model(p.Messages, p.MaxTokens)
	if err != nil {
		return nil, fail(agent.CodeFailed, "the model is not available")
	}
	// Rough token counts: about four characters per token.
	return map[string]any{"text": text, "prompt_tokens": total/4 + 1, "output_tokens": utf8.RuneCountInString(text)/4 + 1}, nil
}

func (b *Box) storage(method string, raw json.RawMessage) (any, *callError) {
	if method == "storage.list" {
		var p struct {
			Prefix string `json:"prefix"`
		}
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		keys := []string{}
		for k := range b.Storage {
			if strings.HasPrefix(k, p.Prefix) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		if len(keys) > MaxStorageKeys {
			keys = keys[:MaxStorageKeys]
		}
		return map[string]any{"keys": keys}, nil
	}
	var p struct {
		Key   string  `json:"key"`
		Value *string `json:"value"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	if p.Key == "" || len(p.Key) > MaxStorageKey {
		return nil, fail(agent.CodeInvalid, "key: 1 to %d bytes", MaxStorageKey)
	}
	switch method {
	case "storage.get":
		if v, ok := b.Storage[p.Key]; ok {
			return map[string]any{"value": v}, nil
		}
		return map[string]any{"value": nil}, nil
	case "storage.delete":
		delete(b.Storage, p.Key)
		return map[string]any{}, nil
	default: // set
		if p.Value == nil {
			return nil, fail(agent.CodeInvalid, "value is required")
		}
		used := 0
		for k, v := range b.Storage {
			if k != p.Key {
				used += len(k) + len(v)
			}
		}
		if quota := b.Manifest.Permissions.StorageKB * 1024; used+len(p.Key)+len(*p.Value) > quota {
			return nil, fail(agent.CodeQuota, "storage is limited to %d KB", b.Manifest.Permissions.StorageKB)
		}
		b.Storage[p.Key] = *p.Value
		return map[string]any{}, nil
	}
}

func (b *Box) notify(raw json.RawMessage) (any, *callError) {
	if err := b.need(manifest.CapNotify); err != nil {
		return nil, err
	}
	var p struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	if p.Title == "" || utf8.RuneCountInString(p.Title) > MaxTitle || utf8.RuneCountInString(p.Body) > MaxBody {
		return nil, fail(agent.CodeInvalid, "title: 1 to %d characters, body: up to %d", MaxTitle, MaxBody)
	}
	b.Notifications = append(b.Notifications, Notification{Title: p.Title, Body: p.Body})
	return map[string]any{"id": fmt.Sprintf("ntf-%d", len(b.Notifications))}, nil
}

func (b *Box) approval(raw json.RawMessage) (any, *callError) {
	if err := b.need(manifest.CapApproval); err != nil {
		return nil, err
	}
	var p struct {
		Title        string          `json:"title"`
		Details      string          `json:"details"`
		Data         json.RawMessage `json:"data"`
		ExpiresHours int             `json:"expires_hours"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	if p.Title == "" || utf8.RuneCountInString(p.Title) > MaxTitle || utf8.RuneCountInString(p.Details) > MaxBody {
		return nil, fail(agent.CodeInvalid, "title: 1 to %d characters, details: up to %d", MaxTitle, MaxBody)
	}
	if len(p.Data) > MaxApprovalData {
		return nil, fail(agent.CodeInvalid, "data is too large")
	}
	if p.ExpiresHours == 0 {
		p.ExpiresHours = 72
	}
	if p.ExpiresHours < 1 || p.ExpiresHours > 168 {
		return nil, fail(agent.CodeInvalid, "expires_hours: 1 to 168")
	}
	pending := 0
	for _, a := range b.Approvals {
		if a.Status == "pending" {
			pending++
		}
	}
	if pending >= MaxPendingApprovals {
		return nil, fail(agent.CodeQuota, "too many requests waiting for an answer")
	}
	a := &Approval{
		ID: fmt.Sprintf("apv-%d", len(b.Approvals)+1), Title: p.Title, Details: p.Details,
		Data: p.Data, ExpiresHours: p.ExpiresHours, Status: "pending",
	}
	b.Approvals = append(b.Approvals, a)
	return map[string]any{"id": a.ID}, nil
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

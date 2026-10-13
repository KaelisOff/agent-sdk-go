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
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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

	// PluginsOff are the box's plugins the owner turned off ("mail",
	// "web", "companies"): their capabilities fail with
	// agent.CodePluginDisabled.
	PluginsOff []string

	// Settings are the values of the agent's form (manifest [[settings]]);
	// the defaults apply to the others, like on a box.
	Settings map[string]any
	// Web answers web.search (nil: no result). Pages are the pages that
	// can be read, by address (others are not_a_page). Directory answers
	// companies.search (nil: no company). The box's rules apply: daily
	// limits, no personal data in queries, only the pages found.
	Web       func(query string) []agent.WebResult
	Pages     map[string]agent.Page
	Directory func(q agent.CompanyQuery) []agent.Company
	// WebQueries and ReadURLs record what the agent searched and read.
	WebQueries []string
	ReadURLs   []string

	// Automations are the box's automations the agent can see
	// (automations.read) and start when Runnable (automations.run); a
	// Graph is given by automations.get. Started and Proposals record what
	// the agent started and proposed.
	Automations []agent.Automation
	Started     []AutomationStart
	Proposals   []agent.AutomationProposal

	mailState *mailState
	web       *webState
	runNow    time.Time
}

// Notification is a notification sent by the agent. Email: it was also
// emailed to the owner (NotifyByEmail, with a mailbox set up).
type Notification struct {
	Title, Body string
	Email       bool
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
	info, failed := b.begin(trig)
	if failed != nil {
		return *failed
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

	res, killed := b.converse(info, toAgentW, fromAgentR, func() { toAgentW.CloseWithError(io.ErrClosedPipe) })
	res.ExitCode = <-exit
	toAgentW.Close()
	return finish(res, killed, logs.String())
}

// MaxLog is what a box keeps of an agent's log (standard error) per run.
const MaxLog = 16 << 10

// RunProcess runs an agent executable once, as the box would for trigger:
// the protocol goes over the process's standard input and output, its
// standard error is its log (the first MaxLog bytes are kept). cmd must
// not be started, nor have Stdin, Stdout or Stderr set.
//
// It is how a compiled agent is tried, e.g. in a sandbox. The process is
// killed when the box stops the run (too many calls, a message that is not
// JSON or too long); to bound its duration, build cmd with
// exec.CommandContext.
func (b *Box) RunProcess(cmd *exec.Cmd, trig agent.Trigger) Result {
	info, failed := b.begin(trig)
	if failed != nil {
		return *failed
	}
	logs := &limitedBuffer{max: MaxLog}
	cmd.Stderr = logs
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = time.Second
	}
	// Standard output is a pipe of our own, not cmd.StdoutPipe: a child
	// of the agent that keeps it open must not block the run once the
	// agent is gone (the read deadline below ends it).
	fromAgent, out, err := os.Pipe()
	if err != nil {
		return Result{Status: "failed", ExitCode: -1, Logs: "agenttest: " + err.Error() + "\n"}
	}
	defer fromAgent.Close()
	cmd.Stdout = out
	toAgent, err := cmd.StdinPipe()
	if err == nil {
		err = cmd.Start()
	}
	out.Close()
	if err != nil {
		return Result{Status: "failed", ExitCode: -1, Logs: "agenttest: cannot start the agent: " + err.Error() + "\n"}
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		_ = fromAgent.SetReadDeadline(time.Now().Add(time.Second))
		close(done)
	}()
	res, killed := b.converse(info, toAgent, fromAgent, func() {
		_ = cmd.Process.Kill()
		_ = fromAgent.SetReadDeadline(time.Now())
	})
	toAgent.Close()
	<-done
	res.ExitCode = cmd.ProcessState.ExitCode()
	if killed == "" && res.ExitCode == -1 {
		killed = "the agent was killed: " + cmd.ProcessState.String()
	}
	return finish(res, killed, logs.String())
}

// begin checks the trigger and prepares a run; failed is the result when
// the run cannot happen.
func (b *Box) begin(trig agent.Trigger) (info agent.Run, failed *Result) {
	if msg := b.checkTrigger(trig); msg != "" {
		return agent.Run{}, &Result{Status: "failed", ExitCode: -1, Logs: "agenttest: " + msg + "\n"}
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
	return agent.Run{
		ID: fmt.Sprintf("run-%d", b.Runs), Trigger: trig, AgentID: b.Manifest.ID, Version: b.Manifest.Version,
		Now: now.In(loc), TimeZone: loc.String(), Locale: "fr-FR",
	}, nil
}

// converse sends start, then answers the agent's calls until its output
// ends. stop is called once when the box stops the run; killed is why.
func (b *Box) converse(info agent.Run, toAgent io.Writer, fromAgent io.Reader, stop func()) (res Result, killed string) {
	send := func(v any) {
		line, _ := json.Marshal(v)
		_, _ = toAgent.Write(append(line, '\n'))
	}
	kill := func(reason string) {
		if killed == "" {
			killed = reason
			stop()
		}
	}
	send(map[string]any{"type": "start", "protocol": 1, "run": info})

	rd := bufio.NewReaderSize(fromAgent, 64<<10)
	var line []byte
	for {
		chunk, err := rd.ReadSlice('\n')
		// A message longer than MaxLine is never held in full: the run
		// stops as soon as it is too long.
		if killed == "" && len(line)+len(chunk) <= MaxLine+1 {
			line = append(line, chunk...)
		} else if killed == "" {
			kill("message longer than 1 MB")
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if len(bytes.TrimSpace(line)) > 0 && killed == "" {
			b.handle(line, &res, send, kill)
		}
		line = line[:0]
		if err != nil {
			break
		}
	}
	return res, killed
}

func finish(res Result, killed, logs string) Result {
	res.Logs = logs
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

// limitedBuffer keeps the first max bytes written to it.
type limitedBuffer struct {
	buf bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.buf.Len(); room > 0 {
		l.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (l *limitedBuffer) String() string { return l.buf.String() }

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
	if p := pluginOf[c]; p != "" && !b.pluginOn(p) {
		return fail(agent.CodePluginDisabled, "the %s plugin is turned off on this box", p)
	}
	return nil
}

// pluginOf maps capabilities to the plugin providing them.
var pluginOf = map[string]string{
	manifest.CapMailSend: "mail", manifest.CapMailRead: "mail",
	manifest.CapWeb: "web", manifest.CapCompany: "companies",
}

func (b *Box) pluginOn(id string) bool { return !slices.Contains(b.PluginsOff, id) }

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
	case "settings.get":
		return b.settingsGet()
	case "web.search":
		return b.webSearch(raw)
	case "web.read":
		return b.webRead(raw)
	case "companies.search":
		return b.companiesSearch(raw)
	case "automations.list", "automations.get", "automations.run", "automations.propose":
		return b.automations(method, raw)
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
		Email bool   `json:"email"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	if p.Title == "" || utf8.RuneCountInString(p.Title) > MaxTitle || utf8.RuneCountInString(p.Body) > MaxBody {
		return nil, fail(agent.CodeInvalid, "title: 1 to %d characters, body: up to %d", MaxTitle, MaxBody)
	}
	emailed := p.Email && !b.NoMailbox && b.pluginOn("mail")
	b.Notifications = append(b.Notifications, Notification{Title: p.Title, Body: p.Body, Email: emailed})
	return map[string]any{"id": fmt.Sprintf("ntf-%d", len(b.Notifications)), "email": emailed}, nil
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

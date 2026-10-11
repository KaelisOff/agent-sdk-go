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
	CodeQuota      = "quota_exceeded"
	CodeUnknown    = "unknown_method"
	CodeTooMany    = "too_many_calls"
	CodeFailed     = "failed"
)

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

package agenttest_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/KaelisOff/agent-sdk-go/agent"
	"github.com/KaelisOff/agent-sdk-go/agenttest"
)

const manifestTOML = `
id = "io.github.example.test"
name = "Test"
version = "1.0.0"
license = "Apache-2.0"
source = "https://github.com/example/test"

[permissions]
model = true
notify = true
approval = true
storage_kb = 1
schedule = ["0 9 * * 1-5"]
`

func newBox(t *testing.T, doc string) *agenttest.Box {
	t.Helper()
	box, err := agenttest.New([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	box.Model = func(msgs []agent.Message, _ int) (string, error) {
		return "réponse à " + msgs[len(msgs)-1].Content, nil
	}
	return box
}

func code(err error) string {
	var e *agent.Error
	if errors.As(err, &e) {
		return e.Code
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func TestRunRecordsWhatTheAgentDid(t *testing.T) {
	box := newBox(t, manifestTOML)
	res := box.Run(func(b *agent.Box, r agent.Run) (string, error) {
		if r.Trigger.Kind != agent.TriggerManual || r.AgentID != "io.github.example.test" {
			t.Errorf("run = %+v", r)
		}
		text, err := b.Chat(agent.User("bonjour"))
		if err != nil {
			return "", err
		}
		if err := b.Notify("Titre", text); err != nil {
			return "", err
		}
		if err := b.Set("compteur", "1"); err != nil {
			return "", err
		}
		if _, err := b.RequestApproval("Continuer ?", "Trois envois faits.", map[string]int{"lot": 2}, 0); err != nil {
			return "", err
		}
		b.Logf("terminé")
		return "fait", nil
	}, agenttest.Manual())

	if res.Status != "ok" || res.ExitCode != 0 || res.Summary != "fait" || !strings.Contains(res.Logs, "terminé") {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Calls) != 4 || res.Calls[0].Method != "model.chat" || res.Calls[3].Error != "" {
		t.Errorf("calls = %+v", res.Calls)
	}
	if len(box.Notifications) != 1 || box.Notifications[0].Body != "réponse à bonjour" {
		t.Errorf("notifications = %+v", box.Notifications)
	}
	if box.Storage["compteur"] != "1" {
		t.Errorf("storage = %v", box.Storage)
	}
	if len(box.Approvals) != 1 || box.Approvals[0].Status != "pending" || box.Approvals[0].ExpiresHours != 72 {
		t.Fatalf("approvals = %+v", box.Approvals)
	}

	// The answer starts another run, which gets the request's data back.
	res = box.Run(func(b *agent.Box, r agent.Run) (string, error) {
		a := r.Trigger.Approval
		if r.Trigger.Kind != agent.TriggerApproval || a == nil || !a.Approved {
			t.Errorf("trigger = %+v", r.Trigger)
			return "", nil
		}
		var data struct{ Lot int }
		if err := json.Unmarshal(a.Data, &data); err != nil || data.Lot != 2 {
			t.Errorf("data = %s", a.Data)
		}
		v, ok, err := b.Get("compteur")
		if err != nil || !ok || v != "1" {
			t.Errorf("storage across runs: %q %v %v", v, ok, err)
		}
		return "accord reçu", nil
	}, box.Answer(box.Approvals[0].ID, true))
	if res.Status != "ok" || box.Approvals[0].Status != "approved" {
		t.Errorf("result = %+v, approval = %+v", res, box.Approvals[0])
	}
}

func TestLimitsAreTheBoxs(t *testing.T) {
	box := newBox(t, manifestTOML)
	got := map[string]string{}
	box.Run(func(b *agent.Box, _ agent.Run) (string, error) {
		got["quota"] = code(b.Set("gros", strings.Repeat("x", 2000)))
		got["title"] = code(b.Notify("", "corps"))
		_, err := b.ChatMax(4096, agent.User("long"))
		got["tokens"] = code(err)
		_, err = b.Chat(agent.Message{Role: "tool", Content: "x"})
		got["role"] = code(err)
		_, err = b.Chat(agent.User(strings.Repeat("é", agenttest.MaxModelChars+1)))
		got["chars"] = code(err)
		got["unknown"] = code(b.Call("network.connect", map[string]string{"host": "example.com"}, nil))
		got["params"] = code(b.Call("notify", map[string]string{"title": "t", "url": "x"}, nil))
		return "", nil
	}, agenttest.Manual())
	want := map[string]string{
		"quota": agent.CodeQuota, "title": agent.CodeInvalid, "tokens": agent.CodeInvalid, "role": agent.CodeInvalid,
		"chars": agent.CodeInvalid, "unknown": agent.CodeUnknown, "params": agent.CodeInvalid,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: code = %q, want %q", k, got[k], w)
		}
	}
}

func TestOnlyTheManifestsCapabilities(t *testing.T) {
	doc := strings.NewReplacer("model = true\n", "", "notify = true\n", "", "storage_kb = 1\n", "").Replace(manifestTOML)
	box := newBox(t, doc)
	var codes []string
	box.Run(func(b *agent.Box, _ agent.Run) (string, error) {
		_, err := b.Chat(agent.User("bonjour"))
		codes = append(codes, code(err), code(b.Notify("Titre", "")), code(b.Set("k", "v")))
		return "", nil
	}, agenttest.Manual())
	for _, c := range codes {
		if c != agent.CodePermission {
			t.Errorf("codes = %v", codes)
			break
		}
	}
	if len(box.Notifications) != 0 || len(box.Storage) != 0 {
		t.Error("a denied call had an effect")
	}
}

func TestTriggers(t *testing.T) {
	box := newBox(t, manifestTOML)
	ran := 0
	h := func(*agent.Box, agent.Run) (string, error) { ran++; return "", nil }
	if res := box.Run(h, agenttest.Scheduled("0 9 * * 1-5")); res.Status != "ok" {
		t.Errorf("declared schedule: %+v", res)
	}
	if res := box.Run(h, agenttest.Scheduled("* * * * *")); res.Status != "failed" || ran != 1 {
		t.Errorf("undeclared schedule ran: %+v", res)
	}
}

func TestTooManyCallsStopTheAgent(t *testing.T) {
	box := newBox(t, manifestTOML)
	res := box.Run(func(b *agent.Box, _ agent.Run) (string, error) {
		for {
			if _, _, err := b.Get("k"); err != nil {
				return "", err
			}
		}
	}, agenttest.Manual())
	if res.Status != "killed" || len(res.Calls) != agenttest.MaxCalls+1 || res.Calls[agenttest.MaxCalls].Error != agent.CodeTooMany {
		t.Errorf("status = %s, calls = %d", res.Status, len(res.Calls))
	}
}

func TestFailures(t *testing.T) {
	box := newBox(t, manifestTOML)
	res := box.Run(func(*agent.Box, agent.Run) (string, error) {
		return "à moitié", errors.New("plus de place")
	}, agenttest.Manual())
	if res.Status != "failed" || res.ExitCode != 1 || res.Summary != "à moitié" || !strings.Contains(res.Logs, "plus de place") {
		t.Errorf("error: %+v", res)
	}
	res = box.Run(func(*agent.Box, agent.Run) (string, error) {
		panic("oups")
	}, agenttest.Manual())
	if res.Status != "failed" || !strings.Contains(res.Logs, "panic: oups") {
		t.Errorf("panic: %+v", res)
	}
	box.Model = nil
	res = box.Run(func(b *agent.Box, _ agent.Run) (string, error) {
		_, err := b.Chat(agent.User("bonjour"))
		return "", err
	}, agenttest.Manual())
	if res.Status != "failed" || !strings.Contains(res.Logs, agent.CodeFailed) {
		t.Errorf("no model: %+v", res)
	}
}

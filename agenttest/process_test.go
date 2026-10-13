package agenttest_test

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/KaelisOff/agent-sdk-go/agent"
	"github.com/KaelisOff/agent-sdk-go/agenttest"
)

// The test binary doubles as the agent executable: TestMain runs as an
// agent when AGENTTEST_HELPER says which one.
func TestMain(m *testing.M) {
	switch os.Getenv("AGENTTEST_HELPER") {
	case "":
		os.Exit(m.Run())
	case "notify":
		os.Exit(agent.Serve(agent.NewBox(os.Stdin, os.Stdout, os.Stderr), func(b *agent.Box, r agent.Run) (string, error) {
			text, err := b.Chat(agent.User("bonjour"))
			if err != nil {
				return "", err
			}
			b.Logf("run %s", r.ID)
			return "envoyé", b.Notify("Titre", text)
		}))
	case "not-json":
		fmt.Println("hello")
		time.Sleep(time.Minute)
	case "flood":
		os.Stdout.WriteString(strings.Repeat("x", 3<<20))
		time.Sleep(time.Minute)
	case "child":
		// A child that keeps standard output open after the agent exits.
		child := exec.Command("sleep", "60")
		child.Stdout = os.Stdout
		_ = child.Start()
		os.Exit(3)
	}
	os.Exit(1)
}

func helper(t *testing.T, name string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), "AGENTTEST_HELPER="+name)
	return cmd
}

func TestRunProcessSpeaksTheProtocol(t *testing.T) {
	box := newBox(t, manifestTOML)
	box.Model = func([]agent.Message, int) (string, error) { return "Bonne journée", nil }
	res := box.RunProcess(helper(t, "notify"), agenttest.Manual())
	if res.Status != "ok" || res.Summary != "envoyé" || !strings.Contains(res.Logs, "run run-1") {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Calls) != 2 || res.Calls[0].Method != "model.chat" || res.Calls[1].Method != "notify" {
		t.Errorf("calls = %+v", res.Calls)
	}
	if len(box.Notifications) != 1 || box.Notifications[0].Body != "Bonne journée" {
		t.Errorf("notifications = %+v", box.Notifications)
	}
}

func TestRunProcessStopsMisbehavingAgents(t *testing.T) {
	for name, want := range map[string]string{
		"not-json": "not a JSON message",
		"flood":    "longer than 1 MB",
	} {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			res := newBox(t, manifestTOML).RunProcess(helper(t, name), agenttest.Manual())
			if res.Status != "killed" || !strings.Contains(res.Logs, want) {
				t.Fatalf("result = %+v", res)
			}
			if time.Since(start) > 10*time.Second {
				t.Errorf("took %v", time.Since(start))
			}
		})
	}
}

func TestRunProcessEndsWhenTheAgentExits(t *testing.T) {
	start := time.Now()
	res := newBox(t, manifestTOML).RunProcess(helper(t, "child"), agenttest.Manual())
	if res.Status != "failed" || res.ExitCode != 3 {
		t.Fatalf("result = %+v", res)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("took %v: a child holding standard output blocked the run", time.Since(start))
	}
}

func TestRunProcessCannotStart(t *testing.T) {
	res := newBox(t, manifestTOML).RunProcess(exec.Command("/nonexistent/agent"), agenttest.Manual())
	if res.Status != "failed" || !strings.Contains(res.Logs, "cannot start") {
		t.Fatalf("result = %+v", res)
	}
}

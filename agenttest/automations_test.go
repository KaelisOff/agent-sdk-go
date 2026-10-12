package agenttest

import (
	"testing"

	"github.com/KaelisOff/agent-sdk-go/agent"
	"github.com/KaelisOff/agent-sdk-go/manifest"
)

func TestAutomations(t *testing.T) {
	b := &Box{Manifest: manifest.Manifest{ID: "io.github.example.test", Name: "Test", Version: "1.0.0",
		Permissions: manifest.Permissions{Automations: []string{"read", "run", "propose"}}}}
	b.Automations = []agent.Automation{
		{ID: "aut_1", Name: "Veille", Enabled: true, Runnable: true, Graph: &agent.Graph{Version: 1}},
		{ID: "aut_2", Name: "Factures", Enabled: true},
	}
	res := b.Run(func(box *agent.Box, _ agent.Run) (string, error) {
		list, err := box.Automations()
		if err != nil || len(list) != 2 || list[0].Graph != nil {
			t.Errorf("list = %+v, %v", list, err)
		}
		if a, err := box.Automation("aut_1"); err != nil || a.Graph == nil {
			t.Errorf("get = %+v, %v", a, err)
		}
		if _, err := box.Automation("aut_9"); !agent.IsCode(err, agent.CodeNotFound) {
			t.Errorf("unknown: %v", err)
		}
		if _, err := box.RunAutomation("aut_1", map[string]string{"ville": "Lyon"}); err != nil {
			t.Errorf("run: %v", err)
		}
		if _, err := box.RunAutomation("aut_2", nil); !agent.IsCode(err, agent.CodePermission) {
			t.Errorf("not open: %v", err)
		}
		g := agent.Graph{Version: 1, Nodes: []agent.Node{{ID: "trigger", Type: "trigger.manual"}, {ID: "n", Type: "notify", Params: map[string]any{"title": "x"}}},
			Edges: []agent.Edge{{From: "trigger", To: "n"}}}
		if _, err := box.ProposeAutomation(agent.AutomationProposal{Name: "Idée", Graph: g}); err != nil {
			t.Errorf("propose: %v", err)
		}
		if _, err := box.ProposeAutomation(agent.AutomationProposal{Name: "Vide", Graph: agent.Graph{Version: 1}}); !agent.IsCode(err, agent.CodeInvalid) {
			t.Errorf("empty graph: %v", err)
		}
		return "ok", nil
	}, Manual())
	if res.Status != "ok" || len(b.Started) != 1 || b.Started[0].Inputs["ville"] != "Lyon" || len(b.Proposals) != 1 {
		t.Errorf("result = %+v, started %+v, proposals %d", res, b.Started, len(b.Proposals))
	}
	// Without the capability.
	b.Manifest.Permissions.Automations = []string{"read"}
	b.Run(func(box *agent.Box, _ agent.Run) (string, error) {
		if _, err := box.RunAutomation("aut_1", nil); !agent.IsCode(err, agent.CodePermission) {
			t.Errorf("without automations.run: %v", err)
		}
		return "ok", nil
	}, Manual())
}

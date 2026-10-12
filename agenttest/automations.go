package agenttest

import (
	"encoding/json"
	"fmt"

	"github.com/KaelisOff/agent-sdk-go/agent"
	"github.com/KaelisOff/agent-sdk-go/manifest"
)

// MaxProposals is how many proposals may wait on a box.
const MaxProposals = 10

// AutomationStart is an automation the agent started.
type AutomationStart struct {
	ID     string
	Inputs map[string]string
}

func (b *Box) automations(method string, raw json.RawMessage) (any, *callError) {
	need := map[string]string{"automations.list": manifest.CapAutoRead, "automations.get": manifest.CapAutoRead,
		"automations.run": manifest.CapAutoRun, "automations.propose": manifest.CapAutoPropose}[method]
	if err := b.need(need); err != nil {
		return nil, err
	}
	find := func(id string) *agent.Automation {
		for i := range b.Automations {
			if b.Automations[i].ID == id {
				return &b.Automations[i]
			}
		}
		return nil
	}
	switch method {
	case "automations.list":
		out := []agent.Automation{}
		for _, a := range b.Automations {
			a.Graph, a.Runs = nil, nil
			out = append(out, a)
		}
		return map[string]any{"automations": out}, nil
	case "automations.get":
		var p struct {
			ID string `json:"id"`
		}
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		a := find(p.ID)
		if a == nil {
			return nil, fail(agent.CodeNotFound, "no such automation")
		}
		return a, nil
	case "automations.run":
		var p struct {
			ID     string            `json:"id"`
			Inputs map[string]string `json:"inputs"`
		}
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		a := find(p.ID)
		switch {
		case a == nil:
			return nil, fail(agent.CodeNotFound, "no such automation")
		case !a.Runnable:
			return nil, fail(agent.CodePermission, "this automation is not open to this agent")
		}
		b.Started = append(b.Started, AutomationStart{ID: p.ID, Inputs: p.Inputs})
		return map[string]any{"run_id": fmt.Sprintf("run_%d", len(b.Started)), "status": "queued"}, nil
	default:
		var p agent.AutomationProposal
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		// A light version of the box's checks (it checks every block).
		hasTrigger := false
		for _, n := range p.Graph.Nodes {
			hasTrigger = hasTrigger || n.ID == "trigger"
		}
		switch {
		case p.Name == "" || len([]rune(p.Name)) > 80:
			return nil, fail(agent.CodeInvalid, "name: 1 to 80 characters")
		case p.Graph.Version != 1 || !hasTrigger || len(p.Graph.Nodes) < 2:
			return nil, fail(agent.CodeInvalid, "graph: version 1, a trigger and at least one block")
		case p.Replaces != "" && find(p.Replaces) == nil:
			return nil, fail(agent.CodeNotFound, "no such automation to replace")
		case len(b.Proposals) >= MaxProposals:
			return nil, fail(agent.CodeQuota, "too many proposals waiting")
		}
		b.Proposals = append(b.Proposals, p)
		return map[string]any{"id": fmt.Sprintf("aut_proposal%d", len(b.Proposals)), "status": "proposed"}, nil
	}
}

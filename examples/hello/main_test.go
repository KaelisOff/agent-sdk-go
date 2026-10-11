package main

import (
	"testing"

	"github.com/KaelisOff/agent-sdk-go/agent"
	"github.com/KaelisOff/agent-sdk-go/agenttest"
)

const weekdays = "0 9 * * 1-5"

func TestTipsThenAsksToGoOn(t *testing.T) {
	box, err := agenttest.Load("manifest.toml")
	if err != nil {
		t.Fatal(err)
	}
	box.Model = func([]agent.Message, int) (string, error) {
		return "  Facturez dès la livraison.  ", nil
	}
	for range 3 {
		if res := box.Run(run, agenttest.Scheduled(weekdays)); res.Status != "ok" {
			t.Fatalf("%+v\n%s", res, res.Logs)
		}
	}
	if len(box.Notifications) != 3 || box.Notifications[0].Body != "Facturez dès la livraison." {
		t.Fatalf("notifications = %+v", box.Notifications)
	}
	if len(box.Approvals) != 1 {
		t.Fatalf("approvals = %+v", box.Approvals)
	}

	// "No": the scheduled runs stop sending tips.
	if res := box.Run(run, box.Answer(box.Approvals[0].ID, false)); res.Summary != "en pause, à votre demande" {
		t.Errorf("answer: %+v", res)
	}
	if res := box.Run(run, agenttest.Scheduled(weekdays)); res.Summary != "en pause" || len(box.Notifications) != 4 {
		t.Errorf("after no: %+v, %d notifications", res, len(box.Notifications))
	}
}

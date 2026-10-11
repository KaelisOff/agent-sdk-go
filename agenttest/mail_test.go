package agenttest_test

import (
	"strings"
	"testing"
	"time"

	"github.com/KaelisOff/agent-sdk-go/agent"
	"github.com/KaelisOff/agent-sdk-go/agenttest"
)

const mailManifest = `
id = "io.github.example.prospection"
name = "Prospection"
version = "1.0.0"
license = "Apache-2.0"
source = "https://github.com/example/prospection"

[permissions]
storage_kb = 4
mail_send = 3
mail_read = true
`

var claire = agent.Sender{Name: "Claire Martin", Company: "Cabinet Martin", PostalAddress: "12 rue de la Paix, 75002 Paris"}

func mailBox(t *testing.T) *agenttest.Box {
	t.Helper()
	box, err := agenttest.New([]byte(mailManifest))
	if err != nil {
		t.Fatal(err)
	}
	paris, _ := time.LoadLocation("Europe/Paris")
	box.Location = paris
	box.Now = time.Date(2026, 10, 12, 10, 0, 0, 0, paris) // a Monday
	return box
}

func TestMailGuardrails(t *testing.T) {
	box := mailBox(t)
	var got []string
	send := func(b *agent.Box, to string) {
		_, err := b.SendMail(agent.Mail{To: to, Subject: "Une offre", Body: "Bonjour.", Sender: claire, Campaign: "plombiers", Variant: "a"})
		var e *agent.Error
		switch {
		case err == nil:
			got = append(got, "ok")
		case agent.IsCode(err, agent.CodeQuota):
			got = append(got, "quota")
		case asError(err, &e):
			got = append(got, e.Code)
		}
	}
	box.OptOut("paul@menuisier.fr")
	box.Run(func(b *agent.Box, _ agent.Run) (string, error) {
		send(b, "jean@plombier.fr")
		send(b, "Jean@Plombier.fr")
		send(b, "paul@menuisier.fr")
		send(b, "noreply@plombier.fr")
		send(b, "Jean <luc@peintre.fr>")
		send(b, "luc@peintre.fr")
		send(b, "marie@fleuriste.fr")
		send(b, "anne@boulangerie.fr") // fourth email: the manifest allows 3 a day
		return "", nil
	}, agenttest.Manual())
	want := "ok,already_contacted,opted_out,invalid_params,invalid_params,ok,ok,quota"
	if strings.Join(got, ",") != want {
		t.Errorf("codes = %v\nwant    %s", got, want)
	}
	if len(box.SentMail()) != 3 || box.SentMail()[0].To != "jean@plombier.fr" {
		t.Errorf("sent = %+v", box.SentMail())
	}

	// Sunday: outside the window.
	box.Now = time.Date(2026, 10, 18, 10, 0, 0, 0, box.Location)
	got = nil
	box.Run(func(b *agent.Box, _ agent.Run) (string, error) {
		send(b, "anne@boulangerie.fr")
		return "", nil
	}, agenttest.Manual())
	if strings.Join(got, ",") != agent.CodeOutsideHours {
		t.Errorf("Sunday: %v", got)
	}
}

func asError(err error, target **agent.Error) bool {
	e, ok := err.(*agent.Error)
	*target = e
	return ok
}

func TestMailRepliesAndStatus(t *testing.T) {
	box := mailBox(t)
	box.Run(func(b *agent.Box, _ agent.Run) (string, error) {
		for _, to := range []string{"jean@plombier.fr", "paul@menuisier.fr", "luc@peintre.fr"} {
			if _, err := b.SendMail(agent.Mail{To: to, Subject: "Une offre", Body: "Bonjour.", Sender: claire, Campaign: "plombiers", Variant: "a"}); err != nil {
				return "", err
			}
		}
		return "", nil
	}, agenttest.Manual())
	box.Reply("jean@plombier.fr", "Oui, rappelez-moi.")
	box.Unsubscribe("paul@menuisier.fr")
	box.Bounce("luc@peintre.fr")

	var replies []agent.Reply
	var next int64
	var st agent.MailStatus
	res := box.Run(func(b *agent.Box, _ agent.Run) (string, error) {
		var err error
		if replies, next, err = b.Replies(0, 10); err != nil {
			return "", err
		}
		if again, _, _ := b.Replies(next, 10); len(again) != 0 {
			t.Errorf("after the cursor: %+v", again)
		}
		st, err = b.MailStatus()
		return "", err
	}, agenttest.Manual())
	if res.Status != "ok" || len(replies) != 3 || next != 3 {
		t.Fatalf("%+v %v %d", res, replies, next)
	}
	if replies[0].Kind != "reply" || replies[0].Text != "Oui, rappelez-moi." || replies[1].Kind != "unsubscribe" || replies[2].Kind != "bounce" {
		t.Errorf("replies = %+v", replies)
	}
	if len(st.Stats) != 1 || st.Stats[0].Sent != 3 || st.Stats[0].Replied != 1 || st.Stats[0].Unsubscribed != 1 || st.Stats[0].Bounced != 1 || st.AgentLimit != 3 {
		t.Errorf("status = %+v", st)
	}
	// A STOP blocks the address for good.
	res = box.Run(func(b *agent.Box, _ agent.Run) (string, error) {
		_, err := b.SendMail(agent.Mail{To: "paul@menuisier.fr", Subject: "Relance", Body: "Bonjour.", Sender: claire})
		if !agent.IsCode(err, agent.CodeAlreadyContacted) && !agent.IsCode(err, agent.CodeOptedOut) {
			t.Errorf("after STOP: %v", err)
		}
		return "", nil
	}, agenttest.Manual())
}

func TestMailNeedsItsCapabilities(t *testing.T) {
	box := newBox(t, manifestTOML) // asks for no email capability
	var codes []string
	box.Run(func(b *agent.Box, _ agent.Run) (string, error) {
		_, err := b.SendMail(agent.Mail{To: "jean@plombier.fr", Subject: "s", Body: "b", Sender: claire})
		codes = append(codes, code(err))
		_, _, err = b.Replies(0, 10)
		codes = append(codes, code(err))
		_, err = b.MailStatus()
		codes = append(codes, code(err))
		return "", nil
	}, agenttest.Manual())
	if strings.Join(codes, ",") != "permission_denied,permission_denied,permission_denied" {
		t.Errorf("codes = %v", codes)
	}
}

func TestMailSender(t *testing.T) {
	box := mailBox(t)
	var codes []string
	box.Run(func(b *agent.Box, _ agent.Run) (string, error) {
		for _, s := range []agent.Sender{{}, {Name: "Claire", Company: "Cabinet", PostalAddress: "12 rue\nParis"}, claire} {
			_, err := b.SendMail(agent.Mail{To: "jean@plombier.fr", Subject: "Une offre", Body: "Bonjour.", Sender: s})
			var e *agent.Error
			if asError(err, &e) {
				codes = append(codes, e.Code)
			} else {
				codes = append(codes, "ok")
			}
		}
		return "", nil
	}, agenttest.Manual())
	if strings.Join(codes, ",") != "invalid_params,invalid_params,ok" {
		t.Errorf("codes = %v", codes)
	}
	if sent := box.SentMail(); len(sent) != 1 || sent[0].Sender.Company != "Cabinet Martin" {
		t.Errorf("sent = %+v", sent)
	}
}

func TestPluginsOffAndNotifyByEmail(t *testing.T) {
	box := mailBox(t)
	box.Manifest.Permissions.Notify = true
	box.PluginsOff = []string{"mail"}
	var code string
	var emailed []bool
	box.Run(func(b *agent.Box, _ agent.Run) (string, error) {
		_, err := b.SendMail(agent.Mail{To: "jean@plombier.fr", Subject: "Une offre", Body: "Bonjour.", Sender: claire})
		if agent.IsCode(err, agent.CodePluginDisabled) {
			code = agent.CodePluginDisabled
		}
		ok, err := b.NotifyByEmail("Point du jour", "Rien")
		if err != nil {
			return "", err
		}
		emailed = append(emailed, ok)
		return "", nil
	}, agenttest.Manual())
	box.PluginsOff = nil
	box.Run(func(b *agent.Box, _ agent.Run) (string, error) {
		ok, err := b.NotifyByEmail("Point du jour", "Rien")
		emailed = append(emailed, ok)
		return "", err
	}, agenttest.Manual())
	if code != agent.CodePluginDisabled || len(emailed) != 2 || emailed[0] || !emailed[1] {
		t.Errorf("code = %q, emailed = %v", code, emailed)
	}
	if n := box.Notifications; len(n) != 2 || n[0].Email || !n[1].Email {
		t.Errorf("notifications = %+v", n)
	}
}

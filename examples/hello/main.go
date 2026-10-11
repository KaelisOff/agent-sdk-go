// Command hello is an example Kaelis agent: a short tip every weekday
// morning, sent as a notification; every third tip, it asks whether to go
// on.
package main

import (
	"fmt"
	"strings"

	"github.com/KaelisOff/agent-sdk-go/agent"
)

func main() { agent.Main(run) }

type state struct {
	Tips   int  `json:"tips"`
	Paused bool `json:"paused"`
}

func run(box *agent.Box, r agent.Run) (string, error) {
	var st state
	if _, err := box.GetJSON("state", &st); err != nil {
		return "", err
	}
	if r.Trigger.Kind == "approval" && r.Trigger.Approval != nil {
		st.Paused = !r.Trigger.Approval.Approved
		if err := box.SetJSON("state", st); err != nil {
			return "", err
		}
		if st.Paused {
			return "en pause, à votre demande", box.Notify("Conseil du jour", "C'est noté : je m'arrête. Relancez-moi depuis le Centre d'agents.")
		}
		return "on continue", box.Notify("Conseil du jour", "Merci ! Je continue chaque matin de semaine.")
	}
	if st.Paused && r.Trigger.Kind == "schedule" {
		return "en pause", nil
	}
	box.Logf("tip #%d (%s)", st.Tips+1, r.Trigger.Kind)
	tip, err := box.ChatMax(120,
		agent.System("Tu donnes un conseil pratique, court (deux phrases maximum), en français, à un professionnel indépendant. Pas de titre."),
		agent.User(fmt.Sprintf("Conseil numéro %d, pour le %s.", st.Tips+1, r.Now.Format("02/01/2006"))))
	if err != nil {
		return "", err
	}
	tip = strings.TrimSpace(tip)
	if err := box.Notify("Conseil du jour", tip); err != nil {
		return "", err
	}
	st.Tips++
	if st.Tips%3 == 0 {
		if _, err := box.RequestApproval("Continuer les conseils du jour ?",
			fmt.Sprintf("J'ai envoyé %d conseils. Voulez-vous que je continue chaque matin de semaine ?", st.Tips), nil, 72); err != nil {
			return "", err
		}
	}
	if err := box.SetJSON("state", st); err != nil {
		return "", err
	}
	return fmt.Sprintf("conseil n°%d envoyé", st.Tips), nil
}

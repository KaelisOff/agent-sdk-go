package agenttest_test

import (
	"strings"
	"testing"

	"github.com/KaelisOff/agent-sdk-go/agent"
	"github.com/KaelisOff/agent-sdk-go/agenttest"
)

const webManifest = `
id = "io.github.example.web"
name = "Web"
version = "1.0.0"
license = "Apache-2.0"
source = "https://github.com/example/web"

[permissions]
web_search = 2
companies = true

[[settings]]
key = "offer"
label = "Votre offre"
type = "textarea"
required = true

[[settings]]
key = "review"
label = "Valider chaque message"
type = "toggle"
default = true
`

func TestSettingsAndWeb(t *testing.T) {
	box, err := agenttest.New([]byte(webManifest))
	if err != nil {
		t.Fatal(err)
	}
	box.Settings = map[string]any{"offer": "Comptabilité pour artisans"}
	box.Web = func(q string) []agent.WebResult {
		return []agent.WebResult{
			{Title: "Plomberie Exemple", URL: "https://plomberie-exemple.fr/"},
			{Title: "Annuaire", URL: "https://annuaire.example/fiche?id=42"},
		}
	}
	box.Pages = map[string]agent.Page{
		"https://plomberie-exemple.fr/contact": {Title: "Contact", Links: []agent.Link{{URL: "mailto:contact@plomberie-exemple.fr"}}},
		"https://annuaire.example/fiche?id=42": {Title: "Fiche"},
	}
	box.Directory = func(q agent.CompanyQuery) []agent.Company {
		return []agent.Company{{SIREN: "111111111", Name: "PLOMBERIE EXEMPLE", Activity: q.Activities[0]}}
	}
	codes := map[string]string{}
	var settings agent.Settings
	var contact agent.Page
	res := box.Run(func(b *agent.Box, _ agent.Run) (string, error) {
		code := func(step string, err error) {
			var e *agent.Error
			switch {
			case err == nil:
				codes[step] = "ok"
			case asError(err, &e):
				codes[step] = e.Code
			}
		}
		var err error
		settings, err = b.Settings()
		code("settings", err)
		_, err = b.ReadPage("https://plomberie-exemple.fr/contact")
		code("read first", err)
		_, err = b.WebSearch("plombier jean@exemple.fr", 0)
		code("pii", err)
		_, err = b.WebSearch("plombier Paris", 0)
		code("search", err)
		contact, err = b.ReadPage("https://plomberie-exemple.fr/contact")
		code("same site", err)
		_, err = b.ReadPage("https://annuaire.example/fiche?id=42")
		code("result", err)
		_, err = b.ReadPage("https://annuaire.example/fiche?id=43")
		code("made up", err)
		_, err = b.ReadPage("https://plomberie-exemple.fr/tarifs")
		code("missing", err)
		_, err = b.WebSearch("plombier Lyon", 0)
		code("search 2", err)
		_, err = b.WebSearch("plombier Lille", 0)
		code("search 3", err)
		page, err := b.SearchCompanies(agent.CompanyQuery{Activities: []string{"43.22A"}, PostalCode: "75011"})
		code("companies", err)
		if len(page.Companies) != 1 {
			t.Errorf("companies = %+v", page)
		}
		_, err = b.SearchCompanies(agent.CompanyQuery{})
		code("empty query", err)
		return "", nil
	}, agenttest.Manual())
	if res.Status != "ok" {
		t.Fatalf("%+v", res)
	}
	want := map[string]string{
		"settings": "ok", "read first": agent.CodePermission, "pii": agent.CodeInvalid, "search": "ok", "same site": "ok",
		"result": "ok", "made up": agent.CodePermission, "missing": agent.CodeNotAPage, "search 2": "ok", "search 3": agent.CodeQuota,
		"companies": "ok", "empty query": agent.CodeInvalid,
	}
	for k, w := range want {
		if codes[k] != w {
			t.Errorf("%s: %q, want %q", k, codes[k], w)
		}
	}
	if settings.String("offer") != "Comptabilité pour artisans" || !settings.Bool("review") {
		t.Errorf("settings = %v", settings)
	}
	if len(contact.Links) != 1 || contact.URL != "https://plomberie-exemple.fr/contact" {
		t.Errorf("contact = %+v", contact)
	}
	if strings.Join(box.WebQueries, "|") != "plombier Paris|plombier Lyon" {
		t.Errorf("queries = %v", box.WebQueries)
	}
}

package manifest

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const sample = `
id = "io.github.example.prospection"
name = "Prospection"
version = "0.1.0"
description = "Trouve des clients par email."
author = "Example"
license = "Apache-2.0"
source = "https://github.com/example/agent-prospection"

[run]
memory_mb = 128
timeout_min = 15

[permissions]
model = true
notify = true
approval = true
storage_kb = 1024
schedule = ["30 8 * * 1-5"]
mail_send = 30
mail_read = true
`

func TestParse(t *testing.T) {
	m, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(m.Permissions.Capabilities(), ","); got != "model,storage,schedule,notify,approval,mail.send,mail.read" {
		t.Errorf("capabilities = %s", got)
	}
	if m.Run.CPUs != DefaultCPUs || m.Run.Pids != DefaultPids || m.Run.TimeoutMin != 15 {
		t.Errorf("run = %+v", m.Run)
	}
	if m.Image() != "localhost/kaelis-agent/io.github.example.prospection:0.1.0" {
		t.Errorf("image = %s", m.Image())
	}
}

func TestRejects(t *testing.T) {
	for name, edit := range map[string][2]string{
		"bad id":             {`id = "io.github.example.prospection"`, `id = "Prospection"`},
		"upper case id":      {`id = "io.github.example.prospection"`, `id = "io.github.Example.prospection"`},
		"bad version":        {`version = "0.1.0"`, `version = "latest"`},
		"no name":            {`name = "Prospection"`, `name = " "`},
		"no license":         {`license = "Apache-2.0"`, ``},
		"no source":          {`source = "https://github.com/example/agent-prospection"`, ``},
		"too much memory":    {`memory_mb = 128`, `memory_mb = 4096`},
		"too long":           {`timeout_min = 15`, `timeout_min = 600`},
		"bad schedule":       {`schedule = ["30 8 * * 1-5"]`, `schedule = ["every morning"]`},
		"unknown key":        {`approval = true`, "approval = true\nnetwork = true"},
		"unknown section":    {`[run]`, "[network]\nallow = true\n[run]"},
		"too much storage":   {`storage_kb = 1024`, `storage_kb = 99999999`},
		"negative storage":   {`storage_kb = 1024`, `storage_kb = -1`},
		"too many emails":    {`mail_send = 30`, `mail_send = 500`},
		"too much web":       {`mail_send = 30`, "mail_send = 30\nweb_search = 5000"},
		"bad setting type":   {`[run]`, "[[settings]]\nkey = \"x\"\nlabel = \"X\"\ntype = \"file\"\n[run]"},
		"bad setting key":    {`[run]`, "[[settings]]\nkey = \"X Y\"\nlabel = \"X\"\ntype = \"text\"\n[run]"},
		"choice, no choices": {`[run]`, "[[settings]]\nkey = \"x\"\nlabel = \"X\"\ntype = \"choice\"\n[run]"},
		"bad default":        {`[run]`, "[[settings]]\nkey = \"x\"\nlabel = \"X\"\ntype = \"toggle\"\ndefault = \"oui\"\n[run]"},
		"schedule out of r":  {`schedule = ["30 8 * * 1-5"]`, `schedule = ["30 25 * * *"]`},
	} {
		doc := strings.Replace(sample, edit[0], edit[1], 1)
		if _, err := Parse([]byte(doc)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestSchedule(t *testing.T) {
	paris, _ := time.LoadLocation("Europe/Paris")
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, paris)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	s, err := ParseSchedule("30 8 * * 1-5") // weekdays at 8:30
	if err != nil {
		t.Fatal(err)
	}
	// 2026-10-11 is a Sunday.
	if s.Matches(at("2026-10-11 08:30")) || !s.Matches(at("2026-10-12 08:30")) || s.Matches(at("2026-10-12 08:31")) {
		t.Error("weekday matching")
	}
	if got := s.Next(at("2026-10-09 09:00")); !got.Equal(at("2026-10-12 08:30")) { // Friday after 9 → Monday
		t.Errorf("Next = %v", got)
	}
	for _, tc := range []struct {
		expr, at string
		want     bool
	}{
		{"0 12 * * *", "2026-10-11 12:00", true},
		{"*/15 * * * *", "2026-10-11 10:45", true},
		{"*/15 * * * *", "2026-10-11 10:46", false},
		{"0 9 1 * *", "2026-11-01 09:00", true},
		{"0 9 * * 7", "2026-10-11 09:00", true},    // 7 = Sunday
		{"0 9 1,15 * 1", "2026-10-12 09:00", true}, // either day field
		{"0 9-17/2 * * 1-5", "2026-10-12 15:00", true},
		{"0 9-17/2 * * 1-5", "2026-10-12 16:00", false},
	} {
		s, err := ParseSchedule(tc.expr)
		if err != nil {
			t.Fatalf("%s: %v", tc.expr, err)
		}
		if got := s.Matches(at(tc.at)); got != tc.want {
			t.Errorf("%s at %s = %v", tc.expr, tc.at, got)
		}
	}
	for _, bad := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "*/0 * * * *", "a * * * *", "5-1 * * * *"} {
		if _, err := ParseSchedule(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSettingValues(t *testing.T) {
	doc := strings.Replace(sample, "[run]", `[[settings]]
key = "offer"
label = "Votre offre"
type = "textarea"
required = true

[[settings]]
key = "cities"
label = "Villes"
type = "list"
default = ["Paris"]

[[settings]]
key = "review"
label = "Valider chaque message"
type = "toggle"
default = true

[run]`, 1)
	m, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if got := MissingRequired(m.Settings, nil); strings.Join(got, ",") != "offer" {
		t.Errorf("missing = %v", got)
	}
	v, err := CheckValues(m.Settings, map[string]any{"offer": " Comptabilité ", "cities": []any{"Lyon", " "}})
	if err != nil {
		t.Fatal(err)
	}
	all := WithDefaults(m.Settings, v)
	if all["offer"] != "Comptabilité" || all["review"] != true || len(all["cities"].([]string)) != 1 {
		t.Errorf("values = %v", all)
	}
	if _, err := CheckValues(m.Settings, map[string]any{"review": "yes"}); !errors.Is(err, ErrSettings) {
		t.Errorf("bad toggle: %v", err)
	}
}

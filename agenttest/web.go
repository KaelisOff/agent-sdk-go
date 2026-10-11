package agenttest

import (
	"encoding/json"

	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KaelisOff/agent-sdk-go/agent"
	"github.com/KaelisOff/agent-sdk-go/manifest"
)

// Web limits of a box.
const (
	MaxQueryWords      = 10
	ReadsPerSearch     = 4
	MaxCompanySearches = 200
	WebMemory          = 7 * 24 * time.Hour
)

type webState struct {
	usage   map[string]int       // day|kind → uses
	allowed map[string]time.Time // "url:…" / "host:…" → when found
}

func (b *Box) webState() *webState {
	if b.web == nil {
		b.web = &webState{usage: map[string]int{}, allowed: map[string]time.Time{}}
	}
	return b.web
}

func (b *Box) use(kind string, limit int) bool {
	k := b.runNow.Format("2006-01-02") + "|" + kind
	w := b.webState()
	if w.usage[k] >= limit {
		return false
	}
	w.usage[k]++
	return true
}

// A light version of the box's check: the box refuses more (IBAN, social
// security numbers, cards…).
var (
	emailRe  = regexp.MustCompile(`[\w.+-]+@[\w-]+(\.[\w-]+)+`)
	phoneRe  = regexp.MustCompile(`(\+33|\b0)\s?[1-9]([\s.-]?\d{2}){4}\b`)
	digitsRe = regexp.MustCompile(`\d{9,}`)
)

func personalData(s string) bool {
	return emailRe.MatchString(s) || phoneRe.MatchString(s) || digitsRe.MatchString(strings.ReplaceAll(s, " ", ""))
}

func (b *Box) settingsGet() (any, *callError) {
	vals, err := manifest.CheckValues(b.Manifest.Settings, b.Settings)
	if err != nil {
		return nil, fail(agent.CodeFailed, "agenttest: invalid Box.Settings: %v", err)
	}
	return map[string]any{"values": manifest.WithDefaults(b.Manifest.Settings, vals)}, nil
}

func (b *Box) webSearch(raw json.RawMessage) (any, *callError) {
	if err := b.need(manifest.CapWeb); err != nil {
		return nil, err
	}
	var p struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	words := strings.Fields(p.Query)
	query := strings.Join(words, " ")
	if len(words) == 0 || len(words) > MaxQueryWords || utf8.RuneCountInString(query) > 200 {
		return nil, fail(agent.CodeInvalid, "query: 1 to %d words", MaxQueryWords)
	}
	if personalData(query) {
		return nil, fail(agent.CodeInvalid, "the query contains personal data")
	}
	if p.MaxResults < 0 || p.MaxResults > 8 {
		return nil, fail(agent.CodeInvalid, "max_results: 1 to 8")
	}
	if !b.use("web_search", b.Manifest.Permissions.WebSearch) {
		return nil, fail(agent.CodeQuota, "at most %d searches a day", b.Manifest.Permissions.WebSearch)
	}
	b.WebQueries = append(b.WebQueries, query)
	var results []agent.WebResult
	if b.Web != nil {
		results = b.Web(query)
	}
	n := p.MaxResults
	if n == 0 {
		n = 5
	}
	if len(results) > n {
		results = results[:n]
	}
	if results == nil {
		results = []agent.WebResult{}
	}
	w := b.webState()
	for _, r := range results {
		if u, err := url.Parse(r.URL); err == nil {
			u.Fragment = ""
			w.allowed["url:"+u.String()] = b.runNow
			w.allowed["host:"+strings.ToLower(u.Hostname())] = b.runNow
		}
	}
	return map[string]any{"results": results}, nil
}

func (b *Box) mayRead(u *url.URL) bool {
	w := b.webState()
	fresh := func(k string) bool {
		t, ok := w.allowed[k]
		return ok && b.runNow.Sub(t) <= WebMemory
	}
	if fresh("url:" + u.String()) {
		return true
	}
	return u.RawQuery == "" && !u.ForceQuery && len(u.EscapedPath()) <= 200 && fresh("host:"+strings.ToLower(u.Hostname()))
}

func (b *Box) webRead(raw json.RawMessage) (any, *callError) {
	if err := b.need(manifest.CapWeb); err != nil {
		return nil, err
	}
	var p struct {
		URL string `json:"url"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	u, err := url.Parse(p.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || len(p.URL) > 500 {
		return nil, fail(agent.CodeInvalid, "url: an http(s) address is required")
	}
	u.Fragment = ""
	if !b.mayRead(u) {
		return nil, fail(agent.CodePermission, "only the pages found by web.search, or other pages of those sites without parameters")
	}
	limit := ReadsPerSearch * b.Manifest.Permissions.WebSearch
	if !b.use("web_read", limit) {
		return nil, fail(agent.CodeQuota, "at most %d pages read a day", limit)
	}
	b.ReadURLs = append(b.ReadURLs, u.String())
	page, ok := b.Pages[u.String()]
	if !ok {
		return nil, fail(agent.CodeNotAPage, "this address is not a readable web page")
	}
	if page.URL == "" {
		page.URL = u.String()
	}
	if page.Links == nil {
		page.Links = []agent.Link{}
	}
	return page, nil
}

var nafRe = regexp.MustCompile(`^\d{2}\.\d{2}[A-Z]$`)

func (b *Box) companiesSearch(raw json.RawMessage) (any, *callError) {
	if err := b.need(manifest.CapCompany); err != nil {
		return nil, err
	}
	var q agent.CompanyQuery
	if err := decode(raw, &q); err != nil {
		return nil, err
	}
	if q.Text == "" && len(q.Activities) == 0 && q.PostalCode == "" && q.Department == "" && q.CityCode == "" {
		return nil, fail(agent.CodeInvalid, "give words, activities or a place")
	}
	for _, a := range q.Activities {
		if !nafRe.MatchString(a) {
			return nil, fail(agent.CodeInvalid, "activity %q is not a NAF code (e.g. 43.22A)", a)
		}
	}
	if q.PerPage < 0 || q.PerPage > 25 || q.Page < 0 || q.Page > 400 {
		return nil, fail(agent.CodeInvalid, "page 1 to 400, per_page 1 to 25")
	}
	if personalData(q.Text) {
		return nil, fail(agent.CodeInvalid, "the text contains personal data")
	}
	if !b.use("companies", MaxCompanySearches) {
		return nil, fail(agent.CodeQuota, "at most %d directory searches a day", MaxCompanySearches)
	}
	var all []agent.Company
	if b.Directory != nil {
		all = b.Directory(q)
	}
	per := q.PerPage
	if per == 0 {
		per = 10
	}
	page := max(q.Page, 1)
	from, to := min((page-1)*per, len(all)), min(page*per, len(all))
	res := agent.CompanyPage{Companies: append([]agent.Company{}, all[from:to]...), Total: len(all), Page: page, Pages: (len(all) + per - 1) / per}
	return res, nil
}

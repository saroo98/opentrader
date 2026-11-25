package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// hosts: REST and streaming base URLs per environment (tests point them at a fake server).
var hosts = map[string][2]string{
	"practice": {"https://api-fxpractice.oanda.com", "https://stream-fxpractice.oanda.com"},
	"live":     {"https://api-fxtrade.oanda.com", "https://stream-fxtrade.oanda.com"},
}

var granularity = map[string]string{"1": "M1", "5": "M5", "15": "M15", "30": "M30", "60": "H1", "1D": "D"}
var intervals = []string{"1", "5", "15", "30", "60", "1D"}
var step = map[string]int64{"1": 60, "5": 300, "15": 900, "30": 1800, "60": 3600, "1D": 86400}

const maxCandles = 5000
const tokenMissing = "Add your OANDA API token in Settings > Plugins > OANDA"

var majors = []string{"EURUSD", "GBPUSD", "USDJPY", "USDCHF", "AUDUSD", "USDCAD", "NZDUSD"}
var g8 = map[string]bool{"EUR": true, "GBP": true, "USD": true, "JPY": true, "CHF": true, "AUD": true, "CAD": true, "NZD": true}

// PluginError is answered to the app; Msg is shown to the user.
type PluginError struct {
	Code int
	Msg  string
}

func (e *PluginError) Error() string { return e.Msg }

func perr(msg string) error { return &PluginError{-32000, msg} }

type Instrument struct {
	Name             string `json:"name"`
	Type             string `json:"type"`
	DisplayName      string `json:"displayName"`
	DisplayPrecision int    `json:"displayPrecision"`
}

// Bar is [time, open, high, low, close, volume].
type Bar [6]float64

type Oanda struct {
	mu      sync.Mutex
	token   string
	env     string
	account string
	rest    *http.Client
	stream  *http.Client
}

func newOanda() *Oanda {
	tr := &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: 20 * time.Second,
		DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, MaxIdleConnsPerHost: 8}
	return &Oanda{env: "practice", rest: &http.Client{Transport: tr, Timeout: 20 * time.Second}, stream: &http.Client{Transport: tr}}
}

func (o *Oanda) creds() (string, string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.token, o.env
}

func (o *Oanda) open(path string, params url.Values, stream bool) (*http.Response, error) {
	token, env := o.creds()
	if token == "" {
		return nil, perr(tokenMissing)
	}
	base := hosts[env][0]
	client := o.rest
	if stream {
		base, client = hosts[env][1], o.stream
	}
	u := base + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept-Datetime-Format", "UNIX")
	req.Header.Set("User-Agent", "opentrader-oanda-plugin/"+version)
	resp, err := client.Do(req)
	if err != nil {
		return nil, perr("Cannot reach OANDA: " + err.Error())
	}
	if resp.StatusCode == 200 {
		return resp, nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	var e struct {
		ErrorMessage string `json:"errorMessage"`
	}
	msg := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &e) == nil && e.ErrorMessage != "" {
		msg = e.ErrorMessage
	}
	switch resp.StatusCode {
	case 401, 403:
		return nil, perr(fmt.Sprintf("OANDA refused the API token (%s account): check it in Settings > Plugins", env))
	case 429:
		return nil, &PluginError{-32005, "OANDA rate limit: retrying"}
	}
	return nil, perr(fmt.Sprintf("OANDA error %d: %s", resp.StatusCode, msg))
}

func (o *Oanda) get(path string, params url.Values, out any) error {
	resp, err := o.open(path, params, false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return perr("OANDA sent an unreadable answer: " + err.Error())
	}
	return nil
}

func (o *Oanda) accountID() (string, error) {
	o.mu.Lock()
	acct := o.account
	o.mu.Unlock()
	if acct != "" {
		return acct, nil
	}
	var r struct {
		Accounts []struct {
			ID string `json:"id"`
		} `json:"accounts"`
	}
	if err := o.get("/v3/accounts", nil, &r); err != nil {
		return "", err
	}
	if len(r.Accounts) == 0 {
		return "", perr("This OANDA token has no account")
	}
	o.mu.Lock()
	o.account = r.Accounts[0].ID
	o.mu.Unlock()
	return r.Accounts[0].ID, nil
}

// Source: the account's instruments and their history.
type Source struct {
	mu          sync.RWMutex
	oanda       *Oanda
	instruments map[string]Instrument // ticker (EURUSD) -> instrument (EUR_USD)
	loaded      bool
	errMsg      string
	gen         int // bumped when the connection settings change
}

func newSource() *Source { return &Source{oanda: newOanda(), instruments: map[string]Instrument{}} }

// configure applies the user's settings; it returns whether the instruments must be (re)loaded.
func (s *Source) configure(settings map[string]string) bool {
	token := strings.TrimSpace(settings["token"])
	env := settings["environment"]
	if _, ok := hosts[env]; !ok {
		env = "practice"
	}
	account := strings.TrimSpace(settings["account"])
	o := s.oanda
	o.mu.Lock()
	changed := token != o.token || env != o.env || (account != "" && account != o.account)
	if changed {
		o.token, o.env, o.account = token, env, account
	}
	o.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if changed {
		s.instruments, s.loaded, s.errMsg = map[string]Instrument{}, false, ""
		s.gen++
	}
	return changed || !s.loaded
}

func (s *Source) load() (int, error) {
	acct, err := s.oanda.accountID()
	if err != nil {
		return 0, err
	}
	var r struct {
		Instruments []Instrument `json:"instruments"`
	}
	if err := s.oanda.get("/v3/accounts/"+acct+"/instruments", nil, &r); err != nil {
		return 0, err
	}
	m := map[string]Instrument{}
	for _, i := range r.Instruments {
		m[strings.ReplaceAll(i.Name, "_", "")] = i
	}
	s.mu.Lock()
	s.instruments, s.loaded = m, true
	s.mu.Unlock()
	return len(m), nil
}

func (s *Source) inst(ticker string) (Instrument, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.loaded {
		if s.errMsg != "" {
			return Instrument{}, perr(s.errMsg)
		}
		return Instrument{}, perr("OANDA instruments are still loading")
	}
	i, ok := s.instruments[ticker]
	if !ok {
		return Instrument{}, &PluginError{-32004, "Unknown symbol " + ticker}
	}
	return i, nil
}

func kind(i Instrument) string {
	if i.Type == "CURRENCY" {
		return "forex"
	}
	return "cfd"
}

func isMajor(t string) bool {
	for _, m := range majors {
		if m == t {
			return true
		}
	}
	return false
}

func (s *Source) search(query string) []map[string]string {
	q := strings.NewReplacer("/", "", "_", "").Replace(strings.ToUpper(query))
	s.mu.RLock()
	defer s.mu.RUnlock()
	tickers := make([]string, 0, len(s.instruments))
	for t := range s.instruments {
		tickers = append(tickers, t)
	}
	rank := func(t string) int {
		switch {
		case isMajor(t):
			return 0
		case s.instruments[t].Type == "CURRENCY":
			return 1
		}
		return 2
	}
	sort.Slice(tickers, func(a, b int) bool {
		ra, rb := rank(tickers[a]), rank(tickers[b])
		if ra != rb {
			return ra < rb
		}
		return tickers[a] < tickers[b]
	})
	rows := []map[string]string{}
	for _, t := range tickers {
		i := s.instruments[t]
		name := i.DisplayName
		if name == "" {
			name = t
		}
		if q == "" || strings.Contains(t, q) || strings.Contains(strings.ReplaceAll(strings.ToUpper(name), "/", ""), q) {
			rows = append(rows, map[string]string{"ticker": t, "description": name, "type": kind(i)})
			if len(rows) == 50 {
				break
			}
		}
	}
	return rows
}

func (s *Source) watchlists() []map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var crosses, exotics, metals, cfds []string
	maj := []string{}
	for _, t := range majors {
		if _, ok := s.instruments[t]; ok {
			maj = append(maj, t)
		}
	}
	for t, i := range s.instruments {
		switch {
		case i.Type == "CURRENCY" && isMajor(t):
		case i.Type == "CURRENCY" && len(t) == 6 && g8[t[:3]] && g8[t[3:]]:
			crosses = append(crosses, t)
		case i.Type == "CURRENCY":
			exotics = append(exotics, t)
		case i.Type == "METAL":
			metals = append(metals, t)
		default:
			cfds = append(cfds, t)
		}
	}
	for _, l := range [][]string{crosses, exotics, metals, cfds} {
		sort.Strings(l)
	}
	lists := []map[string]any{
		{"id": "majors", "name": "Forex majors", "symbols": maj},
		{"id": "forex", "name": "All forex", "sections": []map[string]any{
			{"name": "Majors", "symbols": maj}, {"name": "Crosses", "symbols": nonNil(crosses)}, {"name": "Exotics", "symbols": nonNil(exotics)}}},
	}
	if len(metals) > 0 {
		lists = append(lists, map[string]any{"id": "metals", "name": "Metals", "symbols": metals})
	}
	if len(cfds) > 0 {
		lists = append(lists, map[string]any{"id": "cfds", "name": "CFDs", "symbols": cfds})
	}
	return lists
}

func nonNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

func (s *Source) symbolInfo(ticker string) (map[string]any, error) {
	i, err := s.inst(ticker)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(i.Name, "_")
	scale := 1
	for n := 0; n < i.DisplayPrecision; n++ {
		scale *= 10
	}
	name := i.DisplayName
	if name == "" {
		name = ticker
	}
	return map[string]any{
		"description": name,
		"type":        kind(i),
		"currency":    parts[len(parts)-1],
		"timezone":    "America/New_York",
		// Forex trades from Sunday 17:00 to Friday 17:00 New York; each trading day opens at 17:00 the evening before.
		"session":    "1700-1700:23456",
		"pricescale": scale,
		"minmov":     1,
		"pointValue": 1,
		"intervals":  intervals,
	}, nil
}

func pf(s string) float64 { f, _ := strconv.ParseFloat(s, 64); return f }

// candles are bid candles, oldest first; daily ones are stamped at their trading day's New York midnight.
func (s *Source) candles(i Instrument, gran string, to int64, count int) ([]Bar, error) {
	p := url.Values{"granularity": {gran}, "price": {"B"}}
	if to > 0 {
		p.Set("to", strconv.FormatInt(to, 10)+".000000000") // OANDA's UNIX DateTime: seconds.nanoseconds
	}
	if count > 0 {
		p.Set("count", strconv.Itoa(count))
	}
	var r struct {
		Candles []struct {
			Time   string                      `json:"time"`
			Volume float64                     `json:"volume"`
			Bid    struct{ O, H, L, C string } `json:"bid"`
		} `json:"candles"`
	}
	if err := s.oanda.get("/v3/instruments/"+i.Name+"/candles", p, &r); err != nil {
		return nil, err
	}
	out := make([]Bar, 0, len(r.Candles))
	for _, c := range r.Candles {
		t := int64(pf(c.Time))
		if gran == "D" {
			t = tradingDay(t + 3600) // the candle opens at 17:00 New York the evening before
		}
		out = append(out, Bar{float64(t), pf(c.Bid.O), pf(c.Bid.H), pf(c.Bid.L), pf(c.Bid.C), c.Volume})
	}
	return out, nil
}

func (s *Source) bars(ticker, resolution string, from, to int64, countBack int) ([]Bar, error) {
	i, err := s.inst(ticker)
	if err != nil {
		return nil, err
	}
	gran, ok := granularity[resolution]
	if !ok {
		return nil, &PluginError{-32602, "Resolution " + resolution + " is not served"}
	}
	if countBack <= 0 {
		countBack = 2000
	}
	now := time.Now().Unix()
	// OANDA refuses a `to` in the future; a daily bar's time is New York midnight, its candle ends after it.
	pad := step[resolution]
	extra := 1
	if gran == "D" {
		pad, extra = 2*86400, 3
	}
	end := to + pad
	if end > now {
		end = now
	}
	if end <= from-2*86400 {
		return []Bar{}, nil
	}
	count := countBack + extra
	if count > maxCandles {
		count = maxCandles
	}
	rows, err := s.candles(i, gran, end, count)
	if err != nil {
		return nil, err
	}
	out := []Bar{}
	for _, b := range rows {
		if int64(b[0]) >= from && int64(b[0]) <= to {
			out = append(out, b)
		}
	}
	if len(out) > countBack {
		out = out[len(out)-countBack:]
	}
	return out, nil
}

func asPluginError(err error) *PluginError {
	var pe *PluginError
	if errors.As(err, &pe) {
		return pe
	}
	return &PluginError{-32000, err.Error()}
}

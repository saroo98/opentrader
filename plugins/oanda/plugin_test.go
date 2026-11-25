package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func ts(y int, m time.Month, d, h, min int) int64 {
	return time.Date(y, m, d, h, min, 0, 0, time.UTC).Unix()
}

func TestNYOffset(t *testing.T) {
	cases := []struct {
		utc  int64
		want int64
	}{
		{ts(2026, 1, 15, 12, 0), -5 * 3600},
		{ts(2026, 7, 15, 12, 0), -4 * 3600},
		{ts(2026, 3, 8, 6, 59), -5 * 3600}, // DST 2026: Sun 8 March 07:00 UTC ...
		{ts(2026, 3, 8, 7, 0), -4 * 3600},
		{ts(2026, 11, 1, 5, 59), -4 * 3600}, // ... to Sun 1 November 06:00 UTC
		{ts(2026, 11, 1, 6, 0), -5 * 3600},
	}
	for _, c := range cases {
		if got := nyOffset(c.utc); got != c.want {
			t.Errorf("nyOffset(%v) = %d, want %d", time.Unix(c.utc, 0).UTC(), got, c.want)
		}
	}
}

func TestTradingDay(t *testing.T) {
	// Winter: 17:00 New York = 22:00 UTC starts the next trading day.
	if got, want := tradingDay(ts(2026, 11, 24, 21, 59)), ts(2026, 11, 24, 5, 0); got != want {
		t.Errorf("Tue 16:59 NY -> %v, want Tue", time.Unix(got, 0).UTC())
	}
	if got, want := tradingDay(ts(2026, 11, 24, 22, 0)), ts(2026, 11, 25, 5, 0); got != want {
		t.Errorf("Tue 17:00 NY -> %v, want Wed", time.Unix(got, 0).UTC())
	}
	// Summer: 17:00 New York = 21:00 UTC.
	if got, want := tradingDay(ts(2026, 7, 14, 21, 0)), ts(2026, 7, 15, 4, 0); got != want {
		t.Errorf("summer -> %v", time.Unix(got, 0).UTC())
	}
}

// ── fake OANDA ──

type fakeOanda struct {
	mu      sync.Mutex
	streams []string // instruments of each pricing stream opened
	now     int64
	slow    time.Duration // the instrument list answers this late
}

func (f *fakeOanda) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer good" {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"errorMessage":"Insufficient authorization to perform request."}`)
		return
	}
	q := r.URL.Query()
	switch {
	case r.URL.Path == "/v3/accounts":
		fmt.Fprint(w, `{"accounts":[{"id":"101-001-1-001"}]}`)
	case strings.HasSuffix(r.URL.Path, "/instruments"):
		time.Sleep(f.slow)
		fmt.Fprint(w, `{"instruments":[
			{"name":"EUR_USD","type":"CURRENCY","displayName":"EUR/USD","displayPrecision":5},
			{"name":"USD_JPY","type":"CURRENCY","displayName":"USD/JPY","displayPrecision":3},
			{"name":"EUR_NOK","type":"CURRENCY","displayName":"EUR/NOK","displayPrecision":5},
			{"name":"XAU_USD","type":"METAL","displayName":"Gold","displayPrecision":3}]}`)
	case strings.Contains(r.URL.Path, "/candles"):
		st := map[string]int64{"M1": 60, "M5": 300, "H1": 3600, "D": 86400}[q.Get("granularity")]
		if q.Get("price") != "B" {
			w.WriteHeader(400)
			return
		}
		to := f.now
		if s := q.Get("to"); s != "" {
			if !strings.HasSuffix(s, ".000000000") {
				w.WriteHeader(400)
				return
			}
			to, _ = strconv.ParseInt(strings.TrimSuffix(s, ".000000000"), 10, 64)
			if to > f.now+1 {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"errorMessage":"Invalid value specified for 'to'. Time is in the future"}`)
				return
			}
		}
		n, _ := strconv.Atoi(q.Get("count"))
		if n == 0 {
			n = 500
		}
		var last int64
		if st == 86400 { // daily candles open at 17:00 New York
			last = tradingDay(to) - 7*3600
			if last > to {
				last -= 86400
			}
		} else {
			last = to / st * st
		}
		parts := []string{}
		for i := n - 1; i >= 0; i-- {
			parts = append(parts, fmt.Sprintf(`{"time":"%d.000000000","volume":10,"complete":true,"bid":{"o":"1.10000","h":"1.20000","l":"1.00000","c":"%.5f"}}`,
				last-int64(i)*st, 1.1+float64(i)/1000))
		}
		fmt.Fprintf(w, `{"instrument":"x","granularity":"%s","candles":[%s]}`, q.Get("granularity"), strings.Join(parts, ","))
	case strings.HasSuffix(r.URL.Path, "/pricing/stream"):
		f.mu.Lock()
		f.streams = append(f.streams, q.Get("instruments"))
		f.mu.Unlock()
		fl := w.(http.Flusher)
		for i := 0; i < 100; i++ {
			for _, name := range strings.Split(q.Get("instruments"), ",") {
				fmt.Fprintf(w, `{"type":"PRICE","instrument":"%s","time":"%d.123456789","bids":[{"price":"%.5f","liquidity":1000000}],"asks":[{"price":"1.2"}]}`+"\n",
					name, time.Now().Unix(), 1.15+float64(i)/10000)
			}
			fmt.Fprint(w, `{"type":"HEARTBEAT","time":"1"}`+"\n")
			fl.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	default:
		w.WriteHeader(404)
	}
}

func (f *fakeOanda) lastStream() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.streams) == 0 {
		return ""
	}
	return f.streams[len(f.streams)-1]
}

// ── fake OpenTrader ──

type fakeApp struct {
	t       *testing.T
	conn    net.Conn
	mu      sync.Mutex
	notes   []message
	answers map[string]message
}

func startPlugin(t *testing.T, token string) *fakeApp {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := newPlugin()
	go func() { _ = p.runOnce(ln.Addr().String(), "oanda", "tok") }()
	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(); ln.Close() })
	a := &fakeApp{t: t, conn: conn, answers: map[string]message{}}
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	if !sc.Scan() {
		t.Fatal("no hello")
	}
	var hello struct {
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	_ = json.Unmarshal(sc.Bytes(), &hello)
	if hello.Method != "hello" || hello.Params["plugin"] != "oanda" || hello.Params["token"] != "tok" {
		t.Fatalf("bad hello %s", sc.Text())
	}
	a.send(`{"jsonrpc":"2.0","id":1,"result":{"app":"OpenTrader","api":8}}`)
	a.send(fmt.Sprintf(`{"jsonrpc":"2.0","method":"plugin.configure","params":{"settings":{"token":%q,"environment":"practice","account":""},"provides":["datafeed"]}}`, token))
	go func() {
		for sc.Scan() {
			var m message
			_ = json.Unmarshal(sc.Bytes(), &m)
			a.mu.Lock()
			if m.Method == "" {
				a.answers[string(m.ID)] = m
			} else {
				a.notes = append(a.notes, m)
			}
			a.mu.Unlock()
		}
	}()
	return a
}

func (a *fakeApp) send(line string) {
	if _, err := a.conn.Write([]byte(line + "\n")); err != nil {
		a.t.Fatal(err)
	}
}

func (a *fakeApp) request(id int, method, params string) message {
	a.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`, id, method, params))
	key := strconv.Itoa(id)
	var m message
	waitFor(a.t, 5*time.Second, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		var ok bool
		m, ok = a.answers[key]
		return ok
	}, "answer to "+method)
	return m
}

func (a *fakeApp) notesOf(method string) []json.RawMessage {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []json.RawMessage{}
	for _, n := range a.notes {
		if n.Method == method {
			out = append(out, n.Params)
		}
	}
	return out
}

func waitFor(t *testing.T, d time.Duration, cond func() bool, what string) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func errCode(m message) int {
	if m.Error == nil {
		return 0
	}
	return m.Error.Code
}

func withFake(t *testing.T) *fakeOanda {
	f := &fakeOanda{now: time.Now().Unix()}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	old := hosts["practice"]
	hosts["practice"] = [2]string{srv.URL, srv.URL}
	t.Cleanup(func() { hosts["practice"] = old })
	return f
}

func TestBadToken(t *testing.T) {
	withFake(t)
	a := startPlugin(t, "bad")
	waitFor(t, 5*time.Second, func() bool {
		for _, n := range a.notesOf("log") {
			if strings.Contains(string(n), "refused the API token") {
				return true
			}
		}
		return false
	}, "token error in log")
	if len(a.notesOf("plugin.toast")) == 0 {
		t.Error("no toast for the bad token")
	}
	m := a.request(5, "data.symbolInfo", `{"ticker":"EURUSD"}`)
	if m.Error == nil || errCode(m) == -32004 || !strings.Contains(m.Error.Message, "token") {
		t.Errorf("symbolInfo before load: %+v (must not be -32004)", m.Error)
	}
}

func TestNoToken(t *testing.T) {
	withFake(t)
	a := startPlugin(t, "")
	waitFor(t, 5*time.Second, func() bool { return len(a.notesOf("plugin.toast")) > 0 }, "toast asking for a token")
	if !strings.Contains(string(a.notesOf("plugin.toast")[0]), "Add your OANDA API token") {
		t.Error(string(a.notesOf("plugin.toast")[0]))
	}
}

func TestFlow(t *testing.T) {
	f := withFake(t)
	a := startPlugin(t, "good")
	waitFor(t, 5*time.Second, func() bool { return len(a.notesOf("data.watchlists")) > 0 }, "watchlists")
	var wl struct {
		Watchlists []struct {
			ID       string   `json:"id"`
			Symbols  []string `json:"symbols"`
			Sections []struct {
				Name    string   `json:"name"`
				Symbols []string `json:"symbols"`
			} `json:"sections"`
		} `json:"watchlists"`
	}
	_ = json.Unmarshal(a.notesOf("data.watchlists")[0], &wl)
	if fmt.Sprint(wl.Watchlists[0].Symbols) != "[EURUSD USDJPY]" || fmt.Sprint(wl.Watchlists[1].Sections[2].Symbols) != "[EURNOK]" ||
		wl.Watchlists[2].ID != "metals" {
		t.Errorf("watchlists %+v", wl)
	}
	if len(a.notesOf("data.symbolInfoChanged")) == 0 {
		t.Error("no symbolInfoChanged after loading (app API 8)")
	}

	var search struct {
		Result struct {
			Symbols []map[string]string `json:"symbols"`
		}
	}
	m := a.request(10, "data.search", `{"query":"eur/"}`)
	_ = json.Unmarshal(m.Result, &search.Result)
	if len(search.Result.Symbols) != 2 || search.Result.Symbols[0]["ticker"] != "EURUSD" || search.Result.Symbols[1]["ticker"] != "EURNOK" {
		t.Errorf("search %s", m.Result)
	}

	var info map[string]any
	_ = json.Unmarshal(a.request(11, "data.symbolInfo", `{"ticker":"USDJPY"}`).Result, &info)
	if info["pricescale"] != 1000.0 || info["currency"] != "JPY" || info["type"] != "forex" || info["session"] != "1700-1700:23456" {
		t.Errorf("info %v", info)
	}
	if c := errCode(a.request(12, "data.symbolInfo", `{"ticker":"NOPE"}`)); c != -32004 {
		t.Errorf("unknown symbol code %d", c)
	}

	now := f.now
	var bars struct {
		Bars [][6]float64 `json:"bars"`
	}
	m = a.request(13, "data.bars", fmt.Sprintf(`{"ticker":"EURUSD","resolution":"5","from":%d,"to":%d,"countBack":5}`, now-3600, now+86400))
	if m.Error != nil {
		t.Fatal(m.Error.Message)
	}
	_ = json.Unmarshal(m.Result, &bars)
	if len(bars.Bars) != 5 {
		t.Fatalf("5m bars: %d", len(bars.Bars))
	}
	for i, b := range bars.Bars {
		if int64(b[0])%300 != 0 || int64(b[0]) > now || (i > 0 && b[0] <= bars.Bars[i-1][0]) {
			t.Errorf("bad 5m bar %v", b)
		}
	}

	m = a.request(14, "data.bars", fmt.Sprintf(`{"ticker":"EURUSD","resolution":"1D","from":%d,"to":%d,"countBack":10}`, now-40*86400, now+86400))
	if m.Error != nil {
		t.Fatal(m.Error.Message)
	}
	_ = json.Unmarshal(m.Result, &bars)
	if len(bars.Bars) != 10 {
		t.Fatalf("daily bars: %d", len(bars.Bars))
	}
	for _, b := range bars.Bars {
		bt := int64(b[0])
		if (bt+nyOffset(bt+5*3600))%86400 != 0 {
			t.Errorf("daily bar not at New York midnight: %v", time.Unix(bt, 0).UTC())
		}
	}
	if got := int64(bars.Bars[9][0]); got != tradingDay(now) {
		t.Errorf("last daily bar %v, want today's trading day %v", time.Unix(got, 0).UTC(), time.Unix(tradingDay(now), 0).UTC())
	}
	if c := errCode(a.request(15, "data.bars", `{"ticker":"EURUSD","resolution":"1W","from":0,"to":1,"countBack":5}`)); c != -32602 {
		t.Errorf("1W code %d", c)
	}

	// Live quotes for the subscribed tickers.
	a.send(`{"jsonrpc":"2.0","method":"data.subscribe","params":{"tickers":["EURUSD","XAUUSD","IBKR_NOT_OURS"]}}`)
	type quote struct {
		Ticker    string     `json:"ticker"`
		Time      int64      `json:"time"`
		Price     float64    `json:"price"`
		PrevClose float64    `json:"prevClose"`
		Minute    [6]float64 `json:"minute"`
		Day       [6]float64 `json:"day"`
	}
	quotes := func() []quote {
		out := []quote{}
		for _, n := range a.notesOf("data.quote") {
			var q quote
			_ = json.Unmarshal(n, &q)
			out = append(out, q)
		}
		return out
	}
	waitFor(t, 5*time.Second, func() bool {
		seen := map[string]int{}
		for _, q := range quotes() {
			seen[q.Ticker]++
		}
		return seen["EURUSD"] > 3 && seen["XAUUSD"] > 3
	}, "live quotes for both tickers")
	qs := quotes()
	q := qs[len(qs)-1]
	if q.Price < 1.15 || q.PrevClose == 0 || int64(q.Minute[0]) != q.Time/60*60 || int64(q.Day[0]) != tradingDay(q.Time) || q.Day[2] < q.Price {
		t.Errorf("quote %+v", q)
	}
	if s := f.lastStream(); s != "EUR_USD,XAU_USD" {
		t.Errorf("stream instruments %q", s)
	}
	// Quotes are throttled to 4 a second per ticker.
	n0 := len(quotes())
	time.Sleep(time.Second)
	if n := len(quotes()) - n0; n > 10 {
		t.Errorf("%d quotes in a second for 2 tickers", n)
	}

	// A new set switches the stream at once.
	a.send(`{"jsonrpc":"2.0","method":"data.subscribe","params":{"tickers":["USDJPY"]}}`)
	waitFor(t, 3*time.Second, func() bool { return f.lastStream() == "USD_JPY" }, "stream switched to USD_JPY")
	waitFor(t, 3*time.Second, func() bool {
		for _, q := range quotes() {
			if q.Ticker == "USDJPY" {
				return true
			}
		}
		return false
	}, "USDJPY quotes")
}

// At start the app asks for the chart on screen before the instruments are loaded: the request waits for them
// instead of failing (it left the chart with one bar until the interval was changed).
func TestBarsAskedWhileLoading(t *testing.T) {
	f := withFake(t)
	f.slow = 700 * time.Millisecond
	a := startPlugin(t, "good")
	now := f.now
	m := a.request(20, "data.bars", fmt.Sprintf(`{"ticker":"EURUSD","resolution":"1D","from":%d,"to":%d,"countBack":10}`, now-40*86400, now+86400))
	if m.Error != nil {
		t.Fatalf("bars asked while loading: %s", m.Error.Message)
	}
	var info map[string]any
	_ = json.Unmarshal(a.request(21, "data.symbolInfo", `{"ticker":"EURUSD"}`).Result, &info)
	if info["pricescale"] != 100000.0 {
		t.Errorf("symbol info %v", info)
	}
}

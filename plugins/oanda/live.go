package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Live streams OANDA prices for the subscribed tickers and turns them into data.quote messages.
type Live struct {
	p        *Plugin
	mu       sync.Mutex
	wanted   map[string]bool
	version  int
	state    map[string]*quoteState
	lastSent map[string]time.Time
	wake     chan struct{}
	current  io.Closer // the open stream's body
}

type quoteState struct {
	minute    *Bar
	day       Bar
	prevClose float64
	price     float64
	time      int64
}

func newLive(p *Plugin) *Live {
	return &Live{p: p, wanted: map[string]bool{}, state: map[string]*quoteState{}, lastSent: map[string]time.Time{}, wake: make(chan struct{}, 1)}
}

func (l *Live) poke() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

func (l *Live) set(tickers []string) {
	l.mu.Lock()
	l.wanted = map[string]bool{}
	for _, t := range tickers {
		l.wanted[t] = true
	}
	l.restartLocked()
	l.mu.Unlock()
	l.poke()
}

// reset drops what is kept (the account changed) and restarts the stream.
func (l *Live) reset() {
	l.mu.Lock()
	l.state = map[string]*quoteState{}
	l.restartLocked()
	l.mu.Unlock()
	l.poke()
}

func (l *Live) restartLocked() {
	l.version++
	if l.current != nil {
		l.current.Close()
		l.current = nil
	}
}

func (l *Live) wait(d time.Duration) {
	select {
	case <-l.wake:
	case <-time.After(d):
	}
}

// seed reads the forming minute and day bars and the previous close, before the stream takes over.
func (l *Live) seed(t string, i Instrument) error {
	src := l.p.src
	days, err := src.candles(i, "D", 0, 2)
	if err != nil || len(days) == 0 {
		return err
	}
	mins, err := src.candles(i, "M1", 0, 1)
	if err != nil {
		return err
	}
	st := &quoteState{day: days[len(days)-1], price: days[len(days)-1][4], time: time.Now().Unix()}
	st.prevClose = st.day[1]
	if len(days) > 1 {
		st.prevClose = days[len(days)-2][4]
	}
	if len(mins) > 0 {
		m := mins[len(mins)-1]
		st.minute = &m
	}
	l.mu.Lock()
	l.state[t] = st
	l.mu.Unlock()
	return nil
}

func (l *Live) onPrice(t string, price float64, ts int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.state[t]
	if st == nil {
		return false
	}
	st.price, st.time = price, ts
	m0 := float64(ts / 60 * 60)
	if st.minute == nil || st.minute[0] < m0 {
		st.minute = &Bar{m0, price, price, price, price, 0}
	}
	if st.minute[0] == m0 {
		m := st.minute
		m[2], m[3], m[4], m[5] = max(m[2], price), min(m[3], price), price, m[5]+1
	}
	d0 := float64(tradingDay(ts))
	if st.day[0] < d0 {
		st.prevClose = st.day[4]
		st.day = Bar{d0, price, price, price, price, 0}
	}
	if st.day[0] == d0 {
		d := &st.day
		d[2], d[3], d[4], d[5] = max(d[2], price), min(d[3], price), price, d[5]+1
	}
	return true
}

func (l *Live) send(t string, force bool) {
	l.mu.Lock()
	st := l.state[t]
	if st == nil || (!force && time.Since(l.lastSent[t]) < 250*time.Millisecond) {
		l.mu.Unlock()
		return
	}
	l.lastSent[t] = time.Now()
	q := map[string]any{"ticker": t, "time": st.time, "price": st.price, "prevClose": st.prevClose, "day": st.day}
	if st.minute != nil {
		q["minute"] = *st.minute
	}
	l.mu.Unlock()
	l.p.notify("data.quote", q)
}

func (l *Live) run() {
	backoff := time.Second
	for l.p.alive.Load() {
		l.mu.Lock()
		version := l.version
		wanted := make([]string, 0, len(l.wanted))
		for t := range l.wanted {
			wanted = append(wanted, t)
		}
		l.mu.Unlock()
		sort.Strings(wanted)
		if len(wanted) == 0 {
			l.wait(2 * time.Second)
			continue
		}
		if err := l.streamOnce(version, wanted); err != nil {
			l.p.log("warn", "Live prices: "+err.Error())
			l.wait(backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
	}
}

func (l *Live) streamOnce(version int, tickers []string) error {
	src := l.p.src
	byName := map[string]string{}
	names := []string{}
	// The tickers not seen yet are seeded 8 at a time: a 68-pair watchlist fills in seconds.
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	slots := make(chan struct{}, 8)
	for _, t := range tickers {
		i, err := src.inst(t)
		if err != nil {
			continue
		}
		byName[i.Name] = t
		names = append(names, i.Name)
		l.mu.Lock()
		_, seeded := l.state[t]
		l.mu.Unlock()
		if seeded {
			continue
		}
		wg.Add(1)
		slots <- struct{}{}
		go func(t string, i Instrument) {
			defer func() { <-slots; wg.Done() }()
			if err := l.seed(t, i); err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
				return
			}
			l.send(t, true)
		}(t, i)
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	if len(names) == 0 { // instruments not loaded yet, or none of the tickers is OANDA's
		l.wait(2 * time.Second)
		return nil
	}
	acct, err := src.oanda.accountID()
	if err != nil {
		return err
	}
	resp, err := src.oanda.open("/v3/accounts/"+acct+"/pricing/stream", url.Values{"instruments": {strings.Join(names, ",")}}, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// A new set of tickers closes this stream at once, so the next one starts with it.
	l.mu.Lock()
	if l.version != version {
		l.mu.Unlock()
		return nil
	}
	l.current = resp.Body
	l.mu.Unlock()
	defer func() { l.mu.Lock(); l.current = nil; l.mu.Unlock() }()
	// OANDA sends a heartbeat every 5 seconds: no line for 20 seconds means the stream is dead.
	watchdog := time.AfterFunc(20*time.Second, func() { resp.Body.Close() })
	defer watchdog.Stop()
	r := bufio.NewReaderSize(resp.Body, 64<<10)
	for l.p.alive.Load() {
		line, err := r.ReadBytes('\n')
		if err != nil {
			l.mu.Lock()
			changed := l.version != version
			l.mu.Unlock()
			if changed || !l.p.alive.Load() {
				return nil
			}
			return perr("stream dropped (" + err.Error() + "), reconnecting")
		}
		watchdog.Reset(20 * time.Second)
		l.mu.Lock()
		changed := l.version != version
		l.mu.Unlock()
		if changed {
			return nil
		}
		var msg struct {
			Type       string `json:"type"`
			Instrument string `json:"instrument"`
			Time       string `json:"time"`
			Bids       []struct {
				Price string `json:"price"`
			} `json:"bids"`
		}
		if json.Unmarshal(line, &msg) != nil || msg.Type != "PRICE" || len(msg.Bids) == 0 {
			continue
		}
		t, ok := byName[msg.Instrument]
		if !ok {
			continue
		}
		if l.onPrice(t, pf(msg.Bids[0].Price), int64(pf(msg.Time))) {
			l.send(t, false)
		}
	}
	return nil
}

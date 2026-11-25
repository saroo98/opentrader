// OpenTrader data plugin: live OANDA prices (forex, metals, CFDs) through the OANDA v20 API.
//
// Symbols show as OANDA:EURUSD, OANDA:XAUUSD, ...; prices are the bid, like MetaTrader charts. It needs an OANDA API
// token: a free demo (practice) account gives one with live prices. See plugins/PROTOCOL.md for the protocol.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	pluginID    = "oanda"
	version     = "0.1.1"
	apiVer      = 4 // password settings (3), ready-made watchlists (4)
	devConnFile = "OpenTraderPlugins/oanda-dev.json"
)

type Plugin struct {
	src    *Source
	live   *Live
	conn   net.Conn
	sendMu sync.Mutex
	alive  atomic.Bool
	appAPI int
}

func newPlugin() *Plugin {
	p := &Plugin{src: newSource()}
	p.live = newLive(p)
	return p
}

func (p *Plugin) send(m any) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	p.sendMu.Lock()
	defer p.sendMu.Unlock()
	_, err = p.conn.Write(append(b, '\n'))
	return err
}

func (p *Plugin) notify(method string, params any) {
	if p.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params}) != nil {
		p.alive.Store(false)
	}
}

func (p *Plugin) log(level, msg string) {
	if p.alive.Load() {
		p.notify("log", map[string]string{"level": level, "message": msg})
	} else {
		fmt.Println(msg)
	}
}

func (p *Plugin) toast(level, msg string) {
	if p.alive.Load() && p.appAPI >= 8 {
		p.notify("plugin.toast", map[string]any{"level": level, "message": msg})
	}
}

// loadInstruments loads the account's instruments, retrying until it works or the settings change.
func (p *Plugin) loadInstruments(gen int) {
	s := p.src
	delay := 2 * time.Second
	for p.alive.Load() {
		s.mu.RLock()
		stale, loaded := s.gen != gen, s.loaded
		s.mu.RUnlock()
		if stale || loaded {
			return
		}
		if tok, _ := s.oanda.creds(); tok == "" {
			s.mu.Lock()
			s.errMsg = tokenMissing
			s.mu.Unlock()
			p.log("warn", tokenMissing)
			p.toast("warn", tokenMissing)
			return
		}
		n, err := s.load()
		if err != nil {
			msg := err.Error()
			s.mu.Lock()
			s.errMsg = msg
			s.mu.Unlock()
			p.log("error", msg)
			p.toast("error", msg)
			if strings.Contains(msg, "token") {
				return
			}
			time.Sleep(delay)
			if delay < time.Minute {
				delay *= 2
			}
			continue
		}
		_, env := s.oanda.creds()
		acct, _ := s.oanda.accountID()
		p.log("info", fmt.Sprintf("OANDA %s account %s: %d instruments", env, acct, n))
		p.notify("data.watchlists", map[string]any{"watchlists": s.watchlists()})
		if p.appAPI >= 8 {
			p.notify("data.symbolInfoChanged", map[string]any{})
		}
		p.live.reset()
		return
	}
}

func intParam(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

func str(v any) string { s, _ := v.(string); return s }

func (p *Plugin) handle(method string, params map[string]any) (any, error) {
	switch method {
	case "data.search":
		return map[string]any{"symbols": p.src.search(str(params["query"]))}, nil
	case "data.symbolInfo":
		return p.src.symbolInfo(str(params["ticker"]))
	case "data.bars":
		count := int(intParam(params["countBack"]))
		bars, err := p.src.bars(str(params["ticker"]), str(params["resolution"]), intParam(params["from"]), intParam(params["to"]), count)
		if err != nil {
			return nil, err
		}
		return map[string]any{"bars": bars}, nil
	}
	return nil, &PluginError{-32601, "Unknown method " + method}
}

type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (p *Plugin) onRequest(m message) {
	params := map[string]any{}
	_ = json.Unmarshal(m.Params, &params)
	result, err := p.handle(m.Method, params)
	if err != nil {
		pe := asPluginError(err)
		_ = p.send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": pe.Code, "message": pe.Msg}})
		return
	}
	_ = p.send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
}

func (p *Plugin) onNotification(m message) {
	switch m.Method {
	case "plugin.configure":
		var c struct {
			Settings map[string]string `json:"settings"`
		}
		_ = json.Unmarshal(m.Params, &c)
		if p.src.configure(c.Settings) {
			p.src.mu.RLock()
			gen := p.src.gen
			p.src.mu.RUnlock()
			p.live.reset()
			go p.loadInstruments(gen)
		}
	case "data.subscribe":
		var s struct {
			Tickers []string `json:"tickers"`
		}
		_ = json.Unmarshal(m.Params, &s)
		p.live.set(s.Tickers)
	}
}

func localAppData() string {
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		return v
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".local", "share")
}

// connection: from the environment when the app started us, else the connection file (development).
func connection() (addr, plugin, token string, err error) {
	if a := os.Getenv("OPENTRADER_PLUGIN_ADDR"); a != "" {
		return a, os.Getenv("OPENTRADER_PLUGIN_ID"), os.Getenv("OPENTRADER_PLUGIN_TOKEN"), nil
	}
	path := filepath.Join(localAppData(), devConnFile)
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", "", err
	}
	var c struct {
		Host   string `json:"host"`
		Port   int    `json:"port"`
		Plugin string `json:"plugin"`
		Token  string `json:"token"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return "", "", "", err
	}
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port)), c.Plugin, c.Token, nil
}

func (p *Plugin) runOnce(addr, plugin, token string) error {
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	p.conn = conn
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	if err := p.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "hello",
		"params": map[string]any{"plugin": plugin, "token": token, "api": apiVer, "version": version}}); err != nil {
		return err
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if !sc.Scan() {
		return fmt.Errorf("no answer to hello")
	}
	_ = conn.SetReadDeadline(time.Time{})
	var welcome message
	if err := json.Unmarshal(sc.Bytes(), &welcome); err != nil {
		return err
	}
	if welcome.Error != nil {
		return fmt.Errorf("refused: %s", welcome.Error.Message)
	}
	var res struct {
		App string `json:"app"`
		API int    `json:"api"`
	}
	_ = json.Unmarshal(welcome.Result, &res)
	p.appAPI = res.API
	p.alive.Store(true)
	p.log("info", fmt.Sprintf("Connected to %s (Plugin API %d), plugin %s", res.App, res.API, version))
	go p.live.run()
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var m message
		if json.Unmarshal(line, &m) != nil || m.Method == "" {
			continue
		}
		if len(m.ID) > 0 && string(m.ID) != "null" {
			go p.onRequest(m) // a slow history request must not hold up the others
		} else {
			p.onNotification(m)
		}
	}
	p.alive.Store(false)
	p.live.poke()
	return sc.Err()
}

func main() {
	startedByApp := os.Getenv("OPENTRADER_PLUGIN_ADDR") != ""
	for {
		p := newPlugin()
		addr, plugin, token, err := connection()
		if err == nil {
			err = p.runOnce(addr, plugin, token)
		}
		if err != nil {
			fmt.Println("Not connected:", err)
		} else {
			fmt.Println("The app closed the connection")
		}
		if startedByApp {
			os.Exit(1)
		}
		time.Sleep(3 * time.Second)
	}
}

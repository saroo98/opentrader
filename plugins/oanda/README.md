# OANDA plugin

Live forex prices in OpenTrader for free: every forex pair, plus metals and CFDs, from an OANDA **demo** account.
Symbols show as `OANDA:EURUSD`, `OANDA:XAUUSD`… No MetaTrader or other program needs to run.

- Prices are the bid, like MetaTrader charts. Bars: 1, 5, 15, 30, 60 minutes and days; OpenTrader builds 4 hours,
  weeks and months. Daily bars close at 17:00 New York, the usual forex close.
- Ready-made watchlists: *Forex majors*, *All forex* (majors, crosses, exotics), *Metals*, *CFDs*.
- Read-only: it shows prices, it cannot trade.

## Set it up (about 5 minutes)

**1. Install OpenTrader** (skip if you have it): download the
[Windows installer](https://github.com/deepentropy/opentrader/releases/latest/download/OpenTrader_windows_x64-setup.exe)
and run it. If Windows says "Windows protected your PC", click *More info*, then *Run anyway*.

**2. Get a free OANDA API token**
1. Open a free **demo** account at [oanda.com](https://www.oanda.com/) (email only, no deposit, no ID).
2. Log in to the OANDA hub, open **Tools > API** (*Manage API Access*) and click **Generate**.
3. Copy the token. Keep it to yourself: it gives access to your OANDA account.

**3. Install the plugin**
1. Download **oanda-0.1.1.zip** from the
   [plugin's release](https://github.com/saroo98/opentrader/releases/tag/plugin-oanda-v0.1.1). Don't unzip it.
2. In OpenTrader: click the **T** menu at the top left > **App settings… > Plugins > Install from file…** and pick
   the zip.
3. OpenTrader says the package is not signed by OpenTrader (it is built here, not by the OpenTrader team): click
   **Install anyway**.
4. Tick **Enabled**, click **Settings**, paste the token in **API token**, keep **Account: Demo (practice)**, click
   **Apply**.

The plugin shows **Connected** within a few seconds.

**4. Use it**
- **One pair:** click the symbol box at the top left, type `OANDA:EURUSD` and pick it.
- **All pairs at once:** in the watchlist on the right, click the list name > **Open list… > OANDA > All forex**.

## If something is wrong

Settings > Plugins > OANDA > **Log** says what happened:

| Log says | Do this |
|---|---|
| Add your OANDA API token… | Paste the token in the plugin's Settings and Apply. |
| OANDA refused the API token (practice account) | The token is wrong or revoked, or it belongs to a live account: generate a new one, or set Account to Live. |
| Cannot reach OANDA | Check the internet connection or a firewall / VPN blocking `api-fxpractice.oanda.com`. |

If the chart title or the A / L buttons at the bottom of the price scale are invisible, the chart background and the
app theme don't match: use a light theme with a light chart (Settings > General > Theme), or a dark one with a dark
chart.

## Build it yourself

Needs [Go](https://go.dev/dl/) 1.22 or newer; the plugin uses its standard library only.

```powershell
cd plugins/oanda
./build.ps1        # runs the tests, writes dist/oanda-<version>.zip
```

To publish a new version: raise `version` in `opentrader-plugin.json` and in `main.go`, build, then
`gh release create plugin-oanda-v<version> dist/oanda-<version>.zip`.

| File | |
|---|---|
| `main.go` | Connection to OpenTrader (JSON-RPC over the local plugin socket, see `../PROTOCOL.md`) |
| `oanda.go` | OANDA v20 REST: account, instruments, candles, search, symbol info, watchlists |
| `live.go` | One OANDA pricing stream for the symbols on screen → `data.quote` |
| `nytime.go` | New York time and forex trading days, without a time zone database |
| `plugin_test.go` | End-to-end tests against a fake OANDA server and a fake OpenTrader |

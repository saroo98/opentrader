# SARO chart template

A light chart in the style of a TradingView setup: light grey background, white up candles and black down candles
with black borders and wicks, faint dashed grid, grey dashed crosshair, black scale text, bid and ask lines, New York
time, no watermark. It is added to OpenTrader's template list, next to your own templates. It does not replace the
default: you pick it when you want it.

## Add it

1. Close OpenTrader.
2. Open **PowerShell** (Start menu > type `powershell` > Enter) and paste:

   ```powershell
   irm https://raw.githubusercontent.com/saroo98/opentrader/main/templates/saro/install.ps1 | iex
   ```

3. Open OpenTrader. On a chart: **gear (Chart settings) > Template > SARO**, then **Ok** for this chart or **Apply
   to all** for every chart. Or right-click the chart > **Chart template > SARO**.

Running it again replaces SARO with the latest version; your other templates and settings are kept. A copy of the
settings file is saved first, next to it (`%APPDATA%\com.opentrader.app\opentrader.json.before-saro-<date>`).

**Use the Light theme with it** (Settings > General > Theme > Light): on the Dark theme the chart title and the A / L
buttons are drawn in white, invisible on a light chart.

## Files

- `saro.json`: the template, as OpenTrader saves one (Template > Save as…).
- `install.ps1`: adds it to the template list in OpenTrader's settings file.

To change the template: apply it in OpenTrader, change what you want, Template > Save as… `SARO`, then copy the
`SARO` entry of `ot:chart-settings-templates` in `%APPDATA%\com.opentrader.app\opentrader.json` into `saro.json`.

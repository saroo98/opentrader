# OpenTrader SARO

A build of OpenTrader from this fork, installed next to the official app (its own name, folder and settings).

## What is different

- **OANDA plugin included**: installed and turned on at the first start. Paste your OANDA token in Settings >
  Plugins > OANDA > Settings and every forex pair, metal and CFD of your OANDA account is live (`OANDA:EURUSD`…).
  Getting a free token: [plugins/oanda/README.md](plugins/oanda/README.md).
- **SARO chart template** in the template list (chart Settings > Template, or right-click the chart > Chart
  template). Not the default: pick it when you want it.
- **Light theme** by default (App settings > General > Theme still offers System and Dark).
- **Fixes**:
  - The chart title, the A / L buttons and the currency label follow the chart's own background: they were invisible
    on a light chart under the dark theme (and the other way round).
  - After changing symbol, interval or session, the price scale fits the new bars once, also with auto scale off: a
    range kept from another interval left them as a flat line.
- **No automatic updates**: the official updates would turn it back into the official app. Install a newer OpenTrader
  SARO over it instead; settings are kept.
- Coding agents connect on port 47831 and plugins on 47832 (the official app uses 47821 / 47822), so both apps can
  run at the same time.

## What it does not have

OpenTrader's built-in market data (US stocks, funds, economic series) goes through the OpenTrader gateway, whose key
is only in the official builds. In this build those symbols show no data. Forex, metals and CFDs come from the OANDA
plugin; the MetaTrader 5 and Interactive Brokers plugins of Settings > Plugins work too.

## Build it

As in the [README](README.md) (Rust, Node.js 22+, Tauri prerequisites, the drawing core cloned next to this folder):

```powershell
npm ci
cd src-tauri; cargo test --lib export_bindings; cd ..
$env:NODE_OPTIONS = '--max-old-space-size=8192'   # the frontend build needs more than Node's default memory
npm run tauri build -- --no-sign
```

The installer is `src-tauri/target/release/bundle/nsis/OpenTrader SARO_<version>_x64-setup.exe`.
`src-tauri/bundled/oanda.zip` is the OANDA plugin package (`plugins/oanda/build.ps1`); rebuild it after changing the
plugin.

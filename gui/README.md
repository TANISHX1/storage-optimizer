# GUI Application Shell (Wails & Web Dashboard)

This directory contains the desktop frontend and native Go application wrapper for the **Intelligent Storage Optimizer**.

---

## 1. Architecture: Lightweight Go Native Shell (Wails v2)

We use **Wails v2** to package the application as a lightweight, native Linux desktop application:

* **Why Wails instead of Electron?**
  * **Electron** bundles a complete 200MB+ Chromium browser instance and consumes 500MB+ RAM just to render a UI.
  * **Wails** leverages Linux's native `webkit2gtk` system library. The compiled binary is small (~15-20MB), starts instantly, and uses minimal RAM (~30-50MB).
  * Go directly manages the desktop window, native GTK/KDE folder picker dialogs, system tray, and communicates seamlessly with the Go systems core and REST API.

---

## 2. Running the GUI

You have two convenient ways to run and use the GUI:

### Option A: Direct Web Dashboard via Go Core (Zero Extra Dependencies)
The Go core API server (`storage-optimizer serve`) automatically serves the production GUI dashboard at `http://127.0.0.1:8080/`:
```bash
# 1. Build the frontend
cd gui/frontend && npm run build

# 2. Start the core server
cd ../../go-core
./bin/storage-optimizer serve --port 8080

# 3. Open in browser
xdg-open http://127.0.0.1:8080/
```

### Option B: Native Desktop Window via Wails CLI
If you have the `wails` CLI installed (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`):
```bash
cd gui
wails dev    # Runs live reload desktop window
wails build  # Compiles native desktop binary (build/bin/storage-optimizer-gui)
```

---

## 3. Directory Layout

```
gui/
├── app.go             # Wails Go application lifecycle & native OS dialog bindings
├── main.go            # Wails native desktop window configuration & embedded assets
├── go.mod             # Go module for GUI wrapper
├── wails.json         # Wails project metadata and build scripts
└── frontend/          # High-performance Vanilla JS / HTML5 / CSS3 Design System
    ├── package.json   # Vite build scripts
    ├── index.html     # Semantic layout (Dashboard, Scanner, Duplicates, Stale, Forecast, Trash)
    ├── style.css      # Titanium Dark Theme, Glassmorphism, & Animation Tokens
    ├── app.js         # REST API Client, Canvas forecasting charts, & Modal action handlers
    └── dist/          # Production assets embedded into Go binary
```

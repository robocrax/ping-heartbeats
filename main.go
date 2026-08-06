package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type PingPoint struct {
	Timestamp string  `json:"timestamp"`
	LatencyMs float64 `json:"latency_ms"`
	Success   bool    `json:"success"`
}

type Monitor struct {
	ID              string      `json:"id"`
	Name            string      `json:"name"`
	IP              string      `json:"ip"`
	HeartbeatURL    string      `json:"heartbeat_url"`
	Status          string      `json:"status"` // "up", "down", "pending"
	LastChecked     time.Time   `json:"last_checked"`
	LastSuccess     time.Time   `json:"last_success"`
	LastWebhookSent time.Time   `json:"last_webhook_sent"`
	LastSentStatus  string      `json:"last_sent_status"`
	PingHistory     []PingPoint `json:"ping_history"`
	stopChan        chan struct{}
}

type App struct {
	mu       sync.Mutex
	Monitors map[string]*Monitor `json:"monitors"`
	dataDir  string
	dataFile string
	logFile  string
}

func NewApp(dataDir string) *App {
	_ = os.MkdirAll(dataDir, 0755)

	app := &App{
		Monitors: make(map[string]*Monitor),
		dataDir:  dataDir,
		dataFile: filepath.Join(dataDir, "monitors.json"),
		logFile:  filepath.Join(dataDir, "history.log"),
	}
	app.loadData()
	return app
}

func (a *App) appendHistoryLog(m *Monitor, point PingPoint) {
	statusStr := "UP"
	if !point.Success {
		statusStr = "DOWN"
	}

	entry := fmt.Sprintf("[%s] Monitor: %s (%s) | Status: %s | Latency: %.2fms | Target: %s\n",
		time.Now().Format(time.RFC3339),
		m.Name,
		m.IP,
		statusStr,
		point.LatencyMs,
		m.HeartbeatURL,
	)

	f, err := os.OpenFile(a.logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()

	_, _ = f.WriteString(entry)
}

func (a *App) loadData() {
	data, err := os.ReadFile(a.dataFile)
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &a.Monitors)
}

func (a *App) saveData() {
	data, _ := json.MarshalIndent(a.Monitors, "", "  ")
	_ = os.WriteFile(a.dataFile, data, 0644)
}

func (a *App) StartMonitor(m *Monitor) {
	m.stopChan = make(chan struct{})
	ticker := time.NewTicker(5 * time.Second)

	go func() {
		a.pingAndUpdate(m)
		for {
			select {
			case <-ticker.C:
				a.pingAndUpdate(m)
			case <-m.stopChan:
				ticker.Stop()
				return
			}
		}
	}()
}

var pingTimeRegex = regexp.MustCompile(`time=([0-9.]+)\s*ms`)

func (a *App) pingAndUpdate(m *Monitor) {
	cmd := exec.Command("ping", "-c", "1", "-W", "2", m.IP)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()

	now := time.Now()
	isUp := (err == nil)
	var latency float64 = 0

	if isUp {
		matches := pingTimeRegex.FindStringSubmatch(out.String())
		if len(matches) > 1 {
			latency, _ = strconv.ParseFloat(matches[1], 64)
		}
	}

	a.mu.Lock()
	m.LastChecked = now
	currentStatus := "down"
	if isUp {
		currentStatus = "up"
		m.LastSuccess = now
	}
	m.Status = currentStatus

	point := PingPoint{
		Timestamp: now.Format("15:04:05"),
		LatencyMs: latency,
		Success:   isUp,
	}

	m.PingHistory = append(m.PingHistory, point)
	if len(m.PingHistory) > 30 {
		m.PingHistory = m.PingHistory[len(m.PingHistory)-30:]
	}

	a.appendHistoryLog(m, point)

	shouldSend := false
	statusChanged := (m.LastSentStatus != currentStatus)
	timeSinceLastWebhook := now.Sub(m.LastWebhookSent)

	if statusChanged {
		shouldSend = true
	} else if timeSinceLastWebhook >= 60*time.Second {
		shouldSend = true
	}

	if shouldSend && m.HeartbeatURL != "" {
		m.LastWebhookSent = now
		m.LastSentStatus = currentStatus
		go triggerWebhook(m.HeartbeatURL, isUp)
	}

	a.saveData()
	a.mu.Unlock()
}

func triggerWebhook(baseURL string, isUp bool) {
	if baseURL == "" {
		return
	}
	targetURL := baseURL
	if !isUp {
		targetURL = strings.TrimRight(baseURL, "/") + "/fail"
	}

	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(targetURL)
	if err == nil && resp != nil {
		resp.Body.Close()
	}
}

func main() {
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "/data"
	}

	app := NewApp(dataDir)

	for _, m := range app.Monitors {
		app.StartMonitor(m)
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		tmpl := template.Must(template.New("index").Parse(htmlTemplate))
		tmpl.Execute(w, nil)
	})

	http.HandleFunc("/api/monitors", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		app.mu.Lock()
		defer app.mu.Unlock()

		if r.Method == http.MethodGet {
			list := make([]*Monitor, 0, len(app.Monitors))
			for _, m := range app.Monitors {
				list = append(list, m)
			}

			sort.Slice(list, func(i, j int) bool {
				return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
			})

			json.NewEncoder(w).Encode(list)
			return
		}

		if r.Method == http.MethodPost {
			var input Monitor
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if input.IP == "" || input.HeartbeatURL == "" {
				http.Error(w, "IP and Heartbeat URL are required", 400)
				return
			}
			if input.Name == "" {
				input.Name = input.IP
			}

			if input.ID != "" {
				if existing, ok := app.Monitors[input.ID]; ok {
					if existing.stopChan != nil {
						close(existing.stopChan)
					}
					existing.Name = input.Name
					existing.IP = input.IP
					existing.HeartbeatURL = input.HeartbeatURL
					app.saveData()
					app.StartMonitor(existing)
					json.NewEncoder(w).Encode(existing)
					return
				}
			}

			m := &Monitor{
				ID:           fmt.Sprintf("%d", time.Now().UnixNano()),
				Name:         input.Name,
				IP:           input.IP,
				HeartbeatURL: input.HeartbeatURL,
				Status:       "pending",
				PingHistory:  []PingPoint{},
			}

			app.Monitors[m.ID] = m
			app.saveData()
			app.StartMonitor(m)

			json.NewEncoder(w).Encode(m)
			return
		}
	})

	http.HandleFunc("/api/monitors/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			id := r.URL.Query().Get("id")
			app.mu.Lock()
			if m, ok := app.Monitors[id]; ok {
				if m.stopChan != nil {
					close(m.stopChan)
				}
				delete(app.Monitors, id)
				app.saveData()
			}
			app.mu.Unlock()
			w.WriteHeader(http.StatusOK)
		}
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	fmt.Printf("Heartbeats server running at http://0.0.0.0:%s (Data directory: %s)\n", port, dataDir)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

const htmlTemplate = `<!DOCTYPE html>
<html lang="en" class="dark h-full bg-slate-950 text-slate-100">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0, maximum-scale=1.0, user-scalable=no">
  <title>Heartbeats</title>
  <script src="https://cdn.tailwindcss.com"></script>
  <script>
    tailwind.config = {
      darkMode: 'class',
      theme: { extend: { colors: { darkCard: '#0f172a' } } }
    }
  </script>
  <style>
    @media screen and (max-width: 768px) {
      input, select, textarea {
        font-size: 16px !important;
      }
    }
    .touch-none { touch-action: none; }
  </style>
</head>
<body class="min-h-full flex flex-col font-sans antialiased bg-slate-950 text-slate-100">

  <!-- Top Navigation Bar -->
  <header class="border-b border-slate-800 bg-slate-900/80 backdrop-blur sticky top-0 z-10">
    <div class="max-w-6xl mx-auto px-6 h-16 flex items-center justify-between">
      <div class="flex items-center space-x-3">
        <span class="inline-block w-3 h-3 rounded-full bg-emerald-500 animate-pulse"></span>
        <h1 class="text-xl font-bold tracking-tight text-white">Heartbeats</h1>
      </div>
      <button onclick="openModal()" class="inline-flex items-center gap-2 bg-indigo-600 hover:bg-indigo-500 text-white font-medium px-4 py-2 rounded-lg text-sm transition">
        <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4"/></svg>
        New Monitor
      </button>
    </div>
  </header>

  <!-- Main Content Area -->
  <main class="flex-1 max-w-6xl w-full mx-auto p-6 flex flex-col">
    <div id="monitor-grid" class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4 hidden"></div>

    <div id="empty-state" class="flex-1 flex flex-col items-center justify-center text-center my-auto py-16 hidden">
      <div class="w-16 h-16 rounded-2xl bg-slate-900 border border-slate-800 flex items-center justify-center mb-4 text-indigo-400">
        <svg class="w-8 h-8" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M9 12h6m-3-3v6m-9 1a9 9 0 1118 0 9 9 0 01-18 0z"/></svg>
      </div>
      <h3 class="text-lg font-semibold text-white mb-1">No monitors configured</h3>
      <p class="text-slate-400 text-sm max-w-sm mb-6">Get started by setting up a heartbeat monitor for your local LXC, container, or server device.</p>
      <button onclick="openModal()" class="inline-flex items-center gap-2 bg-indigo-600 hover:bg-indigo-500 text-white font-medium px-5 py-2.5 rounded-lg text-sm transition shadow-lg shadow-indigo-600/20">
        <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4"/></svg>
        Create New Monitor
      </button>
    </div>
  </main>

  <!-- Monitor Form Modal -->
  <div id="modal" onclick="handleBackdropClick(event)" class="fixed inset-0 bg-slate-950/80 backdrop-blur-sm z-50 flex items-center justify-center p-4 hidden">
    <div id="modal-container" class="bg-slate-900 border border-slate-800 rounded-2xl max-w-md w-full p-6 shadow-2xl">
      <div class="flex justify-between items-center mb-5">
        <h2 id="modal-title" class="text-lg font-bold text-white">Create New Monitor</h2>
        <button onclick="closeModal()" class="text-slate-400 hover:text-white">&times;</button>
      </div>
      <form id="monitor-form" onsubmit="saveMonitor(event)" class="space-y-4">
        <input type="hidden" id="monitor-id" value="">
        <div>
          <label class="block text-xs font-medium uppercase tracking-wider text-slate-400 mb-1">Device Name</label>
          <input type="text" id="name" placeholder="Media Server / Router" class="w-full bg-slate-950 border border-slate-800 rounded-lg px-3 py-2 text-base md:text-sm text-white focus:outline-none focus:border-indigo-500">
        </div>
        <div>
          <label class="block text-xs font-medium uppercase tracking-wider text-slate-400 mb-1">Device IP Address</label>
          <input type="text" id="ip" required placeholder="192.168.1.50" class="w-full bg-slate-950 border border-slate-800 rounded-lg px-3 py-2 text-base md:text-sm text-white focus:outline-none focus:border-indigo-500">
        </div>
        <div>
          <label class="block text-xs font-medium uppercase tracking-wider text-slate-400 mb-1">Heartbeat URL</label>
          <input type="url" id="heartbeat_url" required placeholder="https://hc-ping.com/your-uuid" class="w-full bg-slate-950 border border-slate-800 rounded-lg px-3 py-2 text-base md:text-sm text-white focus:outline-none focus:border-indigo-500">
        </div>
        <div class="flex gap-3 pt-3">
          <button type="button" onclick="closeModal()" class="flex-1 bg-slate-800 hover:bg-slate-700 text-slate-200 py-2 rounded-lg text-sm transition">Cancel</button>
          <button type="submit" class="flex-1 bg-indigo-600 hover:bg-indigo-500 text-white font-medium py-2 rounded-lg text-sm transition">Save Monitor</button>
        </div>
      </form>
    </div>
  </div>

  <script>
    let monitorsCache = {};

    function handleBackdropClick(e) {
      if (e.target.id === 'modal') {
        closeModal();
      }
    }

    function openModal(id = null) {
      const form = document.getElementById('monitor-form');
      form.reset();
      
      if (id && monitorsCache[id]) {
        const m = monitorsCache[id];
        document.getElementById('modal-title').innerText = 'Edit Monitor';
        document.getElementById('monitor-id').value = m.id;
        document.getElementById('name').value = m.name;
        document.getElementById('ip').value = m.ip;
        document.getElementById('heartbeat_url').value = m.heartbeat_url;
      } else {
        document.getElementById('modal-title').innerText = 'Create New Monitor';
        document.getElementById('monitor-id').value = '';
      }
      
      document.getElementById('modal').classList.remove('hidden');
    }

    function closeModal() { 
      document.getElementById('modal').classList.add('hidden'); 
    }

    function getSmoothPath(points) {
      if (points.length < 2) return '';
      let d = 'M ' + points[0].x + ' ' + points[0].y;
      for (let i = 0; i < points.length - 1; i++) {
        const p0 = points[i];
        const p1 = points[i + 1];
        const cx = (p0.x + p1.x) / 2;
        d += ' C ' + cx + ' ' + p0.y + ', ' + cx + ' ' + p1.y + ', ' + p1.x + ' ' + p1.y;
      }
      return d;
    }

    function renderSparkline(m) {
      const history = m.ping_history || [];
      const width = 300;
      const height = 64;

      if (history.length === 0) {
        return '<div class="space-y-1.5">' +
          '<div class="flex justify-between items-center text-[11px] font-mono">' +
            '<span class="text-slate-400">Latency</span>' +
            '<span id="readout-' + m.id + '" class="font-semibold text-slate-500">0.0 ms</span>' +
          '</div>' +
          '<div class="h-16 flex items-center justify-center text-xs text-slate-600 bg-slate-950/40 rounded-lg border border-slate-800/40 font-mono">0.0 ms (Unmonitored)</div>' +
        '</div>';
      }

      const validLatencies = history.filter(p => p.success).map(p => p.latency_ms);
      let maxLatency = validLatencies.length > 0 ? Math.max(...validLatencies) : 0.5;
      if (maxLatency <= 0) maxLatency = 0.5; 
      
      const count = history.length;
      const dx = count > 1 ? width / (count - 1) : width;

      let pts = [];
      history.forEach((pt, i) => {
        const x = i * dx;
        let y = height - 8; 
        if (pt.success) {
          const ratio = Math.min(1, Math.max(0, pt.latency_ms / maxLatency));
          y = (height - 8) - ratio * (height - 14);
        } else {
          y = 6;
        }
        pts.push({ x: x, y: y, pt: pt, index: i });
      });

      const pathD = getSmoothPath(pts);

      const lastPt = history[history.length - 1];
      const initialText = lastPt.success ? lastPt.latency_ms.toFixed(1) + ' ms' : 'Offline';
      const initialColor = m.status === 'up' ? 'text-slate-200' : (m.status === 'down' ? 'text-rose-400' : 'text-slate-500');
      const strokeColor = m.status === 'up' ? '#10b981' : '#f43f5e';

      return '<div class="space-y-1.5">' +
        '<div class="flex justify-between items-center text-[11px] font-mono">' +
          '<span class="text-slate-400">Latency</span>' +
          '<span id="readout-' + m.id + '" class="font-semibold ' + initialColor + '" data-default="' + initialText + '" data-default-color="' + initialColor + '">' + initialText + '</span>' +
        '</div>' +
        '<div class="relative touch-none" ' +
             'onpointermove="handleGraphPointer(\'' + m.id + '\', event)" ' +
             'onpointerleave="resetGraphPointer(\'' + m.id + '\')" ' +
             'onpointerdown="handleGraphPointer(\'' + m.id + '\', event)">' +
          '<svg viewBox="0 0 ' + width + ' ' + height + '" class="w-full h-16 overflow-visible">' +
            '<defs>' +
              '<linearGradient id="fade-mask-' + m.id + '" x1="0%" y1="0%" x2="100%" y2="0%">' +
                '<stop offset="0%" stop-color="#fff" stop-opacity="0.05" />' +
                '<stop offset="25%" stop-color="#fff" stop-opacity="1" />' +
              '</linearGradient>' +
              '<mask id="mask-' + m.id + '">' +
                '<rect x="0" y="0" width="' + width + '" height="' + height + '" fill="url(#fade-mask-' + m.id + ')" />' +
              '</mask>' +
            '</defs>' +
            '<g mask="url(#mask-' + m.id + ')">' +
              '<path d="' + pathD + '" fill="none" stroke="#334155" stroke-width="1.5" stroke-dasharray="3 3" />' +
              '<path d="' + pathD + '" fill="none" stroke="' + strokeColor + '" stroke-width="2.5" stroke-linecap="round" />' +
            '</g>' +
            '<line id="cursor-' + m.id + '" x1="0" y1="0" x2="0" y2="' + height + '" stroke="#ffffff" stroke-width="1.5" stroke-dasharray="2 2" class="opacity-0 transition-opacity pointer-events-none" />' +
            '<circle id="dot-' + m.id + '" cx="0" cy="0" r="4" fill="' + strokeColor + '" stroke="#ffffff" stroke-width="1.5" class="opacity-0 transition-opacity pointer-events-none" />' +
          '</svg>' +
        '</div>' +
      '</div>';
    }

    function handleGraphPointer(monitorId, e) {
      const m = monitorsCache[monitorId];
      if (!m || !m.ping_history || m.ping_history.length === 0) return;

      const rect = e.currentTarget.getBoundingClientRect();
      const clientX = e.clientX || (e.touches && e.touches[0] ? e.touches[0].clientX : 0);
      const relativeX = Math.max(0, Math.min(clientX - rect.left, rect.width));
      const percentage = relativeX / rect.width;

      const history = m.ping_history;
      const index = Math.min(Math.floor(percentage * history.length), history.length - 1);
      const pt = history[index];

      const readout = document.getElementById('readout-' + monitorId);
      const cursor = document.getElementById('cursor-' + monitorId);
      const dot = document.getElementById('dot-' + monitorId);

      if (readout && pt) {
        const valText = pt.success ? pt.latency_ms.toFixed(1) + ' ms' : 'Offline';
        readout.innerText = valText + ' (' + pt.timestamp + ')';
        readout.className = 'font-semibold ' + (pt.success ? 'text-indigo-300' : 'text-rose-400');
      }

      const width = 300;
      const height = 64;
      const dx = history.length > 1 ? width / (history.length - 1) : width;
      const cx = index * dx;

      const validLatencies = history.filter(p => p.success).map(p => p.latency_ms);
      let maxLatency = validLatencies.length > 0 ? Math.max(...validLatencies) : 0.5;
      if (maxLatency <= 0) maxLatency = 0.5;

      let cy = height - 8;
      if (pt.success) {
        const ratio = Math.min(1, Math.max(0, pt.latency_ms / maxLatency));
        cy = (height - 8) - ratio * (height - 14);
      } else {
        cy = 6;
      }

      if (cursor) {
        cursor.setAttribute('x1', cx);
        cursor.setAttribute('x2', cx);
        cursor.classList.remove('opacity-0');
      }
      if (dot) {
        dot.setAttribute('cx', cx);
        dot.setAttribute('cy', cy);
        dot.classList.remove('opacity-0');
      }
    }

    function resetGraphPointer(monitorId) {
      const readout = document.getElementById('readout-' + monitorId);
      const cursor = document.getElementById('cursor-' + monitorId);
      const dot = document.getElementById('dot-' + monitorId);

      if (readout) {
        const def = readout.getAttribute('data-default');
        const defColor = readout.getAttribute('data-default-color');
        readout.innerText = def;
        readout.className = 'font-semibold ' + defColor;
      }
      if (cursor) cursor.classList.add('opacity-0');
      if (dot) dot.classList.add('opacity-0');
    }

    async function loadMonitors() {
      const res = await fetch('/api/monitors');
      const data = await res.json();
      const grid = document.getElementById('monitor-grid');
      const empty = document.getElementById('empty-state');

      if (!data || data.length === 0) {
        monitorsCache = {};
        grid.classList.add('hidden');
        empty.classList.remove('hidden');
        return;
      }

      empty.classList.add('hidden');
      grid.classList.remove('hidden');

      monitorsCache = {};
      data.forEach(m => monitorsCache[m.id] = m);

      grid.innerHTML = data.map(function(m) {
        var statusBadge = m.status === 'up' 
          ? 'bg-emerald-500/10 text-emerald-400 border border-emerald-500/20' 
          : (m.status === 'down' ? 'bg-rose-500/10 text-rose-400 border border-rose-500/20' : 'bg-slate-800 text-slate-400');

        return '<div class="bg-slate-900 border border-slate-800 rounded-xl p-5 flex flex-col justify-between hover:border-slate-700 transition relative group">' +
          '<div>' +
            '<div class="flex justify-between items-start mb-2 pr-16">' +
              '<div>' +
                '<h3 class="font-semibold text-white text-base leading-snug">' + m.name + '</h3>' +
                '<p class="text-xs font-mono text-slate-400 mt-0.5">' + m.ip + '</p>' +
              '</div>' +
            '</div>' +

            '<div class="absolute top-4 right-4 flex items-center space-x-1">' +
              '<button onclick="openModal(\'' + m.id + '\')" title="Edit Monitor" class="p-1.5 text-slate-400 hover:text-white rounded-lg hover:bg-slate-800 transition">' +
                '<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z"/></svg>' +
              '</button>' +
              '<button onclick="deleteMonitor(\'' + m.id + '\')" title="Delete Monitor" class="p-1.5 text-slate-400 hover:text-rose-400 rounded-lg hover:bg-slate-800 transition">' +
                '<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"/></svg>' +
              '</button>' +
            '</div>' +

            '<div class="mb-3 mt-1">' +
              '<span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium ' + statusBadge + '">' +
                m.status.toUpperCase() +
              '</span>' +
            '</div>' +

            '<p class="text-xs text-slate-500 truncate mb-4" title="' + m.heartbeat_url + '">' + m.heartbeat_url + '</p>' +
          '</div>' +

          '<div class="pt-3 border-t border-slate-800/80">' +
            renderSparkline(m) +
          '</div>' +
        '</div>';
      }).join('');
    }

    async function saveMonitor(e) {
      e.preventDefault();
      const body = {
        id: document.getElementById('monitor-id').value,
        name: document.getElementById('name').value,
        ip: document.getElementById('ip').value,
        heartbeat_url: document.getElementById('heartbeat_url').value
      };
      await fetch('/api/monitors', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body)
      });
      closeModal();
      document.getElementById('monitor-form').reset();
      loadMonitors();
    }

    async function deleteMonitor(id) {
      if (confirm('Delete this monitor?')) {
        await fetch('/api/monitors/delete?id=' + id, { method: 'POST' });
        loadMonitors();
      }
    }

    loadMonitors();
    setInterval(loadMonitors, 3000);
  </script>
</body>
</html>`

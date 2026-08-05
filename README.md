# Local device pings to heartbeats

A lightweight, modern dark-mode device monitor built with Go and Tailwind CSS. Periodically pings local devices and triggers external Heartbeat webhooks (e.g., BetterStack, Healthchecks.io).

## Features
- **Modern UI**: Built-in dark mode with real-time responsive SVG telemetry graphs.
- **Smart Throttling**: Triggers external heartbeat webhooks immediately on failures/recoveries and rate-limits to 60s during stable states.
- **Microservice Footprint**: Zero heavy JS frameworks—embeds everything into a minimal Go binary running on Alpine Linux.
- Yes, this feature set was written by AI 🤦

## Running with Docker

```bash
docker run -d \
  --name heartbeats \
  -p 8080:8080 \
  -e PORT=8080 \
  -v heartbeats_data:/app \
  robocrax/ping-heartbeats:latest
```

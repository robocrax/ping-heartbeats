cat << 'EOF' > /opt/heartbeats/README.md
# Heartbeats

A lightweight, modern dark-mode device monitor built with Go and Tailwind CSS. Periodically pings devices and pings external Heartbeat URLs (e.g. BetterStack, Healthchecks.io).

## Quick Start with Docker

```bash
docker run -d \
  --name heartbeats \
  -p 8080:8080 \
  -e PORT=8080 \
  -v heartbeats_data:/opt/heartbeats \
  robocrax/ping-heartbeats:latest
```

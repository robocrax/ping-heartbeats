# Send heartbeats when your devices can't

<img width="1153" height="641" alt="image" src="https://github.com/user-attachments/assets/f3429569-260f-43d3-a029-623fc6fe3f4c" />

A lightweight utility you can run alongside any device you want to monitor which runs on the same network. Periodically pings those devices and triggers external Heartbeat webhooks (e.g., BetterStack, Healthchecks.io). 

This is only useful if the device itself does not have health checks, so you run this as a sidecar.

Obviously, this utility and the device you want to monitor have to be on the same network or atleast be able to ping each other via VPN.

## Features
- **Smart Throttling**: Triggers external heartbeat webhooks immediately on failures/recoveries but rate-limits stable states to 60s.
- **Microservice Footprint**: Zero heavy JS frameworks—embeds everything into a minimal Go binary running on Alpine Linux.
- Yes, this was fully made with AI, including this description 🤦
- When a device does not ping and goes down, it sends the heartbeat to another URL so you can be notified

## Running with Docker

Runs on port 8080 by default but you can change it. 

I publish this using Cloudflare tunnels so I don't even have to expose ports.

```bash
docker run -d \
  -p 8080:8080 \
  -e PORT=8080 \
  -v heartbeats_data:/data \
  robocrax/heartbeats-ping-sidecar:latest
```

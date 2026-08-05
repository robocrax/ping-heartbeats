cat << 'EOF' > /opt/heartbeats/Dockerfile
# Build Stage
FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY main.go .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o heartbeats main.go

# Final Stage
FROM alpine:latest
RUN apk add --no-cache iputils ca-certificates tzdata
WORKDIR /opt/heartbeats
COPY --from=builder /app/heartbeats .

ENV PORT=8080
EXPOSE ${PORT}

CMD ["./heartbeats"]
EOF

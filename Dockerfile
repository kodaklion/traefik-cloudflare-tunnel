FROM golang:1.22

# Set destination for COPY
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./


RUN CGO_ENABLED=0 GOOS=linux go build -o /traefik-cloudflare-tunnel

# Run
ENTRYPOINT ["/traefik-cloudflare-tunnel"]
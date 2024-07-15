# The build stage
FROM golang:1.22 AS builder
WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o /app/traefik-cloudflare-tunnel

# The run stage
FROM scratch
WORKDIR /app
COPY --from=builder /app/traefik-cloudflare-tunnel .

# Run image
CMD ["./traefik-cloudflare-tunnel"]
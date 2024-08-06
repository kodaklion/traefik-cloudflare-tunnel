# The build stage
FROM --platform=${BUILDPLATFORM:-linux/amd64} golang:1.22 as builder

ARG TARGETPLATFORM
ARG BUILDPLATFORM
ARG TARGETOS
ARG TARGETARCH

WORKDIR /app/
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -installsuffix -ldflags="-w -s" -o /app/traefik-cloudflare-tunnel

# The run stage
FROM --platform=${TARGETPLATFORM:-linux/amd64} scratch
WORKDIR /app
COPY --from=builder /app/traefik-cloudflare-tunnel .
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# Run image
CMD ["./traefik-cloudflare-tunnel"]
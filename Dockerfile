FROM docker.io/golang:1.27-rc-trixie AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /sharddnsd ./cmd/sharddnsd \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /sharddns ./cmd/sharddns

RUN apt-get update && apt-get install -y --no-install-recommends libcap2-bin \
 && setcap cap_net_bind_service=+ep /sharddnsd \
 && apt-get clean && rm -rf /var/lib/apt/lists/*


FROM gcr.io/distroless/static-debian13

COPY --from=builder /sharddnsd /usr/local/bin/sharddnsd
COPY --from=builder /sharddns /usr/local/bin/sharddns

USER nonroot:nonroot

EXPOSE 53/udp 53/tcp 9053

HEALTHCHECK --interval=10s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/sharddnsd", "-healthcheck"]

ENTRYPOINT ["/usr/local/bin/sharddnsd"]

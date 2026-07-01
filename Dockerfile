FROM docker.io/golang:1.27-rc-trixie AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /sharddns ./cmd/server \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /sharddns-cli ./cmd/cli

FROM gcr.io/distroless/static-debian12
COPY --from=builder /sharddns /usr/local/bin/sharddns
COPY --from=builder /sharddns-cli /usr/local/bin/sharddns-cli
USER nonroot:nonroot
EXPOSE 53/udp 53/tcp 9053
ENTRYPOINT ["/usr/local/bin/sharddns"]

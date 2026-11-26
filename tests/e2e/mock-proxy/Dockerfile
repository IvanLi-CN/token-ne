FROM golang:1.27.4-alpine AS builder
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mock-proxy .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates wget
COPY --from=builder /out/mock-proxy /app/mock-proxy
USER 65534:65534
EXPOSE 1080 3128 8081
ENTRYPOINT ["/app/mock-proxy"]

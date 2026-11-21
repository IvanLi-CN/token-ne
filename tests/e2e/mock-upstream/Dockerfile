FROM golang:1.27.4-alpine AS builder
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mock-upstream .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates wget
COPY --from=builder /out/mock-upstream /app/mock-upstream
USER 65534:65534
EXPOSE 8080
ENTRYPOINT ["/app/mock-upstream"]

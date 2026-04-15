FROM golang:1.24-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /im-server ./cmd/server

FROM alpine:3.20
COPY --from=builder /im-server /im-server
COPY configs/config.yaml /configs/config.yaml
EXPOSE 9000
CMD ["/im-server", "--config", "/configs/config.yaml"]

FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY go.mod .
COPY main.go .
RUN go build -o server .

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/server .
COPY mcproxy-linux-amd64 .
COPY static/ static/
RUN chmod +x mcproxy-linux-amd64
EXPOSE 8080
CMD ["./server"]

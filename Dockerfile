FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY . .
RUN go build -o circuit .

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/circuit .
EXPOSE 8080
CMD ["./circuit"]

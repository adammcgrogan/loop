FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY . .
RUN go build -o loop .

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/loop .
EXPOSE 8080
CMD ["./loop"]

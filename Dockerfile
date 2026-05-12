FROM golang:1.23-alpine AS builder

WORKDIR /app

# Go modullarini yuklash
COPY go.mod go.sum ./
RUN go mod download

# Butun kodni nusxalash
COPY . .

# Build qilish
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o main .

# Final image (eng kichik)
FROM alpine:latest

WORKDIR /app

RUN apk --no-cache add ca-certificates tzdata

# Binary va config faylni copy qilish
COPY --from=builder /app/main .
COPY config.yaml ./

EXPOSE 8080

CMD ["./main"]
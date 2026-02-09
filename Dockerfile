FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /nats-bridge .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=build /nats-bridge /nats-bridge
EXPOSE 8080
ENTRYPOINT ["/nats-bridge"]

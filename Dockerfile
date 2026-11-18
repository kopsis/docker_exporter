FROM golang:1.22-alpine AS build
WORKDIR /app
COPY go.mod ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o docker_exporter .

FROM alpine:3.20 AS runtime
WORKDIR /app
COPY --from=build /app/docker_exporter .

ENTRYPOINT ["./docker_exporter"]

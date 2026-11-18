FROM golang:1.26-alpine@sha256:0000 AS build
RUN CGO_ENABLED=0 go build -o /app ./cmd/app

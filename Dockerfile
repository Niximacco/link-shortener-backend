FROM golang:1.25-alpine AS build

WORKDIR /go/src/github.com/AnthonyNixon/link-shortener-backend

# The module files come first so the dependency download is cached separately
# from the source: editing a handler should not re-download the sdk.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/

RUN go build -o /bin/link-shortener-backend ./cmd/link-shortener-backend

FROM alpine:3.17 AS deploy
RUN apk --no-cache add ca-certificates
RUN update-ca-certificates
COPY --from=build /bin/link-shortener-backend /bin/link-shortener-backend
ENTRYPOINT ["/bin/link-shortener-backend"]
EXPOSE 8080

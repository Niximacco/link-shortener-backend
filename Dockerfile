# The builder's Go has to be at least what go.mod asks for, and go.mod asks for
# 1.26. modernc.org/sqlite needs it: the database is a pure Go SQLite, and the
# module declares 1.26, which an older builder refuses rather than downloading a
# newer toolchain.
#
# These two numbers are stated in two files and nothing makes them agree, so
# there is a test that fails when they disagree - internal/router,
# TestTheBuilderImageCanBuildThisModule. `go vet` and `go test` read go.mod and
# would pass either way; the container build is the only thing that reads this
# file.
FROM golang:1.26-alpine AS build

WORKDIR /go/src/github.com/AnthonyNixon/link-shortener-backend

# The module files come first so the dependency download is cached separately
# from the source: editing a handler should not re-download the sdk.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/

# CGO off gives a static binary, which is what lets the runtime stage be a bare
# alpine with nothing in it but certificates. modernc.org/sqlite is pure Go, so
# the database costs this nothing - it is why that driver was chosen over one
# wrapping the C library.
RUN CGO_ENABLED=0 go build -o /bin/link-shortener-backend ./cmd/link-shortener-backend

FROM alpine:3.17 AS deploy
RUN apk --no-cache add ca-certificates
RUN update-ca-certificates
COPY --from=build /bin/link-shortener-backend /bin/link-shortener-backend
ENTRYPOINT ["/bin/link-shortener-backend"]
EXPOSE 8080

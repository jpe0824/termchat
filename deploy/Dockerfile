# Build from the repository root:
#   docker build -f deploy/Dockerfile -t termchat .
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/jpe0824/termchat/internal/cli.Version=${VERSION}" \
      -o /out/termchat ./cmd/termchat \
 && mkdir /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/termchat /usr/local/bin/termchat
COPY --from=build --chown=nonroot:nonroot /out/data /data
USER nonroot:nonroot
ENV TERMCHAT_SERVER_DATA=/data
VOLUME /data
EXPOSE 7420
ENTRYPOINT ["termchat"]
CMD ["serve", "--data", "/data"]

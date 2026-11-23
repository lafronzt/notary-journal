FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/notary-journal ./cmd/notary-journal

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /data
COPY --from=build /out/notary-journal /notary-journal
USER 65532:65532
EXPOSE 8087
ENV LISTEN_ADDR=0.0.0.0:8087 NOTARY_DB=/data/journal.sqlite3
ENTRYPOINT ["/notary-journal"]
CMD ["serve"]

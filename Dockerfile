# syntax=docker/dockerfile:1@sha256:572cebbc2b15e6f84c077742952674889cb63cd5e9b80abdffcbe587b6f19183
# SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
# Copyright 2026 Abyss Reader
#
# The server kit's image: one static binary (pure-Go SQLite, no cgo) on a distroless base with
# no shell. The simple setup keeps everything in the /data volume.

FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/abyssreader-server ./cmd/abyssreader-server \
 && mkdir -p /out/data /out/doc \
 && go build -o /out/notices ./scripts/notices \
 && CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" /out/notices -o /out/doc/THIRD_PARTY_NOTICES ./cmd/abyssreader-server \
 && cp LICENSE LICENSE-DOCS NOTICE /out/doc/

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/abyssreader-server /usr/local/bin/abyssreader-server
# The licences, the Required Notice lines and the notices of every linked module, generated at
# build time so they always match the binary.
COPY --from=build /out/doc/ /usr/share/doc/abyssreader-server/
# An empty /data owned by the server's user, so a new named volume starts writable.
COPY --from=build --chown=65532:65532 /out/data /data
ENV ABYSS_DATA_DIR=/data \
    ABYSS_LISTEN=:8080
VOLUME ["/data"]
EXPOSE 8080
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["abyssreader-server", "healthcheck"]
ENTRYPOINT ["abyssreader-server"]
CMD ["serve"]

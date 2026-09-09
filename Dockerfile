# syntax=docker/dockerfile:1

# ---- build stage ------------------------------------------------------
# Matches the toolchain pinned in go.mod (go 1.25.7).
FROM --platform=$BUILDPLATFORM golang:1.25.7-bookworm AS builder

ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

# Cache module downloads separately from the source tree.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ENV CGO_ENABLED=0
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
      -ldflags "-s -w \
        -X github.com/foxzi/baton/internal/version.Version=${VERSION} \
        -X github.com/foxzi/baton/internal/version.Commit=${COMMIT} \
        -X github.com/foxzi/baton/internal/version.Date=${DATE}" \
      -o /out/baton ./cmd/baton

# ---- runtime stage ------------------------------------------------------
FROM debian:bookworm-slim AS runtime

# Utilities baton's own step types and scenario authors commonly reach for:
# git (history/local commits), bash/grep/ripgrep (run steps), jq (transforms
# outside baton's own gojq), curl (fetch-style debugging), the archive tools
# for artifact packing/unpacking. No agent CLIs (claude, codex) are
# installed; the agent step type needs them added separately.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
       ca-certificates \
       git \
       bash \
       jq \
       curl \
       grep \
       ripgrep \
       bzip2 \
       zip \
       unzip \
       tar \
       gzip \
    && rm -rf /var/lib/apt/lists/*

RUN groupadd --gid 1000 baton \
    && useradd --uid 1000 --gid baton --shell /bin/bash --create-home baton \
    # HOME is a separate world-writable directory, not the account's own
    # home, so `docker run --user <arbitrary-uid>` (compose's
    # BATON_UID/BATON_GID override) can still write config/cache lookups
    # (os.UserConfigDir/os.UserCacheDir) without root or --privileged.
    && mkdir -p /tmp/baton-home \
    && chmod 1777 /tmp/baton-home

COPY --from=builder /out/baton /usr/local/bin/baton

ENV HOME=/tmp/baton-home
WORKDIR /workspace
RUN chown baton:baton /workspace

USER baton:baton

ENTRYPOINT ["baton"]
CMD ["--help"]

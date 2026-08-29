# The tiny agent image. Two jobs:
#  1. Run the agent directly (the default session image).
#  2. Carry /opt/tiny — a self-contained payload (claude, static tmux,
#     tiny-notify, entrypoint) that an init container copies into ANY
#     glibc-based session image, so spec.image can be golang, maven, your
#     dev image, with nothing baked in.

# Static tmux: the one binary a foreign image is least likely to have.
FROM alpine:3.20 AS tmux
RUN apk add --no-cache build-base ncurses-dev ncurses-static libevent-dev libevent-static bison curl ca-certificates \
 && curl -fsSL https://github.com/tmux/tmux/releases/download/3.5a/tmux-3.5a.tar.gz | tar xz \
 && cd tmux-3.5a \
 && ./configure --enable-static \
 && make -j"$(nproc)" \
 && strip tmux

# codex: OpenAI's coding agent, a static musl binary from the release —
# runs in any linux image, glibc or not. Pinned like claude: new codex
# arrives as a new agent image.
FROM alpine:3.20 AS codex
ARG TARGETARCH
RUN apk add --no-cache curl ca-certificates \
 && case "$TARGETARCH" in amd64) A=x86_64;; arm64) A=aarch64;; *) echo "unsupported arch $TARGETARCH" >&2; exit 1;; esac \
 && curl -fsSL "https://github.com/openai/codex/releases/download/rust-v0.150.1/codex-${A}-unknown-linux-musl.tar.gz" | tar xz -C / \
 && mv /codex-${A}-unknown-linux-musl /codex-bin \
 && curl -fsSL "https://github.com/openai/codex/releases/download/rust-v0.150.1/codex-code-mode-host-${A}-unknown-linux-musl.tar.gz" | tar xz -C / \
 && mv /codex-code-mode-host-${A}-unknown-linux-musl /codex-code-mode-host-bin \
 && chmod 755 /codex-bin /codex-code-mode-host-bin

# mc: the artifact-store client, per-arch static binary from minio.
FROM alpine:3.20 AS mc
ARG TARGETARCH
RUN apk add --no-cache curl ca-certificates \
 && curl -fsSL -o /mc "https://dl.min.io/client/mc/release/linux-${TARGETARCH}/mc" \
 && chmod 755 /mc

# tiny-notify: attention hook without a curl dependency.
FROM golang:1.26-alpine AS notify
WORKDIR /src
COPY notify.go .
RUN CGO_ENABLED=0 go build -ldflags='-s -w' -o /tiny-notify notify.go

FROM node:22-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
      git curl ca-certificates tmux ripgrep procps openssh-client \
    && rm -rf /var/lib/apt/lists/* \
    && npm install -g @anthropic-ai/claude-code

# The injectable payload. bin/claude resolves inside the payload so the
# whole tree relocates to /tiny unchanged.
COPY entrypoint.sh /opt/tiny/entrypoint.sh
COPY --from=tmux /tmux-3.5a/tmux /opt/tiny/bin/tmux
COPY --from=notify /tiny-notify /opt/tiny/bin/tiny-notify
COPY --from=mc /mc /opt/tiny/bin/mc
COPY --from=codex /codex-bin /opt/tiny/bin/codex
COPY --from=codex /codex-code-mode-host-bin /opt/tiny/bin/codex-code-mode-host
RUN cp -a /usr/local/lib/node_modules/@anthropic-ai/claude-code /opt/tiny/claude-code \
 && ln -s ../claude-code/bin/claude.exe /opt/tiny/bin/claude \
 && chmod 755 /opt/tiny/entrypoint.sh

ARG TINY_AGENT_BUILD=dev
ENV TINY_AGENT_BUILD=$TINY_AGENT_BUILD

RUN useradd -m -u 61000 agent && mkdir -p /workspace && chown agent /workspace
USER agent
WORKDIR /workspace
ENTRYPOINT ["/opt/tiny/entrypoint.sh"]

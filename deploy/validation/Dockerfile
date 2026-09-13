# A disposable, private validation artifact. This is not a production image.
# The Dockerfile-specific context allowlist excludes deployment secrets/data.
FROM node:22-alpine AS gui
WORKDIR /src/gui
COPY gui/package.json gui/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY gui/ ./
RUN npm run build

FROM golang:1.26.6-bookworm AS build
RUN apt-get update && apt-get install -y --no-install-recommends libsqlite3-dev && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY go.mod go.sum ./
COPY sdk/go.mod ./sdk/go.mod
RUN --mount=type=cache,id=soulacy-go-mod,target=/go/pkg/mod go mod download
COPY sdk ./sdk
COPY internal ./internal
COPY pkg ./pkg
COPY cmd ./cmd
COPY --from=gui /src/internal/webui/dist ./internal/webui/dist
RUN --mount=type=cache,id=soulacy-go-build,target=/root/.cache/go-build \
    --mount=type=cache,id=soulacy-go-mod,target=/go/pkg/mod \
    go build -trimpath -ldflags='-s -w -X github.com/soulacy/soulacy/internal/config.Version=autopilot-validation' -o /out/soulacy ./cmd/soulacy
RUN --mount=type=cache,id=soulacy-go-build,target=/root/.cache/go-build \
    --mount=type=cache,id=soulacy-go-mod,target=/go/pkg/mod \
    go test -c -ldflags='-s -w' -o /out/autopilot.test ./internal/autopilot && \
    go test -c -ldflags='-s -w' -o /out/gateway.test ./internal/gateway && \
    go test -c -ldflags='-s -w' -o /out/runtime.test ./internal/runtime && \
    go test -c -ldflags='-s -w' -o /out/mobile.test ./internal/channels/mobile && \
    go test -c -ldflags='-s -w' -o /out/llm.test ./internal/llm && \
    go test -c -ldflags='-s -w' -o /out/costs.test ./internal/costs && \
    go test -c -ldflags='-s -w' -o /out/discovery.test ./internal/discovery && \
    go test -c -ldflags='-s -w' -o /out/publishedfiles.test ./internal/publishedfiles && \
    go test -c -ldflags='-s -w' -o /out/safeundo.test ./internal/safeundo && \
    go test -c -ldflags='-s -w' -o /out/learning.test ./internal/learning

FROM python:3.12-slim-bookworm
RUN useradd --system --create-home validation
COPY --from=build /out /validation
COPY deploy/validation/run.py /validation/run.py
USER validation
WORKDIR /tmp
ENV PYTHONUNBUFFERED=1
ENTRYPOINT ["python3", "/validation/run.py"]

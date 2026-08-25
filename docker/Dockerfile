ARG ALLURE_VERSION=3.15.0
ARG SERVICE_VERSION=dev

FROM --platform=$BUILDPLATFORM golang:1.26.5 AS build

ARG TARGETOS
ARG TARGETARCH

ARG SERVICE_VERSION

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY ["cmd/", "./cmd/"]
COPY ["internal/", "./internal/"]

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags="-s -w -X main.serviceVersion=${SERVICE_VERSION}" \
    -o /out/allure-service ./cmd/allure-service

FROM node:24-slim
ARG ALLURE_VERSION

RUN apt-get update \
    && apt-get install -y --no-install-recommends curl \
    && rm -rf /var/lib/apt/lists/* \
    && npm install -g allure@${ALLURE_VERSION} --loglevel=error \
    && npm cache clean --force

COPY --from=build /out/allure-service /usr/local/bin/allure-service
ENV STATIC_CONTENT_PROJECTS=/app/projects
RUN mkdir -p ${STATIC_CONTENT_PROJECTS} && chown node:node ${STATIC_CONTENT_PROJECTS}

USER node
WORKDIR /app

ENV PORT=5050
EXPOSE 5050

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD curl -fsS http://localhost:${PORT}/health || exit 1


ENTRYPOINT ["allure-service"]

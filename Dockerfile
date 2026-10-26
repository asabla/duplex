FROM --platform=$BUILDPLATFORM golang:1.27.3-bookworm@sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5 AS build
ARG TARGETARCH
ARG TARGETOS
RUN apt-get update && apt-get install -y --no-install-recommends unzip && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,id=duplex-go-mod,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,id=duplex-go-mod,target=/go/pkg/mod --mount=type=cache,id=duplex-go-build,target=/root/.cache/go-build CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /duplex ./cmd/duplex
RUN case "$TARGETARCH" in \
      arm64) asset=yt-dlp_linux_aarch64.zip; checksum=d98a0aeb26b670aac94ee892a03a196f3f2cf0a81c3872186199c0c67ab36bde ;; \
      amd64) asset=yt-dlp_linux.zip; checksum=dcddc44b6151ccd855a9a3c8fcdb00e897bb872ec9f4908b1b708b16eaaaa114 ;; \
      *) exit 1 ;; \
    esac; \
    curl -fsSL "https://github.com/yt-dlp/yt-dlp/releases/download/2026.10.21/$asset" -o /yt-dlp.zip && \
    echo "$checksum  /yt-dlp.zip" | sha256sum -c - && mkdir /yt-dlp && unzip -q /yt-dlp.zip -d /yt-dlp && mv "/yt-dlp/${asset%.zip}" /yt-dlp/yt-dlp && chmod 0755 /yt-dlp/yt-dlp

FROM denoland/deno:bin-2.9.8@sha256:b507583d3c68ba69f3ed2ce5969ae5b8b70f43421c781279c7aea06435263543 AS deno

# The official yt-dlp standalone executables include their matching EJS code.
FROM debian:bookworm-slim@sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates libstdc++6 && \
    dpkg-query -W > /runtime-packages.txt && rm -rf /var/lib/apt/lists/* && mkdir /state
COPY --from=build /duplex /usr/local/bin/duplex
COPY --from=build /yt-dlp /opt/yt-dlp
RUN ln -s /opt/yt-dlp/yt-dlp /usr/local/bin/yt-dlp
COPY --from=deno /deno /usr/local/bin/deno
COPY LICENSE /LICENSE
COPY THIRD_PARTY_NOTICES.txt /THIRD_PARTY_NOTICES.txt
COPY docs/licenses /licenses
ENV DENO_DIR=/tmp/deno
WORKDIR /state
ENTRYPOINT ["/usr/local/bin/duplex"]
CMD ["serve", "--config", "/state/config.yaml"]

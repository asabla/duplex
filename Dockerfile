FROM --platform=$BUILDPLATFORM golang:1.27.3-bookworm@sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5 AS build
ARG TARGETARCH
ARG TARGETOS
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,id=duplex-go-mod,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,id=duplex-go-mod,target=/go/pkg/mod --mount=type=cache,id=duplex-go-build,target=/root/.cache/go-build CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /duplex ./cmd/duplex
RUN mkdir /yt-dlp && \
    curl -fsSL https://github.com/yt-dlp/yt-dlp/releases/download/2026.10.21/yt-dlp -o /yt-dlp/yt-dlp && \
    echo 'b7af77f686e95f7845ce8d09d21957718febb8b48b30555adb9dd2292ad7bf14  /yt-dlp/yt-dlp' | sha256sum -c - && \
    chmod 0755 /yt-dlp/yt-dlp && \
    curl -fsSL https://raw.githubusercontent.com/yt-dlp/yt-dlp/2026.10.21/LICENSE -o /yt-dlp/LICENSE && \
    echo '7e12e5df4bae12cb21581ba157ced20e1986a0508dd10d0e8a4ab9a4cf94e85c  /yt-dlp/LICENSE' | sha256sum -c - && \
    curl -fsSL https://raw.githubusercontent.com/yt-dlp/yt-dlp/2026.10.21/THIRD_PARTY_LICENSES.txt -o /yt-dlp/THIRD_PARTY_LICENSES.txt && \
    echo '472aefe951c7db35e1657c1d13fd337140511ed6f2b329205105ad441c5a02b7  /yt-dlp/THIRD_PARTY_LICENSES.txt' | sha256sum -c -

FROM denoland/deno:bin-2.9.8@sha256:b507583d3c68ba69f3ed2ce5969ae5b8b70f43421c781279c7aea06435263543 AS deno

# The official Python zip executable includes its matching EJS code.
FROM debian:bookworm-slim@sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates libstdc++6 python3 && \
    dpkg-query -W > /runtime-packages.txt && rm -rf /var/lib/apt/lists/* && mkdir /state && chown 10001:10001 /state && chmod 0700 /state
COPY --from=build /duplex /usr/local/bin/duplex
COPY --from=build /yt-dlp /opt/yt-dlp
RUN ln -s /opt/yt-dlp/yt-dlp /usr/local/bin/yt-dlp
COPY --from=deno /deno /usr/local/bin/deno
COPY LICENSE /LICENSE
COPY THIRD_PARTY_NOTICES.txt /THIRD_PARTY_NOTICES.txt
COPY docs/licenses /licenses
ENV DENO_DIR=/tmp/deno
WORKDIR /state
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/duplex"]
CMD ["serve", "--config", "/state/config.yaml", "--nfs-listen", "0.0.0.0:2049"]

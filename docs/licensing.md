# Licensing

## Original project files

The original duplex source code and documentation use the [MIT License](../LICENSE).
The copyright holder is asabla. Keep the copyright and permission notice when
copying or distributing these files.

## Third-party files and dependencies

The project license does not replace third-party licenses:

- [THIRD_PARTY_NOTICES.txt](../THIRD_PARTY_NOTICES.txt) contains notices for Go
  modules linked into the application. [go-dependencies.json](go-dependencies.json)
  lists those modules. Regenerate both with `python3 scripts/generate-notices.py`
  after a linked dependency changes, then review the output.
- Embedded HTMX retains its [license](../internal/admin/static/HTMX-LICENSE.txt).
- Embedded Go fonts retain their [license](licenses/go-font-LICENSE.txt).
- The Go toolchain and Deno retain their [Go](licenses/go-LICENSE.txt) and
  [Deno](licenses/deno-LICENSE.txt) notices.
- Development packages retain the licenses supplied with their pinned versions.
  They are not part of the application runtime.

The Docker build obtains a pinned yt-dlp standalone directory and Deno binary.
The yt-dlp directory remains intact under `/opt/yt-dlp`, including its bundled
components and notices. Those components can have different licenses from the
yt-dlp Python source. Review the exact archive's notices and distribution
requirements before redistributing a built image. OS packages also keep their
own licenses; the image records installed versions in `/runtime-packages.txt`.

The image includes the project license at `/LICENSE`, linked-module notices at
`/THIRD_PARTY_NOTICES.txt`, and supporting notices under `/licenses`.

## External services and media

Plex Media Server is a separate product. Its optional container uses its own
license and terms. Plex is not part of the MIT-licensed source.

The project license does not grant rights to videos, audio, subtitles, artwork,
trademarks, or other content obtained from external services. Follow the terms
and permissions that apply to the content and service you use.

Publishing this source repository does not establish binary or container
redistribution acceptance. Build and release validation is described in
[validation](validation.md).

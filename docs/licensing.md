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

The Docker build obtains the checksum-pinned yt-dlp Python zip executable and
Deno binary. The zip includes matching EJS code and the ISC-licensed meriyah and
MIT-licensed astring libraries, with their notices in the JavaScript bundle.
The executable and upstream license files remain under `/opt/yt-dlp`. The full
upstream third-party notice file also describes components of other upstream
builds; it is not an inventory of the installed image.

The image uses Debian Python instead of the upstream PyInstaller archive.
The separate native installation script still uses that archive and retains its
bundled components and notices. OS packages keep their own licenses under
`/usr/share/doc`; the image records installed versions in `/runtime-packages.txt`.

The image includes the project license at `/LICENSE`, linked-module notices at
`/THIRD_PARTY_NOTICES.txt`, and supporting notices under `/licenses`.

## External services and media

Plex Media Server is a separate product. Its optional container uses its own
license and terms. Plex is not part of the MIT-licensed source.

The project license does not grant rights to videos, audio, subtitles, artwork,
trademarks, or other content obtained from external services. Follow the terms
and permissions that apply to the content and service you use.

Review licenses and source requirements when updating any bundled component.
Build and release validation is described in [validation](validation.md).

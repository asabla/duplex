#!/usr/bin/env python3
"""Measure client decoding through a Plex media part without URL credentials."""

import argparse
import concurrent.futures
import http.cookiejar
import http.server
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import threading
import time
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET


def origin(value):
    parsed = urllib.parse.urlsplit(value)
    if (
        parsed.scheme not in ("http", "https")
        or not parsed.hostname
        or parsed.username
        or parsed.password
        or parsed.query
        or parsed.fragment
    ):
        raise argparse.ArgumentTypeError(
            "Use an HTTP origin without credentials or query parameters"
        )
    return value.rstrip("/")


class SameOriginRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, target):
        before = urllib.parse.urlsplit(request.full_url)
        after = urllib.parse.urlsplit(target)
        if (before.scheme, before.netloc) != (after.scheme, after.netloc):
            raise RuntimeError("Cross-origin authenticated redirect rejected")
        return super().redirect_request(
            request, response, code, message, headers, target
        )


class Probe:
    def __init__(self, args):
        self.args = args
        self.output = args.output
        self.output.mkdir(mode=0o700)
        self.plex_opener = urllib.request.build_opener(SameOriginRedirect())
        self.duplex_opener = urllib.request.build_opener(
            SameOriginRedirect(),
            urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()),
        )
        self.token = args.token_file.read_text().strip()
        if not self.token or "\n" in self.token or "\r" in self.token:
            raise ValueError("Invalid token file")
        self.requests = []
        self.lock = threading.Lock()
        self.part_url = None
        self.session_csrf = None

    def plex(self, path):
        request = urllib.request.Request(
            self.args.plex_url + path, headers={"X-Plex-Token": self.token}
        )
        with self.plex_opener.open(request, timeout=self.args.timeout) as response:
            return ET.fromstring(response.read())

    def duplex_json(self, path, body=None):
        data = json.dumps(body).encode() if body is not None else None
        request = urllib.request.Request(
            self.args.duplex_url + path,
            data=data,
            headers={"Content-Type": "application/json"},
        )
        with self.duplex_opener.open(request, timeout=self.args.timeout) as response:
            return json.load(response)

    def login(self):
        if not self.args.duplex_url:
            return
        session = self.duplex_json("/api/v1/session")
        self.duplex_json(
            "/api/v1/login",
            {
                "username": "admin",
                "password": self.args.duplex_password_file.read_text().strip(),
                "csrf": session["csrf"],
            },
        )
        self.session_csrf = self.duplex_json("/api/v1/session")["csrf"]

    def close(self):
        if self.session_csrf:
            request = urllib.request.Request(
                self.args.duplex_url + "/logout",
                data=b"",
                headers={"X-CSRF-Token": self.session_csrf},
            )
            with self.duplex_opener.open(
                request, timeout=self.args.timeout
            ) as response:
                response.read()

    def metrics(self):
        if not self.args.duplex_url:
            return None
        status = self.duplex_json("/api/v1/summary")["status"]
        return {key: status.get(key) for key in ("measurements", "counters", "cache")}

    def save(self, report):
        (self.output / "result.json").write_text(json.dumps(report, indent=2) + "\n")

    def decode(self, url, offset, phase):
        started = time.monotonic()
        row = {"phase": phase, "offset_seconds": offset}
        command = [
            self.args.ffmpeg,
            "-nostdin",
            "-hide_banner",
            "-loglevel",
            "error",
            "-ss",
            str(offset),
            "-i",
            url,
            "-map",
            "0:v:0",
            "-frames:v",
            "1",
            "-an",
            "-f",
            "framemd5",
            "-",
        ]
        try:
            result = subprocess.run(
                command, capture_output=True, timeout=self.args.timeout, check=False
            )
            row["exit_code"] = result.returncode
            frames = [
                line
                for line in result.stdout.decode().splitlines()
                if line and not line.startswith("#")
            ]
            digest = frames[-1].rsplit(",", 1)[-1].strip() if frames else ""
            row["decoded"] = result.returncode == 0 and bool(
                re.fullmatch(r"[0-9a-f]{32}", digest)
            )
            if digest:
                row["frame_md5"] = digest
        except subprocess.TimeoutExpired:
            row.update(decoded=False, error_class="TimeoutExpired")
        row["seconds"] = time.monotonic() - started
        print(json.dumps(row), flush=True)
        return row

    def decode_av(self, url, offset):
        started = time.monotonic()
        row = {
            "phase": "audio_video",
            "offset_seconds": offset,
            "content_seconds": self.args.av_seconds,
        }
        command = [
            self.args.ffmpeg,
            "-nostdin",
            "-hide_banner",
            "-loglevel",
            "error",
            "-ss",
            str(offset),
            "-i",
            url,
            "-map",
            "0:v:0",
            "-map",
            "0:a:0",
            "-t",
            str(self.args.av_seconds),
            "-progress",
            "pipe:1",
            "-f",
            "null",
            "-",
        ]
        try:
            result = subprocess.run(
                command, capture_output=True, timeout=self.args.timeout, check=False
            )
            progress = dict(
                line.split("=", 1)
                for line in result.stdout.decode().splitlines()
                if "=" in line
            )
            row.update(
                exit_code=result.returncode,
                frames=int(progress.get("frame", "0")),
                output_microseconds=int(progress.get("out_time_us", "0")),
            )
            row["decoded"] = (
                result.returncode == 0
                and row["frames"] > 0
                and row["output_microseconds"] >= (self.args.av_seconds - 0.1) * 1e6
            )
        except subprocess.TimeoutExpired:
            row.update(decoded=False, error_class="TimeoutExpired")
        row["seconds"] = time.monotonic() - started
        print(json.dumps(row), flush=True)
        return row

    def run(self):
        self.login()
        video = self.plex("/library/metadata/" + str(self.args.item)).find("Video")
        part = video.find("Media/Part") if video is not None else None
        if part is None or not part.get("key", "").startswith("/library/parts/"):
            raise ValueError("The selected item has no Plex media part")
        duration = float(video.get("duration", "0")) / 1000
        if duration <= 0:
            raise ValueError("The selected item has no duration")
        self.part_url = self.args.plex_url + part.get("key")
        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler(self))
        server.daemon_threads = False
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        url = "http://127.0.0.1:" + str(server.server_port) + "/media.mp4"
        report = {
            "schema": 1,
            "method": "One decoded video frame through Plex HTTP part; not browser rendering",
            "duration_seconds": duration,
            "view_offset_before": video.get("viewOffset"),
            "before": self.metrics(),
            "sequential": [],
        }
        try:
            offsets = [0, round(duration / 2, 3), round(max(0, duration - 45), 3)]
            for phase in ("first_read", "repeat"):
                for offset in offsets:
                    report["sequential"].append(self.decode(url, offset, phase))
                    self.save(report)
            if self.args.av_seconds:
                report["audio_video"] = self.decode_av(url, round(duration * 0.45, 3))
            if self.args.clients > 1:
                offsets = [
                    round(duration * n / self.args.clients, 3)
                    for n in range(self.args.clients)
                ]
                with concurrent.futures.ThreadPoolExecutor(
                    max_workers=self.args.clients
                ) as pool:
                    report["concurrent"] = list(
                        pool.map(
                            lambda offset: self.decode(url, offset, "concurrent"),
                            offsets,
                        )
                    )
            report["after"] = self.metrics()
            report["view_offset_after"] = (
                self.plex("/library/metadata/" + str(self.args.item))
                .find("Video")
                .get("viewOffset")
            )
        finally:
            server.shutdown()
            server.server_close()
            thread.join()
            with self.lock:
                report["requests"] = list(self.requests)
            self.save(report)
        rows = report["sequential"] + report.get("concurrent", [])
        if "audio_video" in report:
            rows.append(report["audio_video"])
        return all(row["decoded"] for row in rows) and not any(
            row.get("safety_byte_limit") or row.get("error_class")
            for row in report["requests"]
        )


def handler(probe):
    class PartProxy(http.server.BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, *_):
            pass

        def setup(self):
            super().setup()
            self.connection.settimeout(probe.args.timeout)

        def do_HEAD(self):
            self.forward(True)

        def do_GET(self):
            self.forward(False)

        def forward(self, head):
            byte_range = self.headers.get("Range", "")
            if self.path != "/media.mp4" or len(byte_range) > 128:
                self.send_error(404)
                return
            row = {"range": byte_range, "method": self.command, "bytes": 0}
            started = time.monotonic()
            sent_headers = False
            try:
                headers = {"X-Plex-Token": probe.token, "Accept-Encoding": "identity"}
                if byte_range:
                    headers["Range"] = byte_range
                request = urllib.request.Request(
                    probe.part_url, headers=headers, method="HEAD" if head else "GET"
                )
                with probe.plex_opener.open(
                    request, timeout=probe.args.timeout
                ) as upstream:
                    row.update(
                        status=upstream.status,
                        headers_seconds=time.monotonic() - started,
                    )
                    self.send_response(upstream.status)
                    for key, value in upstream.headers.items():
                        if key.lower() not in (
                            "connection",
                            "transfer-encoding",
                            "server",
                            "date",
                        ):
                            self.send_header(key, value)
                    self.end_headers()
                    sent_headers = True
                    if not head:
                        self.copy_payload(upstream, row, started)
            except (BrokenPipeError, ConnectionResetError):
                row["client_closed"] = True
            except (
                Exception
            ) as error:  # Retain failures without upstream URLs or credentials.
                row["error_class"] = type(error).__name__
                if not sent_headers:
                    self.send_error(502, "Playback request failed")
                self.close_connection = True
            finally:
                row["seconds"] = time.monotonic() - started
                with probe.lock:
                    probe.requests.append(row)

        def copy_payload(self, upstream, row, started):
            while row["bytes"] < 128 * 1024 * 1024:
                data = upstream.read(65536)
                if not data:
                    row["upstream_eof"] = True
                    return
                if row["bytes"] == 0:
                    row["first_payload_seconds"] = time.monotonic() - started
                self.wfile.write(data)
                row["bytes"] += len(data)
            row["safety_byte_limit"] = True
            self.close_connection = True

    return PartProxy


def arguments():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--plex-url", type=origin, required=True)
    parser.add_argument("--token-file", type=Path, required=True)
    parser.add_argument("--item", type=int, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--duplex-url", type=origin)
    parser.add_argument("--duplex-password-file", type=Path)
    parser.add_argument("--clients", type=int, default=1)
    parser.add_argument("--av-seconds", type=int, default=2)
    parser.add_argument("--timeout", type=int, default=90)
    parser.add_argument("--ffmpeg", default=shutil.which("ffmpeg"))
    args = parser.parse_args()
    if (
        args.item < 1
        or args.clients not in range(1, 5)
        or args.av_seconds not in range(0, 11)
        or args.timeout not in range(1, 181)
        or not args.ffmpeg
        or args.output.exists()
        or not args.output.is_absolute()
        or bool(args.duplex_url) != bool(args.duplex_password_file)
    ):
        parser.error(
            "Use a valid item, 1-4 clients, 0-10 A/V seconds, 1-180 second timeout, installed FFmpeg, new absolute output path, and both Duplex options together"
        )
    return args


if __name__ == "__main__":
    os.umask(0o077)
    try:
        probe = Probe(arguments())
        try:
            success = probe.run()
        finally:
            probe.close()
    except Exception as error:
        print("Playback profile failed: " + type(error).__name__, flush=True)
        raise SystemExit(1) from None
    raise SystemExit(0 if success else 1)

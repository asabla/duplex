#!/usr/bin/env python3
"""Controlled checks inside the exact NFS service and legacy helper images."""

import ctypes
import errno
from pathlib import Path
import socket
import struct
import subprocess
import sys
import time
import urllib.request


def denied_mount(target, filesystem, flags, data):
    libc = ctypes.CDLL(None, use_errno=True)
    result = libc.mount(b"duplex", target, filesystem, flags, data)
    error = ctypes.get_errno()
    if result == 0:
        libc.umount2(target, 2)
        raise AssertionError("An unrelated mount succeeded")
    return error


def helper():
    assert (
        "duplex-mount-helper (enforce)" in Path("/proc/self/attr/current").read_text()
    )
    status = Path("/proc/self/status").read_text()
    assert "CapEff:\t0000000000200000" in status
    assert "NoNewPrivs:\t1" in status and "Seccomp:\t2" in status
    assert denied_mount(b"/tmp", b"tmpfs", 0, b"size=4096") == errno.EACCES
    device = Path("/dev/fuse").open("r+b", buffering=0)
    with device:
        data = (
            f"fd={device.fileno()},rootmode=40000,user_id=10001,group_id=10001".encode()
        )
        assert denied_mount(b"/tmp", b"fuse.duplex", 15, data) == errno.EACCES
        assert denied_mount(b"/library/media", b"fuse.duplex", 0, data) == errno.EACCES
    try:
        connection = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    except PermissionError:
        pass
    else:
        connection.close()
        raise AssertionError("Helper can create internet sockets")
    # Root is not an authorized mount client, even inside the helper container.
    with socket.socket(socket.AF_UNIX, socket.SOCK_SEQPACKET) as connection:
        connection.settimeout(3)
        connection.connect("/run/duplex/mount.sock")
        try:
            connection.send(b"M")
            data, rights, _, _ = connection.recvmsg(2, socket.CMSG_SPACE(16))
            assert data != b"F" and not rights
        except (ConnectionResetError, BrokenPipeError):
            pass
    print(
        "Helper: enforced profile; wrong mounts, internet sockets, and root client denied"
    )


def tools():
    assert (
        subprocess.check_output(["yt-dlp", "--version"], text=True).strip()
        == "2026.10.21"
    )
    assert subprocess.check_output(["deno", "--version"], text=True).startswith(
        "deno 2.9.8 "
    )
    sys.path.insert(0, "/opt/yt-dlp/yt-dlp")
    import yt_dlp_ejs.yt.solver as solver

    script = solver.lib() + "\nconst {meriyah, astring} = lib;\n" + solver.core()
    script += '\nconst r = jsc({type: "preprocessed", preprocessed_player: "_result.n = s => s.toUpperCase()", requests: [{type: "n", challenges: ["abc"]}]}); if (r.responses[0].data.abc !== "ABC") throw new Error("solver failed");\n'
    Path("/tmp/solver.js").write_text(script)
    subprocess.run(
        ["deno", "run", "--no-config", "--no-remote", "/tmp/solver.js"], check=True
    )
    for filename in (
        "/LICENSE",
        "/THIRD_PARTY_NOTICES.txt",
        "/licenses/deno-LICENSE.txt",
        "/opt/yt-dlp/LICENSE",
        "/opt/yt-dlp/THIRD_PARTY_LICENSES.txt",
        "/runtime-packages.txt",
    ):
        assert Path(filename).stat().st_size > 0


def service():
    for filename in ("/proc/1/status", "/proc/self/status"):
        status = Path(filename).read_text()
        assert "Uid:\t10001\t10001\t10001\t10001" in status
        for capability in ("CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb"):
            assert f"{capability}:\t0000000000000000" in status
        assert "NoNewPrivs:\t1" in status and "Seccomp:\t2" in status
    assert "docker-default (enforce)" in Path("/proc/self/attr/current").read_text()
    assert not Path("/dev/fuse").exists()
    assert denied_mount(b"/tmp", b"tmpfs", 0, b"size=4096") == errno.EPERM
    with urllib.request.urlopen("http://127.0.0.1:8765/login") as response:
        assert response.status == 200 and b"password" in response.read()
    tools()
    print(
        "Service: non-root; zero capabilities; AppArmor and seccomp enforced; no FUSE device"
    )


def nfs():
    path = b"/state/data/secrets"
    args = struct.pack("!I", len(path)) + path + bytes((-len(path)) % 4)
    request = struct.pack("!10I", 1, 0, 2, 100005, 3, 1, 0, 0, 0, 0) + args
    with socket.create_connection(("127.0.0.1", 2049), timeout=3) as connection:
        connection.sendall(struct.pack("!I", len(request) | 0x80000000) + request)
        header = connection.recv(4, socket.MSG_WAITALL)
        length = struct.unpack("!I", header)[0] & 0x7FFFFFFF
        reply = connection.recv(length, socket.MSG_WAITALL)
    assert struct.unpack("!7I", reply) == (1, 1, 0, 0, 0, 0, 13)
    print("NFS: private state export refused")


def consumer():
    status = Path("/proc/self/status").read_text()
    for capability in ("CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb"):
        assert f"{capability}:\t0000000000000000" in status
    assert "NoNewPrivs:\t1" in status and "Seccomp:\t2" in status
    assert not Path("/dev/fuse").exists()
    assert "docker-default (enforce)" in Path("/proc/self/attr/current").read_text()
    assert list(Path("/data/media").iterdir()) == []
    # A fresh name forces an RPC through the retained root handle after restart.
    assert not Path(f"/data/media/.restart-probe-{time.time_ns()}").exists()
    assert not Path("/data/media/config.yaml").exists()
    try:
        Path("/data/media/forbidden").write_bytes(b"must fail")
    except OSError as error:
        assert error.errno in (errno.EROFS, errno.EACCES, errno.EPERM)
    else:
        raise AssertionError("NFS consumer wrote to the library")
    print("NFS consumer: read-only projection with no capabilities or FUSE device")


if __name__ == "__main__":
    {"helper": helper, "service": service, "nfs": nfs, "consumer": consumer}[
        sys.argv[1]
    ]()

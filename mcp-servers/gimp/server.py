#!/usr/bin/env -S uv run --no-project --quiet --script
# /// script
# requires-python = ">=3.10"
# dependencies = ["mcp>=1.2.0"]
# ///
"""
gimp MCP server.

A broker over the local GIMP 2.10 install that lets Claude *programmatically
compose and edit raster images* -- logos, banners, social cards, icons, mockups
-- entirely headlessly: create canvases, fill solid / gradient backgrounds, lay
down text with any installed font, draw rectangles / ellipses, load + composite
existing image files, scale / crop / flatten, and export to PNG / JPEG / WebP /
TIFF / XCF. Plus a one-shot `compose_image` that renders a whole layered graphic
from a single structured spec, PDB introspection so Claude can discover and call
*any* of GIMP's ~1300 procedures, and Script-Fu / Python-Fu escape hatches for
anything the dedicated tools don't cover.

It is the image-editor sibling of the `keynote` server: same Result / timeout /
`_annotate` / read-write-split philosophy, but instead of `osascript` it speaks
to a **resident GIMP Script-Fu server** over TCP.

Why a resident server (not `gimp -b` per call)
-----------------------------------------------
GIMP's cold start is ~12s. Spawning it per tool call would make every operation
unusable. Instead we launch `gimp-console` ONCE with its built-in Script-Fu
server (`plug-in-script-fu-server`) bound to 127.0.0.1, keep it resident, and
send each Script-Fu program over the socket (milliseconds per call). The first
tool call pays the one-time boot; everything after is fast, and image state
(open images + layers) persists across calls so Claude can build a graphic
incrementally and Read the exported PNG to see intermediate results.

Wire protocol (GIMP's, not ours)
--------------------------------
* request : 'G' + uint16-BE(len) + script
* response: 'G' + err_byte + uint16-BE(len) + body
* err_byte 0 == success; body is the Scheme-printed value of the last
  expression (strings come back quoted), or "Error: ..." text on failure.
* The uint16 length caps a single script / reply at 65535 bytes. Big binary
  never crosses the socket -- loads/exports go through file paths on disk.

Design notes
------------
* No credentials, no network exposure. The server binds 127.0.0.1 only; it is a
  remote-eval socket, so it must never listen off-host. (`GIMP_HOST` is honored
  but should stay loopback.)
* Lifecycle: any tool auto-starts the resident server if it's down (a benign,
  no-data-at-risk action -- unlike the data mutations the read/write split
  guards). `start_server` / `stop_server` make it explicit; `stop_server` only
  kills the PID we spawned (tracked in a pidfile), never a stray GIMP.
* Read/write split mirrors the other servers: `health`, `list_images`,
  `get_image_info`, `list_fonts`, `pdb_search`, `pdb_info` only inspect; every
  tool that creates / edits / exports an image is separate so each gets its own
  permission prompt.
* GIMP 2.10's Script-Fu is TinyScheme. Colors are accepted as "#rrggbb", a
  [r,g,b] list, or a few names; emitted as `(list r g b)`. Python strings are
  escaped into Scheme string literals by `_sf`.
* GIMP 2.10's Python-Fu is Python *2.7* -- `eval_python_fu` reflects that.

Everything is configurable via env (see constants below).
"""
from __future__ import annotations

import os
import re
import shutil
import signal
import socket
import struct
import subprocess
import threading
import time
from dataclasses import dataclass

from mcp.server.fastmcp import FastMCP


# --- configuration (overridable from .mcp.json `env`) ------------------------
def _default_bin() -> str:
    mac = "/Applications/GIMP.app/Contents/MacOS/gimp-console-2.10"
    if os.path.exists(mac):
        return mac
    for cand in ("gimp-console-2.10", "gimp-console", "gimp"):
        found = shutil.which(cand)
        if found:
            return found
    return mac  # report a sensible path even if missing


GIMP_BIN = os.environ.get("GIMP_BIN", _default_bin())
HOST = os.environ.get("GIMP_HOST", "127.0.0.1")
PORT = int(os.environ.get("GIMP_PORT", "10008"))
STARTUP_TIMEOUT = int(os.environ.get("GIMP_STARTUP_TIMEOUT", "60"))
DEFAULT_TIMEOUT = int(os.environ.get("GIMP_EVAL_TIMEOUT", "120"))
MAX_TIMEOUT = int(os.environ.get("GIMP_MAX_TIMEOUT", "1800"))
OUTPUT_DIR = os.path.expanduser(os.environ.get("GIMP_OUTPUT_DIR", "~/gimp-mcp-output"))
LOGFILE = os.environ.get("GIMP_LOGFILE", "/tmp/gimp-mcp-sf-server.log")
STDOUT_LOG = os.environ.get("GIMP_STDOUT_LOG", "/tmp/gimp-mcp-sf-stdout.log")
PIDFILE = os.environ.get("GIMP_PIDFILE", "/tmp/gimp-mcp-sf-server.pid")
# GIMP's writable profile (it wants these subdirs to exist or data saves warn).
PROFILE_DIR = os.path.expanduser(
    os.environ.get("GIMP_PROFILE_DIR", "~/Library/Application Support/GIMP/2.10")
)

mcp = FastMCP("gimp")
_start_lock = threading.Lock()

# Formats that carry an alpha channel (so export keeps transparency instead of
# flattening onto a background).
_ALPHA_FORMATS = {"png", "webp", "tif", "tiff", "gif", "xcf", "ora"}
_NAMED_COLORS = {
    "white": (255, 255, 255), "black": (0, 0, 0), "red": (220, 40, 40),
    "green": (40, 180, 80), "blue": (40, 80, 200), "yellow": (245, 210, 40),
    "cyan": (40, 200, 210), "magenta": (210, 40, 180), "orange": (245, 140, 40),
    "gray": (128, 128, 128), "grey": (128, 128, 128), "navy": (30, 40, 120),
    "violet": (109, 92, 252), "cyanaccent": (109, 215, 255),
}


# =============================================================================
# low-level: Script-Fu server transport + lifecycle
# =============================================================================
@dataclass
class Eval:
    """Result of one Script-Fu evaluation over the resident server socket."""
    ok: bool            # GIMP error byte was 0
    value: str          # Scheme-printed return value, or GIMP's "Error: ..." text
    transport: str | None = None  # set when we never reached GIMP (down/timeout)

    def render(self) -> str:
        if self.transport:
            return f"TRANSPORT ERROR: {self.transport}"
        if self.ok:
            return self.value.strip() if self.value.strip() else "(ok)"
        return self.value.strip() or "(unspecified GIMP error)"


def _clamp_timeout(timeout: int | None) -> int:
    t = DEFAULT_TIMEOUT if not timeout else int(timeout)
    return max(5, min(t, MAX_TIMEOUT))


def _recvn(sock: socket.socket, n: int) -> bytes:
    buf = b""
    while len(buf) < n:
        chunk = sock.recv(n - len(buf))
        if not chunk:
            break
        buf += chunk
    return buf


def _server_running() -> bool:
    try:
        with socket.create_connection((HOST, PORT), timeout=1.0):
            return True
    except OSError:
        return False


def _ensure_profile_dirs() -> None:
    for sub in ("gradients", "patterns", "brushes", "fonts", "palettes",
                "dynamics", "tool-presets"):
        try:
            os.makedirs(os.path.join(PROFILE_DIR, sub), exist_ok=True)
        except OSError:
            pass
    try:
        os.makedirs(OUTPUT_DIR, exist_ok=True)
    except OSError:
        pass


def _server_sexp() -> str:
    log = LOGFILE.replace("\\", "\\\\").replace('"', '\\"')
    return (f'(plug-in-script-fu-server RUN-NONINTERACTIVE "{HOST}" {PORT} '
            f'"{log}")')


def _start_server() -> str | None:
    """Spawn the resident GIMP Script-Fu server; poll until it accepts. Returns
    an error string, or None on success. Caller must hold _start_lock."""
    if _server_running():
        return None
    if not os.path.exists(GIMP_BIN) and not shutil.which(GIMP_BIN):
        return (f"GIMP binary not found at {GIMP_BIN!r}. Install GIMP 2.10 or set "
                f"GIMP_BIN in .mcp.json env.")
    _ensure_profile_dirs()
    argv = [GIMP_BIN, "-i", "-b", _server_sexp(), "-b", "(gimp-quit 0)"]
    try:
        out = open(STDOUT_LOG, "ab")
        proc = subprocess.Popen(
            argv, stdout=out, stderr=subprocess.STDOUT,
            stdin=subprocess.DEVNULL, start_new_session=True,
        )
    except OSError as e:
        return f"failed to launch GIMP: {e}"
    try:
        with open(PIDFILE, "w") as f:
            f.write(str(proc.pid))
    except OSError:
        pass
    deadline = time.time() + STARTUP_TIMEOUT
    while time.time() < deadline:
        if proc.poll() is not None:  # GIMP died during boot
            tail = _tail(STDOUT_LOG, 12)
            return (f"GIMP exited during startup (code {proc.returncode}).\n"
                    f"--- {STDOUT_LOG} (tail) ---\n{tail}")
        if _server_running():
            return None
        time.sleep(0.5)
    return (f"GIMP Script-Fu server did not open {HOST}:{PORT} within "
            f"{STARTUP_TIMEOUT}s.\n--- {STDOUT_LOG} (tail) ---\n"
            f"{_tail(STDOUT_LOG, 12)}")


def _ensure_server() -> str | None:
    if _server_running():
        return None
    with _start_lock:
        return _start_server()


def _read_pidfile() -> int | None:
    try:
        with open(PIDFILE) as f:
            return int(f.read().strip())
    except (OSError, ValueError):
        return None


def _stop_server() -> str:
    pid = _read_pidfile()
    if pid is None:
        return ("no pidfile -- not stopping anything. (If a server is running it "
                "was started outside this MCP; kill it yourself.)")
    # Only kill if it's actually a GIMP process we plausibly spawned.
    try:
        cmd = subprocess.run(["ps", "-p", str(pid), "-o", "command="],
                             stdout=subprocess.PIPE, text=True, timeout=5).stdout
    except (OSError, subprocess.SubprocessError):
        cmd = ""
    if not cmd.strip():
        _rm(PIDFILE)
        return f"pid {pid} not running (cleared stale pidfile)."
    if "gimp" not in cmd.lower():
        return (f"refused: pid {pid} is not a GIMP process ({cmd.strip()[:80]}). "
                f"Not killing it.")
    try:
        os.killpg(os.getpgid(pid), signal.SIGTERM)
    except OSError:
        try:
            os.kill(pid, signal.SIGTERM)
        except OSError as e:
            return f"failed to terminate pid {pid}: {e}"
    for _ in range(20):
        if not _server_running():
            break
        time.sleep(0.25)
    if _server_running():
        try:
            os.kill(pid, signal.SIGKILL)
        except OSError:
            pass
    _rm(PIDFILE)
    return f"stopped resident GIMP server (pid {pid})."


def _tail(path: str, n: int) -> str:
    try:
        with open(path, "r", errors="replace") as f:
            return "".join(f.readlines()[-n:]).rstrip()
    except OSError:
        return "(no log)"


def _rm(path: str) -> None:
    try:
        os.unlink(path)
    except OSError:
        pass


def _sock_eval(script: str, timeout: int) -> Eval:
    payload = script.encode("utf-8")
    if len(payload) > 0xFFFF:
        return Eval(False, "", transport=(
            f"script is {len(payload)} bytes; the Script-Fu server caps a single "
            f"request at 65535. Split it, or write data to a file and load it."))
    try:
        with socket.create_connection((HOST, PORT), timeout=timeout) as s:
            s.settimeout(timeout)
            s.sendall(b"G" + struct.pack(">H", len(payload)) + payload)
            hdr = _recvn(s, 4)
            if len(hdr) < 4 or hdr[0:1] != b"G":
                return Eval(False, "", transport=f"bad/short response header: {hdr!r}")
            err = hdr[1]
            ln = (hdr[2] << 8) | hdr[3]
            body = _recvn(s, ln).decode("utf-8", "replace")
            return Eval(err == 0, body)
    except (ConnectionRefusedError, OSError) as e:
        return Eval(False, "", transport=f"{type(e).__name__}: {e}")


def _eval(script: str, timeout: int | None = None, ensure: bool = True) -> Eval:
    """Run a Script-Fu program on the resident server, auto-starting it first."""
    t = _clamp_timeout(timeout)
    if ensure:
        err = _ensure_server()
        if err:
            return Eval(False, "", transport=err)
    ev = _sock_eval(script, t)
    if ev.transport and ensure:  # server may have died; try one restart
        with _start_lock:
            err = _start_server()
        if not err:
            ev = _sock_eval(script, t)
    return ev


# =============================================================================
# error annotation + success rendering
# =============================================================================
def _annotate(ev: Eval) -> str:
    txt = ev.render()
    s = (ev.value or "") + (ev.transport or "")
    hints: list[str] = []
    low = s.lower()
    if ev.transport:
        hints.append("The resident GIMP server isn't reachable. Cold start takes "
                     "~12s; call `health` or `start_server` and retry. Check that "
                     "GIMP 2.10 is installed (GIMP_BIN).")
    if "invalid number of arguments" in low or "called with" in low:
        hints.append("Wrong arg count for a PDB procedure. Call `pdb_info "
                     "<procedure>` for its exact signature.")
    if "not a valid image" in low or ("image" in low and "invalid" in low):
        hints.append("That image id doesn't exist. Call `list_images` for the "
                     "currently-open image ids.")
    if "procedure" in low and ("not found" in low or "does not exist" in low):
        hints.append("Unknown procedure name. Search with `pdb_search <keyword>`.")
    if "no fonts" in low or "font" in low and "not" in low:
        hints.append("Font lookup failed. Call `list_fonts` for installed names "
                     "(e.g. 'Sans Bold').")
    if hints:
        return txt + "\n\n--- hints ---\n" + "\n".join("• " + h for h in hints)
    return txt


# Trivial Script-Fu return values that aren't worth echoing after a friendly msg.
_NOISE = {"(ok)", "(#t)", "#t", "(#f)", "#f", "()", "(0)", ""}


def _ok(ev: Eval, msg: str) -> str:
    if ev.ok:
        extra = ev.value.strip()
        keep = extra and extra not in _NOISE and extra != msg
        return msg + (f"\n{extra}" if keep else "")
    return _annotate(ev)


# =============================================================================
# Scheme emit + parse helpers
# =============================================================================
def _sf(s: str) -> str:
    """Escape a Python string into a TinyScheme double-quoted string literal."""
    s = (str(s).replace("\\", "\\\\").replace('"', '\\"')
         .replace("\r\n", "\\n").replace("\r", "\\n").replace("\n", "\\n")
         .replace("\t", "\\t"))
    return '"' + s + '"'


def _rgb(spec) -> tuple[int, int, int]:
    if isinstance(spec, (list, tuple)) and len(spec) >= 3:
        return (int(spec[0]) & 255, int(spec[1]) & 255, int(spec[2]) & 255)
    if isinstance(spec, str):
        t = spec.strip().lower()
        if t in _NAMED_COLORS:
            return _NAMED_COLORS[t]
        h = t[1:] if t.startswith("#") else t
        if len(h) == 3 and re.fullmatch(r"[0-9a-f]{3}", h):
            h = "".join(c * 2 for c in h)
        if len(h) == 6 and re.fullmatch(r"[0-9a-f]{6}", h):
            return (int(h[0:2], 16), int(h[2:4], 16), int(h[4:6], 16))
    raise ValueError(f"unrecognized color {spec!r} (use '#rrggbb', [r,g,b], or a "
                     f"name like 'white'/'navy')")


def _color(spec) -> str:
    r, g, b = _rgb(spec)
    return f"(list {r} {g} {b})"


def _b(v) -> str:
    return "TRUE" if v else "FALSE"


def _is_transparent(bg) -> bool:
    return isinstance(bg, str) and bg.strip().lower() in ("transparent", "none", "alpha")


def _parse_int(text: str) -> int | None:
    m = re.search(r"-?\d+", text or "")
    return int(m.group()) if m else None


def _parse_id_vector(text: str) -> list[int]:
    """Pull ids out of a `(N #(a b c))` style Script-Fu list-of-ids response."""
    m = re.search(r"#\(([^)]*)\)", text or "")
    body = m.group(1) if m else (text or "")
    return [int(x) for x in re.findall(r"-?\d+", body)]


# =============================================================================
# Scheme snippet emitters (shared by the convenience tools AND compose_image)
# =============================================================================
def _emit_background(bg, w: int, h: int) -> str:
    lines = [f'(let ((base (car (gimp-layer-new image {w} {h} RGBA-IMAGE '
             f'"background" 100 LAYER-MODE-NORMAL))))',
             '  (gimp-image-insert-layer image base 0 -1)',
             '  (gimp-image-set-active-layer image base)']
    if _is_transparent(bg):
        lines.append('  (gimp-drawable-fill base FILL-TRANSPARENT)')
    elif isinstance(bg, dict) and bg.get("gradient"):
        lines.append('  (gimp-drawable-fill base FILL-TRANSPARENT)')
        lines.append("  " + _emit_gradient("base", bg["gradient"], w, h))
    else:
        lines.append(f'  (gimp-context-set-foreground {_color(bg)})')
        lines.append('  (gimp-drawable-fill base FILL-FOREGROUND)')
    lines.append(')')
    return "\n".join(lines)


def _emit_gradient(drawable_var: str, g: dict, w: int, h: int) -> str:
    x1, y1 = g.get("x1", 0), g.get("y1", 0)
    x2, y2 = g.get("x2", 0), g.get("y2", h)
    c1, c2 = _color(g.get("color1", "#ffffff")), _color(g.get("color2", "#000000"))
    gt = ("GRADIENT-RADIAL"
          if str(g.get("type", "linear")).lower().startswith("rad")
          else "GRADIENT-LINEAR")
    return (f'(begin (gimp-context-set-foreground {c1}) '
            f'(gimp-context-set-background {c2}) '
            f'(gimp-context-set-gradient-fg-bg-rgb) '
            f'(gimp-image-set-active-layer image {drawable_var}) '
            f'(gimp-drawable-edit-gradient-fill {drawable_var} {gt} 0 FALSE 1 0 '
            f'TRUE {x1} {y1} {x2} {y2}))')


def _emit_shape(el: dict, w: int, h: int) -> str:
    kind = el.get("type")
    sel = "gimp-image-select-ellipse" if kind == "ellipse" else "gimp-image-select-rectangle"
    x, y = el.get("x", 0), el.get("y", 0)
    ew, eh = el.get("width", w), el.get("height", h)
    color = el.get("color", "black")
    return ("(let ((ly (car (gimp-layer-new image "
            f"{w} {h} RGBA-IMAGE {_sf(kind or 'shape')} 100 LAYER-MODE-NORMAL))))"
            " (gimp-image-insert-layer image ly 0 -1)"
            " (gimp-image-set-active-layer image ly)"
            " (gimp-drawable-fill ly FILL-TRANSPARENT)"
            f" (gimp-context-set-foreground {_color(color)})"
            f" ({sel} image CHANNEL-OP-REPLACE {x} {y} {ew} {eh})"
            " (gimp-edit-fill ly FILL-FOREGROUND)"
            " (gimp-selection-none image))")


def _emit_text(el: dict) -> str:
    text = el.get("text", "")
    x, y = el.get("x", 0), el.get("y", 0)
    size = el.get("size", 48)
    color = el.get("color", "black")
    font = el.get("font", "Sans Bold")
    return (f"(begin (gimp-context-set-foreground {_color(color)})"
            f" (let ((tl (car (gimp-text-fontname image -1 {x} {y} {_sf(text)} 0 "
            f"TRUE {size} UNIT-PIXEL {_sf(font)}))))"
            " (gimp-image-set-active-layer image tl)))")


def _emit_image(el: dict) -> str:
    path = os.path.expanduser(str(el.get("path", "")))
    x, y = el.get("x", 0), el.get("y", 0)
    scale = ""
    if el.get("width") and el.get("height"):
        scale = f" (gimp-layer-scale nl {int(el['width'])} {int(el['height'])} FALSE)"
    return (f"(let ((nl (car (gimp-file-load-layer RUN-NONINTERACTIVE image "
            f"{_sf(path)}))))"
            " (gimp-image-insert-layer image nl 0 -1)"
            f"{scale}"
            f" (gimp-layer-set-offsets nl {x} {y})"
            " (gimp-image-set-active-layer image nl))")


def _emit_element(el: dict, w: int, h: int) -> str:
    t = (el.get("type") or "").lower()
    if t == "text":
        return _emit_text(el)
    if t in ("rect", "rectangle", "ellipse", "circle"):
        if t == "circle":
            el = dict(el, type="ellipse")
        elif t == "rectangle":
            el = dict(el, type="rect")
        return _emit_shape(el, w, h)
    if t == "image":
        return _emit_image(el)
    if t == "gradient":
        return ("(let ((gl (car (gimp-layer-new image "
                f"{w} {h} RGBA-IMAGE \"gradient\" 100 LAYER-MODE-NORMAL))))"
                " (gimp-image-insert-layer image gl 0 -1)"
                " (gimp-drawable-fill gl FILL-TRANSPARENT) "
                + _emit_gradient("gl", el, w, h) + ")")
    raise ValueError(f"unknown element type {el.get('type')!r} (use text, rect, "
                     f"ellipse, image, gradient)")


# =============================================================================
# shared export primitive (used by export_image AND compose_image)
# =============================================================================
def _ext(path: str) -> str:
    return os.path.splitext(path)[1].lower().lstrip(".")


def _do_export(image_id: int, path: str, flatten: bool | None, quality: float,
               timeout: int) -> str:
    p = os.path.expanduser(path)
    d = os.path.dirname(p)
    if d and not os.path.isdir(d):
        try:
            os.makedirs(d, exist_ok=True)
        except OSError as e:
            return f"refused: cannot create output dir {d}: {e}"
    ext = _ext(p)
    # Native XCF keeps every layer -- save the live image, no flatten/dup.
    if ext == "xcf":
        ev = _eval(
            f'(let ((d (car (gimp-image-get-active-drawable {image_id})))) '
            f'(gimp-file-save RUN-NONINTERACTIVE {image_id} d {_sf(p)} {_sf(p)}))',
            timeout=timeout)
        return _ok(ev, f"saved layered XCF -> {p}")
    keep_alpha = ext in _ALPHA_FORMATS if flatten is None else (not flatten)
    merge = ("(gimp-image-merge-visible-layers dup CLIP-TO-IMAGE)"
             if keep_alpha else "(gimp-image-flatten dup)")
    if ext in ("jpg", "jpeg"):
        q = max(0.0, min(1.0, float(quality)))
        saver = (f'(file-jpeg-save RUN-NONINTERACTIVE dup d {_sf(p)} {_sf(p)} '
                 f'{q} 0 1 1 "" 0 1 0 0)')
    elif ext == "png":
        saver = (f'(file-png-save RUN-NONINTERACTIVE dup d {_sf(p)} {_sf(p)} '
                 f'0 9 1 1 1 1 1)')
    else:  # webp/tiff/bmp/gif/... -> let GIMP pick by extension
        saver = f'(gimp-file-save RUN-NONINTERACTIVE dup d {_sf(p)} {_sf(p)})'
    script = (f"(let* ((dup (car (gimp-image-duplicate {image_id}))))"
              f"  {merge}"
              "  (let ((d (car (gimp-image-get-active-drawable dup))))"
              f"   {saver})"
              "  (gimp-image-delete dup))")
    ev = _eval(script, timeout=timeout)
    return _ok(ev, f"exported image {image_id} -> {p}"
                   + (" (alpha preserved)" if keep_alpha else " (flattened)"))


# =============================================================================
# READ-ONLY tools
# =============================================================================
@mcp.tool()
def health() -> str:
    """Readiness probe + the place to START. Ensures the resident GIMP Script-Fu
    server is up (booting it if needed -- the first call can take ~12s), then
    reports GIMP version, the bind address, how many images are open, and the
    default output dir.
    """
    err = _ensure_server()
    if err:
        return "# gimp health\nServer: DOWN\n\n" + err
    ver = _eval("(car (gimp-version))")
    imgs = _eval("(gimp-image-list)")
    n = len(_parse_id_vector(imgs.value)) if imgs.ok else "?"
    return ("# gimp health\n"
            f"Binary       : {GIMP_BIN}\n"
            f"Server       : UP at {HOST}:{PORT}\n"
            f"GIMP version : {ver.value.strip().strip(chr(34)) if ver.ok else '?'}\n"
            f"Open images  : {n}\n"
            f"Output dir   : {OUTPUT_DIR}\n"
            f"Server log   : {LOGFILE}")


@mcp.tool()
def list_images() -> str:
    """List every image currently open in the resident GIMP, with id, name, and
    dimensions. These ids are what every editing/export tool takes as `image_id`.
    """
    ev = _eval("(gimp-image-list)")
    if not ev.ok:
        return _annotate(ev)
    ids = _parse_id_vector(ev.value)
    if not ids:
        return "no images open. Create one with new_image or compose_image."
    rows = []
    for i in ids:
        info = _eval(
            f'(list (car (gimp-image-width {i})) (car (gimp-image-height {i})) '
            f'(car (gimp-image-get-name {i})))')
        nums = re.findall(r"-?\d+", info.value)
        name = re.search(r'"([^"]*)"', info.value)
        w = nums[0] if len(nums) > 0 else "?"
        h = nums[1] if len(nums) > 1 else "?"
        rows.append(f"  #{i}  {w}x{h}  {name.group(1) if name else ''}")
    return f"{len(ids)} open image(s):\n" + "\n".join(rows)


@mcp.tool()
def get_image_info(image_id: int) -> str:
    """Full detail on one open image: dimensions, base type, filename, and every
    layer (id, name, opacity, visibility, size). Call list_images first for ids.

    Args:
        image_id: an open image id (from list_images / new_image / compose_image).
    """
    meta = _eval(
        f'(list (car (gimp-image-width {image_id})) '
        f'(car (gimp-image-height {image_id})) '
        f'(car (gimp-image-get-name {image_id})) '
        f'(car (gimp-image-get-filename {image_id})))')
    if not meta.ok:
        return _annotate(meta)
    nums = re.findall(r"-?\d+", meta.value)
    strs = re.findall(r'"([^"]*)"', meta.value)
    w = nums[0] if nums else "?"
    h = nums[1] if len(nums) > 1 else "?"
    out = [f"image #{image_id}: {w}x{h}",
           f"name    : {strs[0] if strs else ''}",
           f"file    : {strs[1] if len(strs) > 1 else '(unsaved)'}",
           "layers (top -> bottom):"]
    layers = _eval(f"(gimp-image-get-layers {image_id})")
    for lid in _parse_id_vector(layers.value):
        li = _eval(
            f'(list (car (gimp-item-get-name {lid})) '
            f'(car (gimp-layer-get-opacity {lid})) '
            f'(car (gimp-item-get-visible {lid})) '
            f'(car (gimp-drawable-width {lid})) '
            f'(car (gimp-drawable-height {lid})))')
        nm = re.search(r'"([^"]*)"', li.value)
        n2 = re.findall(r"-?\d+(?:\.\d+)?", li.value)
        out.append(f"  #{lid}  {nm.group(1) if nm else ''}  "
                   f"opacity={n2[0] if n2 else '?'}  "
                   f"size={n2[2] if len(n2) > 2 else '?'}x"
                   f"{n2[3] if len(n2) > 3 else '?'}")
    return "\n".join(out)


@mcp.tool()
def list_fonts(filter: str = "") -> str:
    """List installed font names usable by add_text / compose_image text elements.

    Args:
        filter: optional case-insensitive regexp; "" lists all (can be long --
                the reply is capped at ~64KB, so filter for big font sets).
    """
    ev = _eval(f"(gimp-fonts-get-list {_sf(filter)})")
    if not ev.ok:
        return _annotate(ev)
    names = re.findall(r'"([^"]*)"', ev.value)
    if not names:
        return f"no fonts match {filter!r}." if filter else "no fonts found."
    head = f"{len(names)} font(s)" + (f" matching {filter!r}" if filter else "")
    return head + ":\n" + "\n".join("  " + n for n in names)


@mcp.tool()
def pdb_search(query: str) -> str:
    """Search GIMP's Procedural Database for procedures whose NAME matches a
    keyword. GIMP exposes ~1300 procedures (every menu action + plug-in); this is
    how Claude discovers what to call via eval_script_fu / pdb_info.

    Args:
        query: a substring/regexp matched against procedure names, e.g. "text",
               "gradient", "drawable-edit", "file-.*-save".
    """
    pat = query if any(c in query for c in ".*[]()|") else f".*{re.escape(query)}.*"
    ev = _eval(f'(gimp-pdb-query {_sf(pat)} "" "" "" "" "" "")')
    if not ev.ok:
        return _annotate(ev)
    names = sorted(set(re.findall(r'"([^"]*)"', ev.value)))
    if not names:
        return f"no procedures match {query!r}."
    shown = names[:200]
    out = f"{len(names)} procedure(s) match {query!r}"
    if len(names) > len(shown):
        out += f" (showing first {len(shown)})"
    return out + ":\n" + "\n".join("  " + n for n in shown)


@mcp.tool()
def pdb_info(procedure: str) -> str:
    """Show a PDB procedure's exact signature: blurb + each input arg
    (type/name/description) + each return value. Use before calling an unfamiliar
    procedure via eval_script_fu so you get the argument count and order right.

    Args:
        procedure: a procedure name, e.g. "gimp-image-new", "gimp-text-fontname",
                   "gimp-drawable-edit-gradient-fill" (see pdb_search).
    """
    info = _eval(f"(gimp-procedural-db-proc-info {_sf(procedure)})")
    if not info.ok:
        return _annotate(info)
    nums = re.findall(r"-?\d+", info.value)
    blurb = re.search(r'"([^"]*)"', info.value)
    nargs = int(nums[-2]) if len(nums) >= 2 else 0
    nvals = int(nums[-1]) if len(nums) >= 1 else 0
    out = [f"# {procedure}", blurb.group(1) if blurb else "", "", "args:"]
    for i in range(nargs):
        a = _eval(f"(gimp-procedural-db-proc-arg {_sf(procedure)} {i})")
        ss = re.findall(r'"([^"]*)"', a.value)
        tp = re.findall(r"-?\d+", a.value)
        out.append(f"  {i}: {ss[0] if ss else '?'} ({_pdb_type(tp)})"
                   f"  -- {ss[1] if len(ss) > 1 else ''}")
    out.append("returns:")
    for i in range(nvals):
        v = _eval(f"(gimp-procedural-db-proc-val {_sf(procedure)} {i})")
        ss = re.findall(r'"([^"]*)"', v.value)
        out.append(f"  {i}: {ss[0] if ss else '?'}"
                   f"  -- {ss[1] if len(ss) > 1 else ''}")
    if nargs == 0 and nvals == 0:
        out.append("  (none)")
    return "\n".join(x for x in out if x is not None)


_PDB_TYPES = {0: "INT32", 1: "INT16", 2: "INT8", 3: "FLOAT", 4: "STRING",
              5: "INT32ARRAY", 6: "INT16ARRAY", 7: "INT8ARRAY", 8: "FLOATARRAY",
              9: "STRINGARRAY", 10: "COLOR", 11: "ITEM", 12: "DISPLAY",
              13: "IMAGE", 14: "LAYER", 15: "CHANNEL", 16: "DRAWABLE",
              17: "SELECTION", 18: "COLORARRAY", 19: "VECTORS", 20: "PARASITE",
              21: "STATUS"}


def _pdb_type(nums: list[str]) -> str:
    return _PDB_TYPES.get(int(nums[0]), nums[0]) if nums else "?"


# =============================================================================
# SERVER lifecycle (write)
# =============================================================================
@mcp.tool()
def start_server() -> str:
    """Explicitly boot the resident GIMP Script-Fu server (idempotent). Any other
    tool also auto-starts it; call this to pay the ~12s cold start up front.
    """
    if _server_running():
        return f"already running at {HOST}:{PORT}."
    t0 = time.time()
    with _start_lock:
        err = _start_server()
    if err:
        return "failed to start:\n" + err
    return f"resident GIMP server up at {HOST}:{PORT} (boot {time.time() - t0:.1f}s)."


@mcp.tool()
def stop_server() -> str:
    """Stop the resident GIMP server THIS MCP started (kills only the pidfile's
    pid, and only if it's a GIMP process). Frees memory; loses all open
    (unexported) images. Any later tool will boot a fresh server.
    """
    return _stop_server()


# =============================================================================
# IMAGE lifecycle + composition (write)
# =============================================================================
@mcp.tool()
def new_image(width: int, height: int, background: str = "white") -> str:
    """Create a new RGBA image and return its id (use that id with the editing /
    export tools, or list_images).

    Args:
        width, height: canvas size in pixels.
        background: "white" (default), "transparent", a named color, "#rrggbb",
                    or "r,g,b". Transparent gives a clean alpha canvas for logos.
    """
    try:
        bg = [int(x) for x in background.split(",")] if "," in background else background
        bg_snip = _emit_background(bg, int(width), int(height))
    except ValueError as e:
        return f"refused: {e}"
    script = (f"(let ((image (car (gimp-image-new {int(width)} {int(height)} RGB))))"
              f" {bg_snip}"
              " image)")
    ev = _eval(script)
    if not ev.ok:
        return _annotate(ev)
    iid = _parse_int(ev.value)
    return (f"created image #{iid} ({width}x{height}, bg={background}).\n"
            f"Add content (add_text / fill_rectangle / ...) then export_image.")


@mcp.tool()
def load_image(path: str) -> str:
    """Open an existing image FILE into GIMP for editing, returning its image id.

    Args:
        path: filesystem path to a PNG/JPEG/etc (~ expanded).
    """
    p = os.path.expanduser(path)
    if not os.path.exists(p):
        return f"refused: no file at {p}"
    ev = _eval(f"(car (gimp-file-load RUN-NONINTERACTIVE {_sf(p)} {_sf(p)}))")
    if not ev.ok:
        return _annotate(ev)
    return f"loaded {p} as image #{_parse_int(ev.value)}."


@mcp.tool()
def add_text(image_id: int, text: str, x: int = 0, y: int = 0, size: int = 48,
             color: str = "black", font: str = "Sans Bold") -> str:
    """Add a text layer to an open image (the core logo / wordmark primitive).
    Returns the new text layer id.

    Args:
        image_id: target open image id.
        text: the string to render.
        x, y: top-left of the text in pixels.
        size: font size in pixels.
        color: text color ("#rrggbb", [r,g,b], or a name).
        font: a font name from list_fonts (e.g. "Sans Bold", "Serif Italic").
    """
    try:
        col = _color(color)
    except ValueError as e:
        return f"refused: {e}"
    script = (f"(begin (gimp-context-set-foreground {col})"
              f" (car (gimp-text-fontname {image_id} -1 {x} {y} {_sf(text)} 0 "
              f"TRUE {size} UNIT-PIXEL {_sf(font)})))")
    ev = _eval(script)
    return _ok(ev, f"added text layer #{_parse_int(ev.value)} to image {image_id}")


@mcp.tool()
def fill_rectangle(image_id: int, x: int, y: int, width: int, height: int,
                   color: str = "black") -> str:
    """Draw a solid filled rectangle on a new layer of an open image.

    Args:
        image_id: target image id.
        x, y, width, height: rectangle geometry in pixels.
        color: fill color.
    """
    return _draw_shape(image_id, "rect", x, y, width, height, color)


@mcp.tool()
def draw_ellipse(image_id: int, x: int, y: int, width: int, height: int,
                 color: str = "black") -> str:
    """Draw a solid filled ellipse (use width==height for a circle) on a new layer.

    Args:
        image_id: target image id.
        x, y: top-left of the ellipse's bounding box.
        width, height: bounding-box size in pixels.
        color: fill color.
    """
    return _draw_shape(image_id, "ellipse", x, y, width, height, color)


def _image_dims(image_id: int) -> tuple[int, int] | None:
    """(width, height) of an open image, or None if it can't be read."""
    dims = _eval(f"(list (car (gimp-image-width {image_id})) "
                 f"(car (gimp-image-height {image_id})))")
    nums = re.findall(r"\d+", dims.value)
    if not dims.ok or len(nums) < 2:
        return None
    return int(nums[0]), int(nums[1])


def _draw_shape(image_id: int, kind: str, x, y, w, h, color) -> str:
    # The emitters use a free Scheme var `image`; bind it to the real id with a
    # `let` (NOT a textual substitution -- that would corrupt `gimp-image-*`).
    wh = _image_dims(image_id)
    if wh is None:
        return f"refused: image {image_id} not found (see list_images)."
    try:
        snip = _emit_shape({"type": kind, "x": x, "y": y, "width": w,
                            "height": h, "color": color}, wh[0], wh[1])
    except ValueError as e:
        return f"refused: {e}"
    ev = _eval(f"(let ((image {image_id})) {snip} "
               f"(car (gimp-image-get-active-layer image)))")
    return _ok(ev, f"drew {kind} on image {image_id}")


@mcp.tool()
def apply_gradient(image_id: int, color1: str, color2: str, x1: int = 0,
                   y1: int = 0, x2: int | None = None, y2: int | None = None,
                   type: str = "linear") -> str:
    """Fill a new layer with a color1->color2 gradient.

    Args:
        image_id: target image id.
        color1, color2: start and end colors.
        x1, y1: gradient start point (default top-left).
        x2, y2: gradient end point (default: straight down to the image bottom).
        type: "linear" (default) or "radial".
    """
    wh = _image_dims(image_id)
    if wh is None:
        return f"refused: image {image_id} not found (see list_images)."
    cw, ch = wh
    g = {"color1": color1, "color2": color2, "x1": x1, "y1": y1,
         "x2": x2 if x2 is not None else x1, "y2": y2 if y2 is not None else ch,
         "type": type}
    try:
        snip = _emit_element({"type": "gradient", **g}, cw, ch)
    except ValueError as e:
        return f"refused: {e}"
    ev = _eval(f"(let ((image {image_id})) {snip} "
               f"(car (gimp-image-get-active-layer image)))")
    return _ok(ev, f"applied {type} gradient to image {image_id}")


@mcp.tool()
def scale_image(image_id: int, width: int, height: int) -> str:
    """Resize an entire image (all layers) to new pixel dimensions.

    Args:
        image_id: target image id.
        width, height: new size in pixels.
    """
    ev = _eval(f"(gimp-image-scale {image_id} {int(width)} {int(height)})")
    return _ok(ev, f"scaled image {image_id} -> {width}x{height}")


@mcp.tool()
def crop_image(image_id: int, x: int, y: int, width: int, height: int) -> str:
    """Crop an image to a rectangular region.

    Args:
        image_id: target image id.
        x, y: top-left of the crop region.
        width, height: crop size in pixels.
    """
    ev = _eval(f"(gimp-image-crop {image_id} {int(width)} {int(height)} "
               f"{int(x)} {int(y)})")
    return _ok(ev, f"cropped image {image_id} -> {width}x{height}+{x}+{y}")


@mcp.tool()
def flatten_image(image_id: int) -> str:
    """Flatten all layers of an image into one (composites onto the background;
    drops alpha). Usually you DON'T need this -- export_image flattens a throwaway
    copy and leaves your layers intact. Use only when you want the live image
    permanently merged.

    Args:
        image_id: target image id.
    """
    ev = _eval(f"(gimp-image-flatten {image_id})")
    return _ok(ev, f"flattened image {image_id}")


@mcp.tool()
def delete_image(image_id: int) -> str:
    """Free an open image from GIMP's memory (does NOT touch any exported file).
    Housekeeping so the resident server doesn't accumulate images.

    Args:
        image_id: the image id to release.
    """
    ev = _eval(f"(gimp-image-delete {image_id})")
    return _ok(ev, f"deleted (freed) image {image_id}")


@mcp.tool()
def export_image(image_id: int, path: str, flatten: bool | None = None,
                 quality: float = 0.92, timeout: int = DEFAULT_TIMEOUT) -> str:
    """Export an open image to a file; format is chosen from the extension
    (.png .jpg .webp .tif .bmp .gif .xcf). Exports a flattened COPY so your live
    layered image is preserved. You can then Read the PNG to see the result.

    Args:
        image_id: the image to export.
        path: destination path (~ expanded; dirs auto-created).
        flatten: None = auto (keep alpha for png/webp/tiff/gif, flatten for
                 jpg/bmp); True = force flatten onto background; False = force
                 keep alpha.
        quality: JPEG/WebP quality 0.0-1.0 (ignored for lossless formats).
        timeout: seconds before aborting.
    """
    return _do_export(image_id, path, flatten, quality, _clamp_timeout(timeout))


@mcp.tool()
def compose_image(width: int, height: int, elements: list[dict],
                  background: str | dict = "white", export: dict | None = None,
                  keep_open: bool = True, timeout: int = DEFAULT_TIMEOUT) -> str:
    """Render a WHOLE layered graphic from one structured spec, in a single pass
    -- the fast path for logos / banners / cards. Creates the canvas, paints the
    background, then stacks each element bottom-to-top, and optionally exports.

    background:
        "white" | "transparent" | a name | "#rrggbb" | "r,g,b", OR
        {"gradient": {"color1","color2","x1","y1","x2","y2","type":"linear|radial"}}

    each element is a dict with a `type`:
        text:     {"type":"text","text":...,"x":..,"y":..,"size":..,"color":..,"font":..}
        rect:     {"type":"rect","x":..,"y":..,"width":..,"height":..,"color":..}
        ellipse:  {"type":"ellipse","x":..,"y":..,"width":..,"height":..,"color":..}
                  (width==height -> circle)
        image:    {"type":"image","path":..,"x":..,"y":..,"width":?,"height":?}
        gradient: {"type":"gradient","color1":..,"color2":..,"x1":..,"y1":..,
                   "x2":..,"y2":..,"type":"linear|radial"}
    Elements paint in order, so later ones sit on top.

    export (optional): {"path":..,"flatten":?,"quality":?} -- written via the same
    rules as export_image (format from the extension).

    Args:
        width, height: canvas size in pixels.
        elements: ordered list of element dicts (may be empty for a blank canvas).
        background: see above.
        export: optional export spec (see above).
        keep_open: keep the composed image open afterwards (default True) so you
                   can tweak it; pass False to free it after exporting.
        timeout: seconds before aborting the (single) build script.
    """
    try:
        bg = ([int(x) for x in background.split(",")]
              if isinstance(background, str) and "," in background else background)
        parts = [_emit_background(bg, int(width), int(height))]
        for el in (elements or []):
            parts.append(_emit_element(el, int(width), int(height)))
    except (ValueError, TypeError, KeyError) as e:
        return f"refused: bad spec -- {e}"
    body = "\n".join(parts)
    script = (f"(let ((image (car (gimp-image-new {int(width)} {int(height)} RGB))))"
              f"\n{body}\n image)")
    ev = _eval(script, timeout=_clamp_timeout(timeout))
    if not ev.ok:
        return ("compose failed:\n" + _annotate(ev)
                + "\n\n(tip: verify element coordinates and any image `path`s.)")
    iid = _parse_int(ev.value)
    msgs = [f"composed image #{iid} ({width}x{height}) with "
            f"{len(elements or [])} element(s)."]
    if isinstance(export, dict) and export.get("path"):
        msgs.append(_do_export(iid, export["path"], export.get("flatten"),
                               float(export.get("quality", 0.92)),
                               _clamp_timeout(timeout)))
    if not keep_open and iid is not None:
        _eval(f"(gimp-image-delete {iid})")
        msgs.append(f"(freed image #{iid})")
    elif iid is not None:
        msgs.append(f"image #{iid} left open -- edit further or delete_image when done.")
    return "\n".join(msgs)


# =============================================================================
# ESCAPE HATCHES (write) -- arbitrary Script-Fu / Python-Fu
# =============================================================================
@mcp.tool()
def eval_script_fu(script: str, timeout: int = DEFAULT_TIMEOUT) -> str:
    """Run ARBITRARY Script-Fu (TinyScheme) on the resident GIMP and return the
    printed result or error -- the ultimate escape hatch. Combined with pdb_search
    / pdb_info you can drive any of GIMP's ~1300 procedures.

    Notes:
        * The whole program must fit in 65535 bytes (server limit).
        * The reply is the Scheme-printed value of the last expression (strings
          come back quoted); errors return as "Error: ...".
        * Use file paths for I/O -- never try to move pixels over the socket.

    Args:
        script: the Script-Fu source, e.g.
                "(car (gimp-image-new 800 600 RGB))".
        timeout: seconds before aborting (default 120, max 1800).
    """
    return _annotate(_eval(script, timeout=_clamp_timeout(timeout)))


@mcp.tool()
def eval_python_fu(code: str, timeout: int = DEFAULT_TIMEOUT) -> str:
    """Run ARBITRARY Python-Fu (GIMP 2.10 => **Python 2.7**) via the PDB's
    python-fu-eval, for when Scheme is awkward. The GIMP API is exposed as the
    `pdb` object (e.g. `pdb.gimp_image_new(800,600,0)`); `print` goes to GIMP's
    console, so return data by writing a file and reporting its path.

    Args:
        code: Python 2.7 source run inside GIMP's interpreter.
        timeout: seconds before aborting.
    """
    ev = _eval(f"(python-fu-eval RUN-NONINTERACTIVE {_sf(code)})",
               timeout=_clamp_timeout(timeout))
    if not ev.ok and ("procedure" in ev.value.lower()
                      and "not" in ev.value.lower()):
        return (_annotate(ev) + "\n\nThis GIMP build may lack the Python-Fu "
                "plug-in. Use eval_script_fu (Script-Fu is always present).")
    return _annotate(ev)


if __name__ == "__main__":
    mcp.run()

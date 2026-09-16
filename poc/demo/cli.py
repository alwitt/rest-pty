#!/usr/bin/env python3
"""Test-drive CLI for the rest-pty session management API.

Talks to the REST API exposed by `api/manager.go`. Built with `click` and
`requests` so the endpoints can be exercised by hand.

Examples:

    ./cli.py create session
    ./cli.py ls sessions
    ./cli.py get session demo
    ./cli.py update session demo driver
    ./cli.py update session demo workspace my_ws
    ./cli.py update session demo buf-cap 32768 --block
    ./cli.py rm session demo
"""

import base64
import ipaddress
import json
import re
import sys

import click
import requests
from prompt_toolkit import prompt
from prompt_toolkit.shortcuts import confirm, radiolist_dialog
from prompt_toolkit.validation import ValidationError, Validator

# Default API location, matching the server defaults in models/configs.go
# (api.service.listenOn 0.0.0.0 / appPort 38281, path prefix "/").
DEFAULT_BASE_URL = "http://localhost:38281"


def sessions_url(base_url):
    """Collection endpoint: /v1/sessions"""
    return f"{base_url.rstrip('/')}/v1/sessions"


def session_url(base_url, name):
    """Single session endpoint: /v1/sessions/{name}"""
    return f"{sessions_url(base_url)}/{name}"


def input_commands_url(base_url, name):
    """Structured input endpoint: /v1/sessions/{name}/io/input/commands"""
    return f"{session_url(base_url, name)}/io/input/commands"


def output_chunk_url(base_url, name):
    """Output chunk read endpoint: /v1/sessions/{name}/io/output/chunk"""
    return f"{session_url(base_url, name)}/io/output/chunk"


def output_tail_url(base_url, name):
    """SSE output tail endpoint: /v1/sessions/{name}/io/output/tail"""
    return f"{session_url(base_url, name)}/io/output/tail"


def start_url(base_url, name):
    """Start session endpoint: /v1/sessions/{name}/start"""
    return f"{session_url(base_url, name)}/start"


def stop_url(base_url, name):
    """Stop session endpoint: /v1/sessions/{name}/stop"""
    return f"{session_url(base_url, name)}/stop"


def driver_url(base_url, name):
    """Update session driver endpoint: /v1/sessions/{name}/driver"""
    return f"{session_url(base_url, name)}/driver"


def workspace_url(base_url, name):
    """Update session workspace endpoint: /v1/sessions/{name}/workspace"""
    return f"{session_url(base_url, name)}/workspace"


def command_url(base_url, name):
    """Update session command endpoint: /v1/sessions/{name}/command"""
    return f"{session_url(base_url, name)}/command"


def name_url(base_url, name):
    """Update session name endpoint: /v1/sessions/{name}/name"""
    return f"{session_url(base_url, name)}/name"


def description_url(base_url, name):
    """Update session description endpoint: /v1/sessions/{name}/description"""
    return f"{session_url(base_url, name)}/description"


def output_buf_cap_url(base_url, name):
    """Update session output buffer capacity endpoint: /v1/sessions/{name}/output-buf-cap"""
    return f"{session_url(base_url, name)}/output-buf-cap"


def run_mode_url(base_url, name):
    """Update session runner mode endpoint: /v1/sessions/{name}/run-mode"""
    return f"{session_url(base_url, name)}/run-mode"


def show(resp):
    """Pretty-print a JSON response and exit non-zero on API/HTTP error.

    The server wraps every reply in goutils.RestAPIBaseResponse, so on failure
    we surface the embedded error.{code,msg,detail} block.
    """
    try:
        body = resp.json()
    except ValueError:
        click.echo(f"HTTP {resp.status_code}: {resp.text}", err=True)
        sys.exit(1)

    click.echo(json.dumps(body, indent=2))

    if not resp.ok or not body.get("success", False):
        err = body.get("error") or {}
        click.echo(
            f"Request failed (HTTP {resp.status_code}): "
            f"{err.get('msg', '')} - {err.get('detail', '')}",
            err=True,
        )
        sys.exit(1)


def _fetch_session(base_url, name):
    """Fetch one session, returning its JSON object; on any failure print the envelope and exit."""
    resp = requests.get(session_url(base_url, name))
    try:
        body = resp.json()
    except ValueError:
        body = None
    if body is None or not resp.ok or not body.get("success", False):
        show(resp)
    return body["session"]


# ======================================================================================
# Root group


@click.group()
@click.option(
    "--base-url",
    envvar="REST_PTY_URL",
    default=DEFAULT_BASE_URL,
    show_default=True,
    help="Base URL of the rest-pty server (env: REST_PTY_URL).",
)
@click.pass_context
def cli(ctx, base_url):
    """Test CLI for the rest-pty session management API."""
    ctx.ensure_object(dict)
    ctx.obj["base_url"] = base_url


# ======================================================================================
# Interactive prompt helpers (prompt_toolkit)

DEFAULT_IO_BUF_CAP = 65536
DEFAULT_TTY_ROWS = 100
DEFAULT_TTY_COLS = 300
# Default container image for docker-driver sessions. `image` is required by the server and has
# no server-side default; this matches the helper image used in the PoC.
DEFAULT_DOCKER_IMAGE = "rest-pty-helper:latest"
# Server-side defaults for omitted docker run-as user/group (goutils runtime/common.go). Shown in
# the prompts; the CLI omits the field so the server default applies rather than sending these.
SERVER_DEFAULT_RUN_AS_USER = "nobody"
SERVER_DEFAULT_RUN_AS_GROUP = "nogroup"
# Server-side defaults for the omitted docker memory settings (goutils runtime/common.go).
SERVER_DEFAULT_MEM_RESERVATION = "32m"
SERVER_DEFAULT_MEM_LIMIT = "128m"
# Server-side default size of a writable dir when size_limit is omitted (8 MiB).
SERVER_DEFAULT_WRITABLE_DIR_SIZE = 8388608
# Where a cairn workspace volume is mounted inside a DOCKER session container (workspace/client.go).
WORKSPACE_MOUNT_PATH = "/mnt/cairn/ws"

# Docker network modes with dedicated radiolist entries; anything else is a named docker network.
NETWORK_MODE_NONE = "none"
NETWORK_MODE_BRIDGE = "bridge"
NETWORK_MODE_HOST = "host"
# Sentinel radiolist value meaning "prompt for a docker network name".
_NETWORK_MODE_CUSTOM = "__custom__"

# Linux capability names as docker accepts them (e.g. NET_RAW), after CAP_ prefix stripping.
CAPABILITY_RE = re.compile(r"^[A-Z][A-Z0-9_]*$")

# A memory size as the server's parser accepts it: a number with an optional binary unit suffix
# (k/m/g/t/p), where the trailing 'b' or 'ib' is optional - "128m", "1.5GiB", "512" are all valid.
# Mirrors goutils' use of docker go-units RAMInBytes (runtime/docker.go), but is deliberately a
# touch stricter (that parser also tolerates oddities like a bare ".5"); anything accepted here is
# accepted by the server.
MEMORY_SIZE_RE = re.compile(r"^(\d+(?:\.\d+)?)\s*([kmgtp](?:i?b)?|b)?$", re.IGNORECASE)
# Binary multipliers keyed by the unit letter, matching RAMInBytes' binaryMap.
MEMORY_UNIT_MULTIPLIERS = {
    "k": 1024,
    "m": 1024**2,
    "g": 1024**3,
    "t": 1024**4,
    "p": 1024**5,
}

# Mirrors the server's session_name_type rule (models/validate.go): alphanumeric and '-'.
SESSION_NAME_RE = re.compile(r"^[a-zA-Z0-9-]+$")
# Mirrors the server's workspace_name_type rule (models/validate.go validWorkspaceNameREGEX),
# which is cairn's `valid_name`: alphanumeric, '-' and '_'. Deliberately NOT SESSION_NAME_RE - a
# workspace name allows '_' where a session name does not.
WORKSPACE_NAME_RE = re.compile(r"^[a-zA-Z0-9_-]+$")


class _RegexValidator(Validator):
    """Validate input against a regex, optionally allowing a blank entry."""

    def __init__(self, pattern, message, allow_blank=False):
        self.pattern = pattern
        self.message = message
        self.allow_blank = allow_blank

    def validate(self, document):
        text = document.text.strip()
        if not text and self.allow_blank:
            return
        if not text or not self.pattern.match(text):
            raise ValidationError(message=self.message, cursor_position=len(document.text))


class _NonEmptyValidator(Validator):
    """Validate that input is not blank."""

    def __init__(self, message="A value is required"):
        self.message = message

    def validate(self, document):
        if not document.text.strip():
            raise ValidationError(message=self.message, cursor_position=len(document.text))


class _IntValidator(Validator):
    """Validate an integer, optionally bounded below and optionally allowing a blank (default)."""

    def __init__(self, min_value=None, allow_blank=False):
        self.min_value = min_value
        self.allow_blank = allow_blank

    def validate(self, document):
        text = document.text.strip()
        if not text:
            if self.allow_blank:
                return
            raise ValidationError(message="A value is required")
        try:
            value = int(text)
        except ValueError:
            raise ValidationError(
                message="Must be an integer", cursor_position=len(document.text)
            )
        if self.min_value is not None and value < self.min_value:
            raise ValidationError(
                message=f"Must be >= {self.min_value}", cursor_position=len(document.text)
            )


def _prompt_int(message, min_value=None, default=None):
    """Prompt for an integer. A blank entry returns `default` when one is provided."""
    text = prompt(
        message,
        validator=_IntValidator(min_value=min_value, allow_blank=default is not None),
        validate_while_typing=False,
    ).strip()
    return default if not text else int(text)


def _parse_interface_port(entry):
    """Parse an '<interface>:<port>' entry into (host_ip, port).

    Raises ValueError with a human message on malformed input.
    """
    host, sep, port_str = entry.rpartition(":")
    if not sep:
        raise ValueError("expected '<interface>:<port>', e.g. 127.0.0.1:8888")
    try:
        ipaddress.ip_address(host)
    except ValueError:
        raise ValueError(f"invalid interface IP '{host}'")
    try:
        port = int(port_str)
    except ValueError:
        raise ValueError(f"invalid port '{port_str}'")
    if not 1 <= port <= 65535:
        raise ValueError(f"port {port} out of range (1-65535)")
    return host, port


def _parse_publish_port(entry):
    """Parse an '<interface>:<port>' entry into a publish_ports element.

    The container port matches the published host port.
    """
    host_ip, port = _parse_interface_port(entry)
    return {"container_port": port, "host_ip": host_ip, "host_port": port}


def _render_publish_port(publish):
    """One-line summary of a publish_ports element, e.g. '127.0.0.1:8888->8888/tcp'."""
    host_ip = publish.get("host_ip", "127.0.0.1")
    host_port = publish.get("host_port", 0)
    protocol = publish.get("protocol", "tcp")
    return f"{host_ip}:{host_port}->{publish['container_port']}/{protocol}"


def _parse_host_mount(entry):
    """Parse a 'HOST_PATH[:CONTAINER_PATH][:ro|rw]' entry into a host_mounts element.

    Follows the docker `-v` shape. Both paths must be absolute (the server rejects relative
    mount paths). When the container path is omitted the server mirrors the host path; when
    neither ro nor rw is given the read_only key is omitted so the server default (read-only)
    applies.

    Raises ValueError with a human message on malformed input.
    """
    parts = entry.split(":")
    if len(parts) > 3:
        raise ValueError("expected 'HOST_PATH[:CONTAINER_PATH][:ro|rw]', e.g. /src:/work:rw")

    host_path, rest = parts[0], parts[1:]
    if not host_path.startswith("/"):
        raise ValueError(f"host path '{host_path}' must be absolute")
    mount = {"path": host_path}

    if rest and rest[-1] in ("ro", "rw"):
        mount["read_only"] = rest.pop() == "ro"
    if rest:
        container_path = rest.pop(0)
        if not container_path.startswith("/"):
            raise ValueError(f"container path '{container_path}' must be absolute")
        mount["mount_path"] = container_path
    if rest:
        raise ValueError(f"unexpected trailing component '{rest[0]}' (expected ro or rw)")
    return mount


def _render_host_mount(mount):
    """One-line summary of a host_mounts element in the same 'HOST[:CONTAINER][:ro|rw]' shape."""
    text = mount["path"]
    if mount.get("mount_path"):
        text += f":{mount['mount_path']}"
    read_only = mount.get("read_only")
    if read_only is not None:
        text += ":ro" if read_only else ":rw"
    return text


def _parse_memory_size(entry):
    """Validate a memory size string, returning it unchanged.

    The server stores these verbatim and only parses them when the container starts, so a typo
    would otherwise surface as a failed `session start` rather than a rejected create/update.

    Raises ValueError with a human message on malformed input.
    """
    text = entry.strip()
    if not MEMORY_SIZE_RE.match(text):
        raise ValueError(f"invalid memory size '{entry}' (expected e.g. 128m, 1.5g or 512)")
    return text


def _memory_to_bytes(entry):
    """Resolve a memory size string to bytes, mirroring the server's binary multipliers.

    Assumes `entry` already passed _parse_memory_size.
    """
    number, unit = MEMORY_SIZE_RE.match(entry.strip()).groups()
    multiplier = MEMORY_UNIT_MULTIPLIERS.get(unit[0].lower(), 1) if unit else 1
    return int(float(number) * multiplier)


def _format_bytes(count):
    """Render a byte count as a compact binary size, e.g. 268435456 -> '256m'.

    Only exact multiples of a unit are abbreviated; anything else renders as the raw count, so
    the result always round-trips back through _parse_memory_size / _memory_to_bytes.
    """
    for unit in ("p", "t", "g", "m", "k"):
        multiplier = MEMORY_UNIT_MULTIPLIERS[unit]
        if count >= multiplier and count % multiplier == 0:
            return f"{count // multiplier}{unit}"
    return str(count)


def _parse_writable_dir(entry):
    """Parse a 'PATH[:SIZE]' entry into a writable_dirs element.

    The path is an in-container directory backed by a memory-backed writable mount (tmpfs) and
    must be absolute. An omitted size leaves size_limit out so the server default applies.

    Raises ValueError with a human message on malformed input.
    """
    parts = entry.split(":")
    if len(parts) > 2:
        raise ValueError("expected 'PATH[:SIZE]', e.g. /scratch:256m")

    path = parts[0]
    if not path.startswith("/"):
        raise ValueError(f"writable dir path '{path}' must be absolute")
    writable_dir = {"path": path}

    if len(parts) == 2:
        # Unlike the memory limits, which stay strings on the wire, size_limit is a byte count.
        writable_dir["size_limit"] = _memory_to_bytes(_parse_memory_size(parts[1]))
    else:
        # Apply default
        writable_dir["size_limit"] = SERVER_DEFAULT_WRITABLE_DIR_SIZE
    return writable_dir


def _render_writable_dir(writable_dir):
    """One-line summary of a writable_dirs element in the same 'PATH[:SIZE]' shape."""
    text = writable_dir["path"]
    if writable_dir.get("size_limit"):
        text += f":{_format_bytes(writable_dir['size_limit'])}"
    return text


def _parse_capability(entry):
    """Normalise a Linux capability name for add_caps: upper-case, without the CAP_ prefix.

    Docker accepts both spellings; storing the short form keeps entries comparable.

    Raises ValueError with a human message on malformed input.
    """
    name = entry.strip().upper()
    if name.startswith("CAP_"):
        name = name[len("CAP_"):]
    if not CAPABILITY_RE.match(name):
        raise ValueError(f"invalid capability name '{entry}' (expected e.g. NET_RAW)")
    return name


def _prompt_optional(message, current=None):
    """Prompt for an optional string; the input is pre-filled with `current` when one is set.

    Returns None on a blank entry so the caller can omit the field and let the server default
    apply.
    """
    text = prompt(message, default=current or "").strip()
    return text or None


def _prompt_memory(message, current=None):
    """Prompt for an optional memory size, re-prompting until it is well-formed.

    Returns None on a blank entry so the caller can omit the field and let the server default
    apply.
    """
    while True:
        text = _prompt_optional(message, current)
        if text is None:
            return None
        try:
            return _parse_memory_size(text)
        except ValueError as exc:
            click.echo(f"  {exc}", err=True)
            current = text


def _collect_entries(label, current, parse, render):
    """Collect a list of entries interactively, optionally keeping the existing ones.

    When `current` is non-empty its entries are shown (via `render`) and the user chooses whether
    to keep them. Further entries are then read one per line until a blank line; each is run
    through `parse`, which raises ValueError with a human message on malformed input, in which
    case the entry is reported and re-prompted.
    """
    entries = []
    if current:
        click.echo(f"Current {label}:")
        for existing in current:
            click.echo(f"  {render(existing)}")
        if confirm(f"Keep current {label}?"):
            entries = list(current)

    while True:
        entry = prompt(f"{label.capitalize()} [blank to continue]: ").strip()
        if not entry:
            break
        try:
            entries.append(parse(entry))
        except ValueError as exc:
            click.echo(f"  {exc}", err=True)
    return entries


def _select_network_mode(current=None):
    """Select the container network mode: none, bridge, host, or a named docker network.

    `current` pre-selects the matching radiolist entry; a value that is not one of the three
    named modes is treated as a custom network name and pre-fills the follow-up name prompt.
    """
    named_modes = (NETWORK_MODE_NONE, NETWORK_MODE_BRIDGE, NETWORK_MODE_HOST)
    default = None
    if current in named_modes:
        default = current
    elif current:
        default = _NETWORK_MODE_CUSTOM

    choice = radiolist_dialog(
        title="Container Network Mode",
        text="Select the docker network mode for the session container:",
        values=[
            (NETWORK_MODE_NONE, "none (no networking - server default)"),
            (NETWORK_MODE_BRIDGE, "bridge (default docker bridge network)"),
            (NETWORK_MODE_HOST, "host (share the host network namespace)"),
            (_NETWORK_MODE_CUSTOM, "custom (a specific docker network, by name)"),
        ],
        default=default,
    ).run()
    if choice is None:
        raise KeyboardInterrupt

    if choice != _NETWORK_MODE_CUSTOM:
        return choice
    return prompt(
        "Docker Network Name: ",
        default=current if default == _NETWORK_MODE_CUSTOM else "",
        validator=_NonEmptyValidator(),
        validate_while_typing=False,
    ).strip()


# ======================================================================================
# create


@cli.group()
def create():
    """Create resources."""


@create.command("session")
@click.pass_context
def create_session(ctx):
    """Define a new session interactively.

    Walks through the common session fields, then the driver selection. The
    PTY driver needs nothing further; the Docker driver additionally collects
    the container image, the run-as user/group, the memory reservation and
    limit, memory-backed writable dirs, capabilities to add back, host bind
    mounts, the network mode (and, when routable, the interfaces/ports to
    publish), and an optional cairn workspace to mount at /mnt/cairn/ws.
    """
    try:
        _create_session_interactive(ctx)
    except (KeyboardInterrupt, EOFError):
        click.echo("Aborted.", err=True)
        sys.exit(1)


def _create_session_interactive(ctx):
    name = prompt(
        "Session Name: ",
        validator=_RegexValidator(
            SESSION_NAME_RE, "Only alphanumeric characters and '-' are allowed"
        ),
        validate_while_typing=False,
    ).strip()

    io_buf_cap = _prompt_int(
        f"Output Buffer Cap [Default {DEFAULT_IO_BUF_CAP}]: ",
        min_value=16384,
        default=DEFAULT_IO_BUF_CAP,
    )

    # The first whitespace-separated token is the command; the rest are arguments.
    command_line = prompt(
        "Run Command: ", validator=_NonEmptyValidator(), validate_while_typing=False
    ).strip()
    tokens = command_line.split()
    command = {"cmd": tokens[0], "args": tokens[1:]}

    driver, driver_metadata = _collect_driver()

    payload = {
        "name": name,
        "command": command,
        "io_buf_cap": io_buf_cap,
        "driver": driver,
        "driver_metadata": driver_metadata,
    }

    # A workspace is a cairn concept only a DOCKER session can mount; the server rejects one on a
    # PTY session. The name is stored as given and resolved against cairn when the session
    # starts, so an unknown workspace fails at `start`, not here.
    if driver == "DOCKER":
        workspace = prompt(
            f"Cairn Workspace (mounted at {WORKSPACE_MOUNT_PATH}) [blank for none]: ",
            validator=_RegexValidator(
                WORKSPACE_NAME_RE,
                "Only alphanumeric characters, '-' and '_' are allowed",
                allow_blank=True,
            ),
            validate_while_typing=False,
        ).strip()
        if workspace:
            payload["workspace_name"] = workspace

    resp = requests.post(sessions_url(ctx.obj["base_url"]), json=payload)
    show(resp)


def _collect_driver(current_driver=None, current_metadata=None):
    """Interactively select a session driver and collect its metadata.

    Prompts for the TTY geometry (common to both drivers), then the driver type, then the
    driver-specific parameters. `current_driver` / `current_metadata` (from an existing
    session) pre-select the driver and pre-fill every prompt, so an update only has to touch
    the fields that change.

    Returns (driver, driver_metadata).
    """
    current_metadata = current_metadata or {}
    # The PTY driver keeps the geometry at the top level; the docker driver nests it under
    # "streaming".
    geometry = current_metadata
    if current_driver == "DOCKER":
        geometry = current_metadata.get("streaming") or {}
    default_rows = geometry.get("display_rows", DEFAULT_TTY_ROWS)
    default_cols = geometry.get("display_cols", DEFAULT_TTY_COLS)

    rows = _prompt_int(
        f"TTY display rows [Default {default_rows}]: ", min_value=30, default=default_rows
    )
    cols = _prompt_int(
        f"TTY display cols [Default {default_cols}]: ", min_value=80, default=default_cols
    )

    driver = radiolist_dialog(
        title="Session Driver",
        text="Select the session driver type:",
        values=[
            ("PTY", "PTY (local pseudo-terminal)"),
            ("DOCKER", "Docker (sandboxed container)"),
        ],
        default=current_driver,
    ).run()
    if driver is None:
        raise KeyboardInterrupt

    if driver == "PTY":
        return driver, {"display_rows": rows, "display_cols": cols}
    # Only carry the existing metadata forward when it describes the same driver.
    docker_current = current_metadata if current_driver == "DOCKER" else None
    return driver, _collect_docker_metadata(rows, cols, current=docker_current)


def _collect_docker_metadata(rows, cols, current=None):
    """Collect docker-driver metadata interactively.

    Collects the image, run-as user/group, memory reservation and limit, memory-backed writable
    dirs, added capabilities, host bind mounts, network mode and - when the mode can accept
    inbound connections - the interfaces/ports to publish (the container port matches the
    published host port). Fields left blank are omitted so the server defaults apply.

    `current` is the existing driver metadata when updating a session. The result starts as a
    copy of it and only the keys this function manages are set or removed, so parameters the
    CLI does not expose (environment, volume_mounts, extra_hosts, ...) survive the
    full-replacement driver update.
    """
    metadata = dict(current or {})

    metadata["image"] = prompt(
        "Container Image: ",
        default=metadata.get("image", DEFAULT_DOCKER_IMAGE),
        validator=_NonEmptyValidator(),
        validate_while_typing=False,
    ).strip()

    metadata["streaming"] = {"display_rows": rows, "display_cols": cols}

    # Run-as user/group accept either numeric IDs or names; the server joins them as user:group.
    _set_or_drop(
        metadata,
        "run_as_user",
        _prompt_optional(
            f"Run As User [Default {SERVER_DEFAULT_RUN_AS_USER}]: ", metadata.get("run_as_user")
        ),
    )
    _set_or_drop(
        metadata,
        "run_as_group",
        _prompt_optional(
            f"Run As Group [Default {SERVER_DEFAULT_RUN_AS_GROUP}]: ",
            metadata.get("run_as_group"),
        ),
    )

    # Memory: a soft reservation the container starts with and a hard limit it cannot exceed.
    mem_reservation = _prompt_memory(
        f"Starting Memory (soft reservation) [Default {SERVER_DEFAULT_MEM_RESERVATION}]: ",
        metadata.get("mem_reservation"),
    )
    _set_or_drop(metadata, "mem_reservation", mem_reservation)
    mem_limit = _prompt_memory(
        f"Max Memory (hard limit) [Default {SERVER_DEFAULT_MEM_LIMIT}]: ",
        metadata.get("mem_limit"),
    )
    _set_or_drop(metadata, "mem_limit", mem_limit)
    # Docker refuses to start a container whose reservation exceeds its limit; flag it here
    # rather than letting the session fail at start.
    if (
        mem_reservation
        and mem_limit
        and _memory_to_bytes(mem_reservation) > _memory_to_bytes(mem_limit)
    ):
        click.echo(
            f"  warning: starting memory {mem_reservation} exceeds the max {mem_limit}; "
            "docker will refuse to start this container",
            err=True,
        )

    # Writable dirs are overlaid on the otherwise read-only rootfs.
    click.echo(
        "Writable dirs are memory-backed (tmpfs): their contents are lost when the session "
        "stops, and they count against the container's memory"
    )
    click.echo(
        "Writable dirs use PATH[:SIZE], e.g. /scratch:256m "
        f"(default {_format_bytes(SERVER_DEFAULT_WRITABLE_DIR_SIZE)})"
    )
    _set_or_drop(
        metadata,
        "writable_dirs",
        _collect_entries(
            "writable dirs",
            metadata.get("writable_dirs"),
            _parse_writable_dir,
            _render_writable_dir,
        ),
    )

    # The container drops every capability by default, so even root cannot e.g. open a raw
    # socket; capabilities listed here are added back on top of that.
    click.echo(
        "Added capabilities are restored on top of the dropped-by-default set, "
        "e.g. NET_RAW for ping / raw sockets"
    )
    _set_or_drop(
        metadata,
        "add_caps",
        _collect_entries("added capabilities", metadata.get("add_caps"), _parse_capability, str),
    )

    click.echo("Host mounts use the docker -v shape: HOST_PATH[:CONTAINER_PATH][:ro|rw]")
    _set_or_drop(
        metadata,
        "host_mounts",
        _collect_entries(
            "host mounts", metadata.get("host_mounts"), _parse_host_mount, _render_host_mount
        ),
    )

    network_mode = _select_network_mode(metadata.get("network_mode"))
    metadata["network_mode"] = network_mode

    # Publishing ports only makes sense on a routable network: the server rejects publish_ports
    # with network mode "none", and in "host" mode the command's ports already sit on the host,
    # so docker discards port bindings.
    if network_mode == NETWORK_MODE_NONE:
        click.echo("  network mode 'none': no networking, ports cannot be published")
        metadata.pop("publish_ports", None)
    elif network_mode == NETWORK_MODE_HOST:
        click.echo(
            "  network mode 'host': the command's ports are already on the host; "
            "docker discards port bindings"
        )
        metadata.pop("publish_ports", None)
    else:
        click.echo("Published ports take the form <interface>:<port>, e.g. 127.0.0.1:8888")
        _set_or_drop(
            metadata,
            "publish_ports",
            _collect_entries(
                "published ports",
                metadata.get("publish_ports"),
                _parse_publish_port,
                _render_publish_port,
            ),
        )

    return metadata


def _set_or_drop(metadata, key, value):
    """Set `metadata[key]` to a truthy `value`, or remove the key so the server default applies."""
    if value:
        metadata[key] = value
    else:
        metadata.pop(key, None)


# ======================================================================================
# ls


@cli.group()
def ls():
    """List resources."""


@ls.command("sessions")
@click.option("--name", default=None, help="Filter by name (case-insensitive, fuzzy).")
@click.option("--limit", type=int, default=None, help="Max number of entries.")
@click.option("--offset", type=int, default=None, help="Number of entries to skip.")
@click.pass_context
def ls_sessions(ctx, name, limit, offset):
    """List sessions."""
    params = {}
    if name is not None:
        params["name"] = name
    if limit is not None:
        params["limit"] = limit
    if offset is not None:
        params["offset"] = offset

    resp = requests.get(sessions_url(ctx.obj["base_url"]), params=params)
    show(resp)


# ======================================================================================
# get


@cli.group()
def get():
    """Fetch a single resource."""


@get.command("session")
@click.argument("name")
@click.pass_context
def get_session(ctx, name):
    """Fetch one session by NAME."""
    resp = requests.get(session_url(ctx.obj["base_url"], name))
    show(resp)


# ======================================================================================
# rm


@cli.group()
def rm():
    """Delete a resource."""


@rm.command("session")
@click.argument("name")
@click.pass_context
def rm_session(ctx, name):
    """Delete one session by NAME (must be IDLE)."""
    resp = requests.delete(session_url(ctx.obj["base_url"], name))
    show(resp)


# ======================================================================================
# update


@cli.group()
def update():
    """Update resources."""


@update.group("session")
@click.argument("name")
@click.pass_context
def update_session(ctx, name):
    """Update one parameter of the session NAME.

    Most changes are only permitted while the session is IDLE; the server
    answers HTTP 409 otherwise. Renaming and changing the description are
    allowed in any state.
    """
    ctx.obj["session_name"] = name


@update_session.command("driver")
@click.pass_context
def update_session_driver(ctx):
    """Change the session driver and its parameters interactively.

    The current session is fetched first and every prompt is pre-filled from
    it, so only the fields that change need to be touched. The driver update
    replaces the whole driver metadata; docker parameters this CLI does not
    expose are carried over untouched. Switching a DOCKER session to PTY drops
    its workspace assignment server-side.
    """
    base_url = ctx.obj["base_url"]
    name = ctx.obj["session_name"]

    current = _fetch_session(base_url, name)
    # The server enforces this too; checking up front avoids walking through every prompt only
    # to be refused with a 409.
    if current.get("state") != "IDLE":
        click.echo(
            f"Session '{name}' is in state {current.get('state')}; "
            "the driver can only be changed while IDLE.",
            err=True,
        )
        sys.exit(1)

    try:
        driver, driver_metadata = _collect_driver(
            current_driver=current.get("driver"),
            current_metadata=current.get("driver_metadata") or {},
        )
    except (KeyboardInterrupt, EOFError):
        click.echo("Aborted.", err=True)
        sys.exit(1)

    resp = requests.put(
        driver_url(base_url, name),
        json={"driver": driver, "driver_metadata": driver_metadata},
    )
    show(resp)


@update_session.command("workspace")
@click.argument("workspace", required=False)
@click.option(
    "--clear", is_flag=True, default=False, help="Clear the workspace assignment instead."
)
@click.pass_context
def update_session_workspace(ctx, workspace, clear):
    """Assign the cairn WORKSPACE to the session, or clear it with --clear.

    Only DOCKER sessions may carry a workspace; it is mounted at /mnt/cairn/ws
    when the session starts. The name is resolved against cairn at start time,
    so an unknown workspace fails on `session start`, not here.
    """
    if clear == bool(workspace):
        raise click.UsageError("Provide exactly one of WORKSPACE or --clear.")
    if workspace and not WORKSPACE_NAME_RE.match(workspace):
        raise click.BadArgumentUsage(
            "WORKSPACE may only contain alphanumeric characters, '-' and '_'."
        )

    resp = requests.put(
        workspace_url(ctx.obj["base_url"], ctx.obj["session_name"]),
        json={"workspace_name": None if clear else workspace},
    )
    show(resp)


@update_session.command("command")
@click.argument("command", nargs=-1, required=True)
@click.pass_context
def update_session_command(ctx, command):
    """Change the COMMAND the session runs.

    The first token is the command; the rest are its arguments. Put `--`
    before a command whose arguments start with '-', e.g.
    `update session demo command -- /bin/bash -l`.
    """
    resp = requests.put(
        command_url(ctx.obj["base_url"], ctx.obj["session_name"]),
        json={"cmd": command[0], "args": list(command[1:])},
    )
    show(resp)


@update_session.command("name")
@click.argument("new_name")
@click.pass_context
def update_session_name(ctx, new_name):
    """Rename the session to NEW_NAME (alphanumeric characters and '-' only)."""
    if not SESSION_NAME_RE.match(new_name):
        raise click.BadArgumentUsage(
            "NEW_NAME may only contain alphanumeric characters and '-'."
        )

    resp = requests.put(
        name_url(ctx.obj["base_url"], ctx.obj["session_name"]),
        params={"name": new_name},
    )
    show(resp)


@update_session.command("description")
@click.argument("text", required=False)
@click.option("--clear", is_flag=True, default=False, help="Clear the description instead.")
@click.pass_context
def update_session_description(ctx, text, clear):
    """Set the session description to TEXT, or clear it with --clear."""
    if clear == bool(text):
        raise click.UsageError("Provide exactly one of TEXT or --clear.")

    resp = requests.put(
        description_url(ctx.obj["base_url"], ctx.obj["session_name"]),
        json={"description": None if clear else text},
    )
    show(resp)


@update_session.command("buf-cap")
@click.argument("capacity", type=click.IntRange(min=16384))
@click.option(
    "--block",
    is_flag=True,
    default=False,
    help="Wait for the change to complete before returning.",
)
@click.pass_context
def update_session_buf_cap(ctx, capacity, block):
    """Change the output buffer CAPACITY in bytes (minimum 16384).

    Applied through the session manager, so like start/stop it is
    non-blocking unless --block is given.
    """
    resp = requests.put(
        output_buf_cap_url(ctx.obj["base_url"], ctx.obj["session_name"]),
        params={"capacity": capacity, "block": str(block).lower()},
    )
    show(resp)


@update_session.command("run-mode")
@click.argument("mode", type=click.Choice(["COMMANDED", "BY_PASSED"]))
@click.pass_context
def update_session_run_mode(ctx, mode):
    """Change the runner MODE.

    COMMANDED feeds structured input commands to the session; BY_PASSED lets
    the user interact with the session directly and ignores input commands.
    """
    resp = requests.put(
        run_mode_url(ctx.obj["base_url"], ctx.obj["session_name"]),
        params={"mode": mode},
    )
    show(resp)


# ======================================================================================
# session - operate a single session's lifecycle and IO


@cli.group()
@click.option("--name", "-n", required=True, help="Target session name.")
@click.pass_context
def session(ctx, name):
    """Operate a single session: start, stop, run input, tail output."""
    ctx.obj["session_name"] = name


@session.command("start")
@click.option(
    "--block",
    is_flag=True,
    default=False,
    help="Wait for the start to complete before returning.",
)
@click.pass_context
def session_start(ctx, block):
    """Start the session runner."""
    resp = requests.post(
        start_url(ctx.obj["base_url"], ctx.obj["session_name"]),
        params={"block": str(block).lower()},
    )
    show(resp)


@session.command("stop")
@click.option(
    "--block",
    is_flag=True,
    default=False,
    help="Wait for the stop to complete before returning.",
)
@click.pass_context
def session_stop(ctx, block):
    """Stop the session runner, returning it to IDLE."""
    resp = requests.post(
        stop_url(ctx.obj["base_url"], ctx.obj["session_name"]),
        params={"block": str(block).lower()},
    )
    show(resp)


@session.command("run")
@click.option(
    "--no-enter",
    is_flag=True,
    default=False,
    help="Do not append a trailing ENTER (carriage return).",
)
@click.argument("command", nargs=-1, required=True)
@click.pass_context
def session_run(ctx, no_enter, command):
    """Send a COMMAND to the session's input.

    The command tokens are joined with spaces and sent as a TEXT input command,
    followed by an ENTER (press return) unless --no-enter is given. The session
    must be in the READY state.
    """
    text = " ".join(command)
    commands = [{"type": "TEXT", "content": text}]
    if not no_enter:
        commands.append({"type": "ENTER"})

    resp = requests.post(
        input_commands_url(ctx.obj["base_url"], ctx.obj["session_name"]),
        json={"commands": commands},
    )
    show(resp)


@session.command("ctrl")
@click.argument("character")
@click.pass_context
def session_ctrl(ctx, character):
    """Send a CTRL- control character to the session.

    CHARACTER is a single ASCII letter, e.g. 'C' to send CTRL-C. The server
    folds it into the control byte (case-insensitive). The session must be in
    the READY state.
    """
    if len(character) != 1 or not character.isalpha() or not character.isascii():
        raise click.BadArgumentUsage(
            f"CHARACTER must be a single ASCII letter, got {character!r}"
        )

    commands = [{"type": "CTRL", "content": character}]
    resp = requests.post(
        input_commands_url(ctx.obj["base_url"], ctx.obj["session_name"]),
        json={"commands": commands},
    )
    show(resp)


@session.command("read")
@click.option(
    "--offset",
    "-o",
    type=int,
    required=True,
    help="Byte offset in the output stream to read from.",
)
@click.option(
    "--length",
    "-l",
    type=int,
    required=True,
    help="Max number of bytes to read (server caps this at the buffer capacity).",
)
@click.option(
    "--strip-ansi",
    "-s",
    is_flag=True,
    default=False,
    help="Ask the server to strip ANSI escape sequences from the returned data.",
)
@click.pass_context
def session_read(ctx, offset, length, strip_ansi):
    """Read one chunk from the session's output ring buffer.

    The requested offset may have aged out of the buffer; in that case the read
    is advanced and the response's "actual_offset" reports where the returned
    data actually starts. The decoded bytes are written to stdout, with a
    summary on stderr.
    """
    params = {"offset": offset, "limit": length}
    if strip_ansi:
        params["strip_ansi"] = "true"

    resp = requests.get(
        output_chunk_url(ctx.obj["base_url"], ctx.obj["session_name"]),
        params=params,
    )

    # On error, surface the standard JSON envelope and exit non-zero.
    try:
        body = resp.json()
    except ValueError:
        click.echo(f"HTTP {resp.status_code}: {resp.text}", err=True)
        sys.exit(1)
    if not resp.ok or not body.get("success", False):
        show(resp)
        return

    click.echo(
        f"actual_offset={body['actual_offset']} read={body['read']}", err=True
    )
    sys.stdout.buffer.write(base64.b64decode(body.get("data") or ""))
    sys.stdout.buffer.flush()


@session.command("tail")
@click.option(
    "--start-at",
    type=int,
    default=0,
    show_default=True,
    help="Byte offset in the output stream to start tailing from.",
)
@click.option(
    "--poll-period-msec",
    type=int,
    default=None,
    help="Milliseconds between buffer availability checks (server default 250).",
)
@click.option(
    "--strip-ansi",
    "-s",
    is_flag=True,
    default=False,
    help="Ask the server to strip ANSI escape sequences from the streamed data.",
)
@click.pass_context
def session_tail(ctx, start_at, poll_period_msec, strip_ansi):
    """Stream the session's output via server-sent events until Ctrl-C.

    Each SSE event carries a base64 chunk from the output ring buffer; the raw
    decoded bytes are written to stdout as they arrive.
    """
    params = {"offset": start_at}
    if poll_period_msec is not None:
        params["poll_period_msec"] = poll_period_msec
    if strip_ansi:
        params["strip_ansi"] = "true"

    out = sys.stdout.buffer
    try:
        with requests.get(
            output_tail_url(ctx.obj["base_url"], ctx.obj["session_name"]),
            params=params,
            headers={"Accept": "text/event-stream"},
            stream=True,
        ) as resp:
            # On error the server returns a normal JSON envelope rather than a stream.
            if not resp.ok:
                show(resp)
                return

            for raw in resp.iter_lines(decode_unicode=True):
                # SSE frames are newline-delimited; we only care about the
                # "data:" lines which carry the base64 payload.
                if not raw or not raw.startswith("data:"):
                    continue
                encoded = raw[len("data:"):].strip()
                if not encoded:
                    continue
                out.write(base64.b64decode(encoded))
                out.flush()
    except KeyboardInterrupt:
        # Disconnecting ends the stream server-side; exit quietly.
        pass


if __name__ == "__main__":
    cli()

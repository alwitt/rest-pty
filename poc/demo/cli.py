#!/usr/bin/env python3
"""Test-drive CLI for the rest-pty session management API.

Talks to the REST API exposed by `api/manager.go`. Built with `click` and
`requests` so the endpoints can be exercised by hand.

Examples:

    ./cli.py create session --name demo --cmd /bin/bash
    ./cli.py ls sessions
    ./cli.py get session demo
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
from prompt_toolkit.shortcuts import radiolist_dialog
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
# Default container image for docker-driver sessions. `image` is required by the server and has
# no server-side default; this matches the helper image used in the PoC.
DEFAULT_DOCKER_IMAGE = "rest-pty-helper:latest"

# Mirrors the server's session_name_type rule (models/validate.go): alphanumeric and '-'.
SESSION_NAME_RE = re.compile(r"^[a-zA-Z0-9-]+$")


class _RegexValidator(Validator):
    """Validate non-blank input against a regex."""

    def __init__(self, pattern, message):
        self.pattern = pattern
        self.message = message

    def validate(self, document):
        text = document.text.strip()
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
    the container image and the interfaces/ports it should listen on.
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

    rows = _prompt_int("TTY display rows [Default 100]: ", min_value=30, default=100)
    cols = _prompt_int("TTY display cols [Default 300]: ", min_value=80, default=300)

    # The first whitespace-separated token is the command; the rest are arguments.
    command_line = prompt(
        "Run Command: ", validator=_NonEmptyValidator(), validate_while_typing=False
    ).strip()
    tokens = command_line.split()
    command = {"cmd": tokens[0], "args": tokens[1:]}

    driver = radiolist_dialog(
        title="Session Driver",
        text="Select the session driver type:",
        values=[
            ("PTY", "PTY (local pseudo-terminal)"),
            ("DOCKER", "Docker (sandboxed container)"),
        ],
    ).run()
    if driver is None:
        raise KeyboardInterrupt

    if driver == "PTY":
        driver_metadata = {"display_rows": rows, "display_cols": cols}
    else:
        driver_metadata = _collect_docker_metadata(rows, cols)

    payload = {
        "name": name,
        "command": command,
        "io_buf_cap": io_buf_cap,
        "driver": driver,
        "driver_metadata": driver_metadata,
    }

    resp = requests.post(sessions_url(ctx.obj["base_url"]), json=payload)
    show(resp)


def _collect_docker_metadata(rows, cols):
    """Collect docker-driver metadata: image and published listening ports.

    The container always uses bridge networking. Listening interfaces/ports are
    collected in a loop until a blank line; for each, the container port matches
    the published host port. All other docker parameters use server defaults.
    """
    image = prompt(
        "Container Image: ",
        default=DEFAULT_DOCKER_IMAGE,
        validator=_NonEmptyValidator(),
        validate_while_typing=False,
    ).strip()

    publish_ports = []
    while True:
        entry = prompt("Interface And Port [blank to continue]: ").strip()
        if not entry:
            break
        try:
            host_ip, port = _parse_interface_port(entry)
        except ValueError as exc:
            click.echo(f"  {exc}", err=True)
            continue
        publish_ports.append(
            {"container_port": port, "host_ip": host_ip, "host_port": port}
        )

    metadata = {
        "image": image,
        "display_rows": rows,
        "display_cols": cols,
        "network_mode": "bridge",
    }
    if publish_ports:
        metadata["publish_ports"] = publish_ports
    return metadata


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

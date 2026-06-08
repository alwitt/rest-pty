# PTY Special Character Handling

Reference notes for `rest-pty` — a REST wrapper around [`creack/pty`](https://github.com/creack/pty),
intended to expose a persistent shell session (bash, ssh, etc.) as a tool, including for an LLM agent.

This document captures the design reasoning around control characters, signals, ASCII/ANSI,
echo, and REST transport encoding.

---

## 1. The core mental model: a PTY is a byte stream

A PTY master is **just a byte stream**. Writing to it is like *typing on a keyboard*. There is **no
distinction at the PTY level** between "text" and "control characters" — `0x03` (Ctrl+C) is written the
exact same way as `h`.

What gives a byte meaning is the **line discipline** (the `termios` terminal settings), which is owned
by the **program running on the slave side**, not by the wrapper. The same byte can mean "interrupt,"
"EOF," or "literal data" depending on the terminal mode.

### EOT / EOF example

In the classic `creack/pty` example:

```go
f.Write([]byte{4}) // EOT
```

- Byte `4` is ASCII **EOT** (End Of Transmission) = **Ctrl+D**.
- A PTY is **not a pipe**: closing isn't how you signal EOF. Instead, in canonical ("cooked") mode the
  line discipline sees the EOF character and makes the slave-side `read()` return.
- EOT only triggers EOF **at the start of a line**. That's why preceding writes end in `\n`. With no
  trailing newline you'd need to send EOT twice (first flushes the partial line, second yields EOF).
- Without it, `grep` never sees EOF, never exits, and `io.Copy` blocks forever.

---

## 2. Control characters vs. signals vs. process control

EOT is **not a signal** — it's an end-of-input indicator. There are three distinct mechanisms, often
confused:

### a) EOF / line-editing characters
Interpreted by the line discipline, never become signals: EOF (`^D`), backspace, kill-line (`^U`), etc.

### b) Signal-generating control characters (`ISIG`)
The line discipline translates a few control bytes into signals sent to the **foreground process group**:

| Key   | Byte   | Signal    |
|-------|--------|-----------|
| Ctrl+C | `0x03` | SIGINT    |
| Ctrl+\ | `0x1c` | SIGQUIT   |
| Ctrl+Z | `0x1a` | SIGTSTP   |

```go
f.Write([]byte{3}) // Ctrl+C -> SIGINT to the foreground process group
```

This is correct **when emulating a user typing**. Two caveats:
- It **only works in cooked/`ISIG` mode**. In raw mode (vim, shells with line editors), the byte is
  passed through as literal data and **no signal is generated**.
- It targets the **foreground process group**, not necessarily the specific child you spawned.

### c) Real signals via the process
The direct way to control a process from your own logic:

```go
c.Process.Signal(syscall.SIGINT)
c.Process.Kill()
```

Independent of terminal modes.

### Which is "standard"?
- **Emulating keyboard input** → write the control byte to the PTY.
- **Process control from your program** → use `c.Process.Signal(...)`.

---

## 3. Signals and process groups (the `bash` case)

If the child is `bash`:

- `c.Process.Signal(sig)` sends the signal to **bash itself** (the single PID you spawned), **not** to a
  command running inside bash. It's effectively `kill(pid, sig)` on that one PID.
- Interactive bash *handles SIGINT itself* and will not forward it to a foreground child. So
  `c.Process.Signal(SIGINT)` while `sleep 100` runs does **not** interrupt the `sleep`.
- Under job control, bash places each command in its **own process group** and makes it the terminal's
  **foreground process group**.

| Method                                | Target                                            |
|---------------------------------------|---------------------------------------------------|
| `c.Process.Signal(sig)`               | **bash only** (the one PID you spawned)           |
| `f.Write([]byte{3})` (Ctrl+C)         | **foreground process group** = the running command |
| `syscall.Kill(-pgid, sig)`            | a whole process group (which one depends on setup) |

To interrupt the command *running inside* bash, send the control byte through the PTY (mirrors real
terminal behavior). Use `c.Process.Signal` to act on bash itself (e.g. tear the whole session down).

---

## 4. ASCII, control bytes, and the Ctrl+letter pattern

ASCII defines **code points 0–31 (C0 controls) and 127 (DEL)**, each with a standardized name. The byte
values and names are ASCII; the **terminal behaviors** (EOF, SIGINT) are **POSIX `termios` conventions**,
not ASCII, and are configurable via `stty`.

| Byte | ASCII name          | Abbrev | Key    |
|------|---------------------|--------|--------|
| 3    | End of Text         | ETX    | Ctrl+C |
| 4    | End of Transmission | EOT    | Ctrl+D |
| 26   | Substitute          | SUB    | Ctrl+Z |
| 28   | File Separator      | FS     | Ctrl+\ |

The `termios` `c_cc` table maps a byte to an action and is configurable:

```
$ stty -a
... intr = ^C; quit = ^\; erase = ^?; ... eof = ^D; ...
$ stty intr ^G    # remap SIGINT to Ctrl+G
```

### Ctrl+letter math

```
Ctrl+<letter> = ASCII(letter) - 64   (equivalently, letter & 0x1f)
```

- `C` = 67 → **3** (ETX)
- `D` = 68 → **4** (EOT)
- `Z` = 90 → **26** (SUB)
- `\` = 92 → **28** (FS)

---

## 5. Other control keys: backspace, delete, Enter

The historically messy ones:

| Key (pressed)        | Byte usually sent     | ASCII / Note          | Go escape | termios slot |
|----------------------|-----------------------|-----------------------|-----------|--------------|
| **Backspace**        | `0x7f` (DEL!)         | DEL                   | —         | `VERASE`     |
| Ctrl+H               | `0x08`                | BS                    | `\b`      | —            |
| **Enter / Return**   | `0x0d` (CR)           | CR                    | `\r`      | —            |
| **Delete** (forward) | `\x1b[3~` (esc seq)   | multi-byte sequence   | —         | —            |

- **Backspace sends DEL (`0x7f`), not BS (`0x08`)** on essentially all modern Unix terminals. It "works"
  because `VERASE` is set to `0x7f` to match.
- **Enter sends CR (`0x0d`), not LF.** The line discipline's `ICRNL` flag maps incoming CR → LF (`0x0a`)
  before the program reads it. That's why programs read lines ending in `\n` even though the keyboard
  sent `\r`. In **raw mode** (`ICRNL` off) you see `\r` and must handle CR→LF yourself.
- **Delete, arrows, Home/End, Page Up/Down, F-keys are escape sequences**, not single bytes.

---

## 6. Go standard library: are these byte values constants?

**No.** Go's stdlib has no named constants for the ASCII byte values (no `ascii.EOT`/`ascii.ETX`). The
`unicode` package only defines boundaries (`MaxASCII = ''`, `MaxRune`, etc.).

The **language** provides escape sequences for a handful of common control characters:

| Escape | Byte | ASCII |
|--------|------|-------|
| `\a`   | 7    | BEL   |
| `\b`   | 8    | BS    |
| `\t`   | 9    | HT    |
| `\n`   | 10   | LF    |
| `\v`   | 11   | VT    |
| `\f`   | 12   | FF    |
| `\r`   | 13   | CR    |

EOT (4), ETX (3), SUB (26), FS (28) have **no escape** — use `'\x04'` or an integer literal.

### Watch out: `VEOF`/`VINTR` are indices, not byte values

The `syscall` package defines `VINTR`, `VQUIT`, `VEOF`, `VSUSP` — but these are **array indices into the
termios `c_cc` table**, not byte values:

```
VINTR = 0x0   // index 0 in c_cc
VQUIT = 0x1   // index 1
VEOF  = 0x4   // index 4   <- coincidentally also 4, but it's an INDEX
VSUSP = 0xa   // index 10
```

`VEOF == 0x4` is a coincidence. `EOT = 4` is the **byte you write**; `VEOF = 4` is the **slot** in
`termios.Cc[]` that holds the EOF byte. (`syscall` is frozen; newer code uses
`golang.org/x/sys/unix`, same constants, still indices.)

Most projects just define their own:

```go
const (
    ETX = 0x03 // Ctrl+C
    EOT = 0x04 // Ctrl+D
    SUB = 0x1a // Ctrl+Z
)
```

---

## 7. ANSI escape sequences — which byte range?

"ANSI" is overloaded:

1. **ANSI escape sequences** → named after **ANSI X3.64 / ECMA-48 / ISO 6429**, a standard for in-band
   control sequences. Nothing to do with a character set.
2. **"ANSI characters" (128–255)** → colloquial (incorrect) name for code pages like Windows-1252 /
   ISO-8859-1.

**ANSI escape sequences are built from ASCII**: an `ESC` (`0x1b`, a 0–31 control char) followed by
**printable ASCII (32–126)**. They are **not** in the 128–255 range.

Example — red foreground, `ESC[31m`:

| Byte         | Char | Set                       |
|--------------|------|---------------------------|
| `0x1b` (27)  | ESC  | Control character (0–31)  |
| `0x5b` (91)  | `[`  | Printable ASCII (32–126)  |
| `0x33` (51)  | `3`  | Printable ASCII (32–126)  |
| `0x31` (49)  | `1`  | Printable ASCII (32–126)  |
| `0x6d` (109) | `m`  | Printable ASCII (32–126)  |

CSI structure: `ESC [` introducer, ASCII digits/`;` parameters, ASCII-letter final byte
(`m`, `A`, `H`, `J`, `K`, …).

Range notes:
- **128–159** = **C1 control set** (not "ANSI characters"). It defines single-byte equivalents like
  `0x9b` = CSI, `0x9d` = OSC — but these are **avoided in practice** (terminals use the 7-bit `ESC` forms;
  128–159 are UTF-8 continuation bytes).
- **160–255** = printable upper half of ISO-8859-1 / Windows-1252 (text, not escapes).

A common CSI strip regex: `\x1b\[[0-9;]*[A-Za-z]` (plus separate handling for OSC `\x1b]…BEL/ST`).

---

## 8. Echo: does the PTY reflect my input (incl. control chars) back?

**Yes, when echo is enabled** — but the form and source depend on terminal mode.

### Cooked mode, `ECHO` on (plain `read()` programs like `cat`, `grep`)
The line discipline echoes input. Control chars are echoed per `ECHOCTL` (on by default) in **caret
notation**:

| You send          | Echoed back as                                  |
|-------------------|-------------------------------------------------|
| Ctrl+C (`0x03`)   | `^C`                                            |
| Ctrl+D (`0x04`)   | usually not echoed (consumed as EOF)            |
| ESC (`0x1b`)      | `^[`                                            |
| Ctrl+\ (`0x1c`)   | `^\`                                            |
| Enter (`0x0d`)    | CR+LF (a real newline)                          |
| Tab (`0x09`)      | tab spacing                                     |
| Backspace (`0x7f`)| `^?`, or with `ECHOE` a destructive `\b \b`     |

### Raw mode — the application echoes itself (interactive bash/readline, ssh, vim)
Interactive bash turns **off** kernel `ECHO` and echoes via line editing. Echo is now **line-redraw ANSI
escape sequences**, not tidy `^X` tokens:
- Arrows / Ctrl+R / history → cursor moves + line repaints (ANSI sequences in your output).
- Tab → completion lists + repaint.
- Ctrl+C → `^C` + fresh prompt.

### Consequences for the wrapper
1. The agent **will see its own input reflected back** — as caret text (cooked) or redraw escapes (raw).
2. **ANSI-stripping alone isn't enough**: caret echoes like `^C` are plain ASCII; readline repaints the
   same line multiple times.
3. You **can't reliably suppress echo from the master side** — interactive programs reset `termios`
   themselves.
4. **No echo is sometimes expected** — password prompts disable `ECHO`.

Cleaning suggestions for an LLM consumer: ANSI-strip **and** collapse `\r`/backspace line-rewrites to the
final line state **and** optionally filter stray caret echoes. Or run commands non-interactively
(feed command, read until prompt) to avoid most line-editing noise.

---

## 9. REST transport encoding

### `application/octet-stream` is a transparent byte pipe — no escape layer

`\r` is **notation**, not something that exists on the wire. To "press Enter," the body must contain the
actual byte `0x0d`, **not** the two characters `\` `r`.

For `echo "Hello"` + Enter, the body is these 13 bytes:

```
65 63 68 6f 20 22 48 65 6c 6c 6f 22 0d
 e  c  h  o     "  H  e  l  l  o  "  <CR>
```

The `\r` → `0x0d` conversion happens on the **caller's side**, and who does it depends on the tool:
- **Programming languages** do it in the string-literal parser (`"...\r"` → `0x0d`).
- **curl does NOT** interpret `\r` automatically:
  ```bash
  curl --data-binary 'echo "Hello"\r'    # WRONG: literal backslash-r (0x5c 0x72)
  curl --data-binary $'echo "Hello"\r'   # RIGHT: $'...' emits a real 0x0d
  printf 'echo "Hello"\r' | curl --data-binary @- ...  # RIGHT
  ```

**Server rule: keep it dumb.** Write received bytes to the PTY verbatim. Do **not** interpret escape
sequences — that creates ambiguity (what if the user wants to type a literal backslash-r, e.g.
`grep '\r' file`?). Escaping is the caller's responsibility.

### JSON + base64 (alternative)

Same principle, one more layer: base64-encode the **real bytes** (including `0x0d`):

```json
{"data": "ZWNobyAiSGVsbG8iDQ=="}
```

`DQ==` decodes to `0x0d`. Server base64-decodes and writes through — still no escape interpretation.

---

## 10. Designing the API surface — and the LLM-agent twist

### The split that matters: bytes-to-terminal vs. signals-to-process

There is no "text path vs. control-character path" at the PTY level. The real dividing line:

1. **Things a user could type** → write bytes to the PTY master (includes ALL control chars, arrows,
   F-keys). Don't special-case them; let the line discipline interpret.
2. **Things with no keystroke, or that must work in raw mode** → out-of-band:
   - No keystroke exists for SIGTERM, SIGKILL, SIGHUP, SIGUSR1/2 → must use `kill()`.
   - Ctrl+C only generates SIGINT in cooked mode; raw-mode programs need a **real signal**.
   - Window resize is an `ioctl` (`TIOCSWINSZ` / `pty.Setsize`), raising SIGWINCH.

### Recommended endpoints

```
/input            (symbolic JSON: text + named keys)   <- agents
/input/raw        (octet-stream: exact bytes)          <- programmatic callers
/signal           (SIGINT/SIGTERM/SIGKILL — raw-mode programs & no-keystroke signals)
/resize           (TIOCSWINSZ / pty.Setsize)
```

### Why a raw byte pipe is the WRONG primary interface for an LLM

1. **LLMs reason about keys symbolically**, not numerically. They reliably "want Ctrl+C," but won't
   reliably emit `0x03` or `\x1b[A`.
2. **JSON can't cleanly carry control bytes** anyway.

So: **the agent names what it wants; the server owns the bytes.** This also centralizes fiddly,
mode-dependent knowledge (CR-vs-LF for Enter, DEL-vs-BS for Backspace, arrow escape sequences) in one
place.

### Agent-facing shape: an ordered token list (ordering matters)

```json
POST /sessions/{id}/input
{
  "input": [
    {"text": "echo \"Hello\""},
    {"key": "enter"}
  ]
}
```

- `{"text": ...}` → write UTF-8 bytes **literally** (no backslash-escape interpretation).
- `{"key": "enter"}` → server emits `0x0d`.
- Interleaving works: `[{"text":"sleep 100"},{"key":"enter"},{"key":"ctrl-c"}]`.

Handle Ctrl+letter with math, not a giant enum:

```json
{"ctrl": "c"}  ->  0x03      // letter & 0x1f
{"ctrl": "d"}  ->  0x04
{"ctrl": "z"}  ->  0x1a
```

Named key vocabulary the server maps to bytes/sequences:

| Token                     | Bytes server sends                  |
|---------------------------|-------------------------------------|
| `enter`                   | `0x0d` (CR — works cooked & raw)     |
| `tab`                     | `0x09`                              |
| `escape`                  | `0x1b`                              |
| `backspace`               | `0x7f` (DEL — matches real terminals)|
| `up`/`down`/`right`/`left`| `\x1b[A` / `B` / `C` / `D`           |
| `home`/`end`              | `\x1b[H` / `\x1b[F`                  |
| `delete`                  | `\x1b[3~`                           |
| `f1`…`f12`                | their escape sequences              |

### The tool schema IS the UX for an LLM
- Make `key` an **enum** so the model discovers valid names and can't invent `0x03`.
- Put worked examples in the tool description (run a command; interrupt with Ctrl+C; history with up arrow).
- Describe `text` as "typed literally, no escape sequences."

### Newline policy decision
Models will put real newlines (`0x0a`) in `text` (heredocs, multi-line scripts) — those are legitimate
JSON bytes; pass them through. **Recommendation:** `text` is written byte-for-byte, and line submission is
always the explicit `enter` key (`0x0d`). Don't auto-append Enter, and don't scan `text` for the
two-character `\r` (reintroduces ambiguity).

### Keep the raw path underneath
Implement the symbolic endpoint on top of the raw byte writer; optionally still expose octet-stream
`/input/raw` for programmatic callers that want exact bytes.

---

## Quick-reference decision table

| Caller wants…                                              | Path        | Mechanism                          |
|-----------------------------------------------------------|-------------|------------------------------------|
| Type text                                                 | `/input`    | bytes → master                     |
| Ctrl+C/D/Z, flow control, backspace, Enter, arrows, F-keys| `/input`    | bytes → master (line disc. interprets) |
| Interrupt a **raw-mode** program                          | `/signal`   | `kill()` SIGINT/SIGTERM            |
| Terminate / kill / SIGHUP / SIGUSR                        | `/signal`   | `kill()` (no keystroke exists)     |
| Resize                                                    | `/resize`   | `TIOCSWINSZ` / `pty.Setsize`       |

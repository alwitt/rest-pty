# REST PTY

Linux pseudo-terminal management with a REST API wrapper.

* [Brain storming session with LLM regarding special character handling.](./pty_special_character_notes.md)

## ANSI Escape Sequence Removal REGEX

The following REGEX works in Golang to remove ANSI sequence.

```re
(?:\x1B[@-Z\\-_]|[\x80-\x9A\x9C-\x9F]|(?:\x1B\[|\x9B)[0-?]*[ -/]*[@-~])
```

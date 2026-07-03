package models

import (
	"bytes"
	"encoding/base64"
	"fmt"

	"github.com/alwitt/goutils"
)

// SessionInputCommandTypeENUMType session input command type ENUM
type SessionInputCommandTypeENUMType string

const (
	// SessionInputCommandTypeText text input to STDIN
	SessionInputCommandTypeText SessionInputCommandTypeENUMType = "TEXT"

	// SessionInputCommandTypeCTRL CTRL character input to STDIN
	SessionInputCommandTypeCTRL SessionInputCommandTypeENUMType = "CTRL"

	// SessionInputCommandTypeCR carriage return input to STDIN
	SessionInputCommandTypeCR SessionInputCommandTypeENUMType = "ENTER"

	// SessionInputCommandTypeRaw raw input data to STDIN. The content is a
	// Base64-encoded byte slice capturing keyboard presses verbatim, including
	// ANSI characters, escape sequences, and control characters.
	SessionInputCommandTypeRaw SessionInputCommandTypeENUMType = "RAW"
)

// Values all valid SessionInputCommandTypeENUMType values
func (SessionInputCommandTypeENUMType) Values() []SessionInputCommandTypeENUMType {
	return []SessionInputCommandTypeENUMType{
		SessionInputCommandTypeText,
		SessionInputCommandTypeCTRL,
		SessionInputCommandTypeCR,
		SessionInputCommandTypeRaw,
	}
}

// SessionInputCommand session input command object
type SessionInputCommand struct {
	// Type session input command type
	Type SessionInputCommandTypeENUMType `json:"type" validate:"required,session_input_cmd_type" jsonschema:"session input command type"`

	/*
		Content command content if needed

		For `TEXT` type, the content is the text to write to STDIN.

		For `CTRL` type, the content is the control character (e.g. C for CTRL+C, etc.).

		For `ENTRY` type, the content is ignored

		For `RAW` type, the content is the raw input byte slice (direct capture of
		keyboard presses, including ANSI characters, escape sequences, and control
		characters) in Base64 encoding.
	*/
	Content *string `json:"content,omitempty" jsonschema:"command content if needed. For TEXT type, the content is the text to write to STDIN. For CTRL type, the content is the control character (e.g. C for CTRL+C, etc.). For ENTER type, the content is ignored. For RAW type, the content is the raw input byte slice (direct capture of keyboard presses, including ANSI characters, escape sequences, and control characters) in Base64 encoding."`
}

const (
	// asciiCR carriage return byte sent for an ENTER command.
	asciiCR byte = 0x0d

	// ctrlMask folds an ASCII letter into its control character (e.g. 'C'/'c' -> 0x03).
	ctrlMask byte = 0x1f
)

// IsValid validates the command's Content against its Type, returning a
// BadInputError describing the first problem found or nil when well formed.
//
// It is the single source of truth for command validity: it is invoked by the
// struct-level validator registered against SessionInputCommand, and used as a
// defensive guard by BuildStdinInputFromCommands.
func (c SessionInputCommand) IsValid() error {
	switch c.Type {
	case SessionInputCommandTypeText:
		// An empty string is allowed (a no-op); a missing Content is not.
		if c.Content == nil {
			return goutils.NewValidationError("TEXT command requires content", nil, true)
		}

	case SessionInputCommandTypeCTRL:
		// Content holds a single letter (e.g. "C" for CTRL+C).
		if c.Content == nil || len(*c.Content) != 1 {
			return goutils.NewValidationError(
				"CTRL command requires a single-character content", nil, true,
			)
		}
		if !isASCIILetter((*c.Content)[0]) {
			return goutils.NewValidationError(
				fmt.Sprintf("CTRL content %q is not an ASCII letter", *c.Content), nil, true,
			)
		}

	case SessionInputCommandTypeCR:
		// Content is ignored.

	case SessionInputCommandTypeRaw:
		// Content is a Base64-encoded byte slice; an empty string decodes to a
		// zero-length slice (a no-op), but missing or malformed Content is not.
		if c.Content == nil {
			return goutils.NewValidationError("RAW command requires content", nil, true)
		}
		if _, err := base64.StdEncoding.DecodeString(*c.Content); err != nil {
			return goutils.NewValidationError(
				"RAW content is not valid Base64", err, true,
			)
		}

	default:
		return goutils.NewValidationError(
			fmt.Sprintf("unsupported command type %q", c.Type), nil, true,
		)
	}

	return nil
}

// BuildStdinInputFromCommands build bytes slice to feed into STDIN from sequence of commands.
//
// Commands are processed in order so callers can interleave text, control
// characters, and carriage returns (e.g. type a command then press ENTER).
// Each command is validated via IsValid; the returned error identifies the
// offending command index.
func BuildStdinInputFromCommands(cmds []SessionInputCommand) ([]byte, error) {
	var buf bytes.Buffer

	for idx, cmd := range cmds {
		if err := cmd.IsValid(); err != nil {
			return nil, goutils.NewBadInputError(
				fmt.Sprintf("bad command %d in sequence", idx), err, true,
			)
		}

		switch cmd.Type {
		case SessionInputCommandTypeText:
			// Text is written to STDIN verbatim; no escape interpretation.
			buf.WriteString(*cmd.Content)

		case SessionInputCommandTypeCTRL:
			// Folding the ASCII letter with ctrlMask yields the control byte
			// and works for both upper and lower case.
			buf.WriteByte((*cmd.Content)[0] & ctrlMask)

		case SessionInputCommandTypeCR:
			// Content is ignored; ENTER is a carriage return.
			buf.WriteByte(asciiCR)

		case SessionInputCommandTypeRaw:
			// Content is Base64-encoded raw bytes written to STDIN verbatim.
			// IsValid above guarantees the content decodes cleanly.
			raw, err := base64.StdEncoding.DecodeString(*cmd.Content)
			if err != nil {
				return nil, goutils.NewBadInputError(
					fmt.Sprintf("bad command %d in sequence", idx), err, true,
				)
			}
			buf.Write(raw)
		}
	}

	return buf.Bytes(), nil
}

// isASCIILetter reports whether b is an ASCII letter (A-Z or a-z).
func isASCIILetter(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

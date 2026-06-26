package models_test

import (
	"encoding/base64"
	"testing"

	"github.com/alwitt/rest-pty/models"
	"github.com/apex/log"
	"github.com/go-playground/validator/v10"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionInputCommandValidate(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	log.SetLevel(log.DebugLevel)

	validate := validator.New()
	require.NoError(models.RegisterWithValidator(validate))

	ptr := func(s string) *string { return &s }

	cases := []struct {
		name      string
		command   models.SessionInputCommand
		expectErr bool
	}{
		// TEXT: content required, empty string allowed
		{
			"text-valid",
			models.SessionInputCommand{Type: models.SessionInputCommandTypeText, Content: ptr("ls -la")},
			false,
		},
		{
			"text-empty-content",
			models.SessionInputCommand{Type: models.SessionInputCommandTypeText, Content: ptr("")},
			false,
		},
		{
			"text-nil-content",
			models.SessionInputCommand{Type: models.SessionInputCommandTypeText, Content: nil},
			true,
		},

		// CTRL: exactly one ASCII letter
		{
			"ctrl-upper",
			models.SessionInputCommand{Type: models.SessionInputCommandTypeCTRL, Content: ptr("C")},
			false,
		},
		{
			"ctrl-lower",
			models.SessionInputCommand{Type: models.SessionInputCommandTypeCTRL, Content: ptr("c")},
			false,
		},
		{
			"ctrl-nil-content",
			models.SessionInputCommand{Type: models.SessionInputCommandTypeCTRL, Content: nil},
			true,
		},
		{
			"ctrl-empty-content",
			models.SessionInputCommand{Type: models.SessionInputCommandTypeCTRL, Content: ptr("")},
			true,
		},
		{
			"ctrl-multi-char",
			models.SessionInputCommand{Type: models.SessionInputCommandTypeCTRL, Content: ptr("CC")},
			true,
		},
		{
			"ctrl-non-letter",
			models.SessionInputCommand{Type: models.SessionInputCommandTypeCTRL, Content: ptr("1")},
			true,
		},

		// ENTER: content ignored
		{
			"enter-nil-content",
			models.SessionInputCommand{Type: models.SessionInputCommandTypeCR, Content: nil},
			false,
		},
		{
			"enter-ignored-content",
			models.SessionInputCommand{Type: models.SessionInputCommandTypeCR, Content: ptr("ignored")},
			false,
		},

		// RAW: content required, valid Base64
		{
			"raw-valid",
			// "ls -la\r" -> Base64
			models.SessionInputCommand{
				Type: models.SessionInputCommandTypeRaw, Content: ptr("bHMgLWxhDQ=="),
			},
			false,
		},
		{
			"raw-escape-sequence",
			// ESC [ A (up arrow) -> Base64
			models.SessionInputCommand{
				Type: models.SessionInputCommandTypeRaw, Content: ptr("G1tB"),
			},
			false,
		},
		{
			"raw-empty-content",
			// Empty string is valid Base64 decoding to a zero-length slice.
			models.SessionInputCommand{Type: models.SessionInputCommandTypeRaw, Content: ptr("")},
			false,
		},
		{
			"raw-nil-content",
			models.SessionInputCommand{Type: models.SessionInputCommandTypeRaw, Content: nil},
			true,
		},
		{
			"raw-invalid-base64",
			models.SessionInputCommand{
				Type: models.SessionInputCommandTypeRaw, Content: ptr("not!base64!"),
			},
			true,
		},

		// Bad / missing type
		{
			"unknown-type",
			models.SessionInputCommand{
				Type: models.SessionInputCommandTypeENUMType("BOGUS"), Content: ptr("x"),
			},
			true,
		},
		{
			"empty-type",
			models.SessionInputCommand{
				Type: models.SessionInputCommandTypeENUMType(""), Content: ptr("x"),
			},
			true,
		},
	}

	// Validating a single entry
	for _, tc := range cases {
		err := validate.Struct(tc.command)
		if tc.expectErr {
			assert.Errorf(err, "case %q: expected validation error", tc.name)
		} else {
			assert.NoErrorf(err, "case %q: expected no validation error", tc.name)
		}
	}

	// Validating an array of entries. The dive tag descends into the slice so
	// each element is validated (fields + struct-level).
	type commandList struct {
		Commands []models.SessionInputCommand `validate:"dive"`
	}

	// An array of only valid entries passes.
	allValid := []models.SessionInputCommand{}
	for _, tc := range cases {
		if !tc.expectErr {
			allValid = append(allValid, tc.command)
		}
	}
	assert.NoError(validate.Struct(commandList{Commands: allValid}))

	// An array containing any single invalid entry fails.
	for _, tc := range cases {
		if !tc.expectErr {
			continue
		}
		list := commandList{Commands: []models.SessionInputCommand{
			{Type: models.SessionInputCommandTypeText, Content: ptr("echo hi")},
			tc.command,
			{Type: models.SessionInputCommandTypeCR},
		}}
		assert.Errorf(
			validate.Struct(list),
			"case %q: expected array validation to fail on the invalid entry", tc.name,
		)
	}
}

func TestBuildStdinInputFromCommands(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	ptr := func(s string) *string { return &s }

	// "ls -la" Base64-encoded for the RAW command.
	rawLsLa := base64.StdEncoding.EncodeToString([]byte("ls -la"))

	// A sequence interleaving every command type: type "echo hi", press ENTER,
	// CTRL+C, then a RAW "ls -la".
	cmds := []models.SessionInputCommand{
		{Type: models.SessionInputCommandTypeText, Content: ptr("echo hi")},
		{Type: models.SessionInputCommandTypeCR},
		{Type: models.SessionInputCommandTypeCTRL, Content: ptr("C")},
		{Type: models.SessionInputCommandTypeRaw, Content: ptr(rawLsLa)},
	}

	out, err := models.BuildStdinInputFromCommands(cmds)
	require.NoError(err)
	assert.Equal([]byte("echo hi\r\x03ls -la"), out)

	// An empty RAW content contributes no bytes.
	out, err = models.BuildStdinInputFromCommands([]models.SessionInputCommand{
		{Type: models.SessionInputCommandTypeRaw, Content: ptr("")},
	})
	require.NoError(err)
	assert.Empty(out)

	// Invalid Base64 in a RAW command is rejected with the offending index.
	_, err = models.BuildStdinInputFromCommands([]models.SessionInputCommand{
		{Type: models.SessionInputCommandTypeText, Content: ptr("hi")},
		{Type: models.SessionInputCommandTypeRaw, Content: ptr("not!base64!")},
	})
	assert.Error(err)
}

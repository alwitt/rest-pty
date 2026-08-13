package models_test

import (
	"testing"

	"github.com/alwitt/rest-pty/models"
	"github.com/go-playground/validator/v10"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCairnConfigValidation pins the `required_with=Enable` / `omitempty` interaction on
// CairnConfig.
//
// These tags look like they could be normalized for consistency with the rest of the file, and
// they can not:
//   - `required` instead of `required_with` would reject every cairn-less deployment, since a
//     CairnConfig with Enable false and both pointers nil IS the zero value.
//   - dropping BaseURL's `omitempty` would panic rather than fail: `required_with` runs even
//     against a nil pointer, passes when Enable is false, and hands off to `url` - which panics
//     on any kind that is not a string.
func TestCairnConfigValidation(t *testing.T) {
	assert := assert.New(t)

	validate := validator.New()
	require.NoError(t, models.RegisterWithValidator(validate))

	validClient := &models.HTTPClientConfig{
		Retry: models.HTTPClientRetryConfig{
			MaxAttempts: 3, InitWaitTimeInSec: 1, MaxWaitTimeInSec: 5,
		},
	}
	ptr := func(v string) *string { return &v }

	type testCase struct {
		name   string
		config models.CairnConfig
		valid  bool
	}

	for _, oneCase := range []testCase{
		{
			// The ordinary cairn-disabled deployment. This is the zero value, and it must pass.
			name:   "disabled-and-empty",
			config: models.CairnConfig{Enable: false},
			valid:  true,
		},
		{
			// Disabled but configured: harmless, and the `url` tag still applies to a set value.
			name: "disabled-but-configured",
			config: models.CairnConfig{
				Enable: false, BaseURL: ptr("http://cairn:38271"), Client: validClient,
			},
			valid: true,
		},
		{
			name: "enabled-and-complete",
			config: models.CairnConfig{
				Enable: true, BaseURL: ptr("http://cairn:38271"), Client: validClient,
			},
			valid: true,
		},
		{
			name:   "enabled-missing-base-url",
			config: models.CairnConfig{Enable: true, Client: validClient},
			valid:  false,
		},
		{
			name:   "enabled-missing-client",
			config: models.CairnConfig{Enable: true, BaseURL: ptr("http://cairn:38271")},
			valid:  false,
		},
		{
			name: "enabled-bad-base-url",
			config: models.CairnConfig{
				Enable: true, BaseURL: ptr("not a url"), Client: validClient,
			},
			valid: false,
		},
		{
			// The retry sub-fields are what keep an enabled integration from being configured
			// with a zero backoff.
			name: "enabled-bad-retry",
			config: models.CairnConfig{
				Enable:  true,
				BaseURL: ptr("http://cairn:38271"),
				Client:  &models.HTTPClientConfig{Retry: models.HTTPClientRetryConfig{}},
			},
			valid: false,
		},
	} {
		t.Run(oneCase.name, func(_ *testing.T) {
			err := validate.Struct(&oneCase.config)
			if oneCase.valid {
				assert.Nil(err, oneCase.name)
			} else {
				assert.NotNil(err, oneCase.name)
			}
		})
	}
}

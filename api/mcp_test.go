// White-box test: serverInstructions is unexported, since api.BuildHTTPServer is its only
// caller and lives in this same package.
package api

import (
	"strings"
	"testing"

	"github.com/alwitt/rest-pty/workspace"
	"github.com/stretchr/testify/assert"
)

// flatten collapse all whitespace runs to single spaces.
//
// The instructions are hard-wrapped raw strings, so a phrase that reads as one sentence can be
// split by a newline at any point. Asserting against the flattened text means re-wrapping a
// paragraph does not break a test that is about what the text says.
func flatten(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// TestServerInstructions the assembly across both values of the one conditional section.
func TestServerInstructions(t *testing.T) {
	for _, cairnEnabled := range []bool{false, true} {
		name := "cairn disabled"
		if cairnEnabled {
			name = "cairn enabled"
		}

		t.Run(name, func(t *testing.T) {
			assert := assert.New(t)

			out := serverInstructions(cairnEnabled)
			flat := flatten(out)

			// The preamble is unconditional - it describes rest-pty itself.
			assert.Contains(flat, "rest-pty gives you a persistent terminal")
			assert.Contains(flat, "there is no exit code and no result attached to what you typed")
			assert.Contains(flat, "writable_dirs")

			if cairnEnabled {
				assert.Contains(flat, "cairn")
				// Read from the constant, not spelled out: a literal here would not catch the
				// mount path drifting away from what the driver actually mounts.
				assert.Contains(flat, workspace.MountPath)
				assert.Contains(flat, "update_session_workspace")
			} else {
				// A deployment that cannot resolve workspaces must not advertise them: every
				// session naming one refuses to start, so the failure would be silent and
				// repeated.
				assert.NotContains(flat, "cairn")
				assert.NotContains(flat, workspace.MountPath)
			}

			// Layout assertions run against the raw text: no leading, trailing, or run-on blank
			// space from a dropped section.
			assert.Equal(strings.TrimSpace(out), out)
			assert.NotContains(out, "\n\n\n")
		})
	}
}

// TestServerInstructionsSectionOrder the preamble comes before the workspace section, so an
// agent reads what a session IS before how a workspace attaches to one.
func TestServerInstructionsSectionOrder(t *testing.T) {
	assert := assert.New(t)

	flat := flatten(serverInstructions(true))

	preambleAt := strings.Index(flat, "rest-pty gives you a persistent terminal")
	cairnAt := strings.Index(flat, workspace.MountPath)

	assert.NotEqual(-1, preambleAt)
	assert.NotEqual(-1, cairnAt)
	assert.Less(preambleAt, cairnAt)
}

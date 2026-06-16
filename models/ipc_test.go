package models_test

import (
	"testing"

	"github.com/alwitt/rest-pty/models"
	"github.com/apex/log"
	"github.com/go-playground/validator/v10"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIPCMessageParsing(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	log.SetLevel(log.DebugLevel)

	validate := validator.New()
	require.NoError(models.RegisterWithValidator(validate))

	cases := []struct {
		name string
		msg  string
		// expectErr whether ParseIPCMessage should return an error
		expectErr bool
		// check asserts on the parsed value for the success cases
		check func(parsed any)
	}{
		/****************************************************************************************
		Request - Session Runner
		****************************************************************************************/
		{
			name: "req-run-commands",
			msg: `{
				"req_id": "req-4",
				"type": "IPC_REQ_RUN_COMMANDS",
				"sender": "client-a",
				"commands": [
					{"type": "TEXT", "content": "ls -la"},
					{"type": "ENTER"}
				]
			}`,
			expectErr: false,
			check: func(parsed any) {
				msg, ok := parsed.(models.IPCMessageReqRunCommands)
				require.True(ok, "expected IPCMessageReqRunCommands")
				assert.Equal(models.IPCMsgTypeReqRunCommands, msg.Type)
				require.Len(msg.Commands, 2)
				assert.Equal(models.SessionInputCommandTypeText, msg.Commands[0].Type)
				assert.Equal(models.SessionInputCommandTypeCR, msg.Commands[1].Type)
			},
		},

		/****************************************************************************************
		Response - Session Runner
		****************************************************************************************/
		{
			name: "resp-run-commands-failure",
			msg: `{
				"req_id": "req-4",
				"type": "IPC_RESP_RUN_COMMANDS",
				"sender": "runner",
				"success": false,
				"error": "boom"
			}`,
			expectErr: false,
			check: func(parsed any) {
				msg, ok := parsed.(models.IPCMessageRespUniversal)
				require.True(ok, "expected IPCMessageRespUniversal")
				assert.Equal(models.IPCMsgTypeRespRunCommands, msg.Type)
				assert.False(msg.Success)
				require.NotNil(msg.ErrorMsg)
				assert.Equal("boom", *msg.ErrorMsg)
			},
		},

		/****************************************************************************************
		Negative cases
		****************************************************************************************/
		{
			name:      "unknown-type",
			msg:       `{"req_id": "req-9", "type": "IPC_BOGUS", "sender": "client-a"}`,
			expectErr: true,
		},
		{
			name:      "missing-type",
			msg:       `{"req_id": "req-9", "sender": "client-a"}`,
			expectErr: true,
		},
		{
			name:      "missing-base-req-id",
			msg:       `{"type": "IPC_RESP_RUN_COMMANDS", "sender": "client-a"}`,
			expectErr: true,
		},
		{
			name:      "missing-base-sender",
			msg:       `{"req_id": "req-9", "type": "IPC_RESP_RUN_COMMANDS"}`,
			expectErr: true,
		},
		{
			name: "run-commands-empty-commands",
			msg: `{
				"req_id": "req-9",
				"type": "IPC_REQ_RUN_COMMANDS",
				"sender": "client-a",
				"commands": []
			}`,
			expectErr: true,
		},
		{
			name: "run-commands-invalid-command",
			msg: `{
				"req_id": "req-9",
				"type": "IPC_REQ_RUN_COMMANDS",
				"sender": "client-a",
				"commands": [{"type": "CTRL", "content": "CC"}]
			}`,
			expectErr: true,
		},
		{
			name:      "malformed-json",
			msg:       `{"req_id": "req-9", "type":`,
			expectErr: true,
		},
	}

	for _, tc := range cases {
		parsed, err := models.ParseIPCMessage(validate, []byte(tc.msg))
		if tc.expectErr {
			assert.Errorf(err, "case %q: expected an error", tc.name)
			continue
		}
		require.NoErrorf(err, "case %q: expected no error", tc.name)
		if tc.check != nil {
			tc.check(parsed)
		}
	}
}

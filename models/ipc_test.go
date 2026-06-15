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
			name: "req-claim-session",
			msg: `{
				"req_id": "req-1",
				"type": "IPC_REQ_CLAIM_SESSION",
				"sender": "client-a",
				"claimant": "user-1"
			}`,
			expectErr: false,
			check: func(parsed any) {
				msg, ok := parsed.(models.IPCMessageReqClaimRelated)
				require.True(ok, "expected IPCMessageReqClaimRelated")
				assert.Equal(models.IPCMsgTypeReqClaimSession, msg.Type)
				assert.Equal("user-1", msg.Claimant)
			},
		},
		{
			name: "req-release-session",
			msg: `{
				"req_id": "req-2",
				"type": "IPC_REQ_RELEASE_SESSION",
				"sender": "client-a",
				"claimant": "user-1"
			}`,
			expectErr: false,
			check: func(parsed any) {
				msg, ok := parsed.(models.IPCMessageReqClaimRelated)
				require.True(ok, "expected IPCMessageReqClaimRelated")
				assert.Equal(models.IPCMsgTypeReqReleaseSession, msg.Type)
				assert.Equal("user-1", msg.Claimant)
			},
		},
		{
			name: "req-verify-claim",
			msg: `{
				"req_id": "req-3",
				"type": "IPC_REQ_VERIFY_CLAIM",
				"sender": "client-a",
				"claimant": "user-1"
			}`,
			expectErr: false,
			check: func(parsed any) {
				msg, ok := parsed.(models.IPCMessageReqClaimRelated)
				require.True(ok, "expected IPCMessageReqClaimRelated")
				assert.Equal(models.IPCMsgTypeReqVerifyClaim, msg.Type)
				assert.Equal("user-1", msg.Claimant)
			},
		},
		{
			name: "req-run-commands",
			msg: `{
				"req_id": "req-4",
				"type": "IPC_REQ_RUN_COMMANDS",
				"sender": "client-a",
				"claimant": "user-1",
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
				assert.Equal("user-1", msg.Claimant)
				require.Len(msg.Commands, 2)
				assert.Equal(models.SessionInputCommandTypeText, msg.Commands[0].Type)
				assert.Equal(models.SessionInputCommandTypeCR, msg.Commands[1].Type)
			},
		},

		/****************************************************************************************
		Response - Session Runner
		****************************************************************************************/
		{
			name: "resp-claim-session",
			msg: `{
				"req_id": "req-1",
				"type": "IPC_RESP_CLAIM_SESSION",
				"sender": "runner",
				"success": true
			}`,
			expectErr: false,
			check: func(parsed any) {
				msg, ok := parsed.(models.IPCMessageRespUniversal)
				require.True(ok, "expected IPCMessageRespUniversal")
				assert.Equal(models.IPCMsgTypeRespClaimSession, msg.Type)
				assert.True(msg.Success)
				assert.Nil(msg.ErrorMsg)
			},
		},
		{
			name: "resp-release-session",
			msg: `{
				"req_id": "req-2",
				"type": "IPC_RESP_RELEASE_SESSION",
				"sender": "runner",
				"success": true
			}`,
			expectErr: false,
			check: func(parsed any) {
				msg, ok := parsed.(models.IPCMessageRespUniversal)
				require.True(ok, "expected IPCMessageRespUniversal")
				assert.Equal(models.IPCMsgTypeRespReleaseSession, msg.Type)
				assert.True(msg.Success)
			},
		},
		{
			name: "resp-verify-claim",
			msg: `{
				"req_id": "req-3",
				"type": "IPC_RESP_VERIFY_CLAIM",
				"sender": "runner",
				"success": true
			}`,
			expectErr: false,
			check: func(parsed any) {
				msg, ok := parsed.(models.IPCMessageRespUniversal)
				require.True(ok, "expected IPCMessageRespUniversal")
				assert.Equal(models.IPCMsgTypeRespVerifyClaim, msg.Type)
				assert.True(msg.Success)
			},
		},
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
			msg:       `{"type": "IPC_REQ_CLAIM_SESSION", "sender": "client-a", "claimant": "user-1"}`,
			expectErr: true,
		},
		{
			name:      "missing-base-sender",
			msg:       `{"req_id": "req-9", "type": "IPC_REQ_CLAIM_SESSION", "claimant": "user-1"}`,
			expectErr: true,
		},
		{
			name:      "claim-missing-claimant",
			msg:       `{"req_id": "req-9", "type": "IPC_REQ_CLAIM_SESSION", "sender": "client-a"}`,
			expectErr: true,
		},
		{
			name: "run-commands-empty-commands",
			msg: `{
				"req_id": "req-9",
				"type": "IPC_REQ_RUN_COMMANDS",
				"sender": "client-a",
				"claimant": "user-1",
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
				"claimant": "user-1",
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

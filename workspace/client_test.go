package workspace_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alwitt/rest-pty/models"
	"github.com/alwitt/rest-pty/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestClient point a cairn client at a stub server, with retries disabled so a test that
// exercises a failure path does not spend the backoff waiting for it.
func newTestClient(t *testing.T, baseURL string) workspace.CairnClient {
	t.Helper()
	client, err := workspace.NewCairnClient(t.Context(), models.CairnConfig{
		Enable:  true,
		BaseURL: &baseURL,
		Client: &models.HTTPClientConfig{
			Retry: models.HTTPClientRetryConfig{
				MaxAttempts:       0,
				InitWaitTimeInSec: 1,
				MaxWaitTimeInSec:  1,
			},
		},
	})
	require.NoError(t, err)
	return client
}

// TestFetchWorkspace the request cairn receives, and the record that comes back out.
func TestFetchWorkspace(t *testing.T) {
	assert := assert.New(t)

	var gotPath, gotName, gotMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		gotName = r.URL.Query().Get("name")
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{
			"success": true,
			"request_id": "req-1",
			"workspaces": [{
				"id": "1b8f6a2e-0000-4000-8000-000000000000",
				"name": "my-ws",
				"description": "ignored by rest-pty",
				"volume_name": "cairn-local-dev-1b8f6a2e-0000-4000-8000-000000000000",
				"volume_state": "READY",
				"created_at": "2026-08-12T00:00:00Z",
				"updated_at": "2026-08-12T00:00:00Z"
			}]
		}`))
	}))
	defer server.Close()

	found, err := newTestClient(t, server.URL).FetchWorkspace(t.Context(), "my-ws")
	require.NoError(t, err)

	// The name travels as a query filter against the collection endpoint - not as a path
	// segment, which cairn reserves for the workspace ID.
	assert.Equal(http.MethodGet, gotMethod)
	assert.Equal("/v1/workspaces", gotPath)
	assert.Equal("my-ws", gotName)

	assert.Equal("my-ws", found.Name)
	assert.Equal("cairn-local-dev-1b8f6a2e-0000-4000-8000-000000000000", found.VolumeName)
	assert.Equal(workspace.VolumeStateReady, found.VolumeState)
	assert.True(found.IsVolumeReady())
	// Fields rest-pty does not declare are dropped rather than failing the decode, so cairn
	// can grow its record without this client caring.
	assert.Equal("1b8f6a2e-0000-4000-8000-000000000000", found.ID)
}

// TestFetchWorkspaceVolumeNotReady a workspace with no volume is returned, NOT an error. The
// caller distinguishes "does not exist" from "exists but cannot be mounted", so this client
// must not collapse the two.
func TestFetchWorkspaceVolumeNotReady(t *testing.T) {
	assert := assert.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The lookup must not pre-filter on volume state, or a NONE workspace would come
		// back as an empty list and be indistinguishable from an unknown name.
		assert.Empty(r.URL.Query()["volume_state"])
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{
			"success": true,
			"request_id": "req-1",
			"workspaces": [{
				"id": "1b8f6a2e-0000-4000-8000-000000000000",
				"name": "no-vol",
				"volume_name": "cairn-local-dev-1b8f6a2e-0000-4000-8000-000000000000",
				"volume_state": "NONE"
			}]
		}`))
	}))
	defer server.Close()

	found, err := newTestClient(t, server.URL).FetchWorkspace(t.Context(), "no-vol")
	require.NoError(t, err)

	assert.Equal(workspace.VolumeStateNone, found.VolumeState)
	assert.False(found.IsVolumeReady())
}

// TestFetchWorkspaceErrors every way the lookup fails to yield exactly one workspace.
func TestFetchWorkspaceErrors(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		payload string
	}{
		// cairn answers an unknown name with 200 and an empty list, so not-found is a length
		// check rather than a status check.
		{"unknown name", http.StatusOK, `{"success": true, "workspaces": []}`},
		{"absent workspaces key", http.StatusOK, `{"success": true}`},
		{"server error", http.StatusInternalServerError, `{"success": false}`},
		{"not authorized", http.StatusForbidden, `{"success": false}`},
		{"malformed body", http.StatusOK, `{"workspaces": [`},
		// Workspace names are unique in cairn, so this is a broken invariant rather than a
		// result to pick from arbitrarily.
		{"duplicate names", http.StatusOK, `{"workspaces": [
			{"name": "dup", "volume_name": "a", "volume_state": "READY"},
			{"name": "dup", "volume_name": "b", "volume_state": "READY"}
		]}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("content-type", "application/json")
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.payload))
				},
			))
			defer server.Close()

			_, err := newTestClient(t, server.URL).FetchWorkspace(t.Context(), "some-ws")
			assert.Error(t, err, tc.name)
		})
	}
}

// TestFetchWorkspaceHonorsContext a cancelled context aborts the lookup rather than letting a
// hung cairn hold session start open. The lookup runs inside driverImpl.Start while the driver
// lock is held, so this is what bounds it in place of a dedicated client timeout knob.
func TestFetchWorkspaceHonorsContext(t *testing.T) {
	// Never responds; the request can only end by cancellation.
	blocked := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-blocked:
		}
	}))
	defer func() {
		close(blocked)
		server.Close()
	}()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := newTestClient(t, server.URL).FetchWorkspace(ctx, "my-ws")
	assert.Error(t, err)
}

// TestNewCairnClientRejectsIncompleteConfig the constructor is the only place BaseURL and
// Client are dereferenced, so it checks them itself rather than trusting that config
// validation ran first.
func TestNewCairnClientRejectsIncompleteConfig(t *testing.T) {
	assert := assert.New(t)
	baseURL := "http://127.0.0.1:44123"

	_, err := workspace.NewCairnClient(t.Context(), models.CairnConfig{Enable: true})
	assert.Error(err)

	_, err = workspace.NewCairnClient(t.Context(), models.CairnConfig{
		Enable: true, BaseURL: &baseURL,
	})
	assert.Error(err)

	_, err = workspace.NewCairnClient(t.Context(), models.CairnConfig{
		Enable: true, Client: &models.HTTPClientConfig{},
	})
	assert.Error(err)
}

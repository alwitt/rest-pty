// White-box test: resolveWorkspaceMount and withVolumeMount are unexported, and they are
// deliberately split out of newDockerCoreDriver precisely so they can be exercised without a
// live Docker daemon.
package session

import (
	"testing"

	"github.com/alwitt/goutils"
	goutilsRuntime "github.com/alwitt/goutils/runtime"
	mockworkspace "github.com/alwitt/rest-pty/mocks/workspace"
	"github.com/alwitt/rest-pty/models"
	"github.com/alwitt/rest-pty/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cairnVolumeName the volume name cairn persisted for the test workspace. Deliberately NOT
// derivable from the workspace name: rest-pty must read this off the record cairn returns, and
// a test whose expected value could be reconstructed from the inputs would not catch a
// re-derivation.
const cairnVolumeName = "cairn-local-dev-1b8f6a2e-0000-4000-8000-000000000000"

// dockerParams a minimal set of docker driver params. Image is the only required field.
func dockerParams() models.SessionDriverDockerParams {
	return models.SessionDriverDockerParams{
		ContainerRuntimeParams: goutilsRuntime.ContainerRuntimeParams{Image: "alpine:latest"},
	}
}

// TestResolveWorkspaceMount the happy path: the volume cairn named, mounted read-write at the
// path cairn fixed.
func TestResolveWorkspaceMount(t *testing.T) {
	assert := assert.New(t)

	cairn := mockworkspace.NewCairnClient(t)
	cairn.EXPECT().
		FetchWorkspace(t.Context(), "my-ws").
		Return(workspace.Workspace{
			ID:          "1b8f6a2e-0000-4000-8000-000000000000",
			Name:        "my-ws",
			VolumeName:  cairnVolumeName,
			VolumeState: workspace.VolumeStateReady,
		}, nil).
		Once()

	mounted, err := resolveWorkspaceMount(
		t.Context(), cairn, "test-session", "my-ws", dockerParams(),
	)
	require.NoError(t, err)

	require.Len(t, mounted.VolumeMounts, 1)
	// The volume name comes off cairn's record. If this ever becomes something reconstructed
	// from the workspace name or ID, the mount-only contract is broken - cairn derives the
	// name from the immutable workspace ID and persists it, so it survives a rename and
	// rest-pty must not second-guess it.
	assert.Equal(cairnVolumeName, mounted.VolumeMounts[0].Name)
	assert.Equal("/mnt/cairn/ws", mounted.VolumeMounts[0].MountPath)
	assert.Equal(workspace.MountPath, mounted.VolumeMounts[0].MountPath)
	// A workspace volume exists to be written to; read-only is left unset (defaults false).
	assert.Nil(mounted.VolumeMounts[0].ReadOnly)
	assert.False(mounted.VolumeMounts[0].IsReadOnly())

	// Nothing else about the session's declared params is touched.
	assert.Equal("alpine:latest", mounted.Image)
	assert.Empty(mounted.WorkingDir)
}

// TestResolveWorkspaceMountPreservesConfiguredMounts the workspace mount is appended to the
// mounts the session declared, and two resolves off the same params do not tread on each other.
//
// The second half is what the fresh-slice merge exists for, and asserting it needs some care:
// the input slice is given spare capacity, because with a len==cap slice a buggy in-place
// `append(params.VolumeMounts, ...)` would reallocate anyway and pass. With spare capacity the
// bug is real - both resolves write the workspace mount into the same backing array index, and
// the first result silently ends up naming the second's volume.
func TestResolveWorkspaceMountPreservesConfiguredMounts(t *testing.T) {
	assert := assert.New(t)

	declared := dockerParams()
	declared.VolumeMounts = make([]goutilsRuntime.ContainerVolumeMount, 1, 4)
	declared.VolumeMounts[0] = goutilsRuntime.ContainerVolumeMount{
		Name: "session-declared-vol", MountPath: "/data",
	}

	cairn := mockworkspace.NewCairnClient(t)
	cairn.EXPECT().
		FetchWorkspace(t.Context(), "ws-one").
		Return(workspace.Workspace{
			Name: "ws-one", VolumeName: "vol-one", VolumeState: workspace.VolumeStateReady,
		}, nil).
		Once()
	cairn.EXPECT().
		FetchWorkspace(t.Context(), "ws-two").
		Return(workspace.Workspace{
			Name: "ws-two", VolumeName: "vol-two", VolumeState: workspace.VolumeStateReady,
		}, nil).
		Once()

	first, err := resolveWorkspaceMount(t.Context(), cairn, "session-one", "ws-one", declared)
	require.NoError(t, err)
	second, err := resolveWorkspaceMount(t.Context(), cairn, "session-two", "ws-two", declared)
	require.NoError(t, err)

	// The session's own mounts survive, and the workspace mount is appended after them.
	require.Len(t, first.VolumeMounts, 2)
	require.Len(t, second.VolumeMounts, 2)
	assert.Equal("session-declared-vol", first.VolumeMounts[0].Name)
	assert.Equal("session-declared-vol", second.VolumeMounts[0].Name)

	// Neither resolve saw the other's mount.
	assert.Equal("vol-one", first.VolumeMounts[1].Name)
	assert.Equal("vol-two", second.VolumeMounts[1].Name)

	// The input params are untouched.
	require.Len(t, declared.VolumeMounts, 1)
	assert.Equal("session-declared-vol", declared.VolumeMounts[0].Name)
}

// TestResolveWorkspaceMountRefusals every way the resolve fails. None of them may fall back to
// running the container unmounted.
func TestResolveWorkspaceMountRefusals(t *testing.T) {
	t.Run("cairn not configured", func(t *testing.T) {
		// A nil client is the ordinary state of a deployment without the integration. The
		// session must refuse to start rather than start without its workspace.
		mounted, err := resolveWorkspaceMount(
			t.Context(), nil, "test-session", "my-ws", dockerParams(),
		)
		assert.Error(t, err)
		assert.Empty(t, mounted.VolumeMounts)
	})

	t.Run("lookup failed", func(t *testing.T) {
		cairn := mockworkspace.NewCairnClient(t)
		cairn.EXPECT().
			FetchWorkspace(t.Context(), "gone-ws").
			Return(workspace.Workspace{}, goutils.NewNotFoundError("no such workspace", nil, true)).
			Once()

		mounted, err := resolveWorkspaceMount(
			t.Context(), cairn, "test-session", "gone-ws", dockerParams(),
		)
		assert.Error(t, err)
		assert.Empty(t, mounted.VolumeMounts)
	})

	t.Run("volume not provisioned", func(t *testing.T) {
		cairn := mockworkspace.NewCairnClient(t)
		cairn.EXPECT().
			FetchWorkspace(t.Context(), "no-vol").
			Return(workspace.Workspace{
				Name:        "no-vol",
				VolumeName:  cairnVolumeName,
				VolumeState: workspace.VolumeStateNone,
			}, nil).
			Once()

		mounted, err := resolveWorkspaceMount(
			t.Context(), cairn, "test-session", "no-vol", dockerParams(),
		)
		require.Error(t, err)
		assert.Empty(t, mounted.VolumeMounts)
		// The state is named in the message: NONE means an operator has not provisioned a
		// volume, which is a different fix from the workspace not existing at all.
		assert.Contains(t, err.Error(), string(workspace.VolumeStateNone))
	})
}

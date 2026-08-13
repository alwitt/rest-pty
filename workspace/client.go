// Package workspace - client for cairn's workspace API
//
// rest-pty is a mount-only cairn client. It resolves the workspace a session was assigned, reads
// the persistent volume name cairn holds for it, and mounts that volume into the session
// container. It never touches cairn's object store, its credentials, or the volume's lifecycle -
// cairn owns all three (cairn's DESIGN §4.4).
package workspace

import (
	"context"
	"fmt"

	"github.com/alwitt/goutils"
	"github.com/alwitt/rest-pty/models"
	"github.com/apex/log"
	"github.com/go-resty/resty/v2"
)

// MountPath the path a workspace's persistent volume is mounted at inside a session container.
//
// Mirrors cairn's models.WorkspaceMountPath (its DESIGN §4.4). It is duplicated rather than
// imported on purpose: cairn's models package pulls gorm, its datatypes, and the rest of
// cairn's dependency set along with it, which is a large amount of module graph to take on
// for one string. cairn is the source of truth - if it ever makes the path configurable,
// this constant is the thing that has to follow.
//
// The value is fixed service-wide precisely so paths round-trip: the session container that
// writes a file and cairn's own transfer sidecars that read it back must see it at the same
// path.
const MountPath = "/mnt/cairn/ws"

// VolumeStateENUM a workspace's persistent volume state, mirroring cairn's
// WorkspaceVolumeStateENUM.
type VolumeStateENUM string

const (
	// VolumeStateNone no persistent volume has been provisioned for the workspace. Only an
	// operator can provision one; rest-pty can not.
	VolumeStateNone VolumeStateENUM = "NONE"
	// VolumeStateReady the persistent volume exists and can be mounted
	VolumeStateReady VolumeStateENUM = "READY"
)

// Workspace the subset of cairn's workspace record rest-pty consumes.
//
// Only the fields a mount needs are declared; encoding/json drops the rest of what cairn
// returns (description, volume metadata, timestamps), so cairn can grow its record without
// this decode caring.
type Workspace struct {
	// ID the workspace ID
	ID string `json:"id"`
	// Name the workspace name; what the session was assigned
	Name string `json:"name"`
	// VolumeName the persistent volume backing the workspace. Derived by cairn from the
	// immutable workspace ID and persisted, so it is stable across a rename - rest-pty reads
	// it and never re-derives it.
	VolumeName string `json:"volume_name"`
	// VolumeState whether the persistent volume exists and can be mounted
	VolumeState VolumeStateENUM `json:"volume_state"`
}

// IsVolumeReady whether the workspace's persistent volume can be mounted
func (w Workspace) IsVolumeReady() bool {
	return w.VolumeState == VolumeStateReady
}

// CairnClient client for cairn's workspace API
type CairnClient interface {
	/*
		FetchWorkspace fetch one workspace by name.

			@param ctxt context.Context - the operational context
			@param name string - the workspace name
			@returns the workspace, or a not-found error when cairn knows no workspace by
			    that name
	*/
	FetchWorkspace(ctxt context.Context, name string) (Workspace, error)
}

// workspaceListResponse cairn's GET /v1/workspaces response
type workspaceListResponse struct {
	// Workspaces the workspaces matching the filters
	Workspaces []Workspace `json:"workspaces,omitempty"`
}

// restCairnClient implement CairnClient against cairn's REST API
type restCairnClient struct {
	goutils.Component

	// client the underlying HTTP client, with cairn's base URL already installed
	client *resty.Client
}

/*
NewCairnClient define a new cairn API client

	@param parentCtxt context.Context - parent context, owned by the OAuth token manager when
	    one is configured
	@param cfg models.CairnConfig - cairn integration config
	@returns new client
*/
func NewCairnClient(
	parentCtxt context.Context, cfg models.CairnConfig,
) (CairnClient, error) {
	// Config validation already guarantees both are set whenever the integration is enabled,
	// but this constructor is the only place that dereferences them and should not depend on
	// a caller having validated first.
	if cfg.BaseURL == nil {
		return nil, goutils.NewValidationError(
			"cairn integration is missing its base URL", nil, true,
		)
	}
	if cfg.Client == nil {
		return nil, goutils.NewValidationError(
			"cairn integration is missing its HTTP client config", nil, true,
		)
	}

	logTags := log.Fields{"module": "workspace", "component": "cairn-client"}

	var authConfig *goutils.HTTPClientAuthConfig
	if cfg.Client.OAuth != nil {
		// goutils takes the audience as an optional pointer; ours is a required string.
		targetAudience := cfg.Client.OAuth.TargetAudience
		authConfig = &goutils.HTTPClientAuthConfig{
			IssuerURL:      cfg.Client.OAuth.IssuerURL,
			ClientID:       cfg.Client.OAuth.ClientID,
			ClientSecret:   cfg.Client.OAuth.ClientSecret,
			TargetAudience: &targetAudience,
			LogTags:        logTags,
		}
	}

	// Transport config is nil: no custom-CA knob is exposed, since rest-pty reaches cairn
	// through the same reverse proxy layer that owns TLS for everything else.
	httpClient, err := goutils.DefineHTTPClient(
		parentCtxt,
		goutils.HTTPClientRetryConfig{
			MaxAttempts:  cfg.Client.Retry.MaxAttempts,
			InitWaitTime: cfg.Client.Retry.InitWaitTime(),
			MaxWaitTime:  cfg.Client.Retry.MaxWaitTime(),
		},
		authConfig,
		nil,
	)
	if err != nil {
		return nil, goutils.NewRuntimeError("failed to define cairn HTTP client", err, true)
	}

	return &restCairnClient{
		Component: goutils.Component{
			LogTags: logTags,
			LogTagModifiers: []goutils.LogMetadataModifier{
				goutils.ModifyLogMetadataByRestRequestParam,
			},
		},
		client: httpClient.SetBaseURL(*cfg.BaseURL),
	}, nil
}

// workspacesEndpoint cairn's workspace collection endpoint, relative to the configured base URL
const workspacesEndpoint = "/v1/workspaces"

/*
FetchWorkspace fetch one workspace by name.

Uses cairn's name-filtered listing rather than its ID-addressed GET: rest-pty only ever holds a
name (that is what a session is assigned), and the per-workspace endpoint additionally queries
Docker for a mount count nothing here uses.

The listing is deliberately NOT filtered on volume state. Whether the volume is mountable is
the caller's check, so a workspace that exists without a volume reports as such instead of
being indistinguishable from one that does not exist.

	@param ctxt context.Context - the operational context
	@param name string - the workspace name
	@returns the workspace, or a not-found error when cairn knows no workspace by that name
*/
func (c *restCairnClient) FetchWorkspace(
	ctxt context.Context, name string,
) (Workspace, error) {
	logTags := c.GetLogTagsForContext(ctxt)

	var parsed workspaceListResponse
	resp, err := c.client.R().
		SetContext(ctxt).
		SetQueryParam("name", name).
		SetResult(&parsed).
		Get(workspacesEndpoint)
	if err != nil {
		return Workspace{}, goutils.NewRuntimeError(
			fmt.Sprintf("failed to query cairn for workspace '%s'", name), err, true,
		)
	}

	if !resp.IsSuccess() {
		return Workspace{}, goutils.NewHTTPRequestError(
			resp.StatusCode(),
			fmt.Sprintf("cairn rejected the lookup of workspace '%s'", name),
			nil,
			true,
		)
	}

	// cairn answers an unknown name with 200 and an empty list, so "not found" is a length
	// check rather than a status check.
	if len(parsed.Workspaces) == 0 {
		return Workspace{}, goutils.NewNotFoundError(
			fmt.Sprintf("no workspace named '%s'", name), nil, true,
		)
	}

	// Workspace names are unique in cairn, so an exact-name filter cannot legitimately match
	// more than one. Treat it as a broken invariant rather than picking one arbitrarily.
	if len(parsed.Workspaces) > 1 {
		return Workspace{}, goutils.NewConsistencyError(
			fmt.Sprintf(
				"cairn returned %d workspaces named '%s'; names are unique",
				len(parsed.Workspaces), name,
			),
			nil,
			true,
		)
	}

	found := parsed.Workspaces[0]
	log.
		WithFields(goutils.UpdateCodePositionInTags(logTags)).
		WithField("workspace", found.Name).
		WithField("volume", found.VolumeName).
		WithField("volume-state", string(found.VolumeState)).
		Debug("Resolved cairn workspace")

	return found, nil
}

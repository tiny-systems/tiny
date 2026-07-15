package adapters

import (
	"context"
	"net/http"

	platformapi "github.com/tiny-systems/platform-api"
)

// NewPlatformClient builds a platform-api ClientWithResponses with an
// optional dev key. When devKey is non-empty, every request carries an
// Authorization: Bearer header so the platform widens scope from
// public-only to workspace-private.
func NewPlatformClient(serverURL, devKey string) (*platformapi.ClientWithResponses, error) {
	var opts []platformapi.ClientOption
	if devKey != "" {
		opts = append(opts, platformapi.WithRequestEditorFn(
			func(_ context.Context, req *http.Request) error {
				req.Header.Set("Authorization", "Bearer "+devKey)
				return nil
			},
		))
	}
	return platformapi.NewClientWithResponses(serverURL, opts...)
}

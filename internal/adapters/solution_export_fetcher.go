package adapters

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// SolutionExportFetcher fetches the canonical export of a public solution
// from the platform's anonymous catalog API — the same artifact
// `tiny publish` uploads, so a clone is a faithful reconstruction.
type SolutionExportFetcher struct {
	baseURL string
	client  *http.Client
}

func NewSolutionExportFetcher(baseURL string) *SolutionExportFetcher {
	return &SolutionExportFetcher{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 60 * time.Second},
	}
}

func (f *SolutionExportFetcher) FetchSolutionExport(ctx context.Context, idOrSlug string) ([]byte, error) {
	u := fmt.Sprintf("%s/v1/solutions/%s/export", f.baseURL, url.PathEscape(idOrSlug))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("solution %q not found (private solutions require the platform)", idOrSlug)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("export fetch failed (%s)", resp.Status)
	}
	return body, nil
}

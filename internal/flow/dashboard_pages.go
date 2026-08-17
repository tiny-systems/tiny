package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/tiny-systems/module/api/v1alpha1"
	"github.com/tiny-systems/module/pkg/schema"
	platform "github.com/tiny-systems/platform-go"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Dashboard pages, the same model and the same UX as the hosted platform: a
// page is a TinyWidgetPage resource, a widget may belong to several pages at
// once (its placement is stored per page), and the editor drives all of it
// through three calls — GetConfiguration for the create form, then
// CreateDashboardPage / DeleteDashboardPage.
//
// tiny answered all three with "unimplemented", so locally the Widgets tab
// could arrange a layout (SaveWidgets works) but "Add tab" and "Delete tab"
// were dead buttons and every widget lived on one implicit page.

// createPageForm is the form the editor renders in the "New tab" modal. The
// shape is the platform's: the model fills exactly one field and presses the
// button, so the wire payload is {"title": "...", "submit": true}.
type createPageForm struct {
	Title  string `json:"title" title:"Title" colSpan:"col-span-12" required:"true"`
	Create bool   `json:"submit" format:"button" align:"right" title:"Create" required:"true"`
}

// GetConfiguration serves the project's forms. Only the create-page form is
// meaningful locally — there is no workspace, access map or billing to
// report — but the editor's create modal is server-driven: it renders
// whatever schema this returns, so an empty response left the modal blank.
func (p projectService) GetConfiguration(_ context.Context, req *platform.GetProjectConfigurationRequest) (*platform.GetProjectConfigurationResponse, error) {
	resp := &platform.GetProjectConfigurationResponse{
		Project: &platform.Project{
			ID:           req.ProjectName,
			Name:         req.ProjectName,
			ResourceName: req.ProjectName,
		},
	}
	schemaBytes, dataBytes, err := schema.CreateSchemaAndData(createPageForm{})
	if err != nil {
		// A missing form is not worth failing the whole configuration call:
		// everything else on this response still renders.
		return resp, nil
	}
	resp.CreateDashboardPageForm = &platform.Configuration{Schema: schemaBytes, Data: dataBytes}
	return resp, nil
}

// CreateDashboardPage adds an empty page. The title arrives inside the form's
// Data (the schema half is the editor's own copy and is ignored); the
// resource name is generated from it, so two pages may share a title and
// still be distinct resources — the platform allows that and the editor
// addresses pages by resource name.
func (p projectService) CreateDashboardPage(ctx context.Context, req *platform.CreateProjectDashboardPage) (*platform.Nil, error) {
	mgr, err := p.svc.manager()
	if err != nil {
		return nil, err
	}
	if req.CreatePageForm == nil || len(req.CreatePageForm.Data) == 0 {
		return nil, fmt.Errorf("create page form is empty")
	}
	var form createPageForm
	if err := json.Unmarshal(req.CreatePageForm.Data, &form); err != nil {
		return nil, fmt.Errorf("parse create page form: %w", err)
	}
	if form.Title == "" {
		// The platform lets this through and the apiserver rejects it with
		// "name or generateName is required", which reads as a cluster fault
		// rather than an empty field.
		return nil, fmt.Errorf("page title is required")
	}

	pages, err := mgr.GetProjectPageWidgets(ctx, req.ProjectName)
	if err != nil {
		return nil, fmt.Errorf("list pages: %w", err)
	}
	// Sort index is max+1, not len(pages): counting collides with an existing
	// page's index as soon as one in the middle has been deleted, and equal
	// indices sort unstably, so tabs swap places on reload.
	idx := 0
	for i := range pages {
		if n, convErr := strconv.Atoi(pages[i].Annotations[v1alpha1.PageSortIdxAnnotation]); convErr == nil && n >= idx {
			idx = n + 1
		}
	}
	if _, err := mgr.CreatePage(ctx, form.Title, req.ProjectName, p.svc.namespace, idx); err != nil {
		return nil, fmt.Errorf("create page: %w", err)
	}
	return &platform.Nil{}, nil
}

// DeleteDashboardPage removes a page by RESOURCE name (what the editor holds;
// titles are not unique). Widgets are not touched: a port that also sits on
// another page keeps that page's own placement, and the node keeps its
// dashboard label, so its widget reappears unplaced on the remaining page —
// the same behaviour as the platform.
func (p projectService) DeleteDashboardPage(ctx context.Context, req *platform.DeleteProjectDashboardPage) (*platform.Nil, error) {
	mgr, err := p.svc.manager()
	if err != nil {
		return nil, err
	}
	if req.PageName == "" {
		return nil, fmt.Errorf("page name is required")
	}

	// Confirm the page belongs to this project before deleting it. The
	// platform deletes by name alone, which in a shared namespace lets one
	// project delete another's page.
	pages, err := mgr.GetProjectPageWidgets(ctx, req.ProjectName)
	if err != nil {
		return nil, fmt.Errorf("list pages: %w", err)
	}
	found := false
	for i := range pages {
		if pages[i].Name == req.PageName {
			found = true
			break
		}
	}
	if !found {
		// Idempotent: deleting a page that is already gone is the state the
		// caller asked for, and the editor reloads either way.
		return &platform.Nil{}, nil
	}

	if err := mgr.DeletePage(ctx, &v1alpha1.TinyWidgetPage{
		ObjectMeta: metav1.ObjectMeta{Name: req.PageName, Namespace: p.svc.namespace},
	}); err != nil {
		return nil, fmt.Errorf("delete page: %w", err)
	}
	return &platform.Nil{}, nil
}

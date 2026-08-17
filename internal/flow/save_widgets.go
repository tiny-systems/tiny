package flow

import (
	"context"
	"fmt"

	"github.com/tiny-systems/module/api/v1alpha1"
	platform "github.com/tiny-systems/platform-go"
)

// SaveWidgets persists the dashboard layout the editor sends: which page each
// widget appears on, its title, and its grid placement.
//
// tiny answered this call with "unimplemented" until now, so arranging a
// dashboard locally did nothing — the drag reverted on the next stream tick
// and there was no way to put a widget on a second page. Pages are
// TinyWidgetPage resources, the same model the platform writes, so a project
// arranged locally and one arranged on the platform are the same thing.
//
// Semantics follow the platform: the addressed page is rebuilt from the
// request, a widget listing several pages is appended to each of them, and a
// page that already carries that port keeps its own entry (its placement on
// that page is its own business).
func (p projectService) SaveWidgets(ctx context.Context, req *platform.SaveWidgetsRequest) (*platform.Nil, error) {
	mgr, err := p.svc.manager()
	if err != nil {
		return nil, err
	}

	pageName := req.PageName
	if pageName == "" {
		pageName = dashboardPageName
	}

	pages, err := mgr.GetProjectPageWidgets(ctx, req.ProjectName)
	if err != nil {
		return nil, fmt.Errorf("read dashboard pages: %w", err)
	}
	refs := make([]*v1alpha1.TinyWidgetPage, 0, len(pages))
	for i := range pages {
		refs = append(refs, &pages[i])
	}

	// Every page the request mentions must exist before widgets can be
	// assigned to it — including pages a widget names but that were never
	// created, which is how a user moves a widget onto a new tab.
	wanted := map[string]bool{pageName: true}
	for _, w := range req.Widgets {
		for _, name := range w.Pages {
			if name != "" {
				wanted[name] = true
			}
		}
	}
	for name := range wanted {
		if findPageByName(refs, name) != nil {
			continue
		}
		created, cerr := mgr.CreatePage(ctx, name, req.ProjectName, p.svc.namespace, len(refs))
		if cerr != nil {
			return nil, fmt.Errorf("create page %q: %w", name, cerr)
		}
		fresh, ferr := mgr.GetProjectPageWidgets(ctx, req.ProjectName)
		if ferr != nil {
			return nil, ferr
		}
		refs = refs[:0]
		for i := range fresh {
			refs = append(refs, &fresh[i])
		}
		if findPageByName(refs, *created) == nil {
			return nil, fmt.Errorf("page %q missing after create", name)
		}
	}

	// The addressed page is authoritative for this save: rebuild it, so a
	// widget dragged off it actually leaves.
	if target := findPageByName(refs, pageName); target != nil {
		target.Spec.Widgets = make([]v1alpha1.TinyWidget, 0, len(req.Widgets))
	}

	for _, rw := range req.Widgets {
		on := rw.Pages
		if len(on) == 0 {
			on = []string{pageName}
		}
		for _, name := range on {
			page := findPageByName(refs, name)
			if page == nil {
				continue
			}
			port := portFromWidgetID(rw.ID)
			if hasWidgetPort(page, port) {
				continue // that page already places this widget its own way
			}
			w := v1alpha1.TinyWidget{Port: port, Name: rw.Title}
			if rw.Grid != nil {
				w.GridX, w.GridY = int(rw.Grid.X), int(rw.Grid.Y)
				w.GridW, w.GridH = int(rw.Grid.W), int(rw.Grid.H)
			}
			page.Spec.Widgets = append(page.Spec.Widgets, w)
		}
	}

	for _, page := range refs {
		if err := mgr.UpdatePage(ctx, page); err != nil {
			return nil, fmt.Errorf("save page %q: %w", page.Name, err)
		}
	}
	return &platform.Nil{}, nil
}

// findPageByName matches a page by its display title first, then by resource
// name — the editor addresses pages by what the user sees.
func findPageByName(pages []*v1alpha1.TinyWidgetPage, name string) *v1alpha1.TinyWidgetPage {
	for _, page := range pages {
		if title, ok := page.Annotations[v1alpha1.PageTitleAnnotation]; ok && title == name {
			return page
		}
		if page.Name == name {
			return page
		}
	}
	return nil
}

func hasWidgetPort(page *v1alpha1.TinyWidgetPage, port string) bool {
	for _, w := range page.Spec.Widgets {
		if w.Port == port {
			return true
		}
	}
	return false
}

// widgetPortsOnPage is used by tests and by the dashboard snapshot to reason
// about membership without re-reading the cluster.
func widgetPortsOnPage(page v1alpha1.TinyWidgetPage) []string {
	out := make([]string, 0, len(page.Spec.Widgets))
	for _, w := range page.Spec.Widgets {
		out = append(out, w.Port)
	}
	return out
}

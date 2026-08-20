package adapters

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tiny-systems/module/api/v1alpha1"
	"github.com/tiny-systems/module/pkg/resource"
	sdktools "github.com/tiny-systems/module/pkg/tools"
	"k8s.io/client-go/util/retry"
)

// Dashboard pages for an agent.
//
// The editor has had pages since the beginning — the tabs across the top of a
// project — and an agent building the same project could pin a widget but not
// say where it went. Everything landed on one implicit page in creation order:
// a credential form beside a live log beside the button that starts the run.
// The layout a person would have arranged by hand was the one thing the agent
// could not hand over.
//
// A page is a TinyWidgetPage, exactly what the editor writes, so a dashboard
// laid out by an agent and one laid out by dragging are the same resources.

// defaultWidgetW spans the grid, which the editor renders in 6 columns. A form
// is the common case and reads better full width than in a narrow column.
const (
	defaultWidgetW = 6
	defaultWidgetH = 6
	gridColumns    = 6
)

func (d *DashboardWriter) manager() (*resource.Manager, error) {
	return resource.NewManagerFromConfig(d.kube.RESTConfig, d.kube.Namespace)
}

// ListPages returns the project's pages in the order the editor shows them.
func (d *DashboardWriter) ListPages(ctx context.Context, projectName string) ([]sdktools.DashboardPageInfo, error) {
	pages, err := d.pages(ctx, projectName)
	if err != nil {
		return nil, err
	}
	out := make([]sdktools.DashboardPageInfo, 0, len(pages))
	for _, p := range pages {
		out = append(out, pageInfo(p))
	}
	return out, nil
}

// CreatePage adds an empty page at the end of the tab strip.
func (d *DashboardWriter) CreatePage(ctx context.Context, projectName, title string) (sdktools.DashboardPageInfo, error) {
	if strings.TrimSpace(title) == "" {
		return sdktools.DashboardPageInfo{}, fmt.Errorf("page title is required")
	}
	mgr, err := d.manager()
	if err != nil {
		return sdktools.DashboardPageInfo{}, err
	}
	existing, err := d.pages(ctx, projectName)
	if err != nil {
		return sdktools.DashboardPageInfo{}, err
	}
	name, err := mgr.CreatePage(ctx, title, projectName, d.kube.Namespace, len(existing))
	if err != nil {
		return sdktools.DashboardPageInfo{}, wrapCRDError(fmt.Errorf("create dashboard page %q: %w", title, err))
	}
	return sdktools.DashboardPageInfo{Name: *name, Title: title, SortIdx: len(existing)}, nil
}

// DeletePage removes a page. The nodes whose widgets sat on it are untouched:
// a placement is layout, and deleting a tab must not delete a running flow.
func (d *DashboardWriter) DeletePage(ctx context.Context, projectName, page string) error {
	found, err := d.findPage(ctx, projectName, page)
	if err != nil {
		return err
	}
	mgr, err := d.manager()
	if err != nil {
		return err
	}
	if err := mgr.DeletePage(ctx, found); err != nil {
		return wrapCRDError(fmt.Errorf("delete dashboard page %q: %w", page, err))
	}
	return nil
}

// PlaceWidget puts a widget somewhere on a page, moves it, or takes it off.
//
// Removal with no page named clears the widget from every page, which is what
// unpinning means: the widget is gone, not merely gone from one tab.
func (d *DashboardWriter) PlaceWidget(ctx context.Context, projectName string, p sdktools.WidgetPlacement) (sdktools.DashboardPageInfo, error) {
	if p.NodeID == "" {
		return sdktools.DashboardPageInfo{}, fmt.Errorf("node id is required")
	}
	port := p.Port
	if port == "" {
		port = v1alpha1.ControlPort
	}
	// Which ports can be a widget is this host's business — buildDashboard
	// renders a node's control form and nothing else. Storing a placement for
	// any other port would report a widget that can never appear, so refuse it
	// here, where the rendering rule actually lives, rather than in the SDK
	// where it would bind every host to tiny's choice.
	if !p.Remove && port != v1alpha1.ControlPort {
		return sdktools.DashboardPageInfo{}, fmt.Errorf(
			"port %q cannot be a widget: the dashboard renders a node's %s form only — expose what you want shown through that node's control schema",
			port, v1alpha1.ControlPort)
	}
	ref := p.NodeID + ":" + port

	if p.Remove && p.Page == "" {
		return sdktools.DashboardPageInfo{}, d.removeEverywhere(ctx, projectName, p.NodeID)
	}

	target, err := d.pageForPlacement(ctx, projectName, p.Page)
	if err != nil {
		return sdktools.DashboardPageInfo{}, err
	}

	mgr, err := d.manager()
	if err != nil {
		return sdktools.DashboardPageInfo{}, err
	}

	var result sdktools.DashboardPageInfo
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh, ferr := d.findPage(ctx, projectName, target)
		if ferr != nil {
			return ferr
		}
		widgets := make([]v1alpha1.TinyWidget, 0, len(fresh.Spec.Widgets)+1)
		for _, w := range fresh.Spec.Widgets {
			if w.Port == ref {
				continue // replaced below, or dropped when removing
			}
			widgets = append(widgets, w)
		}
		if !p.Remove {
			widgets = append(widgets, widgetFor(ref, p, widgets))
		}
		fresh.Spec.Widgets = widgets
		if uerr := mgr.UpdatePage(ctx, fresh); uerr != nil {
			return uerr
		}
		result = pageInfo(*fresh)
		return nil
	})
	if err != nil {
		return sdktools.DashboardPageInfo{}, wrapCRDError(fmt.Errorf("place widget %s: %w", ref, err))
	}
	return result, nil
}

// widgetFor builds the placement. A caller that named no row gets appended
// below whatever is already there — an agent adding one more widget should not
// have to solve a packing problem, and two widgets sharing a cell is a layout
// nobody asked for.
func widgetFor(ref string, p sdktools.WidgetPlacement, existing []v1alpha1.TinyWidget) v1alpha1.TinyWidget {
	w := p.W
	if w <= 0 || w > gridColumns {
		w = defaultWidgetW
	}
	h := p.H
	if h <= 0 {
		h = defaultWidgetH
	}
	x := p.X
	if x < 0 || x >= gridColumns {
		x = 0
	}
	if x+w > gridColumns {
		w = gridColumns - x
	}
	y := p.Y
	if p.AutoY || y < 0 {
		y = nextRow(existing)
	}
	return v1alpha1.TinyWidget{
		Port:  ref,
		Name:  p.Title,
		GridX: x,
		GridY: y,
		GridW: w,
		GridH: h,
	}
}

func nextRow(widgets []v1alpha1.TinyWidget) int {
	bottom := 0
	for _, w := range widgets {
		if end := w.GridY + w.GridH; end > bottom {
			bottom = end
		}
	}
	return bottom
}

// removeEverywhere clears every placement belonging to the node, on every page.
//
// Keyed on the node rather than one port reference: unpinning means the widget
// is gone, and a leftover row for a port the dashboard never renders is
// invisible junk that outlives the node it names.
func (d *DashboardWriter) removeEverywhere(ctx context.Context, projectName, nodeID string) error {
	pages, err := d.pages(ctx, projectName)
	if err != nil {
		return err
	}
	mgr, err := d.manager()
	if err != nil {
		return err
	}
	for i := range pages {
		page := pages[i]
		kept := widgetsWithout(page.Spec.Widgets, nodeID)
		if len(kept) == len(page.Spec.Widgets) {
			continue
		}
		page.Spec.Widgets = kept
		if err := mgr.UpdatePage(ctx, &page); err != nil {
			return wrapCRDError(fmt.Errorf("remove widgets of %s from page %s: %w", nodeID, page.Name, err))
		}
	}
	return nil
}

// widgetsWithout drops every placement belonging to the node, whichever port it
// names.
func widgetsWithout(widgets []v1alpha1.TinyWidget, nodeID string) []v1alpha1.TinyWidget {
	prefix := nodeID + ":"
	kept := make([]v1alpha1.TinyWidget, 0, len(widgets))
	for _, w := range widgets {
		if !strings.HasPrefix(w.Port, prefix) {
			kept = append(kept, w)
		}
	}
	return kept
}

// pageForPlacement resolves the page a widget should land on, creating the
// project's first page when it has none. A pin that fails because nobody has
// opened the dashboard yet would be a strange way to learn about pages.
func (d *DashboardWriter) pageForPlacement(ctx context.Context, projectName, requested string) (string, error) {
	if requested != "" {
		page, err := d.findPage(ctx, projectName, requested)
		if err != nil {
			return "", err
		}
		return page.Name, nil
	}
	pages, err := d.pages(ctx, projectName)
	if err != nil {
		return "", err
	}
	if len(pages) > 0 {
		return pages[0].Name, nil
	}
	created, err := d.CreatePage(ctx, projectName, "Dashboard")
	if err != nil {
		return "", err
	}
	return created.Name, nil
}

// findPage accepts either the resource name or the title, because an agent
// reading a page's title off the dashboard should be able to address it.
func (d *DashboardWriter) findPage(ctx context.Context, projectName, ref string) (*v1alpha1.TinyWidgetPage, error) {
	pages, err := d.pages(ctx, projectName)
	if err != nil {
		return nil, err
	}
	for i := range pages {
		if pages[i].Name == ref {
			return &pages[i], nil
		}
	}
	for i := range pages {
		if pages[i].Annotations[v1alpha1.PageTitleAnnotation] == ref {
			return &pages[i], nil
		}
	}
	titles := make([]string, 0, len(pages))
	for _, p := range pages {
		titles = append(titles, fmt.Sprintf("%s (%s)", p.Annotations[v1alpha1.PageTitleAnnotation], p.Name))
	}
	if len(titles) == 0 {
		return nil, fmt.Errorf("no dashboard page %q — this project has no pages yet", ref)
	}
	return nil, fmt.Errorf("no dashboard page %q — the project has: %s", ref, strings.Join(titles, ", "))
}

// pages lists the project's pages in display order.
func (d *DashboardWriter) pages(ctx context.Context, projectName string) ([]v1alpha1.TinyWidgetPage, error) {
	mgr, err := d.manager()
	if err != nil {
		return nil, err
	}
	list, err := mgr.GetProjectPageWidgets(ctx, projectName)
	if err != nil {
		return nil, wrapCRDError(fmt.Errorf("list dashboard pages: %w", err))
	}
	sort.SliceStable(list, func(i, j int) bool {
		si, sj := sortIdx(list[i]), sortIdx(list[j])
		if si != sj {
			return si < sj
		}
		return list[i].Name < list[j].Name
	})
	return list, nil
}

func sortIdx(p v1alpha1.TinyWidgetPage) int {
	n, err := strconv.Atoi(p.Annotations[v1alpha1.PageSortIdxAnnotation])
	if err != nil {
		return 0
	}
	return n
}

func pageInfo(p v1alpha1.TinyWidgetPage) sdktools.DashboardPageInfo {
	info := sdktools.DashboardPageInfo{
		Name:    p.Name,
		Title:   p.Annotations[v1alpha1.PageTitleAnnotation],
		SortIdx: sortIdx(p),
		Widgets: make([]sdktools.PlacedWidget, 0, len(p.Spec.Widgets)),
	}
	if info.Title == "" {
		info.Title = p.Name
	}
	for _, w := range p.Spec.Widgets {
		nodeID, port := splitWidgetRef(w.Port)
		info.Widgets = append(info.Widgets, sdktools.PlacedWidget{
			NodeID: nodeID,
			Port:   port,
			Title:  w.Name,
			X:      w.GridX,
			Y:      w.GridY,
			W:      w.GridW,
			H:      w.GridH,
		})
	}
	return info
}

// splitWidgetRef splits "<nodeID>:<port>". A node id carries dots but no colon,
// so the last colon is the separator.
func splitWidgetRef(ref string) (nodeID, port string) {
	i := strings.LastIndex(ref, ":")
	if i < 0 {
		return ref, ""
	}
	return ref[:i], ref[i+1:]
}

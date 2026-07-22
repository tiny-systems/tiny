package adapters

import (
	"context"
	"fmt"
	"strings"

	"github.com/tiny-systems/module/api/v1alpha1"
	sdktools "github.com/tiny-systems/module/pkg/tools"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tiny-systems/tiny/internal/kube"
)

// defaultPageName is the page a widget lands on when the project has none yet.
// One page is all the local runtime needs; the editor can add more.
const defaultPageName = "default"

// widget grid geometry. The dashboard is a 12-column grid; control forms are
// readable at half width, and new widgets stack down the left rather than
// overlapping whatever is already placed.
const (
	widgetCols   = 12
	widgetWidth  = 6
	widgetHeight = 4
)

// DashboardWriter implements sdktools.DashboardWriter by editing TinyWidgetPage
// CRDs — the same objects DashboardReader reads and the editor renders.
type DashboardWriter struct {
	kube *kube.Client
}

func NewDashboardWriter(k *kube.Client) *DashboardWriter {
	return &DashboardWriter{kube: k}
}

// SetNodeWidget pins nodeID's port as a widget, or removes it.
//
// Pinning is idempotent: an already-pinned port keeps its position instead of
// being duplicated or moved, so re-running a build doesn't shuffle a dashboard
// the user has arranged.
func (d *DashboardWriter) SetNodeWidget(ctx context.Context, projectName, nodeID, portName string, enabled bool) (string, error) {
	if projectName == "" {
		return "", fmt.Errorf("project is required")
	}
	if nodeID == "" || portName == "" {
		return "", fmt.Errorf("node id and port are required")
	}
	ref := nodeID + ":" + portName

	page, err := d.pageFor(ctx, projectName, ref, enabled)
	if err != nil {
		return "", err
	}
	if page == "" {
		return "", nil // unpinning something that was never pinned
	}

	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		p := &v1alpha1.TinyWidgetPage{}
		if err := d.kube.Client.Get(ctx, client.ObjectKey{Namespace: d.kube.Namespace, Name: page}, p); err != nil {
			if !k8serrors.IsNotFound(err) {
				return err
			}
			if !enabled {
				return nil
			}
			return d.createPage(ctx, page, projectName, ref)
		}

		idx := indexOfWidget(p.Spec.Widgets, ref)
		switch {
		case enabled && idx >= 0:
			return nil // already pinned; leave its position alone
		case enabled:
			p.Spec.Widgets = append(p.Spec.Widgets, newWidget(ref, len(p.Spec.Widgets)))
		case idx < 0:
			return nil // not pinned; nothing to remove
		default:
			p.Spec.Widgets = append(p.Spec.Widgets[:idx], p.Spec.Widgets[idx+1:]...)
		}
		return d.kube.Client.Update(ctx, p)
	})
	if err != nil {
		return "", wrapCRDError(fmt.Errorf("update widget page %s: %w", page, err))
	}
	return page, nil
}

// pageFor picks the page to edit: the one already holding this widget, else the
// project's first page, else the default (created on demand when pinning).
// Returns "" when unpinning and no page holds the widget.
func (d *DashboardWriter) pageFor(ctx context.Context, projectName, ref string, enabled bool) (string, error) {
	pages := &v1alpha1.TinyWidgetPageList{}
	if err := d.kube.Client.List(ctx, pages,
		client.InNamespace(d.kube.Namespace),
		client.MatchingLabels{v1alpha1.ProjectNameLabel: projectName},
	); err != nil {
		return "", wrapCRDError(fmt.Errorf("list widget pages: %w", err))
	}

	for i := range pages.Items {
		if indexOfWidget(pages.Items[i].Spec.Widgets, ref) >= 0 {
			return pages.Items[i].Name, nil // edit where it already lives
		}
	}
	if !enabled {
		return "", nil
	}
	if len(pages.Items) > 0 {
		return pages.Items[0].Name, nil
	}
	return pageNameFor(projectName), nil
}

func (d *DashboardWriter) createPage(ctx context.Context, name, projectName, ref string) error {
	return d.kube.Client.Create(ctx, &v1alpha1.TinyWidgetPage{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: d.kube.Namespace,
			Labels:    map[string]string{v1alpha1.ProjectNameLabel: projectName},
		},
		Spec: v1alpha1.TinyWidgetPageSpec{Widgets: []v1alpha1.TinyWidget{newWidget(ref, 0)}},
	})
}

// newWidget places the nth widget in a 12-column grid, two per row.
func newWidget(ref string, n int) v1alpha1.TinyWidget {
	perRow := widgetCols / widgetWidth
	return v1alpha1.TinyWidget{
		Port:  ref,
		GridX: (n % perRow) * widgetWidth,
		GridY: (n / perRow) * widgetHeight,
		GridW: widgetWidth,
		GridH: widgetHeight,
	}
}

func indexOfWidget(widgets []v1alpha1.TinyWidget, ref string) int {
	for i, w := range widgets {
		if w.Port == ref {
			return i
		}
	}
	return -1
}

// pageNameFor derives a DNS-safe page name from the project.
func pageNameFor(projectName string) string {
	name := strings.ToLower(projectName)
	name = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '-'
	}, name)
	name = strings.Trim(name, "-")
	if name == "" {
		return defaultPageName
	}
	return name + "-" + defaultPageName
}

var _ sdktools.DashboardWriter = (*DashboardWriter)(nil)

package adapters

import (
	"testing"

	"github.com/tiny-systems/module/api/v1alpha1"
	sdktools "github.com/tiny-systems/module/pkg/tools"
)

// Appending is the default because an agent adding one more widget should not
// have to solve a packing problem, and two widgets in the same cell is a layout
// nobody asked for.
func TestWidgetAppendsBelowWhatIsAlreadyThere(t *testing.T) {
	existing := []v1alpha1.TinyWidget{
		{Port: "a:_control", GridY: 0, GridH: 4},
		{Port: "b:_control", GridY: 4, GridH: 6},
	}
	got := widgetFor("c:_control", sdktools.WidgetPlacement{AutoY: true}, existing)
	if got.GridY != 10 {
		t.Fatalf("y = %d, want 10 — below the bottom of the last widget", got.GridY)
	}
	if got.GridW != defaultWidgetW || got.GridH != defaultWidgetH {
		t.Errorf("size = %dx%d, want the defaults", got.GridW, got.GridH)
	}
}

func TestExplicitPlacementIsHonoured(t *testing.T) {
	got := widgetFor("a:_control", sdktools.WidgetPlacement{X: 2, Y: 3, W: 3, H: 2, Title: "Start"}, nil)
	if got.GridX != 2 || got.GridY != 3 || got.GridW != 3 || got.GridH != 2 {
		t.Fatalf("placement = %+v, want 2,3 3x2", got)
	}
	if got.Name != "Start" {
		t.Errorf("title = %q — without one the user reads a node id", got.Name)
	}
}

// A widget wider than the grid would be clipped by the editor, and a column off
// the end silently snaps back — better to fit it than to store a lie.
func TestPlacementIsKeptInsideTheGrid(t *testing.T) {
	got := widgetFor("a:_control", sdktools.WidgetPlacement{X: 4, W: 6}, nil)
	if got.GridX+got.GridW > gridColumns {
		t.Fatalf("widget spans past the grid: x=%d w=%d", got.GridX, got.GridW)
	}
	if off := widgetFor("a:_control", sdktools.WidgetPlacement{X: 9}, nil); off.GridX != 0 {
		t.Errorf("x = %d for an out-of-range column, want 0", off.GridX)
	}
	if zero := widgetFor("a:_control", sdktools.WidgetPlacement{W: 99}, nil); zero.GridW != defaultWidgetW {
		t.Errorf("w = %d for a nonsense width, want the default", zero.GridW)
	}
}

// The stored reference is "<nodeID>:<port>" and a node id carries dots — and,
// in a flow id, no colon. Splitting on the first colon would truncate it.
func TestWidgetRefSplitsOnTheLastColon(t *testing.T) {
	node, port := splitWidgetRef("abb01e65.tinysystems-common-module-v0.ask-40b9f:_control")
	if node != "abb01e65.tinysystems-common-module-v0.ask-40b9f" || port != "_control" {
		t.Fatalf("split = %q / %q", node, port)
	}
	if n, p := splitWidgetRef("bare"); n != "bare" || p != "" {
		t.Errorf("a reference with no port = %q / %q", n, p)
	}
}

func TestPageInfoReadsTitleAndOrder(t *testing.T) {
	page := v1alpha1.TinyWidgetPage{}
	page.Name = "ops5jt5d"
	page.Annotations = map[string]string{
		v1alpha1.PageTitleAnnotation:   "Ops",
		v1alpha1.PageSortIdxAnnotation: "2",
	}
	page.Spec.Widgets = []v1alpha1.TinyWidget{{Port: "n1:_control", Name: "Chat", GridW: 6, GridH: 6}}

	got := pageInfo(page)
	if got.Title != "Ops" || got.SortIdx != 2 {
		t.Fatalf("page = %+v", got)
	}
	if len(got.Widgets) != 1 || got.Widgets[0].NodeID != "n1" || got.Widgets[0].Title != "Chat" {
		t.Fatalf("widgets = %+v", got.Widgets)
	}
}

// A page written by hand, or by an older version, may carry no title
// annotation. Reporting an empty label would make it unaddressable.
func TestPageWithoutATitleFallsBackToItsName(t *testing.T) {
	page := v1alpha1.TinyWidgetPage{}
	page.Name = "page-default"
	if got := pageInfo(page); got.Title != "page-default" {
		t.Fatalf("title = %q, want the resource name as a fallback", got.Title)
	}
}

// Unpinning is keyed on the node, not one port reference. A row naming a port
// the dashboard never renders is invisible junk that would outlive the node.
func TestRemovalMatchesEveryPortOfTheNode(t *testing.T) {
	widgets := []v1alpha1.TinyWidget{
		{Port: "flow.mod.signal-1:_control"},
		{Port: "flow.mod.signal-1:_settings"},
		{Port: "flow.mod.other-2:_control"},
	}
	kept := widgetsWithout(widgets, "flow.mod.signal-1")
	if len(kept) != 1 || kept[0].Port != "flow.mod.other-2:_control" {
		t.Fatalf("survivors = %+v, want only the other node's widget", kept)
	}
}

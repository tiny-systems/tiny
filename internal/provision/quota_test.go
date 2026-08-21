package provision

import "testing"

// A quota that constrained cpu or memory would force every pod in the namespace
// to declare requests and limits — and the next module install would fail
// instead of the runaway loop this exists to stop. Object counts carry no such
// requirement, so the rule is: never anything but counts.
func TestTheQuotaOnlyEverCountsObjects(t *testing.T) {
	for _, limits := range []QuotaLimits{DefaultQuota, {Jobs: 1}, {PersistentVolumeClaims: 1}} {
		for name := range hardFor(limits) {
			if name != "count/jobs.batch" && name != "count/persistentvolumeclaims" {
				t.Fatalf("quota constrains %q — anything but an object count breaks pod admission", name)
			}
		}
	}
}

// Zero is a deliberate choice ("do not bound this"), and asking for nothing at
// all must remove any ceiling rather than leave a stale one looking like
// protection.
func TestAskingForNothingIsEmpty(t *testing.T) {
	if !(QuotaLimits{}).Empty() {
		t.Fatal("a zero limit set did not read as empty")
	}
	if (QuotaLimits{Jobs: 1}).Empty() {
		t.Fatal("a real limit read as empty")
	}
}

func TestTheDefaultBoundsBothThingsComponentsCreate(t *testing.T) {
	if DefaultQuota.Jobs <= 0 || DefaultQuota.PersistentVolumeClaims <= 0 {
		t.Fatalf("default = %+v — both are things a flow can create in a loop", DefaultQuota)
	}
}

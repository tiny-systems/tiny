package provision

import (
	"context"
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Settings are cluster-wide install values — properties of YOUR cluster, not
// of any one module: which ingress controller, the base domain, the storage
// class. They're persisted as annotations on the tinysystems namespace, so
// they travel with the cluster and every module install reads them. Set them
// once (flags on `tiny up`/`tiny install`); modules that need them pick them up.
type Settings struct {
	IngressClass string
	DomainSuffix string
	StorageClass string
	StorageSize  string // default 1Gi when a module needs storage
	// Issuer is the cert-manager (Cluster)Issuer name to annotate ingresses
	// with for TLS; ClusterIssuer selects cluster-issuer vs namespace issuer.
	Issuer        string
	ClusterIssuer bool
}

const (
	annIngressClass  = "tinysystems.io/ingress-class"
	annDomainSuffix  = "tinysystems.io/domain-suffix"
	annStorageClass  = "tinysystems.io/storage-class"
	annStorageSize   = "tinysystems.io/storage-size"
	annIssuer        = "tinysystems.io/issuer"
	annClusterIssuer = "tinysystems.io/issuer-cluster-scoped"
)

func (s Settings) storageSizeOr() string {
	if s.StorageSize == "" {
		return "1Gi"
	}
	return s.StorageSize
}

// Empty reports whether no setting is present.
func (s Settings) Empty() bool { return s == Settings{} }

// Merge overlays o's non-empty fields onto s (o wins). Used to layer
// this-invocation flags over the cluster's saved settings.
func (s Settings) Merge(o Settings) Settings {
	if o.IngressClass != "" {
		s.IngressClass = o.IngressClass
	}
	if o.DomainSuffix != "" {
		s.DomainSuffix = o.DomainSuffix
	}
	if o.StorageClass != "" {
		s.StorageClass = o.StorageClass
	}
	if o.StorageSize != "" {
		s.StorageSize = o.StorageSize
	}
	if o.Issuer != "" {
		s.Issuer = o.Issuer
		s.ClusterIssuer = o.ClusterIssuer
	}
	return s
}

// LoadSettings reads the saved cluster settings off the namespace annotations.
func LoadSettings(ctx context.Context, cfg *rest.Config, namespace string) (Settings, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return Settings{}, err
	}
	ns, err := cs.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil {
		return Settings{}, err
	}
	a := ns.Annotations
	return Settings{
		IngressClass:  a[annIngressClass],
		DomainSuffix:  a[annDomainSuffix],
		StorageClass:  a[annStorageClass],
		StorageSize:   a[annStorageSize],
		Issuer:        a[annIssuer],
		ClusterIssuer: a[annClusterIssuer] == "true",
	}, nil
}

// SaveSettings persists the non-empty settings as namespace annotations
// (a merge patch — it never clears an existing annotation).
func SaveSettings(ctx context.Context, cfg *rest.Config, namespace string, s Settings) error {
	ann := map[string]string{}
	if s.IngressClass != "" {
		ann[annIngressClass] = s.IngressClass
	}
	if s.DomainSuffix != "" {
		ann[annDomainSuffix] = s.DomainSuffix
	}
	if s.StorageClass != "" {
		ann[annStorageClass] = s.StorageClass
	}
	if s.StorageSize != "" {
		ann[annStorageSize] = s.StorageSize
	}
	if s.Issuer != "" {
		ann[annIssuer] = s.Issuer
		if s.ClusterIssuer {
			ann[annClusterIssuer] = "true"
		}
	}
	if len(ann) == 0 {
		return nil
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	patch, _ := json.Marshal(map[string]interface{}{
		"metadata": map[string]interface{}{"annotations": ann},
	})
	_, err = cs.CoreV1().Namespaces().Patch(ctx, namespace, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

package kube

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// ForwardPodPort opens an SPDY port-forward from 127.0.0.1:localPort on this
// machine to podPort inside the named pod — the same mechanism as `kubectl
// port-forward pod/…`. It blocks until the tunnel is ready, then returns a stop
// func and a done channel. Reaches loopback-bound ports in the pod too (the
// forward tunnels into the pod's network namespace).
//
// done closes when the forward exits — either because stop was called or,
// crucially for remote clusters, because the SPDY stream dropped (idle timeout,
// API-server rollout, network blip). Callers watch done to know a forward went
// dead and needs re-establishing. stop is safe to call more than once.
func ForwardPodPort(ctx context.Context, cfg *rest.Config, namespace, podName string, localPort, podPort int) (stop func(), done <-chan struct{}, err error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("clientset: %w", err)
	}

	reqURL := cs.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(podName).
		SubResource("portforward").URL()

	transport, upgrader, err := spdy.RoundTripperFor(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("round tripper: %w", err)
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", reqURL)

	stopChan := make(chan struct{})
	readyChan := make(chan struct{})
	errChan := make(chan error, 1)

	fw, err := portforward.New(dialer, []string{fmt.Sprintf("%d:%d", localPort, podPort)}, stopChan, readyChan, nil, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("port-forwarder: %w", err)
	}

	var once sync.Once
	stopFn := func() { once.Do(func() { close(stopChan) }) }

	// exited closes when ForwardPorts returns — on stop OR on a dropped stream.
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		if err := fw.ForwardPorts(); err != nil {
			select {
			case errChan <- err:
			default:
			}
		}
	}()

	select {
	case <-readyChan:
		return stopFn, exited, nil
	case err := <-errChan:
		return nil, nil, fmt.Errorf("forward: %w", err)
	case <-ctx.Done():
		stopFn()
		return nil, nil, ctx.Err()
	}
}

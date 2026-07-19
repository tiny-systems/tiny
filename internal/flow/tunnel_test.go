package flow

import (
	"reflect"
	"sort"
	"testing"
)

// TestLocalListenPorts covers the parse that turns a _control port config into
// the local ports the tunnel forwards. The first case is the exact JSON an
// http_server node reports.
func TestLocalListenPorts(t *testing.T) {
	tests := []struct {
		name string
		cfg  string
		want []int
	}{
		{
			name: "http_server _control config",
			cfg:  `{"status":"Running","listenAddr":["http://localhost:43157"]}`,
			want: []int{43157},
		},
		{
			name: "multiple listen addrs",
			cfg:  `{"listenAddr":["http://127.0.0.1:8080","https://0.0.0.0:8443"]}`,
			want: []int{8080, 8443},
		},
		{
			name: "bare string listenAddr",
			cfg:  `{"listenAddr":"http://localhost:9000"}`,
			want: []int{9000},
		},
		{
			name: "non-loopback host is skipped",
			cfg:  `{"listenAddr":["http://example.com:80"]}`,
			want: nil,
		},
		{
			name: "no listenAddr field",
			cfg:  `{"status":"Running"}`,
			want: nil,
		},
		{
			name: "empty config",
			cfg:  ``,
			want: nil,
		},
		{
			name: "not json",
			cfg:  `nope`,
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := localListenPorts([]byte(tt.cfg))
			sort.Ints(got)
			sort.Ints(tt.want)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("localListenPorts(%q) = %v, want %v", tt.cfg, got, tt.want)
			}
		})
	}
}

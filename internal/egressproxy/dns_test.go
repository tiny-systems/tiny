package egressproxy

import (
	"bufio"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// A query for one name, id chosen by the caller.
func mkQuery(id uint16, name string) []byte {
	q := make([]byte, dnsHeaderLen)
	binary.BigEndian.PutUint16(q[0:], id)
	q[2] = 0x01 // RD
	binary.BigEndian.PutUint16(q[4:], 1)
	for _, label := range splitLabels(name) {
		q = append(q, byte(len(label)))
		q = append(q, label...)
	}
	q = append(q, 0, 0, 1, 0, 1) // root, A, IN
	return q
}

func splitLabels(name string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(name); i++ {
		if i == len(name) || name[i] == '.' {
			if i > start {
				out = append(out, name[start:i])
			}
			start = i + 1
		}
	}
	return out
}

// fakeUpstream answers every query with the AA bit set so a forwarded
// reply is distinguishable from one the resolver made up itself.
func fakeUpstream(t *testing.T) (addr string, hits *atomic.Int32) {
	t.Helper()
	hits = &atomic.Int32{}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close(); _ = l.Close() })
	go func() {
		buf := make([]byte, 4096)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			hits.Add(1)
			out := append([]byte(nil), buf[:n]...)
			out[2] |= 0x84 // QR + AA
			_, _ = pc.WriteTo(out, from)
		}
	}()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				q, err := readFramed(bufio.NewReader(c))
				if err != nil {
					return
				}
				hits.Add(1)
				q[2] |= 0x84
				_ = writeFramed(c, q)
			}()
		}
	}()
	return pc.LocalAddr().String(), hits
}

func TestClusterNamesForwardEverythingElseIsNXDomain(t *testing.T) {
	up, hits := fakeUpstream(t)
	r := NewResolver(up, []string{"team-a.svc.cluster.local", "svc.cluster.local", "cluster.local"})

	got := r.Answer("udp", mkQuery(7, "tiny-minio.team-a.svc.cluster.local"))
	if got[2]&0x04 == 0 {
		t.Fatal("in-cluster name was not forwarded to the upstream resolver")
	}
	if hits.Load() != 1 {
		t.Fatalf("upstream hit %d times, want 1", hits.Load())
	}

	got = r.Answer("udp", mkQuery(9, "exfil.attacker.example"))
	if binary.BigEndian.Uint16(got[0:]) != 9 {
		t.Fatal("refusal did not keep the query id — a stub resolver would ignore it")
	}
	if got[2]&0x80 == 0 || got[3]&0x0F != rcodeNXDomain {
		t.Fatalf("external name got flags %02x %02x, want a QR=1 NXDOMAIN", got[2], got[3])
	}
	if binary.BigEndian.Uint16(got[6:]) != 0 {
		t.Fatal("refusal carries answers")
	}
	if hits.Load() != 1 {
		t.Fatal("external name reached the upstream resolver — that is the tunnel")
	}
	// A name that merely CONTAINS the zone is not under it.
	r.Answer("udp", mkQuery(10, "cluster.local.attacker.example"))
	if hits.Load() != 1 {
		t.Fatal("zone matched as a substring, not a suffix")
	}
}

func TestMalformedQueriesGetFormErrNotAPanic(t *testing.T) {
	r := NewResolver("127.0.0.1:1", []string{"cluster.local"})
	if out := r.Answer("udp", []byte{1, 2, 3}); out != nil {
		t.Fatal("a fragment shorter than a header should be dropped")
	}
	bad := mkQuery(3, "a.cluster.local")
	bad = bad[:len(bad)-3] // truncated inside qtype/qclass
	out := r.Answer("udp", bad)
	if out == nil || out[3]&0x0F != rcodeFormErr {
		t.Fatal("truncated question should answer FORMERR")
	}
	ptr := append(mkQuery(4, "")[:dnsHeaderLen], 0xC0, 0x0C, 0, 1, 0, 1)
	if out := r.Answer("udp", ptr); out == nil || out[3]&0x0F != rcodeFormErr {
		t.Fatal("compression pointer in the question should be refused, not followed")
	}
}

func TestUpstreamDownIsServFailNotSilence(t *testing.T) {
	r := NewResolver("127.0.0.1:1", []string{"cluster.local"})
	r.timeout = 200 * time.Millisecond
	out := r.Answer("udp", mkQuery(5, "kube-dns.kube-system.svc.cluster.local"))
	if out == nil || out[3]&0x0F != rcodeServFail {
		t.Fatal("unreachable upstream should be SERVFAIL")
	}
}

func TestTCPIsServedWithLengthFraming(t *testing.T) {
	up, _ := fakeUpstream(t)
	r := NewResolver(up, []string{"cluster.local"})
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	go func() { _ = r.ServeTCP(l) }()

	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if err := writeFramed(c, mkQuery(11, "svc.cluster.local")); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	got, err := readFramed(br)
	if err != nil {
		t.Fatal(err)
	}
	if got[2]&0x04 == 0 {
		t.Fatal("tcp query was not forwarded")
	}
	if err := writeFramed(c, mkQuery(12, "evil.example")); err != nil {
		t.Fatal(err)
	}
	if got, err = readFramed(br); err != nil || got[3]&0x0F != rcodeNXDomain {
		t.Fatalf("second query on the same connection: %v, rcode %d", err, got[3]&0x0F)
	}
}

func TestUDPServesConcurrently(t *testing.T) {
	up, _ := fakeUpstream(t)
	r := NewResolver(up, []string{"cluster.local"})
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pc.Close() }()
	go func() { _ = r.ServeUDP(pc) }()
	c, err := net.Dial("udp", pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write(mkQuery(21, "nope.example")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 512)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint16(buf[:n]) != 21 || buf[3]&0x0F != rcodeNXDomain {
		t.Fatal("udp refusal malformed")
	}
}

func TestResolvConfGivesUpstreamAndZones(t *testing.T) {
	p := filepath.Join(t.TempDir(), "resolv.conf")
	body := "search team-a.svc.cluster.local svc.cluster.local cluster.local\nnameserver 10.43.0.10\nnameserver 10.43.0.11\noptions ndots:5\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ns, search, err := ResolvConf(p)
	if err != nil {
		t.Fatal(err)
	}
	if ns != "10.43.0.10:53" {
		t.Fatalf("nameserver %q", ns)
	}
	if len(search) != 3 || search[2] != "cluster.local" {
		t.Fatalf("search %v", search)
	}
	if _, _, err := ResolvConf(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing file should error, not silently resolve nothing")
	}
}

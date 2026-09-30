package egressproxy

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"time"
)

// Resolver is the only nameserver a session pod is given once the proxy
// is on. It forwards names inside the cluster to the real resolver and
// answers everything else NXDOMAIN, so a query can never carry data to a
// nameserver an attacker runs — not directly, and not via the cluster
// resolver's upstream forwarding either, which is the hole a
// NetworkPolicy "DNS to kube-system only" rule leaves open.
//
// External names never need resolving from inside the session: a CONNECT
// proxy resolves the host itself, on the far side of the policy.
type Resolver struct {
	upstream string
	zones    []string
	timeout  time.Duration
}

// NewResolver forwards to upstream ("10.43.0.10:53") the names that fall
// under any of zones, which are the pod's own search domains — typically
// "<ns>.svc.cluster.local svc.cluster.local cluster.local".
func NewResolver(upstream string, zones []string) *Resolver {
	r := &Resolver{upstream: upstream, timeout: 4 * time.Second}
	for _, z := range zones {
		z = strings.ToLower(strings.Trim(strings.TrimSpace(z), "."))
		if z != "" {
			r.zones = append(r.zones, z)
		}
	}
	return r
}

const (
	rcodeFormErr  = 1
	rcodeServFail = 2
	rcodeNXDomain = 3
	dnsHeaderLen  = 12
	dnsMaxMessage = 65535
)

// inCluster is the whole policy: the zone itself or anything beneath it.
func (r *Resolver) inCluster(name string) bool {
	for _, z := range r.zones {
		if name == z || strings.HasSuffix(name, "."+z) {
			return true
		}
	}
	return false
}

// Answer produces the reply to one query. network is "udp" or "tcp" and
// decides how the upstream is spoken to when the name is forwarded.
func (r *Resolver) Answer(network string, query []byte) []byte {
	if len(query) < dnsHeaderLen {
		return nil
	}
	name, end, ok := question(query)
	if !ok {
		return reply(query, dnsHeaderLen, rcodeFormErr, true)
	}
	if !r.inCluster(name) {
		log.Printf("egress DENIED DNS %s", name)
		return reply(query, end, rcodeNXDomain, false)
	}
	out, err := r.forward(network, query)
	if err != nil {
		log.Printf("dns upstream %s: %v", r.upstream, err)
		return reply(query, end, rcodeServFail, false)
	}
	return out
}

// question reads the single question a query carries: its name,
// lowercased and without the trailing dot, and where the question ends.
func question(msg []byte) (name string, end int, ok bool) {
	if binary.BigEndian.Uint16(msg[4:6]) != 1 {
		return "", 0, false
	}
	i := dnsHeaderLen
	var labels []string
	for {
		if i >= len(msg) {
			return "", 0, false
		}
		l := int(msg[i])
		if l == 0 {
			i++
			break
		}
		if l&0xC0 != 0 { // a compression pointer has no business in a question
			return "", 0, false
		}
		i++
		if i+l > len(msg) {
			return "", 0, false
		}
		labels = append(labels, string(msg[i:i+l]))
		i += l
	}
	if i+4 > len(msg) {
		return "", 0, false
	}
	return strings.ToLower(strings.Join(labels, ".")), i + 4, true
}

// reply turns the query's own header and question into a response with
// no answers and the given rcode. Keeping the ID and question is what
// makes a stub resolver accept it as the answer to what it asked.
func reply(query []byte, end int, rcode byte, dropQuestion bool) []byte {
	out := make([]byte, end)
	copy(out, query[:end])
	out[2] = 0x80 | (out[2] & 0x79) // QR=1, keep opcode and RD, clear AA/TC
	out[3] = 0x80 | rcode           // RA=1, no Z bits
	if dropQuestion {
		binary.BigEndian.PutUint16(out[4:], 0)
	}
	binary.BigEndian.PutUint16(out[6:], 0)
	binary.BigEndian.PutUint16(out[8:], 0)
	binary.BigEndian.PutUint16(out[10:], 0)
	return out
}

func (r *Resolver) forward(network string, query []byte) ([]byte, error) {
	conn, err := net.DialTimeout(network, r.upstream, r.timeout)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(r.timeout))
	if network == "tcp" {
		if err := writeFramed(conn, query); err != nil {
			return nil, err
		}
		return readFramed(bufio.NewReader(conn))
	}
	if _, err := conn.Write(query); err != nil {
		return nil, err
	}
	buf := make([]byte, dnsMaxMessage)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// ServeUDP answers queries on pc until it is closed.
func (r *Resolver) ServeUDP(pc net.PacketConn) error {
	buf := make([]byte, dnsMaxMessage)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		q := make([]byte, n)
		copy(q, buf[:n])
		go func() {
			if out := r.Answer("udp", q); out != nil {
				_, _ = pc.WriteTo(out, from)
			}
		}()
	}
}

// ServeTCP answers length-framed queries on l until it is closed. TCP is
// what a stub resolver falls back to after a truncated UDP answer, so
// refusing it would be a way around nothing — but leaving it unserved
// would make those lookups hang instead of fail.
func (r *Resolver) ServeTCP(l net.Listener) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go r.serveTCPConn(conn)
	}
}

func (r *Resolver) serveTCPConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	br := bufio.NewReader(conn)
	for {
		_ = conn.SetDeadline(time.Now().Add(r.timeout))
		q, err := readFramed(br)
		if err != nil {
			return
		}
		out := r.Answer("tcp", q)
		if out == nil || writeFramed(conn, out) != nil {
			return
		}
	}
}

func writeFramed(w io.Writer, msg []byte) error {
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(len(msg)))
	if _, err := w.Write(l[:]); err != nil {
		return err
	}
	_, err := w.Write(msg)
	return err
}

func readFramed(r io.Reader) ([]byte, error) {
	var l [2]byte
	if _, err := io.ReadFull(r, l[:]); err != nil {
		return nil, err
	}
	msg := make([]byte, binary.BigEndian.Uint16(l[:]))
	_, err := io.ReadFull(r, msg)
	return msg, err
}

// ResolvConf reads the proxy pod's own resolver settings — the cluster
// nameserver and the search domains — which is exactly the set of names
// the sessions should still be able to look up.
func ResolvConf(path string) (nameserver string, search []string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "nameserver":
			if nameserver == "" {
				nameserver = net.JoinHostPort(fields[1], "53")
			}
		case "search":
			search = append(search, fields[1:]...)
		}
	}
	if nameserver == "" {
		return "", nil, errors.New("no nameserver in " + path)
	}
	return nameserver, search, sc.Err()
}

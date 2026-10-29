package dnsresolver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

type recordResponseError struct{ code dnsmessage.RCode }

func (e *recordResponseError) Error() string { return "DNS response: " + e.code.String() }

// Address lookups retain Go's host/NSS behavior. Record queries need the wire
// response to retain TXT character strings, CNAME targets, TTLs and NODATA.
func lookupRecords(ctx context.Context, profile Profile, hostname string, kind dnsmessage.Type) ([]dohAnswer, error) {
	timeout := 5 * time.Second
	if profile.Timeout != "" {
		timeout, _ = time.ParseDuration(profile.Timeout)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	servers := profile.Servers
	if len(servers) == 0 {
		var err error
		servers, err = systemRecordServers()
		if err != nil {
			return nil, err
		}
	}
	if kind == dnsmessage.TypePTR {
		if ip, err := netip.ParseAddr(hostname); err == nil {
			hostname = reverseName(ip)
		}
	}
	name, err := dnsmessage.NewName(strings.TrimSuffix(hostname, ".") + ".")
	if err != nil {
		return nil, err
	}
	type result struct {
		answers []dohAnswer
		err     error
	}
	results := make(chan result, len(servers))
	for _, server := range servers {
		go func() { answers, err := queryRecords(ctx, server, name, kind); results <- result{answers, err} }()
	}
	var lastErr error
	for range servers {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case value := <-results:
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if value.err == nil {
				return value.answers, nil
			} else {
				lastErr = value.err
			}
		}
	}
	return nil, lastErr
}

// Read for each request so resolver changes take effect without a restart.
// Do not substitute public servers when the host has no resolver configuration.
func systemRecordServers() ([]string, error) {
	data, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return nil, fmt.Errorf("read system DNS servers (configure explicit profile servers for record queries on this host): %w", err)
	}
	return parseRecordServers(string(data))
}
func parseRecordServers(config string) ([]string, error) {
	var servers []string
	for _, line := range strings.Split(config, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "nameserver" {
			if _, err := netip.ParseAddr(fields[1]); err != nil {
				return nil, fmt.Errorf("invalid system DNS server %q", fields[1])
			}
			servers = append(servers, net.JoinHostPort(fields[1], "53"))
		}
	}
	if len(servers) == 0 {
		return nil, errors.New("no system DNS servers; configure explicit profile servers for record queries")
	}
	return servers, nil
}

func reverseName(ip netip.Addr) string {
	if ip.Is4() {
		a := ip.As4()
		return fmt.Sprintf("%d.%d.%d.%d.in-addr.arpa.", a[3], a[2], a[1], a[0])
	}
	a := ip.As16()
	const hex = "0123456789abcdef"
	var name strings.Builder
	for i := len(a) - 1; i >= 0; i-- {
		name.WriteByte(hex[a[i]&15])
		name.WriteByte('.')
		name.WriteByte(hex[a[i]>>4])
		name.WriteByte('.')
	}
	return name.String() + "ip6.arpa."
}

func queryRecords(ctx context.Context, server string, name dnsmessage.Name, kind dnsmessage.Type) ([]dohAnswer, error) {
	seen := map[string]bool{}
	// Follow aliases for record types other than CNAME, including chains supplied
	// in the same answer. Never return unrelated additional records to workerd.
	for range 16 {
		key := strings.ToLower(name.String())
		if seen[key] {
			return nil, errors.New("DNS CNAME loop")
		}
		if len(seen) >= 16 {
			return nil, errors.New("DNS CNAME chain exceeds limit")
		}
		seen[key] = true
		message, err := exchangeRecords(ctx, server, dnsmessage.Question{Name: name, Type: kind, Class: dnsmessage.ClassINET})
		if err != nil {
			return nil, err
		}
		for range 16 {
			var answers []dohAnswer
			var target *dnsmessage.Name
			for _, resource := range message.Answers {
				if resource.Header.Class != dnsmessage.ClassINET || !strings.EqualFold(resource.Header.Name.String(), name.String()) {
					continue
				}
				if resource.Header.Type == kind {
					data, err := recordData(resource.Body)
					if err != nil {
						return nil, err
					}
					answers = append(answers, dohAnswer{Name: resource.Header.Name.String(), Type: int(kind), TTL: int(resource.Header.TTL), Data: data})
				} else if cname, ok := resource.Body.(*dnsmessage.CNAMEResource); ok {
					value := cname.CNAME
					target = &value
				}
			}
			if len(answers) > 0 {
				return answers, nil
			}
			if target == nil {
				return nil, nil
			}
			name = *target
			key = strings.ToLower(name.String())
			if seen[key] {
				return nil, errors.New("DNS CNAME loop")
			}
			// If this packet contains the target, inspect it before another exchange.
			inPacket := false
			for _, resource := range message.Answers {
				if strings.EqualFold(resource.Header.Name.String(), name.String()) {
					inPacket = true
					break
				}
			}
			if !inPacket {
				break
			}
			if len(seen) >= 16 {
				return nil, errors.New("DNS CNAME chain exceeds limit")
			}
			seen[key] = true
		}
	}
	return nil, errors.New("DNS CNAME chain exceeds limit")
}

func recordData(body dnsmessage.ResourceBody) (string, error) {
	switch record := body.(type) {
	case *dnsmessage.TXTResource:
		// Pinned workerd splits adjacent quoted strings and does not unescape DNS
		// presentation escapes. JSON encoding supplies the only escape layer.
		var data strings.Builder
		for _, text := range record.TXT {
			// Its parser cannot distinguish a pair of literal quotes from a boundary.
			if strings.Contains(text, `""`) || strings.HasSuffix(text, `"`) {
				// An unquoted single string bypasses workerd's chunk parser.
				if len(record.TXT) == 1 && !(strings.HasPrefix(text, `"`) && strings.HasSuffix(text, `"`)) {
					return text, nil
				}
				return "", &recordResponseError{dnsmessage.RCodeNotImplemented}
			}
			data.WriteByte('"')
			data.WriteString(text)
			data.WriteByte('"')
		}
		return data.String(), nil
	case *dnsmessage.CNAMEResource:
		return record.CNAME.String(), nil
	case *dnsmessage.PTRResource:
		return record.PTR.String(), nil
	case *dnsmessage.MXResource:
		return fmt.Sprintf("%d %s", record.Pref, record.MX.String()), nil
	case *dnsmessage.NSResource:
		return record.NS.String(), nil
	case *dnsmessage.SRVResource:
		target := record.Target.String()
		if target != "." {
			target = strings.TrimSuffix(target, ".")
		}
		return fmt.Sprintf("%d %d %d %s", record.Priority, record.Weight, record.Port, target), nil
	}
	return "", errors.New("unsupported DNS answer body")
}

func exchangeRecords(ctx context.Context, server string, question dnsmessage.Question) (*dnsmessage.Message, error) {
	var id [2]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	query := dnsmessage.Message{Header: dnsmessage.Header{ID: binary.BigEndian.Uint16(id[:]), RecursionDesired: true}, Questions: []dnsmessage.Question{question}}
	wire, err := query.Pack()
	if err != nil {
		return nil, err
	}
	for _, network := range []string{"udp", "tcp"} {
		response, err := exchangeRecordPacket(ctx, network, server, wire)
		if err != nil {
			return nil, err
		}
		var message dnsmessage.Message
		// A truncated UDP response may end in the middle of a resource. Inspect its
		// header first; full parsing must wait for the TCP retry.
		var parser dnsmessage.Parser
		header, err := parser.Start(response)
		if err != nil {
			return nil, err
		}
		if !header.Response || header.ID != query.ID || header.OpCode != 0 {
			return nil, errors.New("invalid DNS response header")
		}
		questions, err := parser.AllQuestions()
		if err != nil || len(questions) != 1 || questions[0].Type != question.Type || questions[0].Class != question.Class || !strings.EqualFold(questions[0].Name.String(), question.Name.String()) {
			return nil, errors.New("DNS response question mismatch")
		}
		if header.Truncated {
			if network == "udp" {
				continue
			}
			return nil, errors.New("truncated TCP DNS response")
		}
		if header.RCode != dnsmessage.RCodeSuccess {
			return nil, &recordResponseError{header.RCode}
		}
		if err := message.Unpack(response); err != nil {
			return nil, err
		}
		return &message, nil
	}
	return nil, errors.New("no complete DNS response")
}

func exchangeRecordPacket(ctx context.Context, network, server string, wire []byte) ([]byte, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, server)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}
	if network == "tcp" {
		packet := make([]byte, len(wire)+2)
		binary.BigEndian.PutUint16(packet, uint16(len(wire)))
		copy(packet[2:], wire)
		if _, err := io.Copy(conn, bytes.NewReader(packet)); err != nil {
			return nil, err
		}
		var length [2]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return nil, err
		}
		response := make([]byte, int(binary.BigEndian.Uint16(length[:])))
		_, err = io.ReadFull(conn, response)
		return response, err
	}
	if _, err := conn.Write(wire); err != nil {
		return nil, err
	}
	response := make([]byte, 65535)
	n, err := conn.Read(response)
	return response[:n], err
}

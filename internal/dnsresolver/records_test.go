package dnsresolver

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// The same endpoint accepts UDP and TCP so tests exercise real DNS framing.
func startWireDNSServer(t *testing.T, respond func(*dnsmessage.Message, bool) *dnsmessage.Message) string {
	t.Helper()
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tcp, err := net.Listen("tcp", udp.LocalAddr().String())
	if err != nil {
		udp.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { udp.Close(); tcp.Close() })
	reply := func(wire []byte, stream bool) []byte {
		var query dnsmessage.Message
		if query.Unpack(wire) != nil {
			return nil
		}
		message := respond(&query, stream)
		message.ID = query.ID
		message.Response = true
		if message.Questions == nil {
			message.Questions = query.Questions
		}
		result, _ := message.Pack()
		if message.Truncated && !stream && len(message.Answers) > 0 {
			// Simulate a UDP packet cut off inside the final resource body.
			result = result[:len(result)-3]
		}
		return result
	}
	go func() {
		buffer := make([]byte, 65535)
		for {
			n, client, err := udp.ReadFrom(buffer)
			if err != nil {
				return
			}
			udp.WriteTo(reply(buffer[:n], false), client)
		}
	}()
	go func() {
		for {
			conn, err := tcp.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(time.Second))
				var length [2]byte
				if _, err := io.ReadFull(conn, length[:]); err != nil {
					return
				}
				data := make([]byte, int(binary.BigEndian.Uint16(length[:])))
				if _, err := io.ReadFull(conn, data); err != nil {
					return
				}
				response := reply(data, true)
				binary.BigEndian.PutUint16(length[:], uint16(len(response)))
				conn.Write(append(length[:], response...))
			}()
		}
	}()
	return udp.LocalAddr().String()
}

func wireResource(name string, kind dnsmessage.Type, body dnsmessage.ResourceBody) dnsmessage.Resource {
	return dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(name), Type: kind, Class: dnsmessage.ClassINET, TTL: 123}, Body: body}
}

func TestRawRecordFidelityAndNegativeResponses(t *testing.T) {
	var ptrQueried atomic.Bool
	serverAddr := startWireDNSServer(t, func(query *dnsmessage.Message, _ bool) *dnsmessage.Message {
		q := query.Questions[0]
		response := &dnsmessage.Message{}
		switch q.Name.String() {
		case "text.invalid.":
			response.Answers = []dnsmessage.Resource{
				wireResource("unrelated.invalid.", dnsmessage.TypeTXT, &dnsmessage.TXTResource{TXT: []string{"ignore"}}),
				wireResource(q.Name.String(), dnsmessage.TypeTXT, &dnsmessage.TXTResource{TXT: []string{"a", "b", `a"b\c`, strings.Repeat("x", 255)}}),
			}
		case "alias.invalid.":
			response.Answers = []dnsmessage.Resource{
				wireResource(q.Name.String(), dnsmessage.TypeCNAME, &dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName("next.invalid.")}),
				wireResource("next.invalid.", dnsmessage.TypeCNAME, &dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName("last.invalid.")}),
				wireResource("last.invalid.", dnsmessage.TypeTXT, &dnsmessage.TXTResource{TXT: []string{"target"}}),
			}
		case "external-alias.invalid.":
			response.Answers = []dnsmessage.Resource{wireResource(q.Name.String(), dnsmessage.TypeCNAME, &dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName("text.invalid.")})}
		case "_http._tcp.example.invalid.":
			ptrQueried.Store(true)
			response.Answers = []dnsmessage.Resource{wireResource(q.Name.String(), dnsmessage.TypePTR, &dnsmessage.PTRResource{PTR: dnsmessage.MustNewName("instance.invalid.")})}
		case "missing.invalid.":
			response.RCode = dnsmessage.RCodeNameError
		case "loop.invalid.":
			response.Answers = []dnsmessage.Resource{wireResource(q.Name.String(), dnsmessage.TypeCNAME, &dnsmessage.CNAMEResource{CNAME: q.Name})}
		}
		return response
	})
	server, err := New(Config{DefaultProfile: "test", Profiles: map[string]Profile{"test": {Servers: []string{serverAddr}, Timeout: "1s"}}}, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, kind, data string
		status           int
	}{
		{"text.invalid", "TXT", `"a""b""a"b\c""` + strings.Repeat("x", 255) + `"`, 0},
		{"alias.invalid", "CNAME", "next.invalid.", 0},
		{"alias.invalid", "TXT", `"target"`, 0},
		{"external-alias.invalid", "TXT", `"a""b""a"b\c""` + strings.Repeat("x", 255) + `"`, 0},
		{"_http._tcp.example.invalid", "PTR", "instance.invalid.", 0},
		{"noalias.invalid", "CNAME", "", 0},
		{"noalias.invalid", "TXT", "", 0},
		{"missing.invalid", "CNAME", "", 3},
		{"loop.invalid", "TXT", "", 2},
	} {
		t.Run(tc.name+tc.kind, func(t *testing.T) {
			r := httptest.NewRequest("GET", DoHPath+"?"+url.Values{"name": {tc.name}, "type": {tc.kind}}.Encode(), nil)
			w := httptest.NewRecorder()
			server.ServeHTTP(w, r)
			var response dohResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Status != tc.status {
				t.Fatalf("response=%s", w.Body)
			}
			if tc.data == "" {
				if len(response.Answer) != 0 {
					t.Fatalf("unexpected answers: %s", w.Body)
				}
				return
			}
			if len(response.Answer) != 1 || response.Answer[0].Data != tc.data || response.Answer[0].TTL != 123 {
				t.Fatalf("response=%s", w.Body)
			}
		})
	}
	if !ptrQueried.Load() {
		t.Fatal("PTR name never reached DNS server")
	}
}

func TestRawRecordsTCPFallbackAndResponseValidation(t *testing.T) {
	for _, mode := range []string{"truncated", "id-mismatch", "question-mismatch", "opcode-mismatch", "rcode", "tcp-truncated"} {
		t.Run(mode, func(t *testing.T) {
			var tcpQueries atomic.Int32
			server := startWireDNSServer(t, func(query *dnsmessage.Message, stream bool) *dnsmessage.Message {
				if stream {
					tcpQueries.Add(1)
				}
				q := query.Questions[0]
				message := &dnsmessage.Message{Answers: []dnsmessage.Resource{wireResource(q.Name.String(), dnsmessage.TypeTXT, &dnsmessage.TXTResource{TXT: []string{strings.Repeat("a", 255), strings.Repeat("b", 255), strings.Repeat("c", 255)}})}}
				switch mode {
				case "truncated", "tcp-truncated":
					message.Truncated = !stream || mode == "tcp-truncated"
				case "id-mismatch":
					query.ID ^= 1
				case "question-mismatch":
					message.Questions = []dnsmessage.Question{{Name: dnsmessage.MustNewName("forged.invalid."), Type: q.Type, Class: q.Class}}
				case "rcode":
					message.RCode = dnsmessage.RCodeRefused
				case "opcode-mismatch":
					message.OpCode = 1
				}
				return message
			})
			answers, err := lookupRecords(context.Background(), Profile{Servers: []string{server}, Timeout: "1s"}, "test.invalid", dnsmessage.TypeTXT)
			if mode == "truncated" {
				if err != nil || len(answers) != 1 || tcpQueries.Load() != 1 {
					t.Fatalf("answers=%v err=%v TCP=%d", answers, err, tcpQueries.Load())
				}
			} else if err == nil {
				t.Fatal("invalid response accepted")
			}
			if mode == "rcode" {
				var responseErr *recordResponseError
				if !errors.As(err, &responseErr) || responseErr.code != dnsmessage.RCodeRefused {
					t.Fatalf("rcode lost: %v", err)
				}
			}
		})
	}
}

func TestRawRecordCancellationAndNODATA(t *testing.T) {
	blackhole := startBlackholeDNSServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(20*time.Millisecond, cancel)
	defer timer.Stop()
	defer cancel()
	start := time.Now()
	_, err := lookupRecords(ctx, Profile{Servers: []string{blackhole}, Timeout: "5s"}, "test.invalid", dnsmessage.TypeTXT)
	if !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Fatalf("cancellation: %v elapsed=%v", err, time.Since(start))
	}
	// An authoritative empty success must not become SERVFAIL because another
	// configured server is unavailable.
	server := startWireDNSServer(t, func(*dnsmessage.Message, bool) *dnsmessage.Message { return &dnsmessage.Message{} })
	answers, err := lookupRecords(context.Background(), Profile{Servers: []string{blackhole, server}, Timeout: "100ms"}, "test.invalid", dnsmessage.TypeTXT)
	if err != nil || len(answers) != 0 {
		t.Fatalf("NODATA=%v %v", answers, err)
	}
}

func TestSystemRecordServerConfiguration(t *testing.T) {
	servers, err := parseRecordServers("# example\nsearch private.invalid\nnameserver 127.0.0.53\nnameserver fd00::53 # IPv6\n")
	if err != nil || !reflect.DeepEqual(servers, []string{"127.0.0.53:53", "[fd00::53]:53"}) {
		t.Fatalf("servers=%v err=%v", servers, err)
	}
	for _, value := range []string{"", "nameserver public.example"} {
		if _, err := parseRecordServers(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}

func TestTXTParserRepresentability(t *testing.T) {
	for _, tc := range []struct {
		chunks      []string
		data        string
		unsupported bool
	}{
		{[]string{""}, `""`, false},
		{[]string{`a"b\c`}, `"a"b\c"`, false},
		{[]string{`a""b`}, `a""b`, false},
		{[]string{`a"`}, `a"`, false},
		{[]string{"a", "b"}, `"a""b"`, false},
		{[]string{`a""b`, "c"}, "", true},
		{[]string{`a"`, "b"}, "", true},
		{[]string{`"a"`}, "", true},
	} {
		t.Run(strings.Join(tc.chunks, "|"), func(t *testing.T) {
			data, err := recordData(&dnsmessage.TXTResource{TXT: tc.chunks})
			if tc.unsupported {
				var responseErr *recordResponseError
				if !errors.As(err, &responseErr) || responseErr.code != dnsmessage.RCodeNotImplemented {
					t.Fatalf("data=%q err=%v", data, err)
				}
			} else if err != nil || data != tc.data {
				t.Fatalf("data=%q err=%v want=%q", data, err, tc.data)
			}
		})
	}
}

func TestRawRecordCancellationDuringTCPRetry(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	server := startWireDNSServer(t, func(_ *dnsmessage.Message, stream bool) *dnsmessage.Message {
		if !stream {
			return &dnsmessage.Message{Header: dnsmessage.Header{Truncated: true}}
		}
		close(started)
		<-release
		return &dnsmessage.Message{}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := lookupRecords(ctx, Profile{Servers: []string{server}, Timeout: "5s"}, "test.invalid", dnsmessage.TypeTXT)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("TCP retry did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt TCP read")
	}
}

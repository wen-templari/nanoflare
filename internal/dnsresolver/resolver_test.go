package dnsresolver

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestSystemLookupReturnsAllAddressesAndFamilyFilter(t *testing.T) {
	server, err := New(Config{}, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer server.Close(context.Background())

	response, err := http.Post("http://"+server.Addr()+"/lookup", "application/json", strings.NewReader(`{"hostname":"localhost","family":4}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body lookupResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || len(body.Addresses) == 0 {
		t.Fatalf("status=%d body=%#v", response.StatusCode, body)
	}
	for _, address := range body.Addresses {
		if address.Family != 4 {
			t.Fatalf("address=%#v, want IPv4", address)
		}
	}
}

func TestUnknownProfileAndTimeoutReturnUsefulCodes(t *testing.T) {
	blackhole := startBlackholeDNSServer(t)
	server, err := New(Config{Profiles: map[string]Profile{"slow": {Servers: []string{blackhole}, Timeout: "10ms"}}}, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer server.Close(context.Background())

	for _, test := range []struct{ profile, code string }{{"missing", "EBADPROFILE"}, {"slow", "ETIMEOUT"}} {
		req, _ := http.NewRequest(http.MethodPost, "http://"+server.Addr()+"/lookup", strings.NewReader(`{"hostname":"example.invalid"}`))
		req.Header.Set("X-Nanoflare-DNS-Profile", test.profile)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var body lookupResponse
		_ = json.NewDecoder(response.Body).Decode(&body)
		response.Body.Close()
		if body.Code != test.code {
			t.Fatalf("profile %s: code=%q error=%q", test.profile, body.Code, body.Error)
		}
	}
}

func TestCustomResolverReturnsMultipleAddresses(t *testing.T) {
	dnsAddr := startTestDNSServer(t)
	blackhole := startBlackholeDNSServer(t)
	server, err := New(Config{DefaultProfile: "corporate", Profiles: map[string]Profile{"corporate": {Servers: []string{blackhole, dnsAddr}, Timeout: "1s"}}}, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer server.Close(context.Background())

	response, err := http.Post("http://"+server.Addr()+"/lookup", "application/json", strings.NewReader(`{"hostname":"rac.internal","family":4}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body lookupResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || len(body.Addresses) != 2 {
		t.Fatalf("status=%d body=%#v", response.StatusCode, body)
	}
	if body.Addresses[0].Address != "10.20.30.40" || body.Addresses[1].Address != "10.20.30.41" {
		t.Fatalf("addresses=%#v", body.Addresses)
	}
}

func startTestDNSServer(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		buffer := make([]byte, 1500)
		for {
			n, client, err := conn.ReadFrom(buffer)
			if err != nil {
				return
			}
			var parser dnsmessage.Parser
			header, err := parser.Start(buffer[:n])
			if err != nil {
				continue
			}
			question, err := parser.Question()
			if err != nil {
				continue
			}
			builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: header.ID, Response: true, RecursionAvailable: true})
			builder.EnableCompression()
			_ = builder.StartQuestions()
			_ = builder.Question(question)
			_ = builder.StartAnswers()
			if question.Type == dnsmessage.TypeA {
				for _, address := range [][4]byte{{10, 20, 30, 40}, {10, 20, 30, 41}} {
					_ = builder.AResource(dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 30}, dnsmessage.AResource{A: address})
				}
			}
			response, err := builder.Finish()
			if err == nil {
				_, _ = conn.WriteTo(response, client)
			}
		}
	}()
	return conn.LocalAddr().String()
}

func TestLoadConfigValidation(t *testing.T) {
	config, err := LoadConfig(`{"defaultProfile":"corporate","profiles":{"corporate":{"servers":["10.0.0.53:53"],"timeout":"3s"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if config.DefaultProfile != "corporate" || config.Profiles["system"].Resolver != "system" {
		t.Fatalf("config=%#v", config)
	}
	if _, err := LoadConfig(`{"profiles":{"bad":{"servers":["10.0.0.53"]}}}`); err == nil {
		t.Fatal("expected invalid server error")
	}
}

func TestLookupDeadline(t *testing.T) {
	profile := Profile{Servers: []string{startBlackholeDNSServer(t)}, Timeout: "10ms"}
	started := time.Now()
	_, _ = lookup(context.Background(), profile, "example.invalid", 0)
	if time.Since(started) > time.Second {
		t.Fatal("lookup did not respect timeout")
	}
}

func startBlackholeDNSServer(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		buffer := make([]byte, 1500)
		for {
			if _, _, err := conn.ReadFrom(buffer); err != nil {
				return
			}
		}
	}()
	return conn.LocalAddr().String()
}

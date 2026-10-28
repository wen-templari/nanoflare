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
	addresses, err := lookup(context.Background(), Profile{}, "localhost", 4)
	if err != nil || len(addresses) == 0 {
		t.Fatalf("addresses=%v err=%v", addresses, err)
	}
	for _, address := range addresses {
		if address.Family != 4 {
			t.Fatalf("unexpected family: %+v", address)
		}
	}
}

func TestDoHUnknownProfileAndTimeout(t *testing.T) {
	server, err := New(Config{Profiles: map[string]Profile{"slow": {Servers: []string{startBlackholeDNSServer(t)}, Timeout: "10ms"}}}, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer server.Close(context.Background())
	for _, profile := range []string{"missing", "slow"} {
		req, _ := http.NewRequest("GET", "http://"+server.Addr()+DoHPath+"?name=example.invalid&type=TXT", nil)
		req.Header.Set("X-Nanoflare-DNS-Profile", profile)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var body dohResponse
		err = json.NewDecoder(response.Body).Decode(&body)
		response.Body.Close()
		if err != nil || body.Status != 2 {
			t.Fatalf("profile %s: %+v %v", profile, body, err)
		}
	}
	response, err := http.Post("http://"+server.Addr()+"/lookup", "application/json", strings.NewReader(`{"hostname":"localhost"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("obsolete lookup endpoint: %d", response.StatusCode)
	}
}

func TestCustomResolverReturnsMultipleAddresses(t *testing.T) {
	addresses, err := lookup(context.Background(), Profile{Servers: []string{startBlackholeDNSServer(t), startTestDNSServer(t)}, Timeout: "1s"}, "rac.internal", 4)
	if err != nil || len(addresses) != 2 {
		t.Fatalf("addresses=%v err=%v", addresses, err)
	}
	if addresses[0].Address != "10.20.30.40" || addresses[1].Address != "10.20.30.41" {
		t.Fatalf("addresses=%v", addresses)
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

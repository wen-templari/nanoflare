package dnsresolver

import (
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func startRecordDNSServer(t *testing.T, text string) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	go func() {
		buffer := make([]byte, 4096)
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
			builder.StartQuestions()
			builder.Question(question)
			builder.StartAnswers()
			resource := dnsmessage.ResourceHeader{Name: question.Name, Class: dnsmessage.ClassINET, TTL: 30}
			switch question.Type {
			case dnsmessage.TypeTXT:
				builder.TXTResource(resource, dnsmessage.TXTResource{TXT: []string{text}})
			case dnsmessage.TypeMX:
				builder.MXResource(resource, dnsmessage.MXResource{Pref: 10, MX: dnsmessage.MustNewName("mail.corporate.invalid.")})
			case dnsmessage.TypeNS:
				builder.NSResource(resource, dnsmessage.NSResource{NS: dnsmessage.MustNewName("ns.corporate.invalid.")})
			case dnsmessage.TypeSRV:
				builder.SRVResource(resource, dnsmessage.SRVResource{Priority: 1, Weight: 5, Port: 5432, Target: dnsmessage.MustNewName("database.corporate.invalid.")})
			case dnsmessage.TypePTR:
				builder.PTRResource(resource, dnsmessage.PTRResource{PTR: dnsmessage.MustNewName("database.corporate.invalid.")})
			case dnsmessage.TypeA, dnsmessage.TypeAAAA, dnsmessage.TypeCNAME:
				target := dnsmessage.MustNewName("database.corporate.invalid.")
				if question.Name.String() == "alias.corporate.invalid." {
					builder.CNAMEResource(resource, dnsmessage.CNAMEResource{CNAME: target})
					resource.Name = target
				}
				if question.Type != dnsmessage.TypeAAAA {
					builder.AResource(resource, dnsmessage.AResource{A: [4]byte{10, 20, 30, 40}})
				}
			}
			response, err := builder.Finish()
			if err == nil {
				conn.WriteTo(response, client)
			}
		}
	}()
	return conn.LocalAddr().String()
}

func TestDoHRecordsUseConfiguredProfile(t *testing.T) {
	server, err := New(Config{DefaultProfile: "private", Profiles: map[string]Profile{
		"private": {Servers: []string{startBlackholeDNSServer(t), startRecordDNSServer(t, "private-text")}, Timeout: "1s"},
		"other":   {Servers: []string{startRecordDNSServer(t, "other-text")}, Timeout: "1s"},
		"slow":    {Servers: []string{startBlackholeDNSServer(t)}, Timeout: "10ms"},
	}}, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, kind, profile, want string
		code                      int
	}{
		{"corporate.invalid", "TXT", "", `"private-text"`, 0},
		{"corporate.invalid", "16", "other", `"other-text"`, 0},
		{"corporate.invalid", "MX", "", "10 mail.corporate.invalid.", 0},
		{"alias.corporate.invalid", "CNAME", "", "database.corporate.invalid.", 0},
		{"_db._tcp.corporate.invalid", "SRV", "", "1 5 5432 database.corporate.invalid", 0},
		{"corporate.invalid", "NS", "", "ns.corporate.invalid.", 0},
		{"_http._tcp.corporate.invalid", "PTR", "", "database.corporate.invalid.", 0},
		{"10.20.30.40", "PTR", "", "database.corporate.invalid.", 0},
		{"40.30.20.10.in-addr.arpa.", "12", "", "database.corporate.invalid.", 0},
		{"::ffff:10.0.0.1", "AAAA", "", "::ffff:10.0.0.1", 0},
		{"corporate.invalid", "TXT", "slow", "", 2},
	} {
		t.Run(tc.name+tc.kind+tc.profile, func(t *testing.T) {
			query := url.Values{"name": {tc.name}, "type": {tc.kind}}
			r := httptest.NewRequest("GET", DoHPath+"?"+query.Encode(), nil)
			r.Header.Set("X-Nanoflare-DNS-Profile", tc.profile)
			w := httptest.NewRecorder()
			server.ServeHTTP(w, r)
			var response dohResponse
			if json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Status != tc.code {
				t.Fatalf("response: %s", w.Body)
			}
			if tc.code == 0 && (len(response.Answer) != 1 || response.Answer[0].Data != tc.want) {
				t.Fatalf("answer: %s, want %s", w.Body, tc.want)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := lookupDoH(ctx, server.config.Profiles["private"], "corporate.invalid", 16); err == nil {
		t.Fatal("canceled TXT query succeeded")
	}
}

func TestReverseNames(t *testing.T) {
	for address, want := range map[string]string{
		"10.20.30.40": "40.30.20.10.in-addr.arpa.",
		"fd00::1":     "1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.d.f.ip6.arpa.",
	} {
		if got := reverseName(netip.MustParseAddr(address)); got != want {
			t.Fatalf("%s -> %s, want %s", address, got, want)
		}
	}
}

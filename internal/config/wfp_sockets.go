package config

import (
	"bufio"
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strings"
)

// WFPSocketOptions is the shared native namespace socket policy. Empty Allow disables it.
type WFPSocketOptions struct {
	Allow        []string
	Deny         []string
	AllowLocal   bool
	Certificates []string
}

func (o WFPSocketOptions) clone() WFPSocketOptions {
	return WFPSocketOptions{Allow: append([]string(nil), o.Allow...), Deny: append([]string(nil), o.Deny...), AllowLocal: o.AllowLocal, Certificates: append([]string(nil), o.Certificates...)}
}
func (w *Writer) SetWFPSockets(o WFPSocketOptions)        { w.wfpSockets = o.clone() }
func (w *RuntimeWriter) SetWFPSockets(o WFPSocketOptions) { w.wfpSockets = o.clone() }

// LoadWFPSocketPolicy validates the explicit destination policy and CA files.
func LoadWFPSocketPolicy(allow, deny, files string, allowLocal bool) (WFPSocketOptions, error) {
	var o WFPSocketOptions
	o.AllowLocal = allowLocal
	var err error
	o.Allow, err = parseWFPSocketRules(allow)
	if err != nil {
		return o, fmt.Errorf("NANOFLARE_WFP_SOCKET_ALLOW: %w", err)
	}
	for _, rule := range o.Allow {
		if !allowLocal && (rule == "local" || overlapsWFPLocalRange(rule)) {
			return o, fmt.Errorf("NANOFLARE_WFP_SOCKET_ALLOW: %q overlaps local destinations and requires NANOFLARE_WFP_SOCKET_ALLOW_LOCAL=true; this exposes platform services", rule)
		}
	}
	o.Deny, err = parseWFPSocketRules(deny)
	if err != nil {
		return o, fmt.Errorf("NANOFLARE_WFP_SOCKET_DENY: %w", err)
	}
	for _, file := range strings.Split(files, ",") {
		file = strings.TrimSpace(file)
		if file == "" {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return o, fmt.Errorf("NANOFLARE_WFP_SOCKET_CA_FILES: %w", err)
		}
		certificates, err := parseWFPCertificates(data)
		if err != nil {
			return o, fmt.Errorf("NANOFLARE_WFP_SOCKET_CA_FILES: %s: %w", file, err)
		}
		o.Certificates = append(o.Certificates, certificates...)

	}
	return o, nil
}
func parseWFPSocketRules(value string) ([]string, error) {
	var rules []string
	for _, rule := range strings.Split(value, ",") {
		rule = strings.TrimSpace(rule)
		if rule == "" {
			continue
		}
		switch rule {
		case "public", "private", "local":
		default:
			prefix, err := netip.ParsePrefix(rule)
			if err != nil {
				addr, e := netip.ParseAddr(rule)
				if e != nil || addr.Zone() != "" {
					return nil, fmt.Errorf("invalid destination %q", rule)
				}
				addr = addr.Unmap()
				prefix = netip.PrefixFrom(addr, addr.BitLen())
			} else if prefix.Addr().Is4In6() && prefix.Bits() >= 96 {
				prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
			}
			rule = prefix.Masked().String()
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func (o WFPSocketOptions) effectiveDeny() []string {
	deny := append([]string(nil), o.Deny...)
	if !o.AllowLocal {
		localRanges := wfpLocalRanges()
		deny = append(deny, "local")
		deny = append(deny, localRanges...)
		// workerd uses longest-prefix matching. Match any narrower allow prefix
		// so explicit local or unspecified addresses cannot override the default deny.
		for _, rule := range o.Allow {
			if prefix, err := netip.ParsePrefix(rule); err == nil {
				for _, blocked := range localRanges {
					if netip.MustParsePrefix(blocked).Contains(prefix.Addr()) {
						deny = append(deny, rule)
						break
					}
				}
			}
		}
	}
	return mirroredWFPSocketDenies(deny)
}

// Workerd may match a mapped destination as IPv6 even when the kernel dials
// IPv4. Emit both representations for every IPv4 deny, including custom rules.
func mirroredWFPSocketDenies(rules []string) []string {
	var result []string
	seen := make(map[string]bool)
	add := func(rule string) {
		if !seen[rule] {
			seen[rule] = true
			result = append(result, rule)
		}
	}
	for _, rule := range rules {
		prefix, err := netip.ParsePrefix(rule)
		if err != nil {
			add(rule)
			continue
		}
		if prefix.Addr().Is4In6() && prefix.Bits() >= 96 {
			prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
		}
		prefix = prefix.Masked()
		add(prefix.String())
		if !prefix.Addr().Is4() {
			continue
		}
		var mapped [16]byte
		mapped[10], mapped[11] = 0xff, 0xff
		addr := prefix.Addr().As4()
		copy(mapped[12:], addr[:])
		add(netip.PrefixFrom(netip.AddrFrom16(mapped), 96+prefix.Bits()).String())
	}
	return result
}

// LogWFPSocketPolicy reports the executor-local policy, never certificate contents.
func LogWFPSocketPolicy(o WFPSocketOptions) {
	slog.Info("WFP socket policy", "enabled", len(o.Allow) > 0, "allow", o.Allow, "deny", o.effectiveDeny(), "additional_ca_count", len(o.Certificates))
	if len(o.Allow) == 0 {
		return
	}
	for _, rule := range o.Allow {
		if rule == "private" {
			slog.Warn("WFP socket private policy includes platform private networks; use narrow CIDRs and deny platform subnets")
		}
	}
	if o.AllowLocal {
		slog.Warn("WFP socket local access explicitly enabled; platform loopback services may be reachable")
	}
}

func writeWFPSocketService(out *strings.Builder, o WFPSocketOptions) {
	if len(o.Allow) == 0 {
		return
	}
	fmt.Fprintf(out, "(name = \"nanoflare-wfp-sockets\", network = (allow = [%s], deny = [%s], tlsOptions = (trustBrowserCas = true, trustedCertificates = [%s]))),\n", quotedList(o.Allow), quotedList(o.effectiveDeny()), quotedList(o.Certificates))
}

// wfpSocketBindings returns complete entries, without presentation delimiters.
func wfpSocketBindings(o WFPSocketOptions) []string {
	if len(o.Allow) == 0 {
		return nil
	}
	return []string{`(name = "__NANOFLARE_WFP_SOCKET_OUTBOUND", service = "nanoflare-wfp-sockets")`}
}

func wfpLocalRanges() []string {
	return []string{"0.0.0.0/8", "::/128", "127.0.0.0/8", "::1/128", "169.254.0.0/16", "fe80::/10", "::ffff:0.0.0.0/104", "::ffff:127.0.0.0/104", "::ffff:169.254.0.0/112"}
}
func overlapsWFPLocalRange(rule string) bool {
	prefix, err := netip.ParsePrefix(rule)
	if err != nil {
		return false
	}
	for _, local := range wfpLocalRanges() {
		if prefix.Overlaps(netip.MustParsePrefix(local)) {
			return true
		}
	}
	return false
}

// parseWFPCertificates scans each bundle line once, allowing standard annotations
// outside certificates while still rejecting malformed or truncated PEM blocks.
func parseWFPCertificates(data []byte) ([]string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	var block bytes.Buffer
	var certificates []string
	inCertificate := false
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if inCertificate && bytes.HasPrefix(line, []byte("-----BEGIN")) {
			return nil, fmt.Errorf("nested or malformed certificate PEM")
		}
		if !inCertificate {
			if bytes.Equal(line, []byte("-----BEGIN CERTIFICATE-----")) {
				inCertificate = true
			} else {
				text := string(line)
				if text == "" || strings.HasPrefix(text, "#") || strings.HasPrefix(text, "Subject:") || strings.HasPrefix(text, "Issuer:") || strings.HasPrefix(text, "subject=") || strings.HasPrefix(text, "issuer=") || text == "Bag Attributes" || strings.HasPrefix(text, "friendlyName:") || strings.HasPrefix(text, "localKeyID:") {
					continue
				}
				return nil, fmt.Errorf("invalid certificate PEM or annotation")
			}
		}
		block.Write(line)
		block.WriteByte('\n')
		if bytes.Equal(line, []byte("-----END CERTIFICATE-----")) {
			decoded, rest := pem.Decode(block.Bytes())
			if decoded == nil || decoded.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
				return nil, fmt.Errorf("invalid certificate PEM")
			}
			if _, err := x509.ParseCertificate(decoded.Bytes); err != nil {
				return nil, fmt.Errorf("invalid certificate: %w", err)
			}
			certificates = append(certificates, string(pem.EncodeToMemory(decoded)))
			block.Reset()
			inCertificate = false
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if inCertificate {
		return nil, fmt.Errorf("truncated certificate PEM")
	}
	if len(certificates) == 0 {
		return nil, fmt.Errorf("no certificates")
	}
	return certificates, nil
}

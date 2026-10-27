package config

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strings"

	"github.com/clas/nanoflare/internal/nanoflare"
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

// LoadWFPSocketOptions validates settings without inheriting general outbound defaults.
func LoadWFPSocketOptions(allow, files string) (WFPSocketOptions, error) {
	return LoadWFPSocketPolicy(allow, "", files, false)
}

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
		if rule == "local" && !allowLocal {
			return o, fmt.Errorf("NANOFLARE_WFP_SOCKET_ALLOW: local requires NANOFLARE_WFP_SOCKET_ALLOW_LOCAL=true; this exposes platform services")
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
		count := 0
		for len(strings.TrimSpace(string(data))) > 0 {
			if !strings.HasPrefix(strings.TrimSpace(string(data)), "-----BEGIN CERTIFICATE-----") {
				return o, fmt.Errorf("NANOFLARE_WFP_SOCKET_CA_FILES: invalid certificate PEM in %q", file)
			}
			block, rest := pem.Decode(data)
			if block == nil || block.Type != "CERTIFICATE" {
				return o, fmt.Errorf("NANOFLARE_WFP_SOCKET_CA_FILES: invalid certificate PEM in %q", file)
			}
			if _, err := x509.ParseCertificate(block.Bytes); err != nil {
				return o, fmt.Errorf("NANOFLARE_WFP_SOCKET_CA_FILES: invalid certificate in %q: %w", file, err)
			}
			o.Certificates = append(o.Certificates, string(pem.EncodeToMemory(block)))
			count++
			data = rest
		}
		if count == 0 {
			return o, fmt.Errorf("NANOFLARE_WFP_SOCKET_CA_FILES: no certificates in %q", file)
		}
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
			if _, err := netip.ParsePrefix(rule); err != nil {
				if addr, err := netip.ParseAddr(rule); err != nil || addr.Zone() != "" {
					return nil, fmt.Errorf("invalid destination %q", rule)
				} else {
					rule = netip.PrefixFrom(addr, addr.BitLen()).String()
				}
			}
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func (o WFPSocketOptions) effectiveDeny() []string {
	deny := append([]string(nil), o.Deny...)
	if !o.AllowLocal {
		deny = append(deny, "local", "127.0.0.0/8", "::1/128")
		// workerd uses longest-prefix matching. Match any narrower allow prefix
		// so an explicit loopback address cannot override the default deny.
		for _, rule := range o.Allow {
			if prefix, err := netip.ParsePrefix(rule); err == nil {
				for _, blocked := range []string{"127.0.0.0/8", "::1/128", "169.254.0.0/16", "fe80::/10"} {
					if netip.MustParsePrefix(blocked).Contains(prefix.Addr()) {
						deny = append(deny, rule)
						break
					}
				}
			}
		}
	}
	return deny
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
func wfpSocketBinding(o WFPSocketOptions) string {
	if len(o.Allow) == 0 {
		return ""
	}
	return `(name = "__NANOFLARE_WFP_SOCKET_OUTBOUND", service = "nanoflare-wfp-sockets"),`
}
func wfpSocketBindings(item nanoflare.ActiveDeployment, o WFPSocketOptions) []string {
	if len(item.Deployment.DispatchNamespaces) == 0 || len(o.Allow) == 0 {
		return nil
	}
	return []string{strings.TrimSuffix(wfpSocketBinding(o), ",")}
}

func wfpSocketBindingLine(o WFPSocketOptions) string {
	if binding := wfpSocketBinding(o); binding != "" {
		return "    " + binding + "\n"
	}
	return ""
}

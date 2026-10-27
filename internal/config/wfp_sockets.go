package config

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/netip"
	"os"
	"strings"

	"github.com/clas/nanoflare/internal/nanoflare"
)

// WFPSocketOptions is the shared native namespace socket policy. Empty Allow disables it.
type WFPSocketOptions struct {
	Allow        []string
	Certificates []string
}

func (o WFPSocketOptions) clone() WFPSocketOptions {
	return WFPSocketOptions{append([]string(nil), o.Allow...), append([]string(nil), o.Certificates...)}
}
func (w *Writer) SetWFPSockets(o WFPSocketOptions)        { w.wfpSockets = o.clone() }
func (w *RuntimeWriter) SetWFPSockets(o WFPSocketOptions) { w.wfpSockets = o.clone() }

// LoadWFPSocketOptions validates settings without inheriting general outbound defaults.
func LoadWFPSocketOptions(allow, files string) (WFPSocketOptions, error) {
	var o WFPSocketOptions
	for _, rule := range strings.Split(allow, ",") {
		rule = strings.TrimSpace(rule)
		if rule == "" {
			continue
		}
		switch rule {
		case "public", "private", "local":
		default:
			if _, err := netip.ParsePrefix(rule); err != nil {
				if addr, err := netip.ParseAddr(rule); err != nil || addr.Zone() != "" {
					return o, fmt.Errorf("NANOFLARE_WFP_SOCKET_ALLOW: invalid destination %q", rule)
				}
			}
		}
		o.Allow = append(o.Allow, rule)
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
func writeWFPSocketService(out *strings.Builder, o WFPSocketOptions) {
	if len(o.Allow) == 0 {
		return
	}
	fmt.Fprintf(out, "(name = \"nanoflare-wfp-sockets\", network = (allow = [%s], tlsOptions = (trustBrowserCas = true, trustedCertificates = [%s]))),\n", quotedList(o.Allow), quotedList(o.Certificates))
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

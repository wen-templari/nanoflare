package config

import "github.com/clas/nanoflare/internal/dnsresolver"

// workerd's native node:dns sends JSON DoH through the current global fetch.
// Install before user modules execute, including modules that capture fetch.
func dohFetchSource() string {
	return renderWorkerJavaScript("doh-fetch", struct {
		Host       string
		Path       string
		ServiceURL string
	}{
		Host:       jsStringLiteral(dnsresolver.DoHHost),
		Path:       jsStringLiteral(dnsresolver.DoHPath),
		ServiceURL: jsStringLiteral("http://nanoflare.dns" + dnsresolver.DoHPath),
	})
}

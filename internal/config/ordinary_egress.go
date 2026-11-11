package config

import (
	"fmt"
	"strings"
)

func (w *Writer) SetNetworkCertificates(certificates []string) {
	w.networkCertificates = append([]string(nil), certificates...)
}

func (w *RuntimeWriter) SetNetworkCertificates(certificates []string) {
	w.networkCertificates = append([]string(nil), certificates...)
}

// Native connect() needs a network service's TLS starter. An external HTTP
// proxy service cannot supply one in the pinned workerd runtime.
func writeInternetService(out *strings.Builder, options WorkerdOptions) {
	certificates := ""
	if len(options.NetworkCertificates) > 0 {
		certificates = ", trustedCertificates = [" + quotedList(options.NetworkCertificates) + "]"
	}
	fmt.Fprintf(out, "(name = \"internet\", network = (allow = [%s], tlsOptions = (trustBrowserCas = true%s))),\n", quotedList(normalizedNetworkAllow(options.NetworkAllow)), certificates)
}

func ordinaryEgressBindings(options WorkerdOptions) []string {
	if strings.TrimSpace(options.EgressAddr) == "" {
		return nil
	}
	return []string{`(name = "__NANOFLARE_HTTP_OUTBOUND", wrapped = (moduleName = "nanoflare-internal:egress", innerBindings = [(name = "OUTBOUND", service = "nanoflare-egress")]))`}
}

// Wrapped bindings initialize before user modules, even when the Worker opts
// out of cloudflare:workers' importable env. No experimental flag is added to
// the Worker itself and the extension is internal to the generated config.
func writeOrdinaryEgressExtension(out *strings.Builder, options WorkerdOptions) {
	if strings.TrimSpace(options.EgressAddr) == "" {
		return
	}
	source := egressFetchInstaller + `
export default function(env) {
 installEgressFetch(() => env.OUTBOUND);
 return env.OUTBOUND;
}
`
	fmt.Fprintf(out, "extensions = [(modules = [(name = \"nanoflare-internal:egress\", internal = true, esModule = %s)])],\n", capnpQuote(source))
}

// WorkerLoader receives its bindings as env values rather than wrapped-binding
// config. Resolve the binding from the admitted invocation, including when
// importable env is disabled, and install before user imports can capture fetch.
const egressFetchSource = `import { currentInvocationEnv } from "` + loaderInvocationContextModule + `";
` + egressFetchInstaller + `
installEgressFetch(() => currentInvocationEnv().__NANOFLARE_HTTP_OUTBOUND);
`

const egressFetchInstaller = `function installEgressFetch(outbound) {
 globalThis.fetch = async function fetch(input, init) {
  return outbound().fetch(input, {
   redirect: input instanceof Request ? input.redirect : "follow",
   ...init,
  });
 };
}
`

package config

import (
	_ "embed"
	"fmt"
	"strings"

	"github.com/clas/nanoflare/internal/runtimeassets"
)

// DynamicDispatcher configures one stable, authenticated workerd socket.
// Publication and traffic selection remain the Go gateway's responsibility.
func DynamicDispatcher(options WorkerdOptions, socketPath, brokerAddr, brokerToken, dispatcherToken string) string {
	outbound := "internet"
	var out strings.Builder
	out.WriteString("using Workerd = import \"/workerd/workerd.capnp\";\nconst config :Workerd.Config = (\n services = [\n")
	writeInternetService(&out, options)
	if address := strings.TrimSpace(options.EgressAddr); address != "" {
		outbound = "nanoflare-egress"
		fmt.Fprintf(&out, "(name = %s, external = (address = %s, http = (style = proxy))),\n", capnpQuote(outbound), capnpQuote(address))
	}
	writeWFPSocketService(&out, options.WFPSockets)
	fmt.Fprintf(&out, `(name = "broker", external = (address = %s, http = (injectRequestHeaders = [(name = "X-Nanoflare-Broker-Token", value = %s)]))),
(name = "dispatcher", worker = (
  compatibilityDate = "2026-09-08",
  compatibilityFlags = ["experimental", "enable_abortsignal_rpc"],
  modules = [(name = "dispatcher.js", esModule = %s), (name = "dispatch.js", esModule = %s)],
  bindings = [
%s    (name = "LOADER", workerLoader = (id = "nanoflare-publications")),
    (name = "APPLICATION_LOADER", workerLoader = ()),
    (name = "BROKER", service = "broker"),
    (name = "OUTBOUND", service = "internet"),
    (name = "HTTP_OUTBOUND", service = %s),
    (name = "DISPATCHER_TOKEN", text = %s)
  ],
)),
 ],
 sockets = [(name = "dispatcher", address = %s, http = (forwardedProtoHeader = "X-Forwarded-Proto"), service = "dispatcher")],
);
`, capnpQuote(brokerAddr), capnpQuote(brokerToken), capnpQuote(dynamicDispatcherSource), capnpQuote(runtimeassets.DispatchModule), wfpSocketBindingLine(options.WFPSockets), capnpQuote(outbound), capnpQuote(dispatcherToken), capnpQuote("unix:"+socketPath))
	return out.String()
}

//go:embed workerjs/dynamic-dispatcher.js
var dynamicDispatcherModule string

var dynamicDispatcherSource = applicationLoaderSource + dynamicDispatcherModule

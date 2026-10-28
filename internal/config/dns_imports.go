package config

import (
	"container/list"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/evanw/esbuild/pkg/api"
)

type dnsRewriteKey struct {
	source [32]byte
	module string
	bare   bool
}
type dnsRewriteResult struct {
	source string
	err    error
}

var dnsRewriteCache = struct {
	sync.Mutex
	entries map[dnsRewriteKey]*list.Element
	order   list.List
	bytes   int
}{entries: make(map[dnsRewriteKey]*list.Element)}

type dnsCachedRewrite struct {
	key    dnsRewriteKey
	result dnsRewriteResult
	size   int
}

type dnsImportTarget string
type dnsAnalysisComplete struct{}
type dnsImportEdit struct {
	start, end int
	target     string
}

func rewriteDNSImports(source string, bare bool) (string, error) {
	return rewriteDNSImportsForModule(source, bare, "")
}

// Rewrite only parser-identified specifier ranges. Generated JavaScript is
// never used. Unrelated source, including comments and UMD wrappers, is copied
// byte-for-byte. Cache both success and failure for immutable source revisions.
func rewriteDNSImportsForModule(source string, bare bool, module string) (string, error) {
	if (!strings.Contains(source, "dns") && !strings.Contains(source, `\`)) ||
		(!strings.Contains(source, "import") && !strings.Contains(source, "export") && !strings.Contains(source, "require")) {
		return source, nil
	}
	key := dnsRewriteKey{sha256.Sum256([]byte(source)), module, bare}
	dnsRewriteCache.Lock()
	cached, ok := dnsRewriteCache.entries[key]
	if ok {
		dnsRewriteCache.order.MoveToFront(cached)
		result := cached.Value.(dnsCachedRewrite).result
		dnsRewriteCache.Unlock()
		return result.source, result.err
	}
	dnsRewriteCache.Unlock()
	rewritten, err := rewriteDNSImportRanges(source, bare, module)
	dnsRewriteCache.Lock()
	const limit = 32 << 20
	size := len(rewritten) + len(module) + 256
	if size <= limit {
		if _, exists := dnsRewriteCache.entries[key]; !exists {
			for len(dnsRewriteCache.entries) >= 4096 || dnsRewriteCache.bytes+size > limit {
				oldest := dnsRewriteCache.order.Back()
				entry := oldest.Value.(dnsCachedRewrite)
				delete(dnsRewriteCache.entries, entry.key)
				dnsRewriteCache.bytes -= entry.size
				dnsRewriteCache.order.Remove(oldest)
			}
			entry := dnsCachedRewrite{key, dnsRewriteResult{rewritten, err}, size}
			dnsRewriteCache.entries[key] = dnsRewriteCache.order.PushFront(entry)
			dnsRewriteCache.bytes += size
		}
	}
	dnsRewriteCache.Unlock()
	return rewritten, err
}

func rewriteDNSImportRanges(source string, bare bool, module string) (string, error) {
	for {
		// esbuild's public resolver API exposes source ranges through diagnostics.
		// Bundle enables resolution only; intentional diagnostics stop compilation
		// before linking/emission. No rebuilt program is returned or installed.
		result := api.Build(api.BuildOptions{
			Stdin: &api.StdinOptions{Contents: source + `
import "nanoflare-internal:dns-analysis-complete";`, Sourcefile: "worker.js", Loader: api.LoaderJS},
			Bundle: true, Format: api.FormatESModule, Write: false, LogLevel: api.LogLevelSilent,
			Plugins: []api.Plugin{{Name: "dns-import-ranges", Setup: func(build api.PluginBuild) {
				build.OnResolve(api.OnResolveOptions{Filter: ".*"}, func(args api.OnResolveArgs) (api.OnResolveResult, error) {
					if args.Path == "nanoflare-internal:dns-analysis-complete" {
						return api.OnResolveResult{Errors: []api.Message{{Text: "DNS analysis complete", Detail: dnsAnalysisComplete{}}}}, nil
					}
					target := ""
					switch args.Path {
					case "node:dns", "node:dns/promises":
						target = "nanoflare-internal:" + strings.TrimPrefix(args.Path, "node:")
					case "dns", "dns/promises":
						if bare {
							target = "nanoflare-internal:" + args.Path
						}
					}
					if target == "" {
						return api.OnResolveResult{Path: args.Path, External: true}, nil
					}
					return api.OnResolveResult{Errors: []api.Message{{Text: "DNS import range", Detail: dnsImportTarget(relativeModuleSpecifier(module, target))}}}, nil
				})
			}}},
		})
		edits := []dnsImportEdit{}
		lines := dnsSourceLines(source)
		for _, message := range result.Errors {
			if _, complete := message.Detail.(dnsAnalysisComplete); complete {
				continue
			}
			target, ok := message.Detail.(dnsImportTarget)
			if !ok || message.Location == nil {
				return "", fmt.Errorf("DNS import analysis in %s: %s", module, message.Text)
			}
			loc := message.Location
			// Diagnostic columns and lengths are byte offsets, not Unicode columns.
			if loc.Line < 1 || loc.Line > len(lines) {
				return "", fmt.Errorf("invalid DNS import location")
			}
			start := lines[loc.Line-1] + loc.Column
			end := start + loc.Length
			if start < 0 || end > len(source) || end-start < 2 || (source[start] != '\'' && source[start] != '"' && source[start] != '`') || source[end-1] != source[start] {
				return "", fmt.Errorf("invalid DNS import range in %s", module)
			}
			edits = append(edits, dnsImportEdit{start, end, string(target)})
		}
		if len(edits) == 0 {
			return source, nil
		}
		sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
		var out strings.Builder
		previous := 0
		for _, edit := range edits {
			if edit.start < previous {
				return "", fmt.Errorf("overlapping DNS imports in %s", module)
			}
			out.WriteString(source[previous:edit.start])
			out.WriteByte(source[edit.start])
			out.WriteString(edit.target)
			// Keep line continuations so subsequent uploaded source line numbers stay
			// unchanged even for an escaped multiline module specifier.
			for i := edit.start; i < edit.end; i++ {
				if i+2 < edit.end && source[i] == 0xe2 && source[i+1] == 0x80 && (source[i+2] == 0xa8 || source[i+2] == 0xa9) {
					out.WriteByte('\\')
					out.WriteString(source[i : i+3])
					i += 2
					continue
				}
				if source[i] == '\r' {
					out.WriteString("\\\r")
					if i+1 < edit.end && source[i+1] == '\n' {
						out.WriteByte('\n')
						i++
					}
				} else if source[i] == '\n' {
					out.WriteString("\\\n")
				}
			}
			out.WriteByte(source[edit.start])
			previous = edit.end
		}
		out.WriteString(source[previous:])
		source = out.String()
		// The resolver deduplicates identical paths. Another pass handles any
		// remaining occurrences; cached revisions never repeat this analysis.
	}
}

func dnsSourceLines(source string) []int {
	lines := []int{0}
	for i := 0; i < len(source); i++ {
		switch source[i] {
		case '\r':
			if i+1 < len(source) && source[i+1] == '\n' {
				i++
			}
			lines = append(lines, i+1)
		case '\n':
			lines = append(lines, i+1)
		case 0xe2:
			if i+2 < len(source) && source[i+1] == 0x80 && (source[i+2] == 0xa8 || source[i+2] == 0xa9) {
				i += 2
				lines = append(lines, i+1)
			}
		}
	}
	return lines
}

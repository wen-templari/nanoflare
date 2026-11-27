package config

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
)

//go:embed workerjs/*.js.tmpl
var workerJavaScriptFiles embed.FS

var workerJavaScriptTemplates = template.Must(template.ParseFS(workerJavaScriptFiles, "workerjs/*.js.tmpl"))

// Template values are JavaScript literals or source prepared by the caller.
// Template errors indicate a mismatch between our embedded files and Go data.
func renderWorkerJavaScript(name string, data any) string {
	var out strings.Builder
	if err := workerJavaScriptTemplates.ExecuteTemplate(&out, name+".js.tmpl", data); err != nil {
		panic(fmt.Errorf("render worker JavaScript %s: %w", name, err))
	}
	return out.String()
}

func jsStringLiteral(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

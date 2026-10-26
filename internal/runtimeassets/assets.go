package runtimeassets

import _ "embed"

// BindingsModule is generated from packages/runtime/src/bindings.ts. Keeping
// the generated JavaScript embedded makes Go binaries and tests independent of
// a Node.js installation while the source remains type-checked TypeScript.
//
//go:embed assets/bindings.js
var BindingsModule string

//go:embed assets/dispatch.js
var DispatchModule string

//go:embed assets/dynamic-env.js
var dynamicEnvModule string

//go:embed assets/export-props.js
var ExportPropsModule string

// Include the shared props facade in both dynamic customers and invocation loaders.
// Each generated module remains self-contained.
var DynamicEnvModule = ExportPropsModule + "\n" + dynamicEnvModule

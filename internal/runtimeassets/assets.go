package runtimeassets

import _ "embed"

// BindingsModule is generated from packages/runtime/src/bindings.ts. Keeping
// the generated JavaScript embedded makes Go binaries and tests independent of
// a Node.js installation while the source remains type-checked TypeScript.
//
//go:embed assets/bindings.js
var BindingsModule string

//go:embed assets/dispatch.js
var dispatchModule string

var DispatchModule = ErrorsModule + "\n" + dispatchModule

//go:embed assets/dynamic-env.js
var dynamicEnvModule string

//go:embed assets/errors.js
var ErrorsModule string

//go:embed assets/fetch-signal.js
var FetchSignalModule string

//go:embed assets/runtime-profile.js
var RuntimeProfileModule string

//go:embed assets/export-props.js
var ExportPropsModule string

// Include the shared props facade in both dynamic customers and invocation loaders.
// Each generated module remains self-contained.
var DynamicEnvModule = ExportPropsModule + "\n" + ErrorsModule + "\n" + FetchSignalModule + "\n" + RuntimeProfileModule + "\n" + dynamicEnvModule

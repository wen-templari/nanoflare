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
var DynamicEnvModule string

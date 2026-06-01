package starterworker

import _ "embed"

// WorkerJS is the starter ES-module bundle written by the CLI.
//
//go:embed worker.js
var WorkerJS []byte

package starterworker

import _ "embed"

// WorkerJS is the starter service-worker bundle written by the CLI.
//
//go:embed worker.js
var WorkerJS []byte

package config

import _ "embed"

//go:embed workerjs/application-loader-common.js
var applicationLoaderCode string

//go:embed workerjs/application-loader.js
var applicationLoaderRuntime string

//go:embed workerjs/application-loader-facade.js
var applicationLoaderFacadeModule string

var (
	applicationLoaderSource = applicationLoaderCode + applicationLoaderRuntime
	applicationLoaderFacade = applicationLoaderCode + applicationLoaderFacadeModule
)

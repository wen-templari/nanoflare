package nanoflare

import "errors"

type WorkerLoaderBinding struct {
	Binding string `json:"binding"`
}

// SetWorkerLoadersEnabled controls new declarations in the trusted runtime.
func (s *Service) SetWorkerLoadersEnabled(enabled bool) {
	s.workerLoadersEnabled.Store(enabled)
}

func (s *Service) validateWorkerLoaders(app App, format string, bindings []WorkerLoaderBinding) error {
	if len(bindings) == 0 {
		return nil
	}
	if !s.workerLoadersEnabled.Load() {
		return errors.New("worker_loaders requires --enable-worker-loaders")
	}
	if app.IsPrivate() || format != "modules" {
		return errors.New("worker_loaders requires an ordinary module Worker")
	}
	if len(bindings) > 8 {
		return errors.New("worker_loaders supports at most eight bindings per Worker")
	}
	for _, binding := range bindings {
		if !dispatchBindingNamePattern.MatchString(binding.Binding) || binding.Binding == "__proto__" {
			return errors.New("invalid worker_loader binding name")
		}
	}
	return nil
}

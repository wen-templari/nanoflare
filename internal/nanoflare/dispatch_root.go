package nanoflare

import (
	"context"
	"time"
)

type dispatchRoot struct {
	Context      context.Context
	Cancel       context.CancelFunc
	InstanceID   string
	AppID        string
	DeploymentID string
	Deadline     time.Time
	Expires      time.Time
	Finished     bool
	Cancelled    bool
	Remaining    int
	Release      func()
}

// BeginDispatchRoot is called by the trusted ingress gateway, not a public API.
// Its opaque ID binds child effects to the actual source request and revision.
func (s *Service) BeginDispatchRoot(active ActiveDeployment) (string, time.Time, error) {
	if !s.dispatch.enabled.Load() {
		return "", time.Time{}, ErrDispatchUnavailable
	}
	token, err := randomToken()
	if err != nil {
		return "", time.Time{}, err
	}
	now := time.Now().UTC()
	deadline := now.Add(30 * time.Second)
	s.dispatch.mu.Lock()
	defer s.dispatch.mu.Unlock()
	if s.dispatch.roots == nil {
		s.dispatch.roots = map[string]*dispatchRoot{}
	}
	for id, root := range s.dispatch.roots {
		if !now.Before(root.Expires) {
			if root.Cancel != nil {
				root.Cancel()
			}
			delete(s.dispatch.roots, id)
		}
	}
	if len(s.dispatch.roots) >= 1024 {
		return "", time.Time{}, ErrDispatchOverloaded
	}
	rootContext, rootCancel := context.WithDeadline(context.Background(), deadline)
	s.dispatch.roots[token] = &dispatchRoot{Context: rootContext, Cancel: rootCancel, AppID: active.App.ID, DeploymentID: active.Deployment.ID, Deadline: deadline, Expires: deadline, Remaining: 100}
	return token, deadline, nil
}
func (s *Service) EndDispatchRoot(id string, cancel bool) {
	s.dispatch.mu.Lock()
	var observations []DispatchObservation
	root := s.dispatch.roots[id]
	if root == nil {
		s.dispatch.mu.Unlock()
		return
	}
	if cancel {
		root.Cancelled = true
		root.Expires = time.Now()
	}
	if !root.Finished {
		root.Finished = true
		root.Expires = minTime(root.Expires, time.Now().Add(2*time.Second))
	}
	for token, lease := range s.dispatch.leases {
		if lease.Root == id {
			lease.Expires = minTime(lease.Expires, root.Expires)
			if cancel {
				if event, ok := s.observeDispatchLocked(token, lease, "canceled"); ok {
					observations = append(observations, event)
				}
			}
		}
	}
	release := root.Release
	root.Release = nil
	expires := root.Expires
	if root.Cancel != nil {
		time.AfterFunc(max(time.Until(expires), 0), root.Cancel)
	}
	sink := s.dispatch.observe
	s.dispatch.mu.Unlock()
	if sink != nil {
		for _, event := range observations {
			sink(event)
		}
	}
	if release != nil {
		delay := time.Until(expires)
		if delay <= 0 {
			release()
		} else {
			time.AfterFunc(delay, release)
		}
	}
}

// HoldDispatchRoot keeps a draining source pool alive through bounded background
// work. The callback comes only from the runner's ingress acquisition.
func (s *Service) HoldDispatchRoot(id string, release func()) {
	s.dispatch.mu.Lock()
	root := s.dispatch.roots[id]
	if root == nil {
		s.dispatch.mu.Unlock()
		release()
		return
	}
	if root.Finished || root.Cancelled {
		delay := time.Until(root.Expires)
		s.dispatch.mu.Unlock()
		if delay <= 0 {
			release()
		} else {
			time.AfterFunc(delay, release)
		}
		return
	}
	root.Release = release
	s.dispatch.mu.Unlock()
}

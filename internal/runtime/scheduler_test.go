package runtime

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type recordingCronClient struct {
	urls []string
}

func TestCronRunnerUsesLeasedGenerationSocket(t *testing.T) {
	active := nanoflare.ActiveDeployment{
		App:        nanoflare.App{ID: "app-1"},
		SocketPath: "/tmp/retired.sock",
		Deployment: nanoflare.Deployment{ID: "dep-1", Triggers: nanoflare.TriggerConfig{Crons: []string{"* * * * *"}}},
	}
	client := &recordingCronClient{}
	released := false
	runner := newCronRunner([]nanoflare.ActiveDeployment{active}, nil, client, func(context.Context, nanoflare.ActiveDeployment) (EnsuredWorker, error) {
		return EnsuredWorker{SocketPath: "/tmp/current.sock", Release: func() { released = true }}, nil
	})
	defer runner.cancel()
	if err := runner.invoke(time.Date(2026, 7, 11, 12, 10, 0, 0, time.UTC), runner.jobs[0]); err != nil {
		t.Fatal(err)
	}
	if !released || len(client.urls) != 1 || client.urls[0] != "http://"+SocketHost("/tmp/current.sock")+"/cdn-cgi/handler/scheduled?cron=%2A+%2A+%2A+%2A+%2A&time=1783771800" {
		t.Fatalf("cron did not use and release the current generation socket: %#v, released=%v", client.urls, released)
	}
}

func (c *recordingCronClient) Do(request *http.Request) (*http.Response, error) {
	c.urls = append(c.urls, request.URL.String())
	return &http.Response{
		StatusCode: http.StatusNoContent,
		Status:     "204 No Content",
		Body:       http.NoBody,
	}, nil
}

func TestCronRunnerInvokesDueTriggers(t *testing.T) {
	client := &recordingCronClient{}
	output := NewOutputBuffer()
	runner := newCronRunner([]nanoflare.ActiveDeployment{{
		App:        nanoflare.App{ID: "app-1"},
		SocketPath: "/tmp/cron.sock",
		Deployment: nanoflare.Deployment{
			ID:       "dep-1",
			Triggers: nanoflare.TriggerConfig{Crons: []string{"*/5 * * * *", "1 * * * *"}},
		},
	}}, output, client, nil)
	runner.runDue(time.Date(2026, 7, 11, 12, 10, 0, 0, time.UTC))
	runner.work.Wait()
	if len(client.urls) != 1 {
		t.Fatalf("urls = %#v, want one invocation", client.urls)
	}
	if got, want := client.urls[0], "http://"+SocketHost("/tmp/cron.sock")+"/cdn-cgi/handler/scheduled?cron=%2A%2F5+%2A+%2A+%2A+%2A&time=1783771800"; got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}
	if lines := output.Output("app-1"); len(lines) != 1 || lines[0].Level != "info" || lines[0].DeploymentID != "dep-1" {
		t.Fatalf("output = %#v", lines)
	}
}

func TestCronRunnerLogsFailures(t *testing.T) {
	runner := newCronRunner([]nanoflare.ActiveDeployment{{
		App:        nanoflare.App{ID: "app-1"},
		SocketPath: "/tmp/cron.sock",
		Deployment: nanoflare.Deployment{
			ID:       "dep-1",
			Triggers: nanoflare.TriggerConfig{Crons: []string{"* * * * *"}},
		},
	}}, NewOutputBuffer(), roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusInternalServerError, Status: "500 Internal Server Error", Body: http.NoBody}, nil
	}), nil)
	runner.runDue(time.Date(2026, 7, 11, 12, 10, 0, 0, time.UTC))
	runner.work.Wait()
	if lines := runner.output.Output("app-1"); len(lines) != 1 || lines[0].Level != "error" || lines[0].DeploymentID != "dep-1" {
		t.Fatalf("output = %#v", lines)
	}
}

func TestCronRunnerRunsDueTriggersAsynchronouslyAndWaitsOnStop(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	runner := newCronRunner([]nanoflare.ActiveDeployment{{
		App:        nanoflare.App{ID: "app-1"},
		SocketPath: "/tmp/cron.sock",
		Deployment: nanoflare.Deployment{
			ID:       "dep-1",
			Triggers: nanoflare.TriggerConfig{Crons: []string{"* * * * *"}},
		},
	}}, NewOutputBuffer(), roundTripFunc(func(*http.Request) (*http.Response, error) {
		close(started)
		<-release
		return &http.Response{StatusCode: http.StatusNoContent, Status: "204 No Content", Body: http.NoBody}, nil
	}), nil)
	go runner.Run()

	runner.runDue(time.Date(2026, 7, 11, 12, 10, 0, 0, time.UTC))
	<-started

	stopped := make(chan struct{})
	go func() {
		runner.Stop()
		close(stopped)
	}()
	<-runner.done
	select {
	case <-stopped:
		t.Fatal("Stop returned while a cron invocation was still running")
	default:
	}

	close(release)
	<-stopped
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) Do(request *http.Request) (*http.Response, error) {
	return f(request)
}

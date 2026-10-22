package runtime

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type cronHTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type cronEnsureFunc func(context.Context, nanoflare.ActiveDeployment) (EnsuredWorker, error)

type cronJob struct {
	appID    string
	cron     string
	schedule nanoflare.CronSchedule
	active   nanoflare.ActiveDeployment
}

type cronRunner struct {
	ctx    context.Context
	cancel context.CancelFunc
	jobsMu sync.RWMutex
	jobs   []cronJob
	client cronHTTPClient
	output *OutputBuffer
	ensure cronEnsureFunc
	stop   chan struct{}
	done   chan struct{}
	once   sync.Once
	work   sync.WaitGroup
}

func startCronRunner(active []nanoflare.ActiveDeployment, output *OutputBuffer, ensure cronEnsureFunc) *cronRunner {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	ConfigureTransport(transport)
	runner := newCronRunner(active, output, &http.Client{Timeout: 120 * time.Second, Transport: transport}, ensure)
	if len(runner.jobs) == 0 {
		return nil
	}
	go runner.Run()
	return runner
}

func newCronRunner(active []nanoflare.ActiveDeployment, output *OutputBuffer, client cronHTTPClient, ensure cronEnsureFunc) *cronRunner {
	ctx, cancel := context.WithCancel(context.Background())
	runner := &cronRunner{
		ctx: ctx, cancel: cancel,
		client: client,
		output: output,
		ensure: ensure,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	runner.Update(active)
	return runner
}

// Update replaces future triggers without canceling admitted cron executions.
func (runner *cronRunner) Update(active []nanoflare.ActiveDeployment) {
	jobs := []cronJob{}
	for _, item := range active {
		for _, expression := range item.Deployment.Triggers.Crons {
			schedule, err := nanoflare.ParseCron(expression)
			if err != nil {
				runner.log(item, "error", fmt.Sprintf("skip invalid cron trigger for %s: %v", item.App.ID, err))
				continue
			}
			jobs = append(jobs, cronJob{
				appID:    item.App.ID,
				cron:     expression,
				schedule: schedule,
				active:   item,
			})
		}
	}
	runner.jobsMu.Lock()
	runner.jobs = jobs
	runner.jobsMu.Unlock()
}

func (r *cronRunner) Run() {
	defer close(r.done)
	timer := time.NewTimer(time.Until(nextMinute(time.Now().UTC())))
	defer timer.Stop()
	for {
		select {
		case <-r.stop:
			return
		case now := <-timer.C:
			r.runDue(now.UTC().Truncate(time.Minute))
			timer.Reset(time.Until(nextMinute(time.Now().UTC())))
		}
	}
}

func (r *cronRunner) Stop() {
	r.once.Do(func() {
		r.cancel()
		close(r.stop)
		<-r.done
		r.work.Wait()
	})
}

func (r *cronRunner) runDue(now time.Time) {
	r.jobsMu.RLock()
	jobs := append([]cronJob(nil), r.jobs...)
	r.jobsMu.RUnlock()
	for _, job := range jobs {
		if !job.schedule.Matches(now) {
			continue
		}
		r.work.Add(1)
		go func() {
			defer r.work.Done()
			r.invokeAndLog(now, job)
		}()
	}
}

func (r *cronRunner) invokeAndLog(now time.Time, job cronJob) {
	if err := r.invoke(now, job); err != nil {
		r.log(job.active, "error", fmt.Sprintf("cron trigger %q for %s failed: %v", job.cron, job.appID, err))
		return
	}
	r.log(job.active, "info", fmt.Sprintf("cron trigger %q for %s completed", job.cron, job.appID))
}

func (r *cronRunner) invoke(now time.Time, job cronJob) error {
	ctx, cancel := context.WithTimeout(r.ctx, 120*time.Second)
	defer cancel()
	socketPath := job.active.SocketPath
	var headers http.Header
	if r.ensure != nil {
		ensured, err := r.ensure(ctx, job.active)
		if err != nil {
			return err
		}
		defer ensured.Release()
		socketPath = ensured.SocketPath
		headers = ensured.Headers
	}
	if socketPath == "" {
		return fmt.Errorf("Worker %s has no runtime socket", job.appID)
	}
	values := url.Values{}
	values.Set("cron", job.cron)
	values.Set("time", strconv.FormatInt(now.Unix(), 10))
	target := url.URL{
		Scheme:   "http",
		Host:     SocketHost(socketPath),
		Path:     "/cdn-cgi/handler/scheduled",
		RawQuery: values.Encode(),
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return err
	}
	for name, values := range headers {
		request.Header[name] = append([]string(nil), values...)
	}
	response, err := r.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("worker returned %s", response.Status)
	}
	return nil
}

func (r *cronRunner) log(active nanoflare.ActiveDeployment, level, message string) {
	if r.output != nil {
		r.output.AppendActive(active, level, message)
		return
	}
	fmt.Printf("%s: %s\n", level, message)
}

func nextMinute(now time.Time) time.Time {
	return now.Truncate(time.Minute).Add(time.Minute)
}

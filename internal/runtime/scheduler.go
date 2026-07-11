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
	port     int
	cron     string
	schedule nanoflare.CronSchedule
	active   nanoflare.ActiveDeployment
}

type cronRunner struct {
	host   string
	jobs   []cronJob
	client cronHTTPClient
	output *OutputBuffer
	ensure cronEnsureFunc
	stop   chan struct{}
	done   chan struct{}
	once   sync.Once
}

func startCronRunner(host string, active []nanoflare.ActiveDeployment, output *OutputBuffer) *cronRunner {
	return startCronRunnerWithEnsure(host, active, output, nil)
}

func startCronRunnerWithEnsure(host string, active []nanoflare.ActiveDeployment, output *OutputBuffer, ensure cronEnsureFunc) *cronRunner {
	runner := newCronRunner(host, active, output, &http.Client{Timeout: 10 * time.Second}, ensure)
	if len(runner.jobs) == 0 {
		return nil
	}
	go runner.Run()
	return runner
}

func newCronRunner(host string, active []nanoflare.ActiveDeployment, output *OutputBuffer, client cronHTTPClient, ensure cronEnsureFunc) *cronRunner {
	runner := &cronRunner{
		host:   host,
		client: client,
		output: output,
		ensure: ensure,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	for _, item := range active {
		for _, expression := range item.Deployment.Triggers.Crons {
			schedule, err := nanoflare.ParseCron(expression)
			if err != nil {
				runner.log("error", fmt.Sprintf("skip invalid cron trigger for %s: %v", item.App.ID, err))
				continue
			}
			runner.jobs = append(runner.jobs, cronJob{
				appID:    item.App.ID,
				port:     item.Deployment.Port,
				cron:     expression,
				schedule: schedule,
				active:   item,
			})
		}
	}
	return runner
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
		close(r.stop)
		<-r.done
	})
}

func (r *cronRunner) runDue(now time.Time) {
	for _, job := range r.jobs {
		if !job.schedule.Matches(now) {
			continue
		}
		if err := r.invoke(now, job); err != nil {
			r.log("error", fmt.Sprintf("cron trigger %q for %s failed: %v", job.cron, job.appID, err))
			continue
		}
		r.log("info", fmt.Sprintf("cron trigger %q for %s completed", job.cron, job.appID))
	}
}

func (r *cronRunner) invoke(now time.Time, job cronJob) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	port := job.port
	if r.ensure != nil {
		ensured, err := r.ensure(ctx, job.active)
		if err != nil {
			return err
		}
		defer ensured.Release()
		port = ensured.Port
	}
	values := url.Values{}
	values.Set("cron", job.cron)
	values.Set("time", strconv.FormatInt(now.Unix(), 10))
	target := url.URL{
		Scheme:   "http",
		Host:     r.host + ":" + strconv.Itoa(port),
		Path:     "/cdn-cgi/handler/scheduled",
		RawQuery: values.Encode(),
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return err
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

func (r *cronRunner) log(level, message string) {
	if r.output != nil {
		r.output.Append(level, message)
		return
	}
	fmt.Printf("%s: %s\n", level, message)
}

func nextMinute(now time.Time) time.Time {
	return now.Truncate(time.Minute).Add(time.Minute)
}

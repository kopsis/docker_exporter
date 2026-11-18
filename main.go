package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	version               = "__VERSIONSTRING__"
	defaultDockerURL      = "unix:///var/run/docker.sock"
	listenAddress         = ":9417"
	commandTimeout        = 30 * time.Second
	inlineUpdateTimeout   = 20 * time.Second
	totalUpdateTimeout    = 2 * time.Minute
)

var durationBuckets = prometheus.ExponentialBuckets(0.5, 1.5, 14)

type exporter struct {
	client *client.Client
	metrics *metrics

	mu      sync.Mutex
	running bool
	done    chan struct{}
}

func newExporter(dockerURL string) (*exporter, error) {
	c, err := client.NewClientWithOpts(client.WithHost(dockerURL), client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	e := &exporter{client: c}
	e.metrics = newMetrics()
	return e, nil
}

func (e *exporter) Collect(ch chan<- prometheus.Metric) {
	e.refresh()
	e.metrics.collectMetrics(ch)
}

func (e *exporter) Describe(ch chan<- *prometheus.Desc) {
	e.metrics.describe(ch)
}

func (e *exporter) refresh() {
	e.mu.Lock()
	var done chan struct{}
	if !e.running {
		e.running = true
		e.done = make(chan struct{})
		done = e.done
		e.mu.Unlock()

		go e.probe(done)
	} else {
		done = e.done
		e.mu.Unlock()
		select {
		case <-done:
		case <-time.After(inlineUpdateTimeout):
			e.metrics.inlineTimeouts.Inc()
		}
		return
	}

	select {
	case <-done:
	case <-time.After(inlineUpdateTimeout):
		e.metrics.inlineTimeouts.Inc()
	}
}

func (e *exporter) probe(done chan struct{}) {
	defer func() {
		e.mu.Lock()
		e.running = false
		close(done)
		e.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), totalUpdateTimeout)
	defer cancel()
	probeStart := time.Now()
	defer func() { e.metrics.probeDuration.Observe(time.Since(probeStart).Seconds()) }()
	containers, err := e.listContainers(ctx)
	if err != nil {
		e.metrics.listErrors.Inc()
		log.Printf("listing Docker containers: %v", err)
		e.metrics.unpublishAll()
		return
	}
	e.metrics.containerCount.Set(float64(len(containers)))
	e.metrics.synchronize(containers)

	var wg sync.WaitGroup
	for _, container := range containers {
		container := container
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.updateContainer(ctx, container)
		}()
	}
	wg.Wait()
	e.metrics.successfulProbeTime.Set(float64(time.Now().Unix()))
}

func (e *exporter) listContainers(ctx context.Context) ([]types.Container, error) {
	start := time.Now()
	callCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	containers, err := e.client.ContainerList(callCtx, types.ContainerListOptions{All: true})
	e.metrics.listDuration.Observe(time.Since(start).Seconds())
	return containers, err
}

func (e *exporter) updateContainer(ctx context.Context, listed types.Container) {
	name := displayName(listed)
	inspectStart := time.Now()
	callCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	container, err := e.client.ContainerInspect(callCtx, listed.ID)
	cancel()
	e.metrics.inspectDuration.Observe(time.Since(inspectStart).Seconds())
	if err != nil {
		e.metrics.failedProbe.WithLabelValues(name).Inc()
		e.metrics.unpublish(name)
		log.Printf("inspecting container %s (%s): %v", name, listed.ID, err)
		return
	}

	var stats types.StatsJSON
	if container.State != nil && container.State.Running {
		statsStart := time.Now()
		callCtx, cancel = context.WithTimeout(ctx, commandTimeout)
		response, statsErr := e.client.ContainerStats(callCtx, listed.ID, false)
		if statsErr == nil && response.Body != nil {
			statsErr = json.NewDecoder(response.Body).Decode(&stats)
		} else if statsErr == nil {
			statsErr = fmt.Errorf("Docker returned an empty stats response")
		}
		if response.Body != nil {
			response.Body.Close()
		}
		cancel()
		e.metrics.statsDuration.Observe(time.Since(statsStart).Seconds())
		if statsErr != nil {
			e.metrics.failedProbe.WithLabelValues(name).Inc()
			e.metrics.unpublish(name)
			log.Printf("reading stats for container %s (%s): %v", name, listed.ID, statsErr)
			return
		}
	}

	e.metrics.updateState(name, container)
	if container.State != nil && container.State.Running {
		e.metrics.updateResources(name, stats)
	} else {
		e.metrics.deleteResources(name)
	}
}

func displayName(container types.Container) string {
	if len(container.Names) > 0 && strings.TrimSpace(container.Names[0]) != "" {
		return strings.Trim(container.Names[0], "/")
	}
	if len(container.ID) > 12 {
		return container.ID[:12]
	}
	return container.ID
}

func main() {
	dockerURL := flag.String("docker-url", defaultDockerURL, "URL to use for accessing Docker")
	verbose := flag.Bool("verbose", false, "Enable debug logging")
	showHelp := flag.Bool("help", false, "Display usage instructions")
	flag.Parse()
	if *showHelp {
		flag.PrintDefaults()
		return
	}
	if !*verbose {
		log.SetFlags(log.LstdFlags)
	}
	log.Printf("docker_exporter v%s; probing Docker at %s", version, *dockerURL)

	e, err := newExporter(*dockerURL)
	if err != nil {
		log.Fatal(err)
	}
	registry := prometheus.NewRegistry()
	if err := registry.Register(e); err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: listenAddress, Handler: promhttp.HandlerFor(registry, promhttp.HandlerOpts{})}
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("metrics server: %v", err)
		}
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	<-signals
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutting down metrics server: %v", err)
	}
	e.client.Close()
	fmt.Println("Application logic execution has completed.")
}

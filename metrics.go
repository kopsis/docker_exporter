package main

import (
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/prometheus/client_golang/prometheus"
)

type metrics struct {
	containerCount      prometheus.Gauge
	listErrors          prometheus.Counter
	inlineTimeouts      prometheus.Counter
	successfulProbeTime prometheus.Gauge
	probeDuration       prometheus.Histogram
	listDuration        prometheus.Histogram
	inspectDuration     prometheus.Histogram
	statsDuration       prometheus.Histogram
	failedProbe         *prometheus.CounterVec
	restartCount        *prometheus.GaugeVec
	runningState        *prometheus.GaugeVec
	startTime           *prometheus.GaugeVec
	cpuUsage            *prometheus.GaugeVec
	cpuCapacity         *prometheus.GaugeVec
	memoryUsage         *prometheus.GaugeVec
	networkIn           *prometheus.GaugeVec
	networkOut          *prometheus.GaugeVec
	diskRead            *prometheus.GaugeVec
	diskWrite           *prometheus.GaugeVec

	mu    sync.Mutex
	names map[string]struct{}
}

func newMetrics() *metrics {
	m := &metrics{names: make(map[string]struct{})}
	m.containerCount = prometheus.NewGauge(prometheus.GaugeOpts{Name: "docker_containers", Help: "Number of containers that exist."})
	m.listErrors = prometheus.NewCounter(prometheus.CounterOpts{Name: "docker_probe_list_containers_failed_total", Help: "How many times the attempt to list all containers has failed."})
	m.inlineTimeouts = prometheus.NewCounter(prometheus.CounterOpts{Name: "docker_probe_inline_timeouts_total", Help: "Total number of times we have forced the scrape to happen in the background and returned outdated data because performing an update inline took too long."})
	m.successfulProbeTime = prometheus.NewGauge(prometheus.GaugeOpts{Name: "docker_probe_successfully_completed_time", Help: "When the last Docker probe was successfully completed."})
	m.probeDuration = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "docker_probe_duration_seconds", Help: "How long it takes to query Docker for the complete data set. Includes failed requests.", Buckets: durationBuckets})
	m.listDuration = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "docker_probe_list_containers_duration_seconds", Help: "How long it takes to query Docker for the list of containers. Includes failed requests.", Buckets: durationBuckets})
	m.inspectDuration = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "docker_probe_inspect_duration_seconds", Help: "How long it takes to query Docker for the basic information about a single container. Includes failed requests.", Buckets: durationBuckets})
	m.statsDuration = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "docker_probe_stats_duration_seconds", Help: "How long it takes to query Docker for the resource usage of a single container. Includes failed requests.", Buckets: durationBuckets})
	m.failedProbe = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "docker_probe_container_failed_total", Help: "Number of times the exporter failed to collect information about a specific container."}, []string{"name"})
	m.restartCount = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "docker_container_restart_count", Help: "Number of times the runtime has restarted this container without explicit user action, since the container was last started."}, []string{"name"})
	m.runningState = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "docker_container_running_state", Help: "Whether the container is running (1), restarting (0.5) or stopped (0)."}, []string{"name"})
	m.startTime = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "docker_container_start_time_seconds", Help: "Timestamp indicating when the container was started. Does not get reset by automatic restarts."}, []string{"name"})
	m.cpuUsage = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "docker_container_cpu_used_total", Help: "Accumulated CPU usage of a container, in unspecified units, averaged for all logical CPUs usable by the container."}, []string{"name"})
	m.cpuCapacity = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "docker_container_cpu_capacity_total", Help: "All potential CPU usage available to a container, in unspecified units, averaged for all logical CPUs usable by the container."}, []string{"name"})
	m.memoryUsage = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "docker_container_memory_used_bytes", Help: "Memory usage of a container."}, []string{"name"})
	m.networkIn = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "docker_container_network_in_bytes", Help: "Total bytes received by the container's network interfaces."}, []string{"name"})
	m.networkOut = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "docker_container_network_out_bytes", Help: "Total bytes sent by the container's network interfaces."}, []string{"name"})
	m.diskRead = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "docker_container_disk_read_bytes", Help: "Total bytes read from disk by a container."}, []string{"name"})
	m.diskWrite = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "docker_container_disk_write_bytes", Help: "Total bytes written to disk by a container."}, []string{"name"})
	return m
}

func (m *metrics) collectors() []prometheus.Collector {
	return []prometheus.Collector{m.containerCount, m.listErrors, m.inlineTimeouts, m.successfulProbeTime, m.probeDuration, m.listDuration, m.inspectDuration, m.statsDuration, m.failedProbe, m.restartCount, m.runningState, m.startTime, m.cpuUsage, m.cpuCapacity, m.memoryUsage, m.networkIn, m.networkOut, m.diskRead, m.diskWrite}
}

func (m *metrics) describe(ch chan<- *prometheus.Desc) {
	for _, collector := range m.collectors() {
		collector.Describe(ch)
	}
}

func (m *metrics) collectMetrics(ch chan<- prometheus.Metric) {
	for _, collector := range m.collectors() {
		collector.Collect(ch)
	}
}

func (m *metrics) synchronize(containers []types.Container) {
	current := make(map[string]struct{}, len(containers))
	for _, container := range containers {
		current[displayName(container)] = struct{}{}
	}
	m.mu.Lock()
	for name := range m.names {
		if _, ok := current[name]; !ok {
			m.deleteAll(name)
		}
	}
	m.names = current
	m.mu.Unlock()
}

func (m *metrics) unpublishAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name := range m.names {
		m.unpublish(name)
	}
}

func (m *metrics) unpublish(name string) {
	m.restartCount.DeleteLabelValues(name)
	m.runningState.DeleteLabelValues(name)
	m.startTime.DeleteLabelValues(name)
	m.deleteResources(name)
}

func (m *metrics) deleteAll(name string) {
	m.failedProbe.DeleteLabelValues(name)
	m.unpublish(name)
}

func (m *metrics) deleteResources(name string) {
	m.cpuUsage.DeleteLabelValues(name)
	m.cpuCapacity.DeleteLabelValues(name)
	m.memoryUsage.DeleteLabelValues(name)
	m.networkIn.DeleteLabelValues(name)
	m.networkOut.DeleteLabelValues(name)
	m.diskRead.DeleteLabelValues(name)
	m.diskWrite.DeleteLabelValues(name)
}

func (m *metrics) updateState(name string, c types.ContainerJSON) {
	if c.State == nil {
		return
	}
	m.restartCount.WithLabelValues(name).Set(float64(c.RestartCount))
	state := float64(0)
	if c.State.Running {
		state = 1
	} else if c.State.Restarting {
		state = 0.5
	}
	m.runningState.WithLabelValues(name).Set(state)
	if c.State.Running && strings.TrimSpace(c.State.StartedAt) != "" {
		if t, err := parseDockerTime(c.State.StartedAt); err == nil {
			m.startTime.WithLabelValues(name).Set(float64(t.UnixNano()) / 1e9)
		}
	}
}

func (m *metrics) updateResources(name string, stats container.StatsResponse) {
	windows := stats.MemoryStats.Commit != 0
	if windows {
		// Docker reports Windows CPU usage in 100 ns ticks. The origin is
		// intentionally arbitrary, matching the original exporter behavior.
		m.cpuCapacity.WithLabelValues(name).Set(float64(time.Since(processStart).Nanoseconds() / 100))
		usage := float64(stats.CPUStats.CPUUsage.TotalUsage)
		if stats.NumProcs > 0 {
			usage /= float64(stats.NumProcs)
		}
		m.cpuUsage.WithLabelValues(name).Set(usage)
		m.memoryUsage.WithLabelValues(name).Set(float64(stats.MemoryStats.Commit))
	} else {
		m.cpuCapacity.WithLabelValues(name).Set(float64(stats.CPUStats.SystemUsage))
		m.cpuUsage.WithLabelValues(name).Set(float64(stats.CPUStats.CPUUsage.TotalUsage))
		m.memoryUsage.WithLabelValues(name).Set(float64(stats.MemoryStats.Usage))
	}

	var networkIn, networkOut float64
	for _, network := range stats.Networks {
		networkIn += float64(network.RxBytes)
		networkOut += float64(network.TxBytes)
	}
	m.networkIn.WithLabelValues(name).Set(networkIn)
	m.networkOut.WithLabelValues(name).Set(networkOut)

	var diskRead, diskWrite float64
	if windows {
		diskRead = float64(stats.StorageStats.ReadSizeBytes)
		diskWrite = float64(stats.StorageStats.WriteSizeBytes)
	} else {
		for _, entry := range stats.BlkioStats.IoServiceBytesRecursive {
			switch strings.ToLower(entry.Op) {
			case "read":
				diskRead += float64(entry.Value)
			case "write":
				diskWrite += float64(entry.Value)
			}
		}
	}
	m.diskRead.WithLabelValues(name).Set(diskRead)
	m.diskWrite.WithLabelValues(name).Set(diskWrite)
}

func parseDockerTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}

var processStart = time.Now()

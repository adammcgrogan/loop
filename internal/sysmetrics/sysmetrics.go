package sysmetrics

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type Metrics struct {
	// Go runtime
	Goroutines int
	HeapAllocMB float64
	HeapSysMB   float64
	TotalAllocMB float64
	SysMB       float64
	GCRuns      uint32
	GCPauseMs   float64
	LastGC      string

	// System (/proc — available on Railway's Linux containers)
	LoadAvg1   float64
	LoadAvg5   float64
	LoadAvg15  float64
	MemTotalMB float64
	MemAvailMB float64
	MemUsedMB  float64
	MemUsedPct int
	SysUptime  string

	// App
	AppUptime string
	GOMAXPROCS int
}

func Gather(appStart time.Time) Metrics {
	var m Metrics

	// Go runtime
	m.Goroutines = runtime.NumGoroutine()
	m.GOMAXPROCS = runtime.GOMAXPROCS(0)

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	m.HeapAllocMB  = mbf(ms.HeapAlloc)
	m.HeapSysMB    = mbf(ms.HeapSys)
	m.TotalAllocMB = mbf(ms.TotalAlloc)
	m.SysMB        = mbf(ms.Sys)
	m.GCRuns       = ms.NumGC
	m.GCPauseMs    = float64(ms.PauseTotalNs) / 1e6
	if ms.LastGC > 0 {
		m.LastGC = time.Unix(0, int64(ms.LastGC)).UTC().Format("02 Jan 15:04:05 UTC")
	} else {
		m.LastGC = "never"
	}

	// Load average
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 3 {
			m.LoadAvg1, _  = strconv.ParseFloat(fields[0], 64)
			m.LoadAvg5, _  = strconv.ParseFloat(fields[1], 64)
			m.LoadAvg15, _ = strconv.ParseFloat(fields[2], 64)
		}
	}

	// System memory
	if f, err := os.Open("/proc/meminfo"); err == nil {
		defer f.Close()
		vals := map[string]float64{}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			parts := strings.Fields(scanner.Text())
			if len(parts) >= 2 {
				key := strings.TrimSuffix(parts[0], ":")
				val, _ := strconv.ParseFloat(parts[1], 64)
				vals[key] = val
			}
		}
		m.MemTotalMB = vals["MemTotal"] / 1024
		m.MemAvailMB = vals["MemAvailable"] / 1024
		m.MemUsedMB  = m.MemTotalMB - m.MemAvailMB
		if m.MemTotalMB > 0 {
			m.MemUsedPct = int(m.MemUsedMB / m.MemTotalMB * 100)
		}
	}

	// System uptime
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 1 {
			secs, _ := strconv.ParseFloat(fields[0], 64)
			m.SysUptime = fmtSeconds(secs)
		}
	}

	// App uptime
	m.AppUptime = fmtDuration(time.Since(appStart))

	return m
}

func mbf(b uint64) float64 { return float64(b) / 1024 / 1024 }

func fmtDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return strconv.Itoa(h) + "h " + strconv.Itoa(m) + "m"
	}
	if m > 0 {
		return strconv.Itoa(m) + "m " + strconv.Itoa(s) + "s"
	}
	return strconv.Itoa(s) + "s"
}

func fmtSeconds(secs float64) string {
	d := time.Duration(secs) * time.Second
	days := int(d.Hours()) / 24
	h    := int(d.Hours()) % 24
	m    := int(d.Minutes()) % 60
	if days > 0 {
		return strconv.Itoa(days) + "d " + strconv.Itoa(h) + "h"
	}
	if h > 0 {
		return strconv.Itoa(h) + "h " + strconv.Itoa(m) + "m"
	}
	return strconv.Itoa(m) + "m"
}

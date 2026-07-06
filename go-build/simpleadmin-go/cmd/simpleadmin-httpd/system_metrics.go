package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type cpuTimesSnapshot struct {
	idle  uint64
	total uint64
}

var systemMetricsMu sync.Mutex
var lastCPUTimes cpuTimesSnapshot
var hasLastCPUTimes bool

func systemResourceMetrics(mock bool) map[string]any {
	if mock {
		return map[string]any{
			"cpuUsagePercent": 36,
			"ramUsagePercent": 58,
			"ramUsedHuman":    "1.2 GB",
			"ramTotalHuman":   "2.0 GB",
		}
	}

	cpuUsage := currentCPUUsagePercent()
	ramUsage, ramUsed, ramTotal := currentRAMUsage()
	return map[string]any{
		"cpuUsagePercent": cpuUsage,
		"ramUsagePercent": ramUsage,
		"ramUsedHuman":    humanBytesFromKiB(ramUsed),
		"ramTotalHuman":   humanBytesFromKiB(ramTotal),
	}
}

func currentCPUUsagePercent() int {
	systemMetricsMu.Lock()
	defer systemMetricsMu.Unlock()

	current, ok := readCPUTimesSnapshot()
	if !ok {
		return 0
	}

	if !hasLastCPUTimes {
		lastCPUTimes = current
		hasLastCPUTimes = true
		time.Sleep(120 * time.Millisecond)
		if next, ok := readCPUTimesSnapshot(); ok {
			current = next
		} else {
			return 0
		}
	}

	if current.total < lastCPUTimes.total || current.idle < lastCPUTimes.idle {
		lastCPUTimes = current
		return 0
	}
	totalDelta := current.total - lastCPUTimes.total
	idleDelta := current.idle - lastCPUTimes.idle
	lastCPUTimes = current
	if totalDelta == 0 || idleDelta > totalDelta {
		return 0
	}
	return clampInt(int((100*(totalDelta-idleDelta)+totalDelta/2)/totalDelta), 0, 100)
}

func readCPUTimesSnapshot() (cpuTimesSnapshot, bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuTimesSnapshot{}, false
	}
	lines := strings.SplitN(string(data), "\n", 2)
	fields := strings.Fields(lines[0])
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuTimesSnapshot{}, false
	}

	var values []uint64
	for _, field := range fields[1:] {
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return cpuTimesSnapshot{}, false
		}
		values = append(values, value)
	}

	var total uint64
	for _, value := range values {
		total += value
	}
	idle := values[3]
	if len(values) > 4 {
		idle += values[4]
	}
	return cpuTimesSnapshot{idle: idle, total: total}, true
}

func currentRAMUsage() (percent int, usedKiB int64, totalKiB int64) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, 0
	}

	values := map[string]int64{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		value, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		values[key] = value
	}

	total := values["MemTotal"]
	available := values["MemAvailable"]
	if available <= 0 {
		available = values["MemFree"] + values["Buffers"] + values["Cached"] + values["SReclaimable"] - values["Shmem"]
	}
	if total <= 0 || available < 0 {
		return 0, 0, total
	}
	used := total - available
	if used < 0 {
		used = 0
	}
	percent = clampInt(int((100*used+total/2)/total), 0, 100)
	return percent, used, total
}

func humanBytesFromKiB(valueKiB int64) string {
	if valueKiB <= 0 {
		return "-"
	}
	units := []string{"KB", "MB", "GB", "TB"}
	value := float64(valueKiB)
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if unit > 0 && value < 10 {
		return fmt.Sprintf("%.1f %s", value, units[unit])
	}
	return fmt.Sprintf("%.0f %s", value, units[unit])
}

func clampInt(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

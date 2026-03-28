package status

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// SystemResources holds live system resource metrics read from /proc.
type SystemResources struct {
	CPUUsagePercent float64 `json:"cpuUsagePercent"`
	MemTotalMB      uint64  `json:"memTotalMB"`
	MemUsedMB       uint64  `json:"memUsedMB"`
	MemFreePercent  float64 `json:"memFreePercent"`
	SwapTotalMB     uint64  `json:"swapTotalMB"`
	SwapUsedMB      uint64  `json:"swapUsedMB"`
	DiskTotalMB     uint64  `json:"diskTotalMB"`
	DiskUsedMB      uint64  `json:"diskUsedMB"`
	DiskFreePercent float64 `json:"diskFreePercent"`
	DiskPath        string  `json:"diskPath"`
	DataDiskTotalMB uint64  `json:"dataDiskTotalMB"`
	DataDiskUsedMB  uint64  `json:"dataDiskUsedMB"`
	DataDiskFreePct float64 `json:"dataDiskFreePercent"`
	DataDiskPath    string  `json:"dataDiskPath"`
	UptimeSeconds   uint64  `json:"uptimeSeconds"`
	UptimeFormatted string  `json:"uptimeFormatted"`
	LoadAvg1        float64 `json:"loadAvg1"`
	LoadAvg5        float64 `json:"loadAvg5"`
	LoadAvg15       float64 `json:"loadAvg15"`
	ProcessCount    int     `json:"processCount"`
	CollectedAt     string  `json:"collectedAt"`
}

type cpuSample struct {
	idle  uint64
	total uint64
}

var prevCPUSample cpuSample

// CollectSystemResources reads system resource information from /proc.
func CollectSystemResources(dataDir string) SystemResources {
	res := SystemResources{
		CollectedAt: time.Now().UTC().Format(time.RFC3339),
	}

	res.CPUUsagePercent = readCPUUsage()
	readMemInfo(&res)

	readDiskUsage(&res.DiskTotalMB, &res.DiskUsedMB, &res.DiskFreePercent, "/")
	res.DiskPath = "/"

	if dataDir != "" && dataDir != "/" {
		readDiskUsage(&res.DataDiskTotalMB, &res.DataDiskUsedMB, &res.DataDiskFreePct, dataDir)
		res.DataDiskPath = dataDir
	}

	readUptime(&res)
	readLoadAvg(&res)

	return res
}

func readCPUUsage() float64 {
	idle, total := parseProcStat()
	if total == 0 {
		return 0
	}

	prev := prevCPUSample
	prevCPUSample = cpuSample{idle: idle, total: total}

	if prev.total == 0 {
		return 0
	}

	deltaIdle := idle - prev.idle
	deltaTotal := total - prev.total
	if deltaTotal == 0 {
		return 0
	}

	return float64(deltaTotal-deltaIdle) / float64(deltaTotal) * 100
}

func parseProcStat() (idle, total uint64) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0
	}

	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "cpu ") {
			fields := strings.Fields(line)
			if len(fields) < 5 {
				return 0, 0
			}
			var sum uint64
			for i := 1; i < len(fields); i++ {
				v, _ := strconv.ParseUint(fields[i], 10, 64)
				sum += v
			}
			idleVal, _ := strconv.ParseUint(fields[4], 10, 64)
			return idleVal, sum
		}
	}
	return 0, 0
}

func readMemInfo(res *SystemResources) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return
	}

	info := map[string]uint64{}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		valStr := strings.TrimSpace(parts[1])
		valStr = strings.TrimSuffix(valStr, " kB")
		valStr = strings.TrimSpace(valStr)
		v, _ := strconv.ParseUint(valStr, 10, 64)
		info[key] = v
	}

	memTotal := info["MemTotal"]
	memFree := info["MemFree"]
	buffers := info["Buffers"]
	cached := info["Cached"]

	res.MemTotalMB = memTotal / 1024
	memUsed := memTotal - memFree - buffers - cached
	if memTotal > 0 && memUsed <= memTotal {
		res.MemUsedMB = memUsed / 1024
		res.MemFreePercent = float64(memTotal-memUsed) / float64(memTotal) * 100
	}

	swapTotal := info["SwapTotal"]
	swapFree := info["SwapFree"]
	res.SwapTotalMB = swapTotal / 1024
	if swapTotal > 0 && swapFree <= swapTotal {
		res.SwapUsedMB = (swapTotal - swapFree) / 1024
	}
}

func readUptime(res *SystemResources) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return
	}

	fields := strings.Fields(string(data))
	if len(fields) < 1 {
		return
	}

	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return
	}

	res.UptimeSeconds = uint64(seconds)
	d := time.Duration(seconds) * time.Second
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60

	if days > 0 {
		res.UptimeFormatted = strconv.Itoa(days) + "д " + strconv.Itoa(hours) + "ч " + strconv.Itoa(minutes) + "м"
	} else if hours > 0 {
		res.UptimeFormatted = strconv.Itoa(hours) + "ч " + strconv.Itoa(minutes) + "м"
	} else {
		res.UptimeFormatted = strconv.Itoa(minutes) + "м"
	}
}

func readLoadAvg(res *SystemResources) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return
	}

	fields := strings.Fields(string(data))
	if len(fields) < 4 {
		return
	}

	res.LoadAvg1, _ = strconv.ParseFloat(fields[0], 64)
	res.LoadAvg5, _ = strconv.ParseFloat(fields[1], 64)
	res.LoadAvg15, _ = strconv.ParseFloat(fields[2], 64)

	procs := strings.Split(fields[3], "/")
	if len(procs) == 2 {
		res.ProcessCount, _ = strconv.Atoi(procs[1])
	}
}

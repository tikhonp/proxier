package remote

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Container is one container of the stack as docker reports it.
type Container struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	Restarts int    `json:"restarts"`
	Image    string `json:"image"`
}

// StatsReading is what one stats batch read from a server. Counters are the
// raw values; the deltas between two readings are the stats package's.
type StatsReading struct {
	Load1               float64
	CPUBusy, CPUTotal   uint64
	MemUsed, MemTotal   int64
	DiskUsed, DiskTotal int64
	Iface               string // the device of the default route
	RXBytes, TXBytes    uint64 // that device's counters
	Uptime              float64
	Containers          []Container
}

// Stats runs the batch on the server and parses it.
func Stats(ctx context.Context, env Env, c Conn, dir string) (StatsReading, error) {
	out, err := env.run(ctx, c, "read stats", CmdStats(dir), RunOpts{})
	if err != nil {
		return StatsReading{}, err
	}
	return ParseStats(out)
}

// ParseStats reads the output of CmdStats. Every section but the containers
// must be there; a machine without Docker simply has none.
func ParseStats(out string) (StatsReading, error) {
	sec := map[string][]string{}
	cur := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if name, ok := strings.CutPrefix(line, "##"); ok {
			cur = name
			sec[cur] = nil
			continue
		}
		if cur != "" && strings.TrimSpace(line) != "" {
			sec[cur] = append(sec[cur], line)
		}
	}
	var r StatsReading
	need := func(name string) ([]string, error) {
		if len(sec[name]) == 0 {
			return nil, fmt.Errorf("stats: no %s section", name)
		}
		return sec[name], nil
	}

	l, err := need("loadavg")
	if err != nil {
		return r, err
	}
	if f := strings.Fields(l[0]); len(f) > 0 {
		if r.Load1, err = strconv.ParseFloat(f[0], 64); err != nil {
			return r, fmt.Errorf("stats: loadavg: %w", err)
		}
	}

	l, err = need("stat")
	if err != nil {
		return r, err
	}
	f := strings.Fields(l[0])
	if len(f) < 5 || f[0] != "cpu" {
		return r, fmt.Errorf("stats: cannot read /proc/stat")
	}
	var total, idle uint64
	for i, w := range f[1:] {
		if i >= 8 { // guest columns are already inside user and nice
			break
		}
		n, err := strconv.ParseUint(w, 10, 64)
		if err != nil {
			return r, fmt.Errorf("stats: /proc/stat: %w", err)
		}
		total += n
		if i == 3 || i == 4 { // idle, iowait
			idle += n
		}
	}
	r.CPUTotal, r.CPUBusy = total, total-idle

	l, err = need("meminfo")
	if err != nil {
		return r, err
	}
	var avail int64
	for _, line := range l {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		kb, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			r.MemTotal = kb * 1024
		case "MemAvailable:":
			avail = kb * 1024
		}
	}
	if r.MemTotal == 0 {
		return r, fmt.Errorf("stats: cannot read /proc/meminfo")
	}
	r.MemUsed = r.MemTotal - avail

	l, err = need("df")
	if err != nil {
		return r, err
	}
	f = strings.Fields(l[len(l)-1])
	if len(f) < 4 {
		return r, fmt.Errorf("stats: cannot read df")
	}
	total64, e1 := strconv.ParseInt(f[1], 10, 64)
	used, e2 := strconv.ParseInt(f[2], 10, 64)
	if e1 != nil || e2 != nil {
		return r, fmt.Errorf("stats: cannot read df")
	}
	r.DiskTotal, r.DiskUsed = total64, used

	if l, ok := sec["route"]; ok && len(l) > 0 {
		f := strings.Fields(l[0])
		for i := 0; i+1 < len(f); i++ {
			if f[i] == "dev" {
				r.Iface = f[i+1]
				break
			}
		}
	}
	l, err = need("netdev")
	if err != nil {
		return r, err
	}
	for _, line := range l {
		name, rest, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) != r.Iface || r.Iface == "" {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			return r, fmt.Errorf("stats: cannot read /proc/net/dev")
		}
		r.RXBytes, _ = strconv.ParseUint(f[0], 10, 64)
		r.TXBytes, _ = strconv.ParseUint(f[8], 10, 64)
	}

	l, err = need("uptime")
	if err != nil {
		return r, err
	}
	if f := strings.Fields(l[0]); len(f) > 0 {
		if r.Uptime, err = strconv.ParseFloat(f[0], 64); err != nil {
			return r, fmt.Errorf("stats: uptime: %w", err)
		}
	}

	for _, line := range sec["docker"] {
		p := strings.SplitN(line, "|", 4)
		if len(p) < 4 {
			continue
		}
		n, _ := strconv.Atoi(p[2])
		r.Containers = append(r.Containers, Container{Name: strings.TrimPrefix(p[0], "/"), State: p[1], Restarts: n, Image: p[3]})
	}
	return r, nil
}

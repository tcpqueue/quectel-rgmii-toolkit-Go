package main

import (
	"context"
	"net"
	"strings"
	"time"

	probing "github.com/prometheus-community/pro-bing"
)

// Only the single ping worker owns the resolver cache and pinger.
type icmpProber struct {
	target  string
	address *net.IPAddr
	expires time.Time
}

func (p *icmpProber) probe(ctx context.Context, target string) pingSample {
	sample := pingSample{Status: "dns_error"}
	if p.target != target || p.address == nil || time.Now().After(p.expires) {
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, target)
		if err != nil || len(addresses) == 0 {
			return sample
		}
		chosen := addresses[0]
		for _, address := range addresses {
			if address.IP.To4() != nil {
				chosen = address
				break
			}
		}
		p.target, p.address, p.expires = target, &chosen, time.Now().Add(time.Minute)
	}
	sample.IP = p.address.String()
	for _, privileged := range []bool{false, true} {
		pinger := probing.New(target)
		pinger.SetIPAddr(p.address)
		pinger.SetPrivileged(privileged)
		pinger.SetTrafficClass(0)
		pinger.Count, pinger.Timeout = 1, 900*time.Millisecond
		pinger.RecordRtts, pinger.RecordTTLs = false, false
		err := pinger.RunWithContext(ctx)
		stats := pinger.Statistics()
		sample.Sent = stats.PacketsSent > 0
		if stats.PacketsRecv > 0 {
			sample.Status, sample.RTT = "ok", metric(float64(stats.AvgRtt)/float64(time.Millisecond))
			return sample
		}
		if err != nil && (strings.Contains(err.Error(), "permission") || strings.Contains(err.Error(), "not permitted")) {
			sample.Status = "permission_error"
			if !privileged && !sample.Sent {
				continue
			}
			return sample
		}
		sample.Status = "unavailable"
		if sample.Sent {
			sample.Status = "timeout"
		}
		return sample
	}
	return sample
}

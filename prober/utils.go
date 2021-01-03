// Copyright 2016 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package prober

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"

	"github.com/prometheus/client_golang/prometheus"
)

var protocolToGauge = map[string]float64{
	"ip4": 4,
	"ip6": 6,
}

type resolver struct {
	net.Resolver
}

// A simple wrapper around resolver.LookupIP.
func (r *resolver) resolve(ctx context.Context, target string, protocol string) (*net.IPAddr, error) {
	ips, err := r.LookupIP(ctx, protocol, target)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		return &net.IPAddr{IP: ip}, nil
	}
	// Go doc did not specify when this could happen, better be defensive.
	return nil, errors.New("calling LookupIP returned empty list of addresses")
}

// Returns the IP for the IPProtocol and lookup time.
func chooseProtocol(ctx context.Context, IPProtocol string, fallbackIPProtocol bool, target string, registry *prometheus.Registry, logger log.Logger) (ip *net.IPAddr, lookupTime float64, err error) {
	var fallbackProtocol string
	probeDNSLookupTimeSeconds := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "probe_dns_lookup_time_seconds",
		Help: "Returns the time taken for probe dns lookup in seconds",
	})

	probeIPProtocolGauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "probe_ip_protocol",
		Help: "Specifies whether probe ip protocol is IP4 or IP6",
	})

	probeIPAddrHash := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "probe_ip_addr_hash",
		Help: "Specifies the hash of IP address. It's useful to detect if the IP address changes.",
	})
	registry.MustRegister(probeIPProtocolGauge)
	registry.MustRegister(probeDNSLookupTimeSeconds)
	registry.MustRegister(probeIPAddrHash)

	if IPProtocol == "ip6" || IPProtocol == "" {
		IPProtocol = "ip6"
		fallbackProtocol = "ip4"
	} else {
		IPProtocol = "ip4"
		fallbackProtocol = "ip6"
	}

	resolveStart := time.Now()

	defer func() {
		lookupTime = time.Since(resolveStart).Seconds()
		probeDNSLookupTimeSeconds.Add(lookupTime)
	}()

	r := &resolver{
		Resolver: net.Resolver{},
	}

	level.Info(logger).Log("msg", "Resolving target address", "ip_protocol", IPProtocol)
	if ip, err := r.resolve(ctx, target, IPProtocol); err == nil {
		level.Info(logger).Log("msg", "Resolved target address", "ip", ip.String())
		probeIPProtocolGauge.Set(protocolToGauge[IPProtocol])
		probeIPAddrHash.Set(ipHash(ip.IP))
		return ip, lookupTime, nil
	} else if !fallbackIPProtocol {
		level.Error(logger).Log("msg", "Resolution with IP protocol failed", "err", err)
		return nil, 0.0, fmt.Errorf("unable to find ip; no fallback: %s", err)
	}

	level.Info(logger).Log("msg", "Resolving target address", "ip_protocol", fallbackProtocol)
	ip, err = r.resolve(ctx, target, fallbackProtocol)
	if err != nil {
		// This could happen when the domain don't have A and AAAA record (e.g.
		// only have MX record).
		level.Error(logger).Log("msg", "Resolution with IP protocol failed", "err", err)
		return nil, 0.0, fmt.Errorf("unable to find ip; exhausted fallback: %s", err)
	}
	level.Info(logger).Log("msg", "Resolved target address", "ip", ip.String())
	probeIPProtocolGauge.Set(protocolToGauge[fallbackProtocol])
	probeIPAddrHash.Set(ipHash(ip.IP))
	return ip, lookupTime, nil
}

func ipHash(ip net.IP) float64 {
	h := fnv.New32a()
	h.Write(ip)
	return float64(h.Sum32())
}

// Package ipgeo resolves an IP address to its region using the offline
// ip2region database (bundled at build time). No network access is performed.
package ipgeo

import (
	_ "embed"
	"net"
	"strings"
	"sync"

	"github.com/lionsoul2014/ip2region/binding/golang/xdb"
)

//go:embed ip2region.xdb
var xdbBuffer []byte

// Region is an IP's resolved location. Any field may be empty.
type Region struct {
	Country  string `json:"country"`
	Province string `json:"province"`
	City     string `json:"city"`
	ISP      string `json:"isp"`
}

var (
	once     sync.Once
	searcher *xdb.Searcher
	cacheMu  sync.RWMutex
	cache    = make(map[string]Region)
)

func initSearcher() {
	s, err := xdb.NewWithBuffer(xdb.IPv4, xdbBuffer)
	if err == nil {
		searcher = s
	}
}

// Lookup resolves an IPv4 address. ok is false for IPv6, invalid, or unknown
// addresses. Results are cached in memory for the process lifetime.
func Lookup(ip string) (Region, bool) {
	ip = strings.TrimSpace(ip)
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil {
		return Region{}, false
	}
	once.Do(initSearcher)
	if searcher == nil {
		return Region{}, false
	}
	cacheMu.RLock()
	r, hit := cache[ip]
	cacheMu.RUnlock()
	if hit {
		return r, r != (Region{})
	}
	raw, err := searcher.Search(ip)
	if err != nil {
		r = Region{}
	} else {
		r = parseRegion(raw)
	}
	cacheMu.Lock()
	cache[ip] = r
	cacheMu.Unlock()
	return r, r != (Region{})
}

func parseRegion(raw string) Region {
	clean := func(s string) string {
		s = strings.TrimSpace(s)
		if s == "0" {
			return ""
		}
		return s
	}
	parts := strings.Split(raw, "|")
	switch len(parts) {
	case 5: // 国家|省|市|ISP|国家码
		return Region{Country: clean(parts[0]), Province: clean(parts[1]), City: clean(parts[2]), ISP: clean(parts[3])}
	case 4: // 国家|省|市|ISP
		return Region{Country: clean(parts[0]), Province: clean(parts[1]), City: clean(parts[2]), ISP: clean(parts[3])}
	default:
		return Region{Country: clean(raw)}
	}
}

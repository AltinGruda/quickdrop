package server

import (
	"net"
	"runtime"
	"sort"
	"strings"
)

// IPCandidate is one plausible LAN address the phone could reach this PC with.
type IPCandidate struct {
	IP      string
	Iface   string
	Name    string
	Private bool
	Score   int
	Reason  string
}

// EnumerateCandidates lists usable LAN IPs, most likely first. Candidates are
// the up, non-loopback, non-point-to-point interfaces (the last filter drops
// most VPN virtual adapters); among those we prefer private RFC1918 addresses
// whose interface name looks like Wi-Fi or Ethernet for the current OS.
func EnumerateCandidates() ([]IPCandidate, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var cands []IPCandidate
	for _, iface := range ifs {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if iface.Flags&net.FlagPointToPoint != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ip, _, err := net.ParseCIDR(a.String())
			if err != nil {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil {
				cands = append(cands, classify(ip4, iface))
			}
		}
	}
	if len(cands) == 0 {
		return nil, ErrNoNetwork
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].Score > cands[j].Score })
	return cands, nil
}

func classify(ip net.IP, iface net.Interface) IPCandidate {
	c := IPCandidate{IP: ip.String(), Iface: iface.Name}
	c.Private = isPrivateIP(ip)

	reasons := []string{}
	if c.Private {
		c.Score += 4
		reasons = append(reasons, "private RFC1918 address")
	}
	if isLikelyWifiOrEthernet(iface.Name, ip) {
		c.Score += 2
		reasons = append(reasons, "interface name looks like Wi-Fi/Ethernet")
	}
	if !c.Private {
		reasons = append(reasons, "public/routed address")
	}
	if iface.Flags&net.FlagPointToPoint != 0 {
		reasons = append(reasons, "point-to-point")
	}
	c.Reason = strings.Join(reasons, "; ")
	return c
}

// isLikelyWifiOrEthernet matches the interface names Windows/macOS/Linux give to
// real Wi-Fi and Ethernet adapters, so VPN, VM, and virtual adapters rank lower.
func isLikelyWifiOrEthernet(name string, ip net.IP) bool {
	lower := strings.ToLower(name)
	switch runtime.GOOS {
	case "windows":
		for _, pat := range []string{"wi-fi", "wifi", "ethernet", "wireless", "wlan", "lan"} {
			if strings.Contains(lower, pat) {
				return true
			}
		}
	case "darwin":
		return strings.HasPrefix(lower, "en") && len(lower) <= 4
	default:
		for _, pat := range []string{"enp", "en", "eth", "wl", "wlan"} {
			if strings.HasPrefix(lower, pat) {
				return true
			}
		}
	}
	_ = ip
	return false
}

// isPrivateIP reports whether ip falls in the RFC1918 private ranges.
func isPrivateIP(ip net.IP) bool {
	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 10:
			return true
		case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31:
			return true
		case ip4[0] == 192 && ip4[1] == 168:
			return true
		}
	}
	return false
}
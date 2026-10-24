// Relay modifications, 2026. SPDX-License-Identifier: Apache-2.0
package collector

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ParseBackend accepts one origin, with no path, credentials, query or fragment.
// IP literals work with either transport; HTTPS certificates must cover the IP.
func ParseBackend(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "https" && u.Scheme != "http") ||
		u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" ||
		strings.ContainsAny(u.Host, " \\@\t\r\n") || strings.HasSuffix(u.Host, ":") {
		return nil, fmt.Errorf("backend must be http://IP[:port] or https://hostname[:port], without path or credentials")
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("backend port must be 1..65535")
		}
	}
	return u, nil
}

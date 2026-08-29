package report

import (
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

var nonPublicEvidencePrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("2001:db8::/32"),
}

func publicEvidenceURL(value *url.URL) bool {
	if value == nil || !strings.EqualFold(value.Scheme, "https") || value.User != nil ||
		value.Host == "" || (value.Port() != "" && value.Port() != "443") {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(value.Hostname()), ".")
	if internalEvidenceHostname(host) {
		return false
	}
	if address, err := netip.ParseAddr(host); err == nil {
		return publicEvidenceAddress(address)
	}
	return !legacyEvidenceIPv4(host) && validEvidenceHostname(host)
}

func internalEvidenceHostname(host string) bool {
	return host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") ||
		strings.HasSuffix(host, ".lan") || strings.HasSuffix(host, ".home") ||
		strings.HasSuffix(host, ".home.arpa") || strings.HasSuffix(host, ".corp")
}

func publicEvidenceAddress(value netip.Addr) bool {
	if value.Is4In6() {
		value = value.Unmap()
	}
	if !value.IsGlobalUnicast() || value.IsPrivate() || value.IsLoopback() ||
		value.IsLinkLocalUnicast() || value.IsLinkLocalMulticast() || value.IsUnspecified() {
		return false
	}
	for _, prefix := range nonPublicEvidencePrefixes {
		if prefix.Contains(value) {
			return false
		}
	}
	return true
}

func legacyEvidenceIPv4(host string) bool {
	parts := strings.Split(host, ".")
	if len(parts) == 0 || len(parts) > 4 {
		return false
	}
	for _, part := range parts {
		if !legacyEvidenceIPv4Part(part) {
			return false
		}
	}
	return true
}

func legacyEvidenceIPv4Part(value string) bool {
	if value == "0" {
		return true
	}
	digits, base := value, 10
	if strings.HasPrefix(value, "0x") || strings.HasPrefix(value, "0X") {
		digits, base = value[2:], 16
	} else if len(value) > 1 && value[0] == '0' {
		digits, base = value[1:], 8
	}
	if digits == "" {
		return false
	}
	_, err := strconv.ParseUint(digits, base, 64)
	return err == nil
}

func validEvidenceHostname(value string) bool {
	if !strings.Contains(value, ".") || len(value) > 253 {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if !validEvidenceHostnameLabel(label) {
			return false
		}
	}
	return true
}

func validEvidenceHostnameLabel(value string) bool {
	if len(value) == 0 || len(value) > 63 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
			return false
		}
	}
	return true
}

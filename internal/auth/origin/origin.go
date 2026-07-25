// Package origin owns canonical browser Origin validation shared by auth configuration and HTTP.
package origin

import (
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// Canonical returns the unique browser Origin form for an HTTP(S) origin.
func Canonical(value string) (string, bool) {
	if value == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n") {
		return "", false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" ||
		parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", false
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	hostname := parsed.Hostname()
	if hostname == "" || strings.Contains(hostname, "%") {
		return "", false
	}
	if !canonicalBrowserHost(hostname) {
		return "", false
	}
	port := parsed.Port()
	expectedHost := hostname
	if strings.Contains(hostname, ":") {
		expectedHost = "[" + hostname + "]"
	}
	if port != "" {
		portNumber, parseErr := strconv.ParseUint(port, 10, 16)
		if parseErr != nil || portNumber == 0 {
			return "", false
		}
		expectedHost = net.JoinHostPort(hostname, port)
		port = strconv.FormatUint(portNumber, 10)
	}
	if parsed.Host != expectedHost {
		return "", false
	}

	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	hostname = strings.ToLower(hostname)
	canonicalHost := hostname
	if port != "" {
		canonicalHost = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		canonicalHost = "[" + hostname + "]"
	}
	return scheme + "://" + canonicalHost, true
}

// canonicalBrowserHost 拒绝浏览器会序列化为其他 Origin 的主机写法。
func canonicalBrowserHost(hostname string) bool {
	for _, value := range hostname {
		if value > 0x7f {
			return false
		}
	}
	if canonical, candidate := canonicalBrowserIPv4(hostname); candidate {
		return hostname == canonical
	}
	if browserIPv4Candidate(hostname) {
		return false
	}
	address, err := netip.ParseAddr(hostname)
	if err != nil || !address.Is6() {
		return true
	}
	return hostname == browserIPv6(address)
}

// canonicalBrowserIPv4 按浏览器 URL 解析使用的 WHATWG IPv4 数字语法生成规范地址。
func canonicalBrowserIPv4(hostname string) (string, bool) {
	parts := strings.Split(hostname, ".")
	if len(parts) > 1 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 || len(parts) > 4 {
		return "", false
	}
	numbers := make([]uint64, len(parts))
	for index, part := range parts {
		value, ok := browserIPv4Number(part)
		if !ok || (index < len(parts)-1 && value > 255) {
			return "", false
		}
		numbers[index] = value
	}
	lastLimit := uint64(1) << (8 * (5 - len(parts)))
	if numbers[len(numbers)-1] >= lastLimit {
		return "", false
	}
	address := numbers[len(numbers)-1]
	for index := 0; index < len(numbers)-1; index++ {
		address += numbers[index] << (8 * (3 - index))
	}
	return netip.AddrFrom4([4]byte{
		byte(address >> 24),
		byte(address >> 16),
		byte(address >> 8),
		byte(address),
	}).String(), true
}

// browserIPv4Candidate 按浏览器的 ends-in-a-number 规则识别 IPv4 候选主机。
func browserIPv4Candidate(hostname string) bool {
	parts := strings.Split(hostname, ".")
	if len(parts) > 1 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return len(parts) > 0 && isBrowserIPv4Number(parts[len(parts)-1])
}

func isBrowserIPv4Number(value string) bool {
	_, ok := browserIPv4Number(value)
	return ok
}

// browserIPv4Number 解析浏览器 URL 允许的十进制、八进制或十六进制 IPv4 数字分段。
func browserIPv4Number(value string) (uint64, bool) {
	if value == "" {
		return 0, false
	}
	base := 10
	digits := value
	if len(value) >= 2 && value[0] == '0' {
		base = 8
		digits = value[1:]
		if value[1] == 'x' || value[1] == 'X' {
			base = 16
			digits = value[2:]
		}
	}
	if digits == "" {
		return 0, true
	}
	parsed, err := strconv.ParseUint(digits, base, 32)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

// browserIPv6 以浏览器 Origin 使用的压缩十六进制形式序列化 IPv6 地址。
func browserIPv6(address netip.Addr) string {
	parts := address.As16()
	groups := make([]uint16, len(parts)/2)
	for index := range groups {
		groups[index] = uint16(parts[index*2])<<8 | uint16(parts[index*2+1])
	}

	bestStart, bestLength := -1, 0
	for index := 0; index < len(groups); {
		if groups[index] != 0 {
			index++
			continue
		}
		end := index
		for end < len(groups) && groups[end] == 0 {
			end++
		}
		if length := end - index; length > bestLength && length > 1 {
			bestStart, bestLength = index, length
		}
		index = end
	}

	var builder strings.Builder
	for index := 0; index < len(groups); index++ {
		if index == bestStart {
			builder.WriteString("::")
			index += bestLength - 1
			continue
		}
		if builder.Len() > 0 && !strings.HasSuffix(builder.String(), ":") {
			builder.WriteByte(':')
		}
		builder.WriteString(strconv.FormatUint(uint64(groups[index]), 16))
	}
	return builder.String()
}

// IsCanonical reports whether value is already in the unique Origin form.
func IsCanonical(value string) bool {
	canonical, ok := Canonical(value)
	return ok && canonical == value
}

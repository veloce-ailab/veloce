package service

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
)

var ErrUnsafeURL = errors.New("target URL is blocked by SSRF protection")

type URLGuardOptions struct {
	AllowPrivateNetworks bool
	AllowedHosts         []string
	Resolve              bool
}

func ValidateConfiguredHTTPURL(raw string) error {
	if !SSRFProtectionEnabled() {
		return nil
	}
	return ValidateOutboundHTTPURL(raw, CurrentURLGuardOptions())
}

func ValidateConfiguredTCPAddress(raw string) error {
	if !SSRFProtectionEnabled() {
		return nil
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	return validateOutboundHost(host, CurrentURLGuardOptions())
}

func ValidateConfiguredStatusTarget(target string, checkType string) error {
	if strings.EqualFold(strings.TrimSpace(checkType), StatusCheckTCP) {
		address, err := statusTCPGuardAddress(target)
		if err != nil {
			return err
		}
		return ValidateConfiguredTCPAddress(address)
	}
	return ValidateConfiguredHTTPURL(target)
}

func statusTCPGuardAddress(target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", errors.New("tcp target is required")
	}
	defaultPort := ""
	if parsed, err := url.Parse(target); err == nil && parsed.Host != "" {
		target = parsed.Host
		switch parsed.Scheme {
		case "http":
			defaultPort = "80"
		case "https":
			defaultPort = "443"
		}
	}
	if _, _, err := net.SplitHostPort(target); err == nil {
		return target, nil
	}
	if defaultPort == "" {
		return "", errors.New("tcp target must include a port")
	}
	return net.JoinHostPort(target, defaultPort), nil
}

func ValidateOutboundHTTPURL(raw string, options URLGuardOptions) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("invalid URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("URL must use http or https")
	}
	return validateOutboundHost(parsed.Hostname(), options)
}

func CurrentURLGuardOptions() URLGuardOptions {
	return URLGuardOptions{
		AllowPrivateNetworks: settingBool("ssrf_allow_private_networks", false),
		AllowedHosts:         parseDelimitedList(model.GetSystemSetting("ssrf_allowed_hosts", "")),
		Resolve:              true,
	}
}

func SSRFProtectionEnabled() bool {
	return settingBool("ssrf_protection_enabled", true)
}

// validateOutboundHost rejects hosts that resolve into the deployment's own
// network. Names are resolved so that a public hostname pointing at a private
// address cannot be used to reach internal services.
func validateOutboundHost(host string, options URLGuardOptions) error {
	host = normalizeGuardHost(host)
	if host == "" {
		return errors.New("host is required")
	}
	if options.AllowPrivateNetworks || hostAllowed(host, options.AllowedHosts) {
		return nil
	}
	if blockedHostname(host) {
		return ErrUnsafeURL
	}
	if ip := net.ParseIP(host); ip != nil {
		if unsafeIP(ip) {
			return ErrUnsafeURL
		}
		return nil
	}
	if !options.Resolve {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return err
	}
	if len(ips) == 0 {
		return errors.New("host did not resolve")
	}
	for _, resolved := range ips {
		if unsafeIP(resolved.IP) {
			return ErrUnsafeURL
		}
	}
	return nil
}

func normalizeGuardHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	host = strings.TrimSuffix(host, ".")
	if unescaped, err := url.QueryUnescape(host); err == nil {
		host = unescaped
	}
	return strings.Trim(host, "[]")
}

func hostAllowed(host string, allowedHosts []string) bool {
	host = normalizeGuardHost(host)
	for _, allowed := range allowedHosts {
		allowed = normalizeGuardHost(allowed)
		if allowed == "" {
			continue
		}
		if host == allowed {
			return true
		}
		if strings.HasPrefix(allowed, "*.") && strings.HasSuffix(host, strings.TrimPrefix(allowed, "*")) {
			return true
		}
	}
	return false
}

func blockedHostname(host string) bool {
	return host == "localhost" || strings.HasSuffix(host, ".localhost")
}

func unsafeIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast()
}

// GuardedRedirectPolicy re-validates every hop of a redirect chain. Validating
// only the initially configured URL is not enough: a host that passes the guard
// can answer with a redirect to localhost or a cloud metadata endpoint, and
// net/http follows it without consulting the guard again. Credentials are
// stripped when the redirect leaves the original host so custom headers cannot
// be replayed against an internal service.
func GuardedRedirectPolicy() func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if err := ValidateConfiguredHTTPURL(req.URL.String()); err != nil {
			return err
		}
		if len(via) > 0 && !sameGuardedHost(via[0].URL, req.URL) {
			req.Header.Del("Authorization")
			req.Header.Del("Proxy-Authorization")
			req.Header.Del("Cookie")
			req.Header.Del("X-Api-Key")
			req.Header.Del("Api-Key")
		}
		return nil
	}
}

func sameGuardedHost(first, second *url.URL) bool {
	if first == nil || second == nil {
		return false
	}
	return strings.EqualFold(first.Hostname(), second.Hostname())
}

func validateHTTPURLSyntax(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("invalid URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("URL must use http or https")
	}
	return nil
}

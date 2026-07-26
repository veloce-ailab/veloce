package service

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
)

var ErrUnsafeURL = errors.New("target URL is blocked by SSRF protection")

type URLGuardOptions struct {
	AllowPrivateNetworks bool
	AllowedHosts         []string
	Resolve              bool
}

type URLGuardHooks struct {
	ValidateConfiguredHTTPURL    func(raw string) error
	ValidateConfiguredTCPAddress func(raw string) error
	ValidateConfiguredStatus     func(target string, checkType string) error
	ValidateOutboundHTTPURL      func(raw string, options URLGuardOptions) error
	CurrentOptions               func() URLGuardOptions
	Enabled                      func() bool
}

var urlGuardHooks URLGuardHooks

func RegisterURLGuardHooks(hooks URLGuardHooks) {
	urlGuardHooks = hooks
}

func ValidateConfiguredHTTPURL(raw string) error {
	if urlGuardHooks.ValidateConfiguredHTTPURL != nil {
		return urlGuardHooks.ValidateConfiguredHTTPURL(raw)
	}
	return validateHTTPURLSyntax(raw)
}

func ValidateConfiguredTCPAddress(raw string) error {
	if urlGuardHooks.ValidateConfiguredTCPAddress != nil {
		return urlGuardHooks.ValidateConfiguredTCPAddress(raw)
	}
	_, _, err := net.SplitHostPort(strings.TrimSpace(raw))
	return err
}

func ValidateConfiguredStatusTarget(target string, checkType string) error {
	if urlGuardHooks.ValidateConfiguredStatus != nil {
		return urlGuardHooks.ValidateConfiguredStatus(target, checkType)
	}
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
	if urlGuardHooks.ValidateOutboundHTTPURL != nil {
		return urlGuardHooks.ValidateOutboundHTTPURL(raw, options)
	}
	return validateHTTPURLSyntax(raw)
}

func CurrentURLGuardOptions() URLGuardOptions {
	if urlGuardHooks.CurrentOptions != nil {
		return urlGuardHooks.CurrentOptions()
	}
	return URLGuardOptions{}
}

func SSRFProtectionEnabled() bool {
	if urlGuardHooks.Enabled != nil {
		return urlGuardHooks.Enabled()
	}
	return false
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

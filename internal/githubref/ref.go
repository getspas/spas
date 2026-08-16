package githubref

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/getspas/spas/internal/provider"
)

const ID provider.ID = "github"

type Provider struct{}

func (Provider) ID() provider.ID { return ID }

var componentPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func (Provider) Resolve(request provider.RepositoryRequest) (provider.RepositoryRef, error) {
	value := strings.TrimSpace(request.Raw)
	if value == "" {
		return provider.RepositoryRef{}, fmt.Errorf("GitHub repository is required")
	}

	switch {
	case strings.HasPrefix(value, "git@github.com:"):
		if request.Transport != "" && request.Transport != provider.SSH {
			return provider.RepositoryRef{}, fmt.Errorf("repository URL uses SSH but --transport is %q", request.Transport)
		}
		return fromPath(strings.TrimPrefix(value, "git@github.com:"), provider.SSH)
	case strings.HasPrefix(value, "ssh://"):
		parsed, err := url.Parse(value)
		if err != nil {
			return provider.RepositoryRef{}, fmt.Errorf("parse GitHub SSH URL: %w", err)
		}
		if parsed.Hostname() != "github.com" || parsed.Port() != "" ||
			parsed.User == nil || parsed.User.Username() != "git" {
			return provider.RepositoryRef{}, fmt.Errorf("current implementation supports only git@github.com SSH repositories")
		}
		if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
			return provider.RepositoryRef{}, fmt.Errorf("repository URL must not contain a query string or fragment")
		}
		if parsed.User != nil {
			if _, present := parsed.User.Password(); present {
				return provider.RepositoryRef{}, fmt.Errorf("repository URL must not contain credentials")
			}
		}
		if request.Transport != "" && request.Transport != provider.SSH {
			return provider.RepositoryRef{}, fmt.Errorf("repository URL uses SSH but --transport is %q", request.Transport)
		}
		return fromPath(strings.TrimPrefix(parsed.Path, "/"), provider.SSH)
	case strings.HasPrefix(value, "https://"):
		parsed, err := url.Parse(value)
		if err != nil {
			return provider.RepositoryRef{}, fmt.Errorf("parse GitHub HTTPS URL: %w", err)
		}
		if parsed.Hostname() != "github.com" || parsed.Port() != "" ||
			parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return provider.RepositoryRef{}, fmt.Errorf("current implementation supports credential-free https://github.com repository URLs only")
		}
		if request.Transport != "" && request.Transport != provider.HTTPS {
			return provider.RepositoryRef{}, fmt.Errorf("repository URL uses HTTPS but --transport is %q", request.Transport)
		}
		return fromPath(strings.TrimPrefix(parsed.Path, "/"), provider.HTTPS)
	default:
		if strings.Contains(value, "://") || strings.Contains(value, "@") {
			return provider.RepositoryRef{}, fmt.Errorf("current implementation supports GitHub repositories only")
		}
		if request.Transport == "" {
			request.Transport = provider.HTTPS
		}
		return fromPath(value, request.Transport)
	}
}

func fromPath(value string, transport provider.Transport) (provider.RepositoryRef, error) {
	value = strings.TrimSuffix(strings.TrimSuffix(value, "/"), ".git")
	parts := strings.Split(value, "/")
	if len(parts) != 2 || !componentPattern.MatchString(parts[0]) || !componentPattern.MatchString(parts[1]) {
		return provider.RepositoryRef{}, fmt.Errorf("GitHub repository must be OWNER/REPOSITORY")
	}
	if parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." {
		return provider.RepositoryRef{}, fmt.Errorf("invalid GitHub repository")
	}
	if transport != provider.HTTPS && transport != provider.SSH {
		return provider.RepositoryRef{}, fmt.Errorf("transport must be https or ssh")
	}
	canonical := parts[0] + "/" + parts[1]
	remoteURL := "https://github.com/" + canonical + ".git"
	if transport == provider.SSH {
		remoteURL = "git@github.com:" + canonical + ".git"
	}
	return provider.RepositoryRef{
		Provider:  ID,
		Canonical: canonical,
		Transport: transport,
		RemoteURL: remoteURL,
	}, nil
}

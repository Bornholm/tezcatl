// Package api is a minimal Kubernetes API server client: bearer token
// authentication, one-shot JSON requests, long-lived streams (watch,
// pod logs) and the canonical list+watch loop. Plain HTTP, no
// client-go involved.
package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/errors"
)

// Paths of the serviceaccount credentials mounted in every pod.
const (
	defaultTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	defaultCAFile    = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
)

const (
	requestTimeout = 30 * time.Second

	// watchTimeoutSeconds bounds every watch request server-side; the
	// list+watch loop then re-lists, which keeps the caller's state
	// fresh even when individual watch events were missed.
	watchTimeoutSeconds = "300"

	listWatchInitialBackoff = time.Second
	listWatchMaxBackoff     = 30 * time.Second
)

type Config struct {
	// Server is the API server base URL (works with kubectl proxy:
	// http://127.0.0.1:8001). When set alongside Kubeconfig, it
	// overrides the kubeconfig's server, like kubectl --server.
	Server string
	// Kubeconfig is a kubectl configuration file providing server and
	// credentials; Context picks a context (default: current-context).
	// When neither Server nor Kubeconfig is set, the client tries
	// in-cluster autodetection (KUBERNETES_SERVICE_HOST/PORT and the
	// mounted serviceaccount), then $KUBECONFIG and ~/.kube/config.
	Kubeconfig string
	Context    string
	// Token is a static bearer token; TokenFile is re-read at every
	// request (serviceaccount tokens are rotated by the kubelet).
	Token              string
	TokenFile          string
	CAFile             string
	InsecureSkipVerify bool
}

// settings are the resolved connection parameters, whatever their
// origin (explicit config, kubeconfig file, in-cluster).
type settings struct {
	server     string
	token      string
	tokenFile  string
	ca         []byte
	clientCert *tls.Certificate
	insecure   bool
}

type Client struct {
	base      string
	client    *http.Client
	token     string
	tokenFile string
}

func NewClient(cfg *Config) (*Client, error) {
	resolved, err := resolve(cfg)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	tlsConfig := &tls.Config{InsecureSkipVerify: resolved.insecure}

	if len(resolved.ca) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(resolved.ca) {
			return nil, errors.New("no certificate found in the configured CA bundle")
		}

		tlsConfig.RootCAs = pool
	}

	if resolved.clientCert != nil {
		tlsConfig.Certificates = []tls.Certificate{*resolved.clientCert}
	}

	return &Client{
		base: strings.TrimSuffix(resolved.server, "/"),
		client: &http.Client{
			// No overall timeout: watch and log streams are long-lived.
			Transport: &http.Transport{
				TLSClientConfig:       tlsConfig,
				ResponseHeaderTimeout: requestTimeout,
			},
		},
		token:     resolved.token,
		tokenFile: resolved.tokenFile,
	}, nil
}

// resolve picks the connection source: an explicit kubeconfig, an
// explicit server, the in-cluster serviceaccount, then the default
// kubeconfig locations ($KUBECONFIG, ~/.kube/config). Explicit fields
// (server, token, CA…) overlay whatever a kubeconfig provides, like
// the matching kubectl flags.
func resolve(cfg *Config) (*settings, error) {
	if cfg.Kubeconfig != "" {
		resolved, err := loadKubeconfig(expandHome(cfg.Kubeconfig), cfg.Context)
		if err != nil {
			return nil, errors.WithStack(err)
		}

		return overlay(resolved, cfg)
	}

	if cfg.Server != "" {
		return overlay(&settings{}, cfg)
	}

	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := os.Getenv("KUBERNETES_SERVICE_PORT")
	if host != "" && port != "" {
		resolved := &settings{
			server:    "https://" + net.JoinHostPort(host, port),
			tokenFile: defaultTokenFile,
		}

		ca, err := os.ReadFile(defaultCAFile)
		if err != nil {
			return nil, errors.Wrapf(err, "could not read CA file %q", defaultCAFile)
		}
		resolved.ca = ca

		return overlay(resolved, cfg)
	}

	if path := defaultKubeconfigPath(); path != "" {
		resolved, err := loadKubeconfig(path, cfg.Context)
		if err != nil {
			return nil, errors.WithStack(err)
		}

		return overlay(resolved, cfg)
	}

	return nil, errors.New("no api_server or kubeconfig configured, not running in-cluster, and no kubeconfig at $KUBECONFIG or ~/.kube/config")
}

// overlay applies the explicit configuration fields on top of resolved
// settings.
func overlay(resolved *settings, cfg *Config) (*settings, error) {
	if cfg.Server != "" {
		resolved.server = cfg.Server
	}

	if cfg.Token != "" {
		resolved.token = cfg.Token
		resolved.tokenFile = ""
	}

	if cfg.TokenFile != "" {
		resolved.tokenFile = cfg.TokenFile
		resolved.token = ""
	}

	if cfg.CAFile != "" {
		ca, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, errors.Wrapf(err, "could not read CA file %q", cfg.CAFile)
		}
		resolved.ca = ca
	}

	if cfg.InsecureSkipVerify {
		resolved.insecure = true
	}

	if resolved.server == "" {
		return nil, errors.New("no API server URL resolved")
	}

	return resolved, nil
}

// expandHome expands a leading ~/ (no shell is involved when the path
// comes from the plugin's JSON configuration).
func expandHome(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}

	return filepath.Join(home, path[2:])
}

// defaultKubeconfigPath returns the first existing kubeconfig among
// the $KUBECONFIG entries then ~/.kube/config, or empty.
func defaultKubeconfigPath() string {
	paths := []string{}

	if env := os.Getenv("KUBECONFIG"); env != "" {
		paths = strings.Split(env, string(os.PathListSeparator))
	}

	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".kube", "config"))
	}

	for _, path := range paths {
		if path == "" {
			continue
		}

		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}

	return ""
}

func (c *Client) bearer() (string, error) {
	if c.token != "" {
		return c.token, nil
	}

	if c.tokenFile == "" {
		return "", nil
	}

	raw, err := os.ReadFile(c.tokenFile)
	if err != nil {
		return "", errors.WithStack(err)
	}

	return strings.TrimSpace(string(raw)), nil
}

func (c *Client) do(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	token, err := c.bearer()
	if err != nil {
		return nil, errors.WithStack(err)
	}

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	res, err := c.client.Do(req)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		res.Body.Close()

		return nil, errors.Errorf("unexpected status %d for %s: %s", res.StatusCode, path, strings.TrimSpace(string(body)))
	}

	return res, nil
}

// Get performs a one-shot JSON request.
func (c *Client) Get(ctx context.Context, path string, into any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	res, err := c.do(ctx, path)
	if err != nil {
		return errors.WithStack(err)
	}
	defer res.Body.Close()

	if err := json.NewDecoder(res.Body).Decode(into); err != nil {
		return errors.WithStack(err)
	}

	return nil
}

// Stream opens a long-lived request (watch, pod logs with follow) and
// returns its body; it lives until closed or the context is canceled.
func (c *Client) Stream(ctx context.Context, path string) (io.ReadCloser, error) {
	res, err := c.do(ctx, path)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return res.Body, nil
}

// Watch event types, as sent by the API server.
const (
	Added    = "ADDED"
	Modified = "MODIFIED"
	Deleted  = "DELETED"
	Bookmark = "BOOKMARK"
	Error    = "ERROR"
)

// WatchEvent is one frame of a watch stream.
type WatchEvent struct {
	Type   string          `json:"type"`
	Object json.RawMessage `json:"object"`
}

// ObjectMeta is the subset of Kubernetes object metadata the plugin
// uses.
type ObjectMeta struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	UID               string            `json:"uid"`
	ResourceVersion   string            `json:"resourceVersion"`
	CreationTimestamp string            `json:"creationTimestamp"`
	Generation        int64             `json:"generation"`
	Labels            map[string]string `json:"labels"`
	OwnerReferences   []OwnerReference  `json:"ownerReferences"`
}

type OwnerReference struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Controller bool   `json:"controller"`
}

// ListWatch runs the canonical Kubernetes consumption loop against one
// collection: list to acquire a resource version, watch from it, hand
// every ADDED/MODIFIED/DELETED frame to onEvent, and start over (with
// an exponential backoff on errors) when the watch expires. onList
// receives the raw list document after each successful list, letting
// the caller reconcile its state; watching from the list's resource
// version never replays past events. Returns only when the context is
// canceled.
func (c *Client) ListWatch(ctx context.Context, path string, query url.Values, onList func(list json.RawMessage) error, onEvent func(event *WatchEvent) error) error {
	backoff := listWatchInitialBackoff

	for {
		if ctx.Err() != nil {
			return errors.WithStack(ctx.Err())
		}

		resourceVersion, err := c.list(ctx, path, query, onList)
		if err != nil {
			slog.WarnContext(ctx, "kubernetes list failed", slog.String("path", path), slog.Any("error", err))

			if err := sleep(ctx, backoff); err != nil {
				return errors.WithStack(err)
			}
			backoff = min(backoff*2, listWatchMaxBackoff)

			continue
		}

		backoff = listWatchInitialBackoff

		if err := c.watch(ctx, path, query, resourceVersion, onEvent); err != nil {
			if ctx.Err() != nil {
				return errors.WithStack(ctx.Err())
			}

			slog.WarnContext(ctx, "kubernetes watch interrupted", slog.String("path", path), slog.Any("error", err))

			if err := sleep(ctx, backoff); err != nil {
				return errors.WithStack(err)
			}
			backoff = min(backoff*2, listWatchMaxBackoff)
		}
	}
}

func (c *Client) list(ctx context.Context, path string, query url.Values, onList func(list json.RawMessage) error) (string, error) {
	raw := json.RawMessage{}
	if err := c.Get(ctx, withQuery(path, cloneValues(query)), &raw); err != nil {
		return "", errors.WithStack(err)
	}

	meta := struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
	}{}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return "", errors.WithStack(err)
	}

	if onList != nil {
		if err := onList(raw); err != nil {
			return "", errors.WithStack(err)
		}
	}

	return meta.Metadata.ResourceVersion, nil
}

func (c *Client) watch(ctx context.Context, path string, query url.Values, resourceVersion string, onEvent func(event *WatchEvent) error) error {
	values := cloneValues(query)
	values.Set("watch", "true")
	values.Set("resourceVersion", resourceVersion)
	values.Set("timeoutSeconds", watchTimeoutSeconds)

	body, err := c.Stream(ctx, withQuery(path, values))
	if err != nil {
		return errors.WithStack(err)
	}
	defer body.Close()

	decoder := json.NewDecoder(body)

	for {
		event := &WatchEvent{}
		if err := decoder.Decode(event); err != nil {
			// A clean EOF is the server-side timeout: re-list, no backoff
			// needed.
			if errors.Is(err, io.EOF) {
				return nil
			}

			return errors.WithStack(err)
		}

		switch event.Type {
		case Bookmark:
			continue
		case Error:
			// Typically 410 Gone: the resource version expired.
			return errors.Errorf("watch error: %s", string(event.Object))
		default:
			if err := onEvent(event); err != nil {
				return errors.WithStack(err)
			}
		}
	}
}

func withQuery(path string, values url.Values) string {
	if len(values) == 0 {
		return path
	}

	return path + "?" + values.Encode()
}

func cloneValues(query url.Values) url.Values {
	values := url.Values{}
	for key, entries := range query {
		values[key] = entries
	}

	return values
}

func sleep(ctx context.Context, duration time.Duration) error {
	select {
	case <-time.After(duration):
		return nil
	case <-ctx.Done():
		return errors.WithStack(ctx.Err())
	}
}

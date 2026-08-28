package api

import (
	"crypto/tls"
	"encoding/base64"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

// kubeconfig is the subset of the kubectl configuration file format
// the plugin understands: static tokens, token files and client
// certificates, inline (base64 data) or by path. Exec-based credential
// plugins and auth providers are not supported (no client-go): use a
// token, a client certificate, or kubectl proxy with api_server.
type kubeconfig struct {
	CurrentContext string `yaml:"current-context"`
	Clusters       []struct {
		Name    string `yaml:"name"`
		Cluster struct {
			Server                   string `yaml:"server"`
			CertificateAuthority     string `yaml:"certificate-authority"`
			CertificateAuthorityData string `yaml:"certificate-authority-data"`
			InsecureSkipTLSVerify    bool   `yaml:"insecure-skip-tls-verify"`
		} `yaml:"cluster"`
	} `yaml:"clusters"`
	Contexts []struct {
		Name    string `yaml:"name"`
		Context struct {
			Cluster string `yaml:"cluster"`
			User    string `yaml:"user"`
		} `yaml:"context"`
	} `yaml:"contexts"`
	Users []struct {
		Name string `yaml:"name"`
		User kubeconfigUser
	} `yaml:"users"`
}

type kubeconfigUser struct {
	Token                 string         `yaml:"token"`
	TokenFile             string         `yaml:"tokenFile"`
	ClientCertificate     string         `yaml:"client-certificate"`
	ClientCertificateData string         `yaml:"client-certificate-data"`
	ClientKey             string         `yaml:"client-key"`
	ClientKeyData         string         `yaml:"client-key-data"`
	Exec                  map[string]any `yaml:"exec"`
	AuthProvider          map[string]any `yaml:"auth-provider"`
}

// loadKubeconfig resolves one context of a kubeconfig file into
// connection settings. contextName empty means current-context.
// Relative paths (CA, certificates, token file) are resolved against
// the kubeconfig's directory, like kubectl does.
func loadKubeconfig(path string, contextName string) (*settings, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.Wrapf(err, "could not read kubeconfig %q", path)
	}

	cfg := kubeconfig{}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, errors.Wrapf(err, "malformed kubeconfig %q", path)
	}

	if contextName == "" {
		contextName = cfg.CurrentContext
	}
	if contextName == "" {
		return nil, errors.Errorf("kubeconfig %q has no current-context; set the context option", path)
	}

	clusterName := ""
	userName := ""
	for _, context := range cfg.Contexts {
		if context.Name == contextName {
			clusterName = context.Context.Cluster
			userName = context.Context.User
			break
		}
	}
	if clusterName == "" {
		return nil, errors.Errorf("no context %q in kubeconfig %q", contextName, path)
	}

	resolved := &settings{}
	baseDir := filepath.Dir(path)

	found := false
	for _, cluster := range cfg.Clusters {
		if cluster.Name != clusterName {
			continue
		}
		found = true

		resolved.server = cluster.Cluster.Server
		resolved.insecure = cluster.Cluster.InsecureSkipTLSVerify

		ca, err := inlineOrFile(cluster.Cluster.CertificateAuthorityData, cluster.Cluster.CertificateAuthority, baseDir)
		if err != nil {
			return nil, errors.Wrapf(err, "could not load the CA of cluster %q", clusterName)
		}
		resolved.ca = ca
	}
	if !found {
		return nil, errors.Errorf("no cluster %q in kubeconfig %q", clusterName, path)
	}

	if resolved.server == "" {
		return nil, errors.Errorf("cluster %q in kubeconfig %q has no server", clusterName, path)
	}

	// A context without user is valid (anonymous, or kubectl proxy).
	for _, user := range cfg.Users {
		if user.Name != userName || userName == "" {
			continue
		}

		if err := applyUser(resolved, &user.User, baseDir); err != nil {
			return nil, errors.Wrapf(err, "could not load the credentials of user %q", userName)
		}
	}

	return resolved, nil
}

func applyUser(resolved *settings, user *kubeconfigUser, baseDir string) error {
	resolved.token = user.Token

	if user.TokenFile != "" {
		resolved.tokenFile = resolvePath(user.TokenFile, baseDir)
	}

	cert, err := inlineOrFile(user.ClientCertificateData, user.ClientCertificate, baseDir)
	if err != nil {
		return errors.WithStack(err)
	}

	key, err := inlineOrFile(user.ClientKeyData, user.ClientKey, baseDir)
	if err != nil {
		return errors.WithStack(err)
	}

	if len(cert) > 0 || len(key) > 0 {
		pair, err := tls.X509KeyPair(cert, key)
		if err != nil {
			return errors.Wrap(err, "malformed client certificate")
		}

		resolved.clientCert = &pair
	}

	hasCredentials := resolved.token != "" || resolved.tokenFile != "" || resolved.clientCert != nil
	if !hasCredentials && (user.Exec != nil || user.AuthProvider != nil) {
		return errors.New("exec/auth-provider credentials are not supported: use a token, a client certificate, or kubectl proxy with api_server")
	}

	return nil
}

// inlineOrFile returns the base64 inline data when present, the file
// content otherwise (path resolved against the kubeconfig directory),
// or nothing.
func inlineOrFile(data string, path string, baseDir string) ([]byte, error) {
	if data != "" {
		decoded, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return nil, errors.Wrap(err, "malformed base64 data")
		}

		return decoded, nil
	}

	if path == "" {
		return nil, nil
	}

	content, err := os.ReadFile(resolvePath(path, baseDir))
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return content, nil
}

func resolvePath(path string, baseDir string) string {
	if filepath.IsAbs(path) {
		return path
	}

	return filepath.Join(baseDir, path)
}

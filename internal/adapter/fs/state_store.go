package fs

import (
	"context"
	"net/url"
	"os"
	"path/filepath"

	"github.com/bornholm/tezcatl/internal/core/port"
	"github.com/pkg/errors"
)

// StateStore persists opaque state blobs as files in a directory, with
// atomic writes (temporary file + rename).
type StateStore struct {
	dir string
}

func NewStateStore(dir string) (*StateStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, errors.WithStack(err)
	}

	return &StateStore{dir: dir}, nil
}

func (s *StateStore) Save(ctx context.Context, key string, data []byte) error {
	path := s.path(key)

	tmp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return errors.WithStack(err)
	}

	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return errors.WithStack(err)
	}

	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return errors.WithStack(err)
	}

	if err := tmp.Close(); err != nil {
		return errors.WithStack(err)
	}

	if err := os.Rename(tmp.Name(), path); err != nil {
		return errors.WithStack(err)
	}

	return nil
}

func (s *StateStore) Load(ctx context.Context, key string) ([]byte, error) {
	data, err := os.ReadFile(s.path(key))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.WithStack(port.ErrStateNotFound)
		}

		return nil, errors.WithStack(err)
	}

	return data, nil
}

func (s *StateStore) Close() error {
	return nil
}

func (s *StateStore) path(key string) string {
	return filepath.Join(s.dir, url.PathEscape(key)+".state")
}

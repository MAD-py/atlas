//go:build unix

package file

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"
)

type LockSuite struct {
	suite.Suite
}

func TestLock(t *testing.T) {
	suite.Run(t, new(LockSuite))
}

func (s *LockSuite) path() string {
	return filepath.Join(s.T().TempDir(), "test.db")
}

func (s *LockSuite) TestLock_SecondHolderFails() {
	path := s.path()
	f1, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	s.Require().NoError(err)
	defer f1.Close()
	s.Require().NoError(Lock(f1))

	f2, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	s.Require().NoError(err)
	defer f2.Close()
	s.ErrorIs(Lock(f2), ErrLocked)
}

func (s *LockSuite) TestUnlock_AllowsAnotherHolder() {
	path := s.path()
	f1, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	s.Require().NoError(err)
	s.Require().NoError(Lock(f1))
	s.Require().NoError(Unlock(f1))
	s.Require().NoError(f1.Close())

	f2, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	s.Require().NoError(err)
	defer f2.Close()
	s.Require().NoError(Lock(f2))
}

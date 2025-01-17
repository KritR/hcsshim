//go:build linux
// +build linux

package runc

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/Microsoft/hcsshim/internal/guest/runtime"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

// readPidFile reads the integer pid stored in the given file.
func (r *runcRuntime) readPidFile(pidFile string) (pid int, err error) {
	data, err := ioutil.ReadFile(pidFile)
	if err != nil {
		return -1, errors.Wrap(err, "failed reading from pid file")
	}
	pid, err = strconv.Atoi(string(data))
	if err != nil {
		return -1, errors.Wrapf(err, "failed converting pid text \"%s\" to integer form", data)
	}
	return pid, nil
}

// cleanupContainer cleans up any state left behind by the container.
func (r *runcRuntime) cleanupContainer(id string) error {
	containerDir := r.getContainerDir(id)
	if err := os.RemoveAll(containerDir); err != nil {
		return errors.Wrapf(err, "failed removing the container directory for container %s", id)
	}
	return nil
}

// cleanupProcess cleans up any state left behind by the process.
func (r *runcRuntime) cleanupProcess(id string, pid int) error {
	processDir := r.getProcessDir(id, pid)
	if err := os.RemoveAll(processDir); err != nil {
		return errors.Wrapf(err, "failed removing the process directory for process %d in container %s", pid, id)
	}
	return nil
}

// getProcessDir returns the path to the state directory of the given process.
func (r *runcRuntime) getProcessDir(id string, pid int) string {
	containerDir := r.getContainerDir(id)
	return filepath.Join(containerDir, strconv.Itoa(pid))
}

// getContainerDir returns the path to the state directory of the given
// container.
func (r *runcRuntime) getContainerDir(id string) string {
	return filepath.Join(containerFilesDir, id)
}

// makeContainerDir creates the state directory for the given container.
func (r *runcRuntime) makeContainerDir(id string) error {
	dir := r.getContainerDir(id)
	if err := os.MkdirAll(dir, os.ModeDir); err != nil {
		return errors.Wrapf(err, "failed making container directory for container %s", id)
	}
	return nil
}

// getLogDir gets the path to the runc logs directory.
func (r *runcRuntime) getLogDir(id string) string {
	return filepath.Join(r.runcLogBasePath, id)
}

// makeLogDir creates the runc logs directory if it doesnt exist.
func (r *runcRuntime) makeLogDir(id string) error {
	dir := r.getLogDir(id)
	if err := os.MkdirAll(dir, os.ModeDir); err != nil {
		return errors.Wrapf(err, "failed making runc log directory for container %s", id)
	}
	return nil
}

// getLogPath returns the path to the log file used by the runC wrapper for a particular container
func (r *runcRuntime) getLogPath(id string) string {
	return filepath.Join(r.getLogDir(id), "runc.log")
}

// getLogPath returns the path to the log file used by the runC wrapper.
func (r *runcRuntime) getGlobalLogPath() string {
	// runcLogBasePath should be created by r.initialize
	return filepath.Join(r.runcLogBasePath, "global-runc.log")
}

// processExists returns true if the given process exists in /proc, false if
// not.
// It should be noted that processes which have exited, but have not yet been
// waited on (i.e. zombies) are still considered to exist by this function.
func (r *runcRuntime) processExists(pid int) bool {
	_, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	return !os.IsNotExist(err)
}

type standardLogEntry struct {
	Level   logrus.Level `json:"level"`
	Message string       `json:"msg"`
	Err     error        `json:"error,omitempty"`
}

func (l *standardLogEntry) asError() (err error) {
	// TODO (helsaawy): match with errors from
	// https://github.com/opencontainers/runc/blob/master/libcontainer/error.go
	msg := l.Message

	if strings.HasPrefix(msg, "container") && strings.HasSuffix(msg, "does not exist") {
		// currently: "container <container id> does not exist"
		err = runtime.ErrContainerDoesNotExist
	} else if strings.Contains(msg, "container with id exists") ||
		strings.Contains(msg, "container with given ID already exists") {
		err = runtime.ErrContainerAlreadyExists
	} else if strings.Contains(msg, "invalid id format") ||
		strings.Contains(msg, "invalid container ID format") {
		err = runtime.ErrInvalidContainerID
	} else if strings.Contains(msg, "container") &&
		strings.Contains(msg, "that is not stopped") {
		err = runtime.ErrContainerNotStopped
	} else {
		err = errors.New(msg)
	}

	if l.Err != nil {
		err = errors.Wrapf(err, l.Err.Error())
	}

	return
}

func getRuncLogError(logPath string) error {
	reader, err := os.OpenFile(logPath, syscall.O_RDONLY, 0644)
	if err != nil {
		return nil
	}
	defer reader.Close()

	var lastErr error
	dec := json.NewDecoder(reader)
	for {
		entry := &standardLogEntry{}
		if err := dec.Decode(entry); err != nil {
			break
		}
		if entry.Level <= logrus.ErrorLevel {
			lastErr = entry.asError()
		}
	}
	return lastErr
}

func createRuncCommand(logPath string, args ...string) *exec.Cmd {
	args = append([]string{"--log", logPath, "--log-format", "json"}, args...)
	return exec.Command("runc", args...)
}

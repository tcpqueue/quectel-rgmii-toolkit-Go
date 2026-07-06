//go:build linux
// +build linux

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	tioCGPTN   = 0x80045430
	tioCSPTLCK = 0x40045431
)

func openNativeTTY(path string) (*os.File, error) {
	return openNativeTTYWithFlags(path, os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC|syscall.O_NONBLOCK, "O_RDWR")
}

func openNativeTTYReadOnly(path string) (*os.File, error) {
	return openNativeTTYWithFlags(path, os.O_RDONLY|syscall.O_NOCTTY|syscall.O_CLOEXEC|syscall.O_NONBLOCK, "O_RDONLY")
}

func openNativeTTYWriteOnly(path string) (*os.File, error) {
	return openNativeTTYWithFlags(path, os.O_WRONLY|syscall.O_NOCTTY|syscall.O_CLOEXEC|syscall.O_NONBLOCK, "O_WRONLY")
}

func openNativeTTYWithFlags(path string, flags int, label string) (*os.File, error) {
	logATDebug("open native TTY %s: %s", label, path)
	f, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.SetNonblock(int(f.Fd()), true); err != nil {
		_ = f.Close()
		return nil, err
	}
	if shouldSkipTermiosForATDevice(path) {
		logATDebug("skip termios for AT device: %s", path)
		return f, nil
	}
	if err := configureTTYRaw(f.Fd()); err != nil {
		logATDebug("termios init failed on %s: %v", path, err)
		// Some modem character devices can pass AT commands but do not fully
		// support TCGETS/TCSETS. Treat termios setup as best-effort instead
		// of rejecting an otherwise usable AT port.
		log.Printf("AT 设备 %s termios 初始化失败，继续尝试原始读写: %v", path, err)
	}
	return f, nil
}

func shouldSkipTermiosForATDevice(path string) bool {
	return isSMDATDevice(path)
}

func isSMDATDevice(path string) bool {
	base := filepath.Base(path)
	return strings.HasPrefix(base, "smd")
}

func configureTTYRaw(fd uintptr) error {
	return configureTTYRawWithEcho(fd, false)
}

func configureTTYRawWithEcho(fd uintptr, echo bool) error {
	var term syscall.Termios
	if err := ioctl(fd, syscall.TCGETS, uintptr(unsafe.Pointer(&term))); err != nil {
		return err
	}

	term.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON | syscall.IXOFF | syscall.IXANY | syscall.IMAXBEL
	term.Oflag &^= syscall.OPOST | syscall.ONLCR
	term.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	if echo {
		term.Lflag |= syscall.ECHO
	}
	term.Cflag &^= syscall.CSIZE | syscall.PARENB | syscall.CSTOPB
	term.Cflag |= syscall.CS8 | syscall.CREAD | syscall.CLOCAL
	term.Cc[syscall.VMIN] = 0
	term.Cc[syscall.VTIME] = 1
	term.Ispeed = syscall.B115200
	term.Ospeed = syscall.B115200

	return ioctl(fd, syscall.TCSETS, uintptr(unsafe.Pointer(&term)))
}

func ioctl(fd uintptr, req uintptr, arg uintptr) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, arg)
	if errno != 0 {
		return errno
	}
	return nil
}

func waitReadableFD(fd int, timeout time.Duration) (bool, error) {
	if timeout <= 0 {
		timeout = 20 * time.Millisecond
	}
	for {
		var readSet syscall.FdSet
		setFDSetBit(&readSet, fd)
		tv := syscall.NsecToTimeval(timeout.Nanoseconds())
		n, err := syscall.Select(fd+1, &readSet, nil, nil, &tv)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return false, err
		}
		return n > 0, nil
	}
}

func setFDSetBit(set *syscall.FdSet, fd int) {
	if fd < 0 || fd >= 1024 {
		return
	}
	bytes := (*[128]byte)(unsafe.Pointer(set))
	bytes[fd/8] |= 1 << uint(fd%8)
}

func drainTTY(f *os.File, maxDuration time.Duration) {
	if f == nil || maxDuration <= 0 {
		return
	}
	deadline := time.Now().Add(maxDuration)
	buf := make([]byte, 1024)
	fd := int(f.Fd())
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining > 20*time.Millisecond {
			remaining = 20 * time.Millisecond
		}
		ready, err := waitReadableFD(fd, remaining)
		if err != nil {
			logATDebug("AT drain select err=%v", err)
			return
		}
		if !ready {
			return
		}

		n, err := syscall.Read(fd, buf)
		logATDebug("AT drain chunk n=%d err=%v", n, err)
		if n > 0 {
			continue
		}
		if err != nil && isTemporaryTTYReadError(err) {
			continue
		}
		return
	}
}

func readTTYUntilIdle(f *os.File, maxDuration, idleDuration time.Duration) (string, error) {
	deadline := time.Now().Add(maxDuration)
	idleDeadline := time.Now().Add(idleDuration)
	buf := make([]byte, 1024)
	var out strings.Builder

	for time.Now().Before(deadline) {
		n, err := f.Read(buf)
		if n > 0 {
			out.Write(buf[:n])
			idleDeadline = time.Now().Add(idleDuration)
			continue
		}
		if err != nil && !isTemporaryTTYReadError(err) {
			logATDebug("AT read non-temporary err=%v current=%s", err, outputSummary(out.String()))
			if errors.Is(err, io.EOF) {
				if out.Len() > 0 {
					return out.String(), nil
				}
				// Some SMD character devices report EOF while no response is ready yet.
				// Keep waiting until the command timeout instead of treating it as a
				// terminal failure.
				time.Sleep(20 * time.Millisecond)
				continue
			}
			return out.String(), err
		}
		time.Sleep(20 * time.Millisecond)
		if out.Len() > 0 && time.Now().After(idleDeadline) {
			break
		}
	}
	return out.String(), nil
}

func isTemporaryTTYReadError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EINTR) {
		return true
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return isTemporaryTTYReadError(pathErr.Err)
	}
	return false
}

func killProcessByCmdline(needle string) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	self := os.Getpid()
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 || pid == self {
			continue
		}
		cmdlinePath := filepath.Join("/proc", entry.Name(), "cmdline")
		data, err := os.ReadFile(cmdlinePath)
		if err != nil || len(data) == 0 {
			continue
		}
		cmdline := strings.ReplaceAll(string(data), "\x00", " ")
		if strings.Contains(cmdline, needle) {
			_ = syscall.Kill(pid, syscall.SIGTERM)
			time.Sleep(200 * time.Millisecond)
			_ = syscall.Kill(pid, syscall.SIGKILL)
			log.Printf("已停止匹配 %s 的旧端口桥进程: pid=%d", needle, pid)
		}
	}
}

func lockGlobalATFile() (func(), error) {
	lockPath := os.Getenv("SIMPLEADMIN_AT_LOCK_FILE")
	if strings.TrimSpace(lockPath) == "" {
		lockPath = "/tmp/simpleadmin-go-at.lock"
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, fmt.Errorf("open AT lock file %s: %w", lockPath, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock AT lock file %s: %w", lockPath, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

func shouldRecoverBusyATDevice(device string, err error) bool {
	return false
}

func recoverBusyATDevices() {
	// Direct /dev/smd11 mode does not kill reader or bridge processes at runtime.
	time.Sleep(250 * time.Millisecond)
}

type atCommandSession struct {
	device    string
	readFile  *os.File
	writeFile *os.File
	smdReader *smdATReader
}

func openATCommandSession(path string) (*atCommandSession, error) {
	logATDebug("open AT command session: %s", path)
	if isSMDATDevice(path) {
		return openPersistentSMDATCommandSession(path)
	}
	f, err := openNativeTTY(path)
	if err != nil {
		return nil, err
	}
	return &atCommandSession{device: path, readFile: f, writeFile: f}, nil
}

func openPersistentSMDATCommandSession(path string) (*atCommandSession, error) {
	// /dev/smd11 behaves like the verified shell workflow:
	//   dd if=/dev/smd11 bs=4096 ... &
	//   payload > /dev/smd11
	// Keep one process-local reader helper open for the lifetime of the service,
	// using the same 4096-byte read size as the verified dd command. Each AT
	// payload is written through shell redirection fed from stdin, so the payload is
	// not embedded in a printf command and does not need shell escaping.
	// Response completion is decided by terminal AT lines such as OK,
	// ERROR, +CME ERROR, or +CMS ERROR; no runtime kill/rebuild loop is used.
	reader, err := getSMDATReader(path)
	if err != nil {
		return nil, err
	}
	return &atCommandSession{device: path, smdReader: reader}, nil
}

func closeATSession(session *atCommandSession) error {
	if session == nil {
		return nil
	}
	if session.smdReader != nil {
		// Keep the /dev/smd* reader alive across page polling and AT requests.
		return nil
	}
	var err error
	if session.writeFile != nil && session.writeFile != session.readFile {
		err = session.writeFile.Close()
	}
	if session.readFile != nil {
		if closeErr := session.readFile.Close(); err == nil {
			err = closeErr
		}
	}
	return err
}

func writeATSessionString(session *atCommandSession, payload string) error {
	if session == nil {
		return errors.New("AT session is not open")
	}
	if session.smdReader != nil {
		return session.smdReader.writePayload(payload)
	}
	if session.writeFile == nil {
		return errors.New("AT session writer is not open")
	}
	return writeFileWithDeadline(session.device, session.writeFile, payload, 3*time.Second)
}

func writeFileWithDeadline(device string, f *os.File, payload string, timeout time.Duration) error {
	data := []byte(payload)
	written := 0
	deadline := time.Now().Add(timeout)
	for written < len(data) {
		if time.Now().After(deadline) {
			return fmt.Errorf("AT session write timed out after %d/%d bytes", written, len(data))
		}
		n, err := f.Write(data[written:])
		logATDebug("AT write chunk device=%s n=%d err=%v progress=%d/%d", device, n, err, written+n, len(data))
		if err != nil {
			if isTemporaryTTYReadError(err) {
				time.Sleep(20 * time.Millisecond)
				continue
			}
			return err
		}
		if n <= 0 {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		written += n
	}
	return nil
}

func drainATSession(session *atCommandSession, maxDuration time.Duration) {
	if session == nil {
		return
	}
	if session.smdReader != nil {
		session.smdReader.drain(maxDuration)
		return
	}
	if session.readFile == nil {
		return
	}
	drainTTY(session.readFile, maxDuration)
}

func readATSessionUntilTokens(session *atCommandSession, maxDuration time.Duration, terminalTokens ...string) (string, error) {
	if session == nil {
		return "", errors.New("AT session is not open")
	}
	if session.smdReader != nil {
		return session.smdReader.readUntilTokens(maxDuration, terminalTokens...)
	}
	if session.readFile == nil {
		return "", errors.New("AT session reader is not open")
	}
	return readTTYUntilTokens(session.readFile, maxDuration, terminalTokens...)
}

const (
	smdATReadBufferSize     = 4096
	smdATMaxBufferedBytes   = 512 * 1024
	smdATReaderReadyFDEnv   = "SIMPLEADMIN_SMD_READY_FD"
	smdATReaderReadyTimeout = 2 * time.Second
)

var smdATReaderRegistry = struct {
	mu      sync.Mutex
	readers map[string]*smdATReader
}{readers: make(map[string]*smdATReader)}

type smdATReader struct {
	path   string
	notify chan struct{}

	mu      sync.Mutex
	buffer  []byte
	lastErr error
}

func getSMDATReader(path string) (*smdATReader, error) {
	smdATReaderRegistry.mu.Lock()
	defer smdATReaderRegistry.mu.Unlock()
	if smdATReaderRegistry.readers == nil {
		smdATReaderRegistry.readers = make(map[string]*smdATReader)
	}
	if reader := smdATReaderRegistry.readers[path]; reader != nil {
		return reader, nil
	}
	reader, err := startSMDATReader(path)
	if err != nil {
		return nil, err
	}
	smdATReaderRegistry.readers[path] = reader
	return reader, nil
}

func startSMDATReader(path string) (*smdATReader, error) {
	reader := &smdATReader{path: path, notify: make(chan struct{}, 1)}
	stdout, cmd, err := startSMDReaderProcess(path)
	if err != nil {
		return nil, err
	}
	go reader.readLoop(stdout, cmd)
	logATDebug("started persistent SMD AT reader helper: %s pid=%d", path, cmd.Process.Pid)
	return reader, nil
}

func openSMDReaderFile(path string) (*os.File, error) {
	logATDebug("open persistent SMD reader: %s", path)
	fd, err := syscall.Open(path, syscall.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func startSMDReaderProcess(path string) (io.ReadCloser, *exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	readyR, readyW, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	defer readyR.Close()

	cmd := exec.Command(exe, "__smd-reader", path)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	cmd.ExtraFiles = []*os.File{readyW}
	cmd.Env = append(os.Environ(), smdATReaderReadyFDEnv+"=3")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = readyW.Close()
		return nil, nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		_ = readyW.Close()
		_ = stdout.Close()
		return nil, nil, err
	}
	_ = readyW.Close()
	if err := waitSMDReaderReady(readyR, cmd); err != nil {
		_ = stdout.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		return nil, nil, err
	}
	return stdout, cmd, nil
}

func waitSMDReaderReady(readyR *os.File, cmd *exec.Cmd) error {
	done := make(chan error, 1)
	go func() {
		buf := []byte{0}
		n, err := readyR.Read(buf)
		if n > 0 {
			done <- nil
			return
		}
		if err == nil {
			err = io.EOF
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("SMD reader helper did not report ready: %w", err)
		}
		return nil
	case <-time.After(smdATReaderReadyTimeout):
		return fmt.Errorf("SMD reader helper did not open device before timeout; pid=%d", cmd.Process.Pid)
	}
}

func signalSMDReaderReady() {
	fdText := strings.TrimSpace(os.Getenv(smdATReaderReadyFDEnv))
	if fdText == "" {
		return
	}
	fd, err := strconv.Atoi(fdText)
	if err != nil || fd < 0 {
		return
	}
	f := os.NewFile(uintptr(fd), "smd-reader-ready")
	if f == nil {
		return
	}
	_, _ = f.Write([]byte{1})
	_ = f.Close()
}

func runSMDReaderCommand(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "missing smd reader device")
		os.Exit(2)
	}
	f, err := openSMDReaderFile(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "open smd reader failed: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()
	signalSMDReaderReady()
	buf := make([]byte, smdATReadBufferSize)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			if writeErr := writeAll(os.Stdout, buf[:n]); writeErr != nil {
				fmt.Fprintf(os.Stderr, "smd reader stdout write failed: %v\n", writeErr)
				os.Exit(1)
			}
		}
		if err == nil {
			continue
		}
		if isTemporaryTTYReadError(err) {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		fmt.Fprintf(os.Stderr, "smd reader read failed: %v\n", err)
		os.Exit(1)
	}
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrUnexpectedEOF
		}
		data = data[n:]
	}
	return nil
}

func (r *smdATReader) readLoop(stdout io.ReadCloser, cmd *exec.Cmd) {
	buf := make([]byte, smdATReadBufferSize)
	for {
		n, err := stdout.Read(buf)
		if n > 0 {
			r.append(buf[:n])
		}
		if err == nil {
			continue
		}
		logATDebug("persistent SMD reader helper ended on %s: %v", r.path, err)
		r.setLastErr(err)
		_ = stdout.Close()
		_ = cmd.Wait()
		time.Sleep(250 * time.Millisecond)
		for {
			newStdout, newCmd, openErr := startSMDReaderProcess(r.path)
			if openErr == nil {
				logATDebug("persistent SMD reader helper restarted: %s pid=%d", r.path, newCmd.Process.Pid)
				r.setLastErr(nil)
				stdout = newStdout
				cmd = newCmd
				break
			}
			logATDebug("persistent SMD reader helper restart failed on %s: %v", r.path, openErr)
			r.setLastErr(openErr)
			time.Sleep(time.Second)
		}
	}
}

func (r *smdATReader) append(data []byte) {
	r.mu.Lock()
	r.buffer = append(r.buffer, data...)
	if len(r.buffer) > smdATMaxBufferedBytes {
		r.buffer = append([]byte(nil), r.buffer[len(r.buffer)-smdATMaxBufferedBytes:]...)
	}
	r.mu.Unlock()
	r.signal()
}

func (r *smdATReader) setLastErr(err error) {
	r.mu.Lock()
	r.lastErr = err
	r.mu.Unlock()
	r.signal()
}

func (r *smdATReader) signal() {
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

func (r *smdATReader) drain(maxDuration time.Duration) {
	r.mu.Lock()
	r.buffer = nil
	r.mu.Unlock()
	if maxDuration <= 0 {
		logATDebug("drained persistent SMD AT reader buffer: %s", r.path)
		return
	}
	deadline := time.NewTimer(maxDuration)
	defer deadline.Stop()
	idle := time.NewTimer(30 * time.Millisecond)
	defer idle.Stop()
	for {
		select {
		case <-r.notify:
			r.mu.Lock()
			r.buffer = nil
			r.mu.Unlock()
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(30 * time.Millisecond)
		case <-idle.C:
			logATDebug("drained persistent SMD AT reader buffer after idle: %s", r.path)
			return
		case <-deadline.C:
			logATDebug("drained persistent SMD AT reader buffer after max duration: %s", r.path)
			return
		}
	}
}

func (r *smdATReader) snapshot() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(append([]byte(nil), r.buffer...)), r.lastErr
}

func (r *smdATReader) writePayload(payload string) error {
	// Keep the shell redirection semantics that work with /dev/smd11, but do not
	// use printf. The AT payload is passed over stdin and copied to the device by
	// the shell-opened redirection, which avoids quoting issues and keeps SMS Ctrl-Z
	// payloads byte-for-byte. This cat is write-only and does not read /dev/smd11.
	cmd := exec.Command("/bin/sh", "-c", `cat > "$1"`, "smd-writer", r.path)
	cmd.Stdin = strings.NewReader(payload)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("SMD shell stdin writer failed: %w: %s", err, msg)
		}
		return fmt.Errorf("SMD shell stdin writer failed: %w", err)
	}
	return nil
}

func (r *smdATReader) readUntilTokens(maxDuration time.Duration, terminalTokens ...string) (string, error) {
	if maxDuration <= 0 {
		maxDuration = time.Second
	}
	deadline := time.NewTimer(maxDuration)
	defer deadline.Stop()
	for {
		out, lastErr := r.snapshot()
		if out != "" {
			logATDebug("SMD persistent reader snapshot: %s", outputSummary(out))
		}
		if containsAnyToken(out, terminalTokens...) {
			return out, nil
		}
		select {
		case <-r.notify:
			continue
		case <-deadline.C:
			if out != "" || lastErr == nil {
				return out, nil
			}
			return out, lastErr
		}
	}
}

func readTTYUntilTokens(f *os.File, maxDuration time.Duration, terminalTokens ...string) (string, error) {
	deadline := time.Now().Add(maxDuration)
	buf := make([]byte, 1024)
	var out strings.Builder
	fd := int(f.Fd())

	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining > 50*time.Millisecond {
			remaining = 50 * time.Millisecond
		}
		ready, selectErr := waitReadableFD(fd, remaining)
		if selectErr != nil {
			logATDebug("AT read select err=%v current=%s", selectErr, outputSummary(out.String()))
			return out.String(), selectErr
		}
		if !ready {
			continue
		}

		n, err := syscall.Read(fd, buf)
		if n > 0 {
			chunk := string(buf[:n])
			logATDebug("AT read chunk: %s", outputSummary(chunk))
			out.WriteString(chunk)
			if containsAnyToken(out.String(), terminalTokens...) {
				return out.String(), nil
			}
			continue
		}
		if err != nil && !isTemporaryTTYReadError(err) {
			logATDebug("AT read non-temporary err=%v current=%s", err, outputSummary(out.String()))
			if errors.Is(err, io.EOF) {
				if out.Len() > 0 {
					return out.String(), nil
				}
				// Some SMD character devices report EOF while no response is ready yet.
				// Keep waiting until the command timeout instead of treating it as a
				// terminal failure.
				continue
			}
			return out.String(), err
		}
	}
	return out.String(), nil
}

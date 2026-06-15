// Package gimp drives a host GIMP for the automated finishing stage by speaking the GIMP
// Script-Fu server's wire protocol over TCP — the SAME resident server the vendored GIMP MCP
// uses (auto-started if not already running), so the engine and Claude share one GIMP.
//
// Wire protocol (GIMP's): request 'G' + uint16-BE(len) + script; response 'G' + errByte +
// uint16-BE(len) + body. errByte 0 == success. A single message is capped at 65535 bytes.
package gimp

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const maxScript = 0xFFFF

// Client is a connection broker to the resident GIMP Script-Fu server.
type Client struct {
	bin     string
	host    string
	port    int
	timeout time.Duration
	mu      sync.Mutex
}

// New returns a GIMP client for the given gimp-console binary and Script-Fu server address.
func New(bin, host string, port int) *Client {
	if host == "" {
		host = "127.0.0.1"
	}
	if port == 0 {
		port = 10008
	}
	return &Client{bin: bin, host: host, port: port, timeout: 60 * time.Second}
}

func (c *Client) addr() string { return fmt.Sprintf("%s:%d", c.host, c.port) }

// Available reports whether the Script-Fu server can be reached or started.
func (c *Client) Available() error { return c.ensureServer() }

func (c *Client) running() bool {
	conn, err := net.DialTimeout("tcp", c.addr(), time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// ensureServer starts a resident gimp-console with the Script-Fu server if one isn't already up.
func (c *Client) ensureServer() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running() {
		return nil
	}
	if c.bin == "" {
		return fmt.Errorf("GIMP binary not set (GIMP_BIN)")
	}
	if _, err := exec.LookPath(c.bin); err != nil {
		if _, statErr := os.Stat(c.bin); statErr != nil {
			return fmt.Errorf("GIMP binary not found at %q", c.bin)
		}
	}
	sexp := fmt.Sprintf(`(plug-in-script-fu-server RUN-NONINTERACTIVE "%s" %d "/tmp/astrostack-gimp-sf.log")`, c.host, c.port)
	cmd := exec.Command(c.bin, "-i", "-b", sexp, "-b", "(gimp-quit 0)")
	cmd.Stdout, cmd.Stderr = nil, nil
	cmd.SysProcAttr = detachAttr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch gimp: %w", err)
	}
	_ = cmd.Process.Release()

	deadline := time.Now().Add(c.timeout)
	for time.Now().Before(deadline) {
		if c.running() {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("gimp Script-Fu server did not open %s within %s", c.addr(), c.timeout)
}

// Eval runs a Script-Fu program on the resident server and returns the printed result.
func (c *Client) Eval(script string) (string, error) {
	if len(script) > maxScript {
		return "", fmt.Errorf("script is %d bytes; the Script-Fu server caps a request at %d", len(script), maxScript)
	}
	if err := c.ensureServer(); err != nil {
		return "", err
	}
	conn, err := net.DialTimeout("tcp", c.addr(), c.timeout)
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(c.timeout))

	payload := []byte(script)
	header := []byte{'G', byte(len(payload) >> 8), byte(len(payload) & 0xFF)}
	if _, err := conn.Write(append(header, payload...)); err != nil {
		return "", err
	}

	respHdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, respHdr); err != nil {
		return "", fmt.Errorf("read gimp response header: %w", err)
	}
	if respHdr[0] != 'G' {
		return "", fmt.Errorf("bad gimp response header: %v", respHdr)
	}
	errByte := respHdr[1]
	bodyLen := binary.BigEndian.Uint16(respHdr[2:4])
	body := make([]byte, bodyLen)
	if _, err := io.ReadFull(conn, body); err != nil {
		return "", fmt.Errorf("read gimp response body: %w", err)
	}
	text := strings.TrimSpace(string(body))
	if errByte != 0 {
		return "", fmt.Errorf("gimp: %s", text)
	}
	return text, nil
}

package ipc

import (
	"bufio"
	"encoding/json"
	"net"

	v1 "github.com/allenpark2-coder/ai-debug-gateway/internal/protocol/v1"
)

// Client is a synchronous, one-request-at-a-time connection to a
// Server. Callers wanting concurrent requests should use multiple
// Clients; the daemon accepts any number of connections.
type Client struct {
	conn   net.Conn
	reader *bufio.Reader
}

// Dial connects to the Unix domain socket at path.
func Dial(path string) (*Client, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, reader: bufio.NewReaderSize(conn, 4096)}, nil
}

// Call sends req, defaulting Version to the current protocol version
// when unset, and returns the daemon's response.
func (c *Client) Call(req v1.Request) (v1.Response, error) {
	if req.Version == "" {
		req.Version = v1.Version
	}

	data, err := json.Marshal(req)
	if err != nil {
		return v1.Response{}, err
	}
	data = append(data, '\n')
	if _, err := c.conn.Write(data); err != nil {
		return v1.Response{}, err
	}

	line, err := readFrame(c.reader, v1.MaxFrameBytes)
	if err != nil {
		return v1.Response{}, err
	}
	var resp v1.Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return v1.Response{}, err
	}
	return resp, nil
}

// Close closes the underlying connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

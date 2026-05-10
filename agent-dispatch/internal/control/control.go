package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

type Operation string

const (
	OpSteer    Operation = "steer"
	OpFollowUp Operation = "follow_up"
	OpStop     Operation = "stop"
)

type Request struct {
	Operation Operation `json:"operation"`
	Message   string    `json:"message,omitempty"`
	Force     bool      `json:"force,omitempty"`
}

type Response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type Handler func(Request) Response

type Server struct {
	path     string
	listener net.Listener
}

func Listen(path string) (*Server, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create socket dir: %w", err)
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen control socket: %w", err)
	}
	return &Server{path: path, listener: ln}, nil
}

func (s *Server) Serve(handler Handler) error {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return err
		}
		go handleConn(conn, handler)
	}
}

func (s *Server) Close() error {
	err := s.listener.Close()
	_ = os.Remove(s.path)
	return err
}

func Send(path string, req Request) (Response, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return Response{}, fmt.Errorf("connect control socket: %w", err)
	}
	defer conn.Close() //nolint:errcheck
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, err
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return Response{}, err
	}
	if !resp.OK {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

func handleConn(conn net.Conn, handler Handler) {
	defer conn.Close() //nolint:errcheck
	var req Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(Response{OK: false, Error: err.Error()})
		return
	}
	_ = json.NewEncoder(conn).Encode(handler(req))
}

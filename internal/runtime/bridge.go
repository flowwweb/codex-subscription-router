package runtime

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
)

type OutputHub struct {
	mu     sync.Mutex
	writer io.Writer
}

func (h *OutputHub) Write(data []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.writer == nil {
		return len(data), nil
	}
	return writeAll(h.writer, data)
}

func (h *OutputHub) attach(writer io.Writer, acknowledgement []byte) (func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.writer != nil {
		return nil, errors.New("an app-server bridge is already connected")
	}
	if _, err := writeAll(writer, acknowledgement); err != nil {
		return nil, err
	}
	h.writer = writer
	return func() {
		h.mu.Lock()
		h.writer = nil
		h.mu.Unlock()
	}, nil
}

func writeAll(writer io.Writer, data []byte) (int, error) {
	written := 0
	for len(data) > 0 {
		count, err := writer.Write(data)
		written += count
		data = data[count:]
		if err != nil {
			return written, err
		}
		if count == 0 {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

func ServeBridge(ctx context.Context, listener net.Listener, instance string, output *OutputHub, handleLine func([]byte)) error {
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept app-server bridge: %w", err)
		}
		go serveBridgeConnection(ctx, connection, instance, output, handleLine)
	}
}

func serveBridgeConnection(ctx context.Context, connection net.Conn, instance string, output *OutputHub, handleLine func([]byte)) {
	defer connection.Close()
	connectionDone := make(chan struct{})
	defer close(connectionDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.Close()
		case <-connectionDone:
		}
	}()
	scanner := bufio.NewScanner(connection)
	scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)
	if !scanner.Scan() {
		return
	}
	var hello struct {
		Instance string `json:"instance"`
	}
	if json.Unmarshal(scanner.Bytes(), &hello) != nil ||
		len(hello.Instance) != len(instance) ||
		subtle.ConstantTimeCompare([]byte(hello.Instance), []byte(instance)) != 1 {
		_, _ = io.WriteString(connection, "{\"ok\":false}\n")
		return
	}
	detach, err := output.attach(connection, []byte("{\"ok\":true}\n"))
	if err != nil {
		_, _ = io.WriteString(connection, "{\"ok\":false,\"error\":\"bridge already connected\"}\n")
		return
	}
	defer detach()
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
			handleLine(append([]byte(nil), scanner.Bytes()...))
		}
	}
}

func RunBridge(ctx context.Context, receipt Receipt, input io.Reader, output io.Writer) error {
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", receipt.BridgeAddress)
	if err != nil {
		return fmt.Errorf("connect to router daemon bridge: %w", err)
	}
	defer connection.Close()
	if err := json.NewEncoder(connection).Encode(map[string]string{"instance": receipt.Instance}); err != nil {
		return fmt.Errorf("authenticate router daemon bridge: %w", err)
	}
	reader := bufio.NewReader(connection)
	acknowledgement, err := reader.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("read router daemon bridge acknowledgement: %w", err)
	}
	var ack struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(acknowledgement, &ack); err != nil || !ack.OK {
		if ack.Error == "" {
			ack.Error = "authentication failed"
		}
		return fmt.Errorf("router daemon bridge rejected connection: %s", ack.Error)
	}
	copyDone := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(output, reader)
		copyDone <- copyErr
	}()
	_, inputErr := io.Copy(connection, input)
	if tcp, ok := connection.(*net.TCPConn); ok {
		_ = tcp.CloseWrite()
	}
	select {
	case outputErr := <-copyDone:
		return errors.Join(inputErr, outputErr)
	case <-ctx.Done():
		return ctx.Err()
	}
}

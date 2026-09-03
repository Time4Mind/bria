package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
)

type codec struct {
	reader          *bufio.Reader
	writer          io.Writer
	maxMessageBytes int
	writeMu         sync.Mutex
}

type incomingMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func newCodec(input io.Reader, output io.Writer, maxMessageBytes int) *codec {
	bufferSize := 64 * 1024
	if maxMessageBytes < bufferSize {
		bufferSize = maxMessageBytes
	}
	return &codec{
		reader:          bufio.NewReaderSize(input, bufferSize),
		writer:          output,
		maxMessageBytes: maxMessageBytes,
	}
}

func (codec *codec) readMessage() (incomingMessage, error) {
	line, err := codec.readLine()
	if err != nil {
		return incomingMessage{}, err
	}
	var message incomingMessage
	if err := json.Unmarshal(line, &message); err != nil {
		return incomingMessage{}, ErrMalformedMessage
	}
	if len(message.ID) == 0 && message.Method == "" {
		return incomingMessage{}, ErrMalformedMessage
	}
	if len(message.ID) != 0 && message.Method == "" && len(message.Result) == 0 && len(message.Error) == 0 {
		return incomingMessage{}, ErrMalformedMessage
	}
	return message, nil
}

func (codec *codec) readLine() ([]byte, error) {
	line := make([]byte, 0, min(codec.maxMessageBytes, 4096))
	for {
		fragment, err := codec.reader.ReadSlice('\n')
		if len(line)+len(fragment) > codec.maxMessageBytes+1 {
			return nil, ErrMessageTooLarge
		}
		line = append(line, fragment...)
		switch {
		case err == nil:
			line = bytes.TrimSuffix(line, []byte{'\n'})
			line = bytes.TrimSuffix(line, []byte{'\r'})
			if len(line) > codec.maxMessageBytes {
				return nil, ErrMessageTooLarge
			}
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(line) > 0:
			if len(line) > codec.maxMessageBytes {
				return nil, ErrMessageTooLarge
			}
			return line, nil
		case errors.Is(err, io.EOF):
			return nil, io.EOF
		default:
			return nil, ErrTransport
		}
	}
}

func (codec *codec) writeRequest(ctx context.Context, id RequestID, method string, params any) error {
	return codec.write(ctx, struct {
		ID     RequestID `json:"id"`
		Method string    `json:"method"`
		Params any       `json:"params"`
	}{ID: id, Method: method, Params: params})
}

func (codec *codec) writeNotification(ctx context.Context, method string, params any) error {
	return codec.write(ctx, struct {
		Method string `json:"method"`
		Params any    `json:"params"`
	}{Method: method, Params: params})
}

func (codec *codec) writeResponse(ctx context.Context, id json.RawMessage, result any) error {
	if len(id) == 0 {
		return ErrInvalidRequest
	}
	return codec.write(ctx, struct {
		ID     json.RawMessage `json:"id"`
		Result any             `json:"result"`
	}{ID: bytes.Clone(id), Result: result})
}

func (codec *codec) write(ctx context.Context, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ErrInvalidRequest
	}
	encoded = append(encoded, '\n')

	codec.writeMu.Lock()
	defer codec.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	written, err := codec.writer.Write(encoded)
	if err != nil || written != len(encoded) {
		return ErrTransport
	}
	return nil
}

func (message incomingMessage) isNotification() bool {
	return len(message.ID) == 0 && message.Method != ""
}

func (message incomingMessage) isServerRequest() bool {
	return len(message.ID) != 0 && message.Method != ""
}

func (message incomingMessage) responseID() (RequestID, error) {
	if len(message.ID) == 0 || message.Method != "" {
		return 0, ErrInvalidResponse
	}
	var id int64
	if err := json.Unmarshal(message.ID, &id); err != nil || id < 1 {
		return 0, ErrInvalidResponse
	}
	return RequestID(id), nil
}

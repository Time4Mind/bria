package executor

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"bria/internal/domain"
	"bria/internal/nodelink"
)

type wireRequest struct {
	SessionID domain.SessionID `json:"session_id"`
	Action    Action           `json:"action"`
	Payload   []byte           `json:"payload,omitempty"`
}

type wireResponse struct {
	Accepted bool   `json:"accepted"`
	Payload  []byte `json:"payload,omitempty"`
}

func EncodeRequestEnvelope(coordinatorID, targetID domain.ComputerID, request Request) (nodelink.Envelope, error) {
	if err := validateRequest(request); err != nil || strings.TrimSpace(string(coordinatorID)) == "" || strings.TrimSpace(string(targetID)) == "" || coordinatorID == targetID {
		return nodelink.Envelope{}, ErrInvalidRequest
	}
	payload, err := json.Marshal(wireRequest{SessionID: request.SessionID, Action: request.Action, Payload: request.Payload})
	if err != nil {
		return nodelink.Envelope{}, ErrInvalidRequest
	}
	return nodelink.Envelope{
		Version: nodelink.ProtocolVersion, Kind: nodelink.KindCommand, OperationID: request.OperationID,
		Generation: request.Generation, CoordinatorID: coordinatorID, SourceComputerID: coordinatorID,
		TargetComputerID: targetID, Payload: payload,
	}, nil
}

func DecodeRequestEnvelope(envelope nodelink.Envelope) (Request, error) {
	if envelope.Version != nodelink.ProtocolVersion || envelope.Kind != nodelink.KindCommand || envelope.SourceComputerID != envelope.CoordinatorID || envelope.SourceComputerID == envelope.TargetComputerID {
		return Request{}, ErrInvalidRequest
	}
	var wire wireRequest
	if err := decodeWire(envelope.Payload, &wire); err != nil {
		return Request{}, ErrInvalidRequest
	}
	request := Request{OperationID: envelope.OperationID, Generation: envelope.Generation, SessionID: wire.SessionID, Action: wire.Action, Payload: append([]byte(nil), wire.Payload...)}
	if err := validateRequest(request); err != nil {
		return Request{}, err
	}
	return request, nil
}

func EncodeResponseEnvelope(requestEnvelope nodelink.Envelope, response Response) (nodelink.Envelope, error) {
	request, err := DecodeRequestEnvelope(requestEnvelope)
	if err != nil {
		return nodelink.Envelope{}, err
	}
	response, err = validateResponse(request, response)
	if err != nil {
		return nodelink.Envelope{}, err
	}
	payload, err := json.Marshal(wireResponse{Accepted: response.Accepted, Payload: response.Payload})
	if err != nil {
		return nodelink.Envelope{}, ErrInvalidResponse
	}
	return nodelink.Envelope{
		Version: nodelink.ProtocolVersion, Kind: nodelink.KindAcknowledgement, OperationID: response.OperationID,
		Generation: requestEnvelope.Generation, CoordinatorID: requestEnvelope.CoordinatorID,
		SourceComputerID: requestEnvelope.TargetComputerID, TargetComputerID: requestEnvelope.CoordinatorID, Payload: payload,
	}, nil
}

func DecodeResponseEnvelope(request Request, envelope nodelink.Envelope) (Response, error) {
	if err := validateRequest(request); err != nil || envelope.Version != nodelink.ProtocolVersion || envelope.Kind != nodelink.KindAcknowledgement || envelope.OperationID != request.OperationID || envelope.Generation != request.Generation || envelope.TargetComputerID != envelope.CoordinatorID || envelope.SourceComputerID == envelope.CoordinatorID {
		return Response{}, ErrInvalidResponse
	}
	var wire wireResponse
	if err := decodeWire(envelope.Payload, &wire); err != nil {
		return Response{}, ErrInvalidResponse
	}
	return validateResponse(request, Response{OperationID: envelope.OperationID, Accepted: wire.Accepted, Payload: wire.Payload})
}

func decodeWire(payload []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrInvalidRequest
	}
	return nil
}
